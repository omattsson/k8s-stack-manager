package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// ListEntitiesPager is the interface for paging through Azure Table Storage entities
type ListEntitiesPager interface {
	More() bool
	NextPage(context.Context) (aztables.ListEntitiesResponse, error)
}

// AzureTableClient defines the interface for Azure Table operations
type AzureTableClient interface {
	AddEntity(ctx context.Context, entity []byte, options *aztables.AddEntityOptions) (aztables.AddEntityResponse, error)
	GetEntity(ctx context.Context, partitionKey, rowKey string, options *aztables.GetEntityOptions) (aztables.GetEntityResponse, error)
	UpdateEntity(ctx context.Context, entity []byte, options *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error)
	UpsertEntity(ctx context.Context, entity []byte, options *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error)
	DeleteEntity(ctx context.Context, partitionKey, rowKey string, options *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error)
	NewListEntitiesPager(options *aztables.ListEntitiesOptions) ListEntitiesPager
}
