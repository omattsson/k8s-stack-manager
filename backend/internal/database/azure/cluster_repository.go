package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/crypto"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// ClusterRepository implements models.ClusterRepository for Azure Table Storage.
// Partition key: "clusters", Row key: cluster ID.
type ClusterRepository struct {
	client        AzureTableClient
	tableName     string
	encryptionKey []byte // nil or empty means encryption disabled
}

// NewClusterRepository creates a new Azure Table Storage cluster repository.
func NewClusterRepository(accountName, accountKey, endpoint string, useAzurite bool, encryptionKey string) (*ClusterRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "Clusters", useAzurite)
	if err != nil {
		return nil, err
	}
	repo := &ClusterRepository{client: client, tableName: "Clusters"}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
	}
	return repo, nil
}

// NewTestClusterRepository creates a repository for unit testing.
func NewTestClusterRepository(encryptionKey string) *ClusterRepository {
	repo := &ClusterRepository{tableName: "Clusters"}
	if encryptionKey != "" {
		repo.encryptionKey = crypto.DeriveKey(encryptionKey)
	}
	return repo
}

// SetTestClient injects a mock client for testing.
func (r *ClusterRepository) SetTestClient(client AzureTableClient) {
	r.client = client
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
		return dberrors.NewDatabaseError("marshal", err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError("create", err)
	}
	return nil
}

func (r *ClusterRepository) FindByID(id string) (*models.Cluster, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, "clusters", id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}
	return r.clusterFromEntity(entity)
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
		return dberrors.NewDatabaseError("marshal", err)
	}

	_, err = r.client.UpdateEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError("update", err)
	}
	return nil
}

func (r *ClusterRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, "clusters", id, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *ClusterRepository) List() ([]models.Cluster, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'clusters'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	results := make([]models.Cluster, 0, len(entities))
	for _, e := range entities {
		c, err := r.clusterFromEntity(e)
		if err != nil {
			return nil, err
		}
		results = append(results, *c)
	}
	return results, nil
}

func (r *ClusterRepository) FindDefault() (*models.Cluster, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'clusters'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, func(e map[string]interface{}) bool {
		return getBool(e, "IsDefault")
	})
	if err != nil {
		return nil, mapAzureError("find_default", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound)
	}
	return r.clusterFromEntity(entities[0])
}

func (r *ClusterRepository) SetDefault(id string) error {
	ctx := context.Background()

	// Find the current default and unset it.
	filter := "PartitionKey eq 'clusters'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, func(e map[string]interface{}) bool {
		return getBool(e, "IsDefault")
	})
	if err != nil {
		return mapAzureError("set_default", err)
	}

	for _, e := range entities {
		e["IsDefault"] = false
		e["UpdatedAt"] = time.Now().UTC().Format(time.RFC3339)
		entityBytes, err := json.Marshal(e)
		if err != nil {
			return dberrors.NewDatabaseError("marshal", err)
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
	if kubeconfigData != "" && len(r.encryptionKey) > 0 {
		encrypted, err := crypto.Encrypt([]byte(kubeconfigData), r.encryptionKey)
		if err != nil {
			return nil, dberrors.NewDatabaseError("encrypt", err)
		}
		kubeconfigData = base64.StdEncoding.EncodeToString(encrypted)
	}

	return map[string]interface{}{
		"PartitionKey":   "clusters",
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

func (r *ClusterRepository) clusterFromEntity(e map[string]interface{}) (*models.Cluster, error) {
	kubeconfigData := getString(e, "KubeconfigData")
	if kubeconfigData != "" && len(r.encryptionKey) > 0 {
		decoded, err := base64.StdEncoding.DecodeString(kubeconfigData)
		if err != nil {
			return nil, dberrors.NewDatabaseError("decode", err)
		}
		decrypted, err := crypto.Decrypt(decoded, r.encryptionKey)
		if err != nil {
			return nil, dberrors.NewDatabaseError("decrypt", err)
		}
		kubeconfigData = string(decrypted)
	}

	return &models.Cluster{
		ID:             getString(e, "ID"),
		Name:           getString(e, "Name"),
		Description:    getString(e, "Description"),
		APIServerURL:   getString(e, "APIServerURL"),
		KubeconfigData: kubeconfigData,
		KubeconfigPath: getString(e, "KubeconfigPath"),
		Region:         getString(e, "Region"),
		HealthStatus:   getStringDefault(e, "HealthStatus", models.ClusterUnreachable),
		MaxNamespaces:  getInt(e, "MaxNamespaces"),
		IsDefault:      getBool(e, "IsDefault"),
		CreatedAt:      parseTime(e, "CreatedAt"),
		UpdatedAt:      parseTime(e, "UpdatedAt"),
	}, nil
}
