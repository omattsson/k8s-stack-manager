package azure

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// nextID generates a collision-resistant numeric ID using a cryptographically
// secure random component. We use 48 bits of randomness to keep collision
// probability extremely low even under concurrency across multiple instances.
func nextID() (uint, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 48)
	rb, err := rand.Int(rand.Reader, max)
	if err != nil {
		return 0, fmt.Errorf("failed to generate random ID component: %w", err)
	}
	return uint(rb.Uint64()), nil
}

// TableRepository implements the Repository interface for Azure Table Storage
type TableRepository struct {
	client    AzureTableClient
	tableName string
}

// NewTableRepository creates a new Azure Table Storage repository
func NewTableRepository(accountName, accountKey, endpoint, tableName string, useAzurite bool) (*TableRepository, error) {
	var serviceURL string
	if useAzurite {
		serviceURL = fmt.Sprintf("http://%s/%s", endpoint, accountName)
	} else {
		serviceURL = fmt.Sprintf("https://%s.table.%s", accountName, endpoint)
	}

	if accountName == "" || accountKey == "" {
		return nil, dberrors.NewDatabaseError("azure_client", errors.New("invalid connection string: missing account name or key"))
	}

	protocol := "https"
	if useAzurite {
		protocol = "http"
	}
	// Create service client
	serviceClient, err := aztables.NewServiceClientFromConnectionString(
		fmt.Sprintf("DefaultEndpointsProtocol=%s;AccountName=%s;AccountKey=%s;TableEndpoint=%s",
			protocol,
			accountName,
			accountKey,
			serviceURL),
		nil,
	)
	if err != nil {
		return nil, dberrors.NewDatabaseError("azure_client", err)
	}

	// Get table client
	tableClient := serviceClient.NewClient(tableName)

	// Create table if it doesn't exist
	_, err = serviceClient.CreateTable(context.Background(), tableName, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) {
			if respErr.ErrorCode == "TableAlreadyExists" {
				// Table already exists, which is fine
				return &TableRepository{
					client:    newAzureClientAdapter(tableClient),
					tableName: tableName,
				}, nil
			}
			// Return the underlying status code error
			return nil, fmt.Errorf("create_table: %v", respErr.RawResponse.Status)
		}
		// Return other errors as-is
		return nil, dberrors.NewDatabaseError("create_table", err)
	}

	return &TableRepository{
		client:    newAzureClientAdapter(tableClient),
		tableName: tableName,
	}, nil
}

