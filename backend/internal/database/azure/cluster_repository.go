package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
	"backend/pkg/crypto"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

const tableClusters = "Clusters"
const filterPKClusters = odataPartitionKeyEq + "clusters'"


// ClusterRepository implements models.ClusterRepository for Azure Table Storage.
// Partition key: "clusters", Row key: cluster ID.
type ClusterRepository struct {
	client        AzureTableClient
	tableName     string
	encryptionKey []byte // nil or empty means encryption disabled
	mu            sync.Mutex
}

// NewClusterRepository creates a new Azure Table Storage cluster repository.
func NewClusterRepository(accountName, accountKey, endpoint string, useAzurite bool, encryptionKey string) (*ClusterRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableClusters, useAzurite)
	if err != nil {
		return nil, err
	}
	repo := &ClusterRepository{client: client, tableName: tableClusters}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
	} else {
		slog.Warn("KUBECONFIG_ENCRYPTION_KEY is not set — clusters with kubeconfig_data will be rejected; use kubeconfig_path instead")
	}
	return repo, nil
}

// NewTestClusterRepository creates a repository for unit testing.
func NewTestClusterRepository(encryptionKey string) *ClusterRepository {
	repo := &ClusterRepository{tableName: tableClusters}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
	}
	return repo
}

// SetTestClient injects a mock client for testing.
func (r *ClusterRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// clusterEntity is the typed Azure Table entity for clusters.
type clusterEntity struct {
	PartitionKey   string  `json:"PartitionKey"`
	RowKey         string  `json:"RowKey"`
	ID             string  `json:"ID"`
	Name           string  `json:"Name"`
	Description    string  `json:"Description"`
	APIServerURL   string  `json:"APIServerURL"`
	KubeconfigData string  `json:"KubeconfigData"`
	KubeconfigPath string  `json:"KubeconfigPath"`
	Region         string  `json:"Region"`
	HealthStatus   string  `json:"HealthStatus"`
	MaxNamespaces  float64 `json:"MaxNamespaces"`
	IsDefault      bool    `json:"IsDefault"`
	CreatedAt      string  `json:"CreatedAt"`
	UpdatedAt      string  `json:"UpdatedAt"`
}

func (r *ClusterRepository) clusterEntityToModel(e *clusterEntity) (*models.Cluster, error) {
	kubeconfigData := e.KubeconfigData
	if kubeconfigData != "" && len(r.encryptionKey) > 0 {
		decoded, err := base64.StdEncoding.DecodeString(kubeconfigData)
		if err == nil {
			decrypted, decErr := crypto.Decrypt(decoded, r.encryptionKey)
			if decErr != nil {
				return nil, dberrors.NewDatabaseError("decrypt kubeconfig data", decErr)
			}
			kubeconfigData = string(decrypted)
		}
	}

	healthStatus := e.HealthStatus
	if healthStatus == "" {
		healthStatus = models.ClusterUnreachable
	}

	c := &models.Cluster{
		ID:             e.ID,
		Name:           e.Name,
		Description:    e.Description,
		APIServerURL:   e.APIServerURL,
		KubeconfigData: kubeconfigData,
		KubeconfigPath: e.KubeconfigPath,
		Region:         e.Region,
		HealthStatus:   healthStatus,
		MaxNamespaces:  int(e.MaxNamespaces),
		IsDefault:      e.IsDefault,
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	c.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return c, nil
}

func (r *ClusterRepository) Create(cluster *models.Cluster) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if cluster.ID == "" {
		cluster.ID = newID()
	}
	if cluster.HealthStatus == "" {
		cluster.HealthStatus = models.ClusterUnreachable
	}
	cluster.CreatedAt = now
	cluster.UpdatedAt = now

	entity, err := r.clusterToEntity(cluster)
	if err != nil {
		return err
	}
	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opCreate, err)
	}
	return nil
}

