package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// User repository constants.
const (
	tableUsers      = "Users"
	fieldUsername    = "Username"
	fieldEmail       = "Email"
	fieldDisplayName = "DisplayName"
	fieldRole        = "Role"
	fieldPasswordHash = "PasswordHash"
	fieldAuthProvider = "AuthProvider"
	fieldExternalID   = "ExternalID"
	fieldCreatedAt    = "CreatedAt"
	fieldUpdatedAt    = "UpdatedAt"
	fieldPartitionKey = "PartitionKey"
	fieldRowKey       = "RowKey"
)


// UserRepository implements models.UserRepository for Azure Table Storage.
// Partition key: "users", Row key: username.
type UserRepository struct {
	client    AzureTableClient
	tableName string
}

// derefString returns the value of a *string or "" if nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// NewUserRepository creates a new Azure Table Storage user repository.
func NewUserRepository(accountName, accountKey, endpoint string, useAzurite bool) (*UserRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableUsers, useAzurite)
	if err != nil {
		return nil, err
	}
	return &UserRepository{client: client, tableName: tableUsers}, nil
}

// NewTestUserRepository creates a UserRepository for unit testing without connecting.
func NewTestUserRepository() *UserRepository {
	return &UserRepository{tableName: tableUsers}
}

// SetTestClient injects a mock client for testing.
func (r *UserRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// userEntity is the typed Azure Table entity for users.
type userEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	ID           string `json:"ID"`
	Username     string `json:"Username"`
	PasswordHash string `json:"PasswordHash"`
	DisplayName  string `json:"DisplayName"`
	Role         string `json:"Role"`
	AuthProvider string `json:"AuthProvider"`
	ExternalID   string `json:"ExternalID"`
	Email        string `json:"Email"`
	CreatedAt    string `json:"CreatedAt"`
	UpdatedAt    string `json:"UpdatedAt"`
}

func (e *userEntity) toModel() *models.User {
	u := &models.User{
		ID:           e.ID,
		Username:     e.Username,
		PasswordHash: e.PasswordHash,
		DisplayName:  e.DisplayName,
		Role:         e.Role,
		AuthProvider: e.AuthProvider,
		Email:        e.Email,
	}
	if u.AuthProvider == "" {
		u.AuthProvider = "local"
	}
	if e.ExternalID != "" {
		u.ExternalID = &e.ExternalID
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return u
}

func (r *UserRepository) Create(user *models.User) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if user.ID == "" {
		user.ID = newID()
	}
	if user.Username == "" {
		return dberrors.NewDatabaseError(opCreate, dberrors.ErrValidation)
	}

	user.CreatedAt = now
	user.UpdatedAt = now

	entity := map[string]interface{}{
		fieldPartitionKey: pkUsers,
		fieldRowKey:       user.Username,
		"ID":           user.ID,
		fieldUsername:     user.Username,
		fieldPasswordHash: user.PasswordHash,
		fieldDisplayName:  user.DisplayName,
		fieldRole:         user.Role,
		fieldAuthProvider: user.AuthProvider,
		fieldExternalID:   derefString(user.ExternalID),
		fieldEmail:        user.Email,
		fieldCreatedAt:    now.Format(time.RFC3339),
		fieldUpdatedAt:    now.Format(time.RFC3339),
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

func (r *UserRepository) FindByID(id string) (*models.User, error) {
	ctx := context.Background()

	// ID is not the row key, so we must scan with a filter.
	filter := odataPartitionKeyEq + pkUsers + "' and ID eq '" + id + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[userEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}

	return entities[0].toModel(), nil
}

func (r *UserRepository) FindByUsername(username string) (*models.User, error) {
	ctx := context.Background()

	// Direct point query — PK="users", RK=username.
	resp, err := r.client.GetEntity(ctx, pkUsers, username, nil)
	if err != nil {
		return nil, mapAzureError("find_by_username", err)
	}

	var entity userEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError(opUnmarshal, err)
	}

	return entity.toModel(), nil
}

func (r *UserRepository) FindByExternalID(provider, externalID string) (*models.User, error) {
	ctx := context.Background()

	filter := odataPartitionKeyEq + pkUsers + "' and AuthProvider eq '" + escapeODataString(provider) + "' and ExternalID eq '" + escapeODataString(externalID) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[userEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("find_by_external_id", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_external_id", dberrors.ErrNotFound)
	}

	return entities[0].toModel(), nil
}

func (r *UserRepository) Update(user *models.User) error {
	ctx := context.Background()
	now := time.Now().UTC()
	user.UpdatedAt = now

	entity := map[string]interface{}{
		fieldPartitionKey: pkUsers,
		fieldRowKey:       user.Username,
		"ID":           user.ID,
		fieldUsername:     user.Username,
		fieldPasswordHash: user.PasswordHash,
		fieldDisplayName:  user.DisplayName,
		fieldRole:         user.Role,
		fieldAuthProvider: user.AuthProvider,
		fieldExternalID:   derefString(user.ExternalID),
		fieldEmail:        user.Email,
		fieldCreatedAt:    user.CreatedAt.Format(time.RFC3339),
		fieldUpdatedAt:    now.Format(time.RFC3339),
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

func (r *UserRepository) Delete(id string) error {
	ctx := context.Background()

	// We need to find the username (row key) from the ID first.
	user, err := r.FindByID(id)
	if err != nil {
		return err
	}

	_, err = r.client.DeleteEntity(ctx, pkUsers, user.Username, nil)
	if err != nil {
		return mapAzureError(opDelete, err)
	}
	return nil
}

func (r *UserRepository) Count() (int64, error) {
	items, err := r.List()
	if err != nil {
		return 0, err
	}
	return int64(len(items)), nil
}

func (r *UserRepository) List() ([]models.User, error) {
	ctx := context.Background()

	filter := odataPartitionKeyEq + pkUsers + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[userEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError(opList, err)
	}

	users := make([]models.User, 0, len(entities))
	for _, e := range entities {
		users = append(users, *e.toModel())
	}
	return users, nil
}
