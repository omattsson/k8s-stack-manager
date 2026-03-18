package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// UserRepository implements models.UserRepository for Azure Table Storage.
// Partition key: "users", Row key: username.
type UserRepository struct {
	client    AzureTableClient
	tableName string
}

// NewUserRepository creates a new Azure Table Storage user repository.
func NewUserRepository(accountName, accountKey, endpoint string, useAzurite bool) (*UserRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "Users", useAzurite)
	if err != nil {
		return nil, err
	}
	return &UserRepository{client: client, tableName: "Users"}, nil
}

// NewTestUserRepository creates a UserRepository for unit testing without connecting.
func NewTestUserRepository() *UserRepository {
	return &UserRepository{tableName: "Users"}
}

// SetTestClient injects a mock client for testing.
func (r *UserRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *UserRepository) Create(user *models.User) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if user.ID == "" {
		user.ID = newID()
	}
	if user.Username == "" {
		return dberrors.NewDatabaseError("create", dberrors.ErrValidation)
	}

	user.CreatedAt = now
	user.UpdatedAt = now

	entity := map[string]interface{}{
		"PartitionKey": "users",
		"RowKey":       user.Username,
		"ID":           user.ID,
		"Username":     user.Username,
		"PasswordHash": user.PasswordHash,
		"DisplayName":  user.DisplayName,
		"Role":         user.Role,
		"CreatedAt":    now.Format(time.RFC3339),
		"UpdatedAt":    now.Format(time.RFC3339),
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

func (r *UserRepository) FindByID(id string) (*models.User, error) {
	ctx := context.Background()

	// ID is not the row key, so we must scan with a filter.
	filter := "PartitionKey eq 'users' and ID eq '" + id + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}

	return userFromEntity(entities[0]), nil
}

func (r *UserRepository) FindByUsername(username string) (*models.User, error) {
	ctx := context.Background()

	// Direct point query — PK="users", RK=username.
	resp, err := r.client.GetEntity(ctx, "users", username, nil)
	if err != nil {
		return nil, mapAzureError("find_by_username", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}

	return userFromEntity(entity), nil
}

func (r *UserRepository) Update(user *models.User) error {
	ctx := context.Background()
	now := time.Now().UTC()
	user.UpdatedAt = now

	entity := map[string]interface{}{
		"PartitionKey": "users",
		"RowKey":       user.Username,
		"ID":           user.ID,
		"Username":     user.Username,
		"PasswordHash": user.PasswordHash,
		"DisplayName":  user.DisplayName,
		"Role":         user.Role,
		"CreatedAt":    user.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":    now.Format(time.RFC3339),
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

func (r *UserRepository) Delete(id string) error {
	ctx := context.Background()

	// We need to find the username (row key) from the ID first.
	user, err := r.FindByID(id)
	if err != nil {
		return err
	}

	_, err = r.client.DeleteEntity(ctx, "users", user.Username, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *UserRepository) List() ([]models.User, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'users'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	users := make([]models.User, 0, len(entities))
	for _, e := range entities {
		users = append(users, *userFromEntity(e))
	}
	return users, nil
}

func userFromEntity(e map[string]interface{}) *models.User {
	return &models.User{
		ID:           getString(e, "ID"),
		Username:     getString(e, "Username"),
		PasswordHash: getString(e, "PasswordHash"),
		DisplayName:  getString(e, "DisplayName"),
		Role:         getString(e, "Role"),
		CreatedAt:    parseTime(e, "CreatedAt"),
		UpdatedAt:    parseTime(e, "UpdatedAt"),
	}
}