func (r *ClusterRepository) FindByID(id string) (*models.Cluster, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, pkClusters, id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity clusterEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError(opUnmarshal, err)
	}
	return r.clusterEntityToModel(&entity)
}

func (r *ClusterRepository) Update(cluster *models.Cluster) error {
	ctx := context.Background()
	now := time.Now().UTC()
	cluster.UpdatedAt = now

	entity, err := r.clusterToEntity(cluster)
	if err != nil {
		return err
	}
	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.UpdateEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opUpdate, err)
	}
	return nil
}

func (r *ClusterRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, pkClusters, id, nil)
	if err != nil {
		return mapAzureError(opDelete, err)
	}
	return nil
}

func (r *ClusterRepository) List() ([]models.Cluster, error) {
	ctx := context.Background()

	filter := filterPKClusters
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[clusterEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError(opList, err)
	}

	results := make([]models.Cluster, 0, len(entities))
	for _, e := range entities {
		c, err := r.clusterEntityToModel(&e)
		if err != nil {
			return nil, err
		}
		results = append(results, *c)
	}
	return results, nil
}

func (r *ClusterRepository) FindDefault() (*models.Cluster, error) {
	ctx := context.Background()

	filter := filterPKClusters
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[clusterEntity](ctx, pager, func(e *clusterEntity) bool {
		return e.IsDefault
	}, 1)
	if err != nil {
		return nil, mapAzureError("find_default", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound)
	}
	return r.clusterEntityToModel(&entities[0])
}

func (r *ClusterRepository) SetDefault(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ctx := context.Background()

	// Find the current default and unset it.
	// SetDefault needs to read-modify-write, so we use the entity struct for the
	// read (typed deserialization), then marshal back for the write.
	filter := filterPKClusters
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[clusterEntity](ctx, pager, func(e *clusterEntity) bool {
		return e.IsDefault
	}, 0)
	if err != nil {
		return mapAzureError("set_default", err)
	}

	for _, e := range entities {
		e.IsDefault = false
		e.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		entityBytes, err := json.Marshal(e)
		if err != nil {
			return dberrors.NewDatabaseError(opMarshal, err)
		}
		if _, err := r.client.UpdateEntity(ctx, entityBytes, nil); err != nil {
			return mapAzureError("set_default", err)
		}
	}

	// Set the new default.
	cluster, err := r.FindByID(id)
	if err != nil {
		return err
	}
	cluster.IsDefault = true
	return r.Update(cluster)
}

func (r *ClusterRepository) clusterToEntity(c *models.Cluster) (map[string]interface{}, error) {
	kubeconfigData := c.KubeconfigData
	if kubeconfigData != "" {
		if len(r.encryptionKey) == 0 {
			return nil, dberrors.NewDatabaseError("validation", fmt.Errorf("kubeconfig_data cannot be stored without KUBECONFIG_ENCRYPTION_KEY configured; use kubeconfig_path instead: %w", dberrors.ErrValidation))
		}
		encrypted, err := crypto.Encrypt([]byte(kubeconfigData), r.encryptionKey)
		if err != nil {
			return nil, dberrors.NewDatabaseError("encrypt", err)
		}
		kubeconfigData = base64.StdEncoding.EncodeToString(encrypted)
	}

	return map[string]interface{}{
		"PartitionKey":   pkClusters,
		"RowKey":         c.ID,
		"ID":             c.ID,
		"Name":           c.Name,
		"Description":    c.Description,
		"APIServerURL":   c.APIServerURL,
		"KubeconfigData": kubeconfigData,
		"KubeconfigPath": c.KubeconfigPath,
		"Region":         c.Region,
		"HealthStatus":   c.HealthStatus,
		"MaxNamespaces":  c.MaxNamespaces,
		"IsDefault":      c.IsDefault,
		"CreatedAt":      c.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":      c.UpdatedAt.Format(time.RFC3339),
	}, nil
}