// SetTestClient sets a test client - only available in test builds
func (r *TableRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// NewTestTableRepository creates a TableRepository for unit testing without connecting
// to any real Azure service. Use SetTestClient to inject a mock client.
func NewTestTableRepository(tableName string) *TableRepository {
	return &TableRepository{
		tableName: tableName,
	}
}

// Create implements the Repository interface
func (r *TableRepository) Create(ctx context.Context, entity interface{}) error {
	item, ok := entity.(*models.Item)
	if !ok {
		return dberrors.NewDatabaseError("type_assertion", errors.New("entity must be *models.Item"))
	}

	// Initialize version for new entities (consistent with GORM default:1 and MockRepository)
	if item.Version == 0 {
		item.Version = 1
	}

	// Create Azure Table entity
	now := time.Now().UTC()

	// Generate a numeric ID (Azure Table Storage has no auto-increment)
	if item.ID == 0 {
		id, err := nextID()
		if err != nil {
			return dberrors.NewDatabaseError("create", err)
		}
		item.ID = id
	}

	entityJSON := map[string]interface{}{
		"PartitionKey": "items",
		"RowKey":       strconv.FormatUint(uint64(item.ID), 10),
		"Name":         item.Name,
		"Price":        item.Price,
		"Version":      item.Version,
		"CreatedAt":    now.Format(time.RFC3339),
		"UpdatedAt":    now.Format(time.RFC3339),
	}

	entityBytes, err := json.Marshal(entityJSON)
	if err != nil {
		return dberrors.NewDatabaseError("marshal", err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.ErrorCode == "EntityAlreadyExists" {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}

	item.CreatedAt = now
	item.UpdatedAt = now
	return nil
}

// FindByID implements the Repository interface
func (r *TableRepository) FindByID(ctx context.Context, id uint, dest interface{}) error {
	item, ok := dest.(*models.Item)
	if !ok {
		return dberrors.NewDatabaseError("type_assertion", fmt.Errorf("dest must be *models.Item"))
	}

	// Get the entity
	result, err := r.client.GetEntity(ctx, "items", strconv.FormatUint(uint64(id), 10), nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return dberrors.NewDatabaseError("find", dberrors.ErrNotFound)
		}
		return dberrors.NewDatabaseError("find", err)
	}

	// Parse the entity
	var entityData map[string]interface{}
	if err := json.Unmarshal(result.Value, &entityData); err != nil {
		return dberrors.NewDatabaseError("unmarshal", err)
	}

	// Map entity to item
	item.ID = id

	name, ok := entityData["Name"].(string)
	if !ok {
		return dberrors.NewDatabaseError("unmarshal", fmt.Errorf("missing or invalid Name field"))
	}
	item.Name = name

	price, ok := entityData["Price"].(float64)
	if !ok {
		return dberrors.NewDatabaseError("unmarshal", fmt.Errorf("missing or invalid Price field"))
	}
	item.Price = price

	// Default to version 1 when the Version field is missing or invalid to
	// keep optimistic-lock semantics consistent with the GORM repository.
	item.Version = 1
	if v, ok := entityData["Version"]; ok {
		if vf, ok := v.(float64); ok && vf > 0 {
			item.Version = uint(vf)
		}
	}

	createdAtStr, ok := entityData["CreatedAt"].(string)
	if !ok {
		return dberrors.NewDatabaseError("unmarshal", fmt.Errorf("missing or invalid CreatedAt field"))
	}
	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return dberrors.NewDatabaseError("parse_time", err)
	}
	item.CreatedAt = createdAt

	updatedAtStr, ok := entityData["UpdatedAt"].(string)
	if !ok {
		return dberrors.NewDatabaseError("unmarshal", fmt.Errorf("missing or invalid UpdatedAt field"))
	}
	updatedAt, err := time.Parse(time.RFC3339, updatedAtStr)
	if err != nil {
		return dberrors.NewDatabaseError("parse_time", err)
	}
	item.UpdatedAt = updatedAt

	return nil
}

// Update implements the Repository interface.
// For models that implement Versionable (e.g. Item), optimistic locking is enforced:
// the entity is fetched first to compare versions, and the ETag from the GET response
// is passed to UpdateEntity so Azure Table Storage rejects stale writes.
func (r *TableRepository) Update(ctx context.Context, entity interface{}) error {
	item, ok := entity.(*models.Item)
	if !ok {
		return dberrors.NewDatabaseError("type_assertion", fmt.Errorf("entity must be *models.Item"))
	}

	if item.ID == 0 {
		return dberrors.NewDatabaseError("update", dberrors.ErrValidation)
	}

	// Fetch existing entity (also validates existence)
	existing, err := r.client.GetEntity(ctx, "items", strconv.FormatUint(uint64(item.ID), 10), nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return dberrors.NewDatabaseError("update", dberrors.ErrNotFound)
		}
		return dberrors.NewDatabaseError("find", err)
	}

	// Parse existing entity once for version checking and CreatedAt preservation.
	var existingData map[string]interface{}
	if err := json.Unmarshal(existing.Value, &existingData); err != nil {
		return dberrors.NewDatabaseError("unmarshal", err)
	}

	// Optimistic locking: compare version if the entity is Versionable
	if ver, ok := entity.(models.Versionable); ok {
		currentVersion := ver.GetVersion()

		// Default stored version to 1 for legacy rows that predate versioning,
		// consistent with the model default and FindByID behavior.
		storedVersionUint := uint(1)

		if storedVersion, ok := existingData["Version"]; ok {
			switch v := storedVersion.(type) {
			case float64:
				// Only treat strictly positive values as valid stored versions.
				// Non-positive values leave the default of 1, matching FindByID.
				if v > 0 {
					storedVersionUint = uint(v)
				}
			case json.Number:
				if n, err := v.Int64(); err == nil && n > 0 {
					storedVersionUint = uint(n)
				}
			}
		}

		if currentVersion != storedVersionUint {
			return dberrors.NewDatabaseError("update", errors.New("version mismatch"))
		}

		// Increment version for the update
		ver.SetVersion(currentVersion + 1)
	}

	// Create Azure Table entity
	now := time.Now().UTC()

	// Preserve the stored CreatedAt so callers that skip FindByID before
	// updating don't accidentally clobber it with a zero time.
	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		if caStr, ok := existingData["CreatedAt"].(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339, caStr); parseErr == nil {
				createdAt = parsed
			}
		}
	}

	entityJson := map[string]interface{}{
		"PartitionKey": "items",
		"RowKey":       strconv.FormatUint(uint64(item.ID), 10),
		"Name":         item.Name,
		"Price":        item.Price,
		"Version":      item.Version,
		"CreatedAt":    createdAt.Format(time.RFC3339),
		"UpdatedAt":    now.Format(time.RFC3339),
	}

	entityBytes, err := json.Marshal(entityJson)
	if err != nil {
		return dberrors.NewDatabaseError("marshal", err)
	}

	// Use ETag from the GET response for conditional update
	updateOpts := &aztables.UpdateEntityOptions{
		IfMatch:    &existing.ETag,
		UpdateMode: aztables.UpdateModeMerge,
	}
	_, err = r.client.UpdateEntity(ctx, entityBytes, updateOpts)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 412 {
			// Precondition failed — concurrent modification
			if ver, ok := entity.(models.Versionable); ok {
				ver.SetVersion(ver.GetVersion() - 1) // Roll back
			}
			return dberrors.NewDatabaseError("update", errors.New("version mismatch"))
		}
		return dberrors.NewDatabaseError("update", err)
	}

	item.UpdatedAt = now
	return nil
}

