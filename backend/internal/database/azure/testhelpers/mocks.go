package testhelpers

import (
	"context"

	"backend/internal/database/azure"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// mockTablePager implements azure.ListEntitiesPager for testing
type MockTablePager struct {
	nextPage int
	pages    [][]byte
	err      error
}

// Verify interface compliance at compile time
var _ azure.ListEntitiesPager = (*MockTablePager)(nil)

// NewMockTablePager creates a new mock pager with the given pages and error
func NewMockTablePager(pages [][]byte, err error) *MockTablePager {
	return &MockTablePager{
		pages: pages,
		err:   err,
	}
}

// More implements azure.ListEntitiesPager
func (p *MockTablePager) More() bool {
	return p.nextPage < len(p.pages)
}

// NextPage implements azure.ListEntitiesPager
func (p *MockTablePager) NextPage(ctx context.Context) (aztables.ListEntitiesResponse, error) {
	if p.err != nil {
		return aztables.ListEntitiesResponse{}, p.err
	}

	if !p.More() {
		return aztables.ListEntitiesResponse{}, nil
	}

	response := aztables.ListEntitiesResponse{
		Entities: [][]byte{p.pages[p.nextPage]},
	}
	p.nextPage++
	return response, nil
}

// mockTableClient implements azure.AzureTableClient for testing
type MockTableClient struct {
	addEntityFn            func(ctx context.Context, entity []byte, options *aztables.AddEntityOptions) (aztables.AddEntityResponse, error)
	getEntityFn            func(ctx context.Context, partitionKey, rowKey string, options *aztables.GetEntityOptions) (aztables.GetEntityResponse, error)
	updateEntityFn         func(context.Context, []byte, *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error)
	deleteEntityFn         func(context.Context, string, string, *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error)
	newListEntitiesPagerFn func(*aztables.ListEntitiesOptions) azure.ListEntitiesPager
}

// Verify interface compliance at compile time
var _ azure.AzureTableClient = (*MockTableClient)(nil)

// NewMockTableClient creates a new mock table client with default no-op implementations
func NewMockTableClient() *MockTableClient {
	return &MockTableClient{
		addEntityFn: func(ctx context.Context, entity []byte, options *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
			return aztables.AddEntityResponse{}, nil
		},
		getEntityFn: func(ctx context.Context, partitionKey, rowKey string, options *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
			return aztables.GetEntityResponse{}, nil
		},
		updateEntityFn: func(ctx context.Context, entity []byte, options *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
			return aztables.UpdateEntityResponse{}, nil
		},
		deleteEntityFn: func(ctx context.Context, partitionKey, rowKey string, options *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
			return aztables.DeleteEntityResponse{}, nil
		},
		newListEntitiesPagerFn: func(options *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
			return &MockTablePager{}
		},
	}
}

// AddEntity implements azure.AzureTableClient
func (m *MockTableClient) AddEntity(ctx context.Context, entity []byte, options *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
	if m.addEntityFn != nil {
		return m.addEntityFn(ctx, entity, options)
	}
	return aztables.AddEntityResponse{}, nil
}

// GetEntity implements azure.AzureTableClient
func (m *MockTableClient) GetEntity(ctx context.Context, partitionKey, rowKey string, options *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
	if m.getEntityFn != nil {
		return m.getEntityFn(ctx, partitionKey, rowKey, options)
	}
	return aztables.GetEntityResponse{}, nil
}

// UpdateEntity implements azure.AzureTableClient
func (m *MockTableClient) UpdateEntity(ctx context.Context, entity []byte, options *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
	if m.updateEntityFn != nil {
		return m.updateEntityFn(ctx, entity, options)
	}
	return aztables.UpdateEntityResponse{}, nil
}

// DeleteEntity implements azure.AzureTableClient
func (m *MockTableClient) DeleteEntity(ctx context.Context, partitionKey, rowKey string, options *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
	if m.deleteEntityFn != nil {
		return m.deleteEntityFn(ctx, partitionKey, rowKey, options)
	}
	return aztables.DeleteEntityResponse{}, nil
}

// NewListEntitiesPager implements azure.AzureTableClient
func (m *MockTableClient) NewListEntitiesPager(options *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
	if m.newListEntitiesPagerFn != nil {
		return m.newListEntitiesPagerFn(options)
	}
	return &MockTablePager{}
}

// Setter methods for configuring mock behavior from external test packages.

func (m *MockTableClient) SetAddEntity(fn func(context.Context, []byte, *aztables.AddEntityOptions) (aztables.AddEntityResponse, error)) {
	m.addEntityFn = fn
}

func (m *MockTableClient) SetGetEntity(fn func(context.Context, string, string, *aztables.GetEntityOptions) (aztables.GetEntityResponse, error)) {
	m.getEntityFn = fn
}

func (m *MockTableClient) SetUpdateEntity(fn func(context.Context, []byte, *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error)) {
	m.updateEntityFn = fn
}

func (m *MockTableClient) SetDeleteEntity(fn func(context.Context, string, string, *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error)) {
	m.deleteEntityFn = fn
}

func (m *MockTableClient) SetNewListEntitiesPager(fn func(*aztables.ListEntitiesOptions) azure.ListEntitiesPager) {
	m.newListEntitiesPagerFn = fn
}