// Delete implements the Repository interface
func (r *TableRepository) Delete(ctx context.Context, entity interface{}) error {
	item, ok := entity.(*models.Item)
	if !ok {
		return dberrors.NewDatabaseError("type_assertion", fmt.Errorf("entity must be *models.Item"))
	}

	if item.ID == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrValidation)
	}

	_, err := r.client.DeleteEntity(ctx, "items", strconv.FormatUint(uint64(item.ID), 10), nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
		}
		return dberrors.NewDatabaseError("delete", err)
	}

	return nil
}

// List implements the Repository interface
func (r *TableRepository) List(ctx context.Context, dest interface{}, conditions ...interface{}) error {
	items, ok := dest.(*[]models.Item)
	if !ok {
		return dberrors.NewDatabaseError("type_assertion", fmt.Errorf("dest must be *[]models.Item"))
	}

	// Process conditions
	var (
		result             []models.Item
		pagination         *models.Pagination
		nameContainsFilter string
	)

	// Base filter for partition key
	filterParts := []string{"PartitionKey eq 'items'"}

	// Build filters from conditions
	for _, condition := range conditions {
		switch cond := condition.(type) {
		case models.Filter:
			switch cond.Field {
			case "name":
				name := cond.Value.(string)
				if cond.Op == "exact" {
					filterParts = append(filterParts, fmt.Sprintf("Name eq '%s'", name))
				} else {
					nameContainsFilter = strings.ToLower(name)
				}
			case "price":
				price := cond.Value.(float64)
				if cond.Op == ">=" {
					filterParts = append(filterParts, fmt.Sprintf("Price ge %f", price))
				} else if cond.Op == "<=" {
					filterParts = append(filterParts, fmt.Sprintf("Price le %f", price))
				}
			}
		case models.Pagination:
			pagination = &cond
		}
	}

	// Combine all filter parts with AND
	filter := strings.Join(filterParts, " and ")

	// Get pager for table query
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	// Fetch and process all entities
	for pager.More() {
		response, err := pager.NextPage(ctx)
		if err != nil {
			return dberrors.NewDatabaseError("list", err)
		}

		for _, entityBytes := range response.Entities {
			var entityData map[string]interface{}
			if err := json.Unmarshal(entityBytes, &entityData); err != nil {
				return dberrors.NewDatabaseError("unmarshal", err)
			}

			rowKey, ok := entityData["RowKey"].(string)
			if !ok || rowKey == "" {
				return dberrors.NewDatabaseError("list", fmt.Errorf("entity missing or invalid RowKey"))
			}
			id, err := strconv.ParseUint(rowKey, 10, 64)
			if err != nil {
				return dberrors.NewDatabaseError("list", fmt.Errorf("invalid RowKey %q: %w", rowKey, err))
			}

			createdAtStr, ok := entityData["CreatedAt"].(string)
			if !ok || createdAtStr == "" {
				return dberrors.NewDatabaseError("list", fmt.Errorf("entity missing or invalid CreatedAt"))
			}
			createdAt, err := time.Parse(time.RFC3339, createdAtStr)
			if err != nil {
				return dberrors.NewDatabaseError("list", fmt.Errorf("invalid CreatedAt %q: %w", createdAtStr, err))
			}

			updatedAtStr, ok := entityData["UpdatedAt"].(string)
			if !ok || updatedAtStr == "" {
				return dberrors.NewDatabaseError("list", fmt.Errorf("entity missing or invalid UpdatedAt"))
			}
			updatedAt, err := time.Parse(time.RFC3339, updatedAtStr)
			if err != nil {
				return dberrors.NewDatabaseError("list", fmt.Errorf("invalid UpdatedAt %q: %w", updatedAtStr, err))
			}

			name, ok := entityData["Name"].(string)
			if !ok {
				return dberrors.NewDatabaseError("list", fmt.Errorf("entity missing or invalid Name"))
			}

			price, ok := entityData["Price"].(float64)
			if !ok {
				return dberrors.NewDatabaseError("list", fmt.Errorf("entity missing or invalid Price"))
			}

			item := models.Item{
				Base: models.Base{
					ID:        uint(id),
					CreatedAt: createdAt,
					UpdatedAt: updatedAt,
				},
				Name:  name,
				Price: price,
			}

			// Populate Version if present; default to 1 when absent or invalid
			if v, ok := entityData["Version"]; ok {
				if vf, ok := v.(float64); ok {
					item.Version = uint(vf)
				} else {
					item.Version = 1
				}
			} else {
				item.Version = 1
			}

			// Apply name contains filter if specified
			if nameContainsFilter != "" {
				if !strings.Contains(strings.ToLower(item.Name), nameContainsFilter) {
					continue
				}
			}

			result = append(result, item)
		}
	}

	// Apply pagination after all filtering
	if pagination != nil {
		start := pagination.Offset
		if start >= len(result) {
			*items = []models.Item{}
			return nil
		}

		end := start + pagination.Limit
		if end > len(result) {
			end = len(result)
		}
		result = result[start:end]
	}

	*items = result
	return nil
}

// Ping implements the Repository interface
func (r *TableRepository) Ping(ctx context.Context) error {
	// List tables to check connectivity
	pager := r.client.NewListEntitiesPager(nil)
	_, err := pager.NextPage(ctx)
	if err != nil {
		return dberrors.NewDatabaseError("ping", err)
	}
	return nil
}

// Close implements the Repository interface. Azure Table Storage uses HTTP
// clients that don't require explicit cleanup, so this is a no-op.
func (r *TableRepository) Close() error {
	return nil
}

// Helper functions for error handling

// IsTableExistsError checks if the error is a TableAlreadyExists error
func IsTableExistsError(err error) bool {
	var respErr *azcore.ResponseError
	return err != nil && errors.As(err, &respErr) && respErr.ErrorCode == "TableAlreadyExists"
}

// IsEntityExistsError checks if the error is an EntityAlreadyExists error
func IsEntityExistsError(err error) bool {
	var respErr *azcore.ResponseError
	return err != nil && errors.As(err, &respErr) && respErr.ErrorCode == "EntityAlreadyExists"
}

// IsNotFoundError checks if the error is a 404 Not Found error
func IsNotFoundError(err error) bool {
	var respErr *azcore.ResponseError
	return err != nil && errors.As(err, &respErr) && respErr.StatusCode == 404
}
