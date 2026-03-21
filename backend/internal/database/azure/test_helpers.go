package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// testPager implements azure.ListEntitiesPager for testing
type testPager struct {
	pages   [][]byte
	current int
	err     error
}

func (p *testPager) More() bool {
	return p.current < len(p.pages)
}

func (p *testPager) NextPage(ctx context.Context) (aztables.ListEntitiesResponse, error) {
	if p.err != nil {
		return aztables.ListEntitiesResponse{}, p.err
	}

	if !p.More() {
		return aztables.ListEntitiesResponse{}, nil
	}

	entityBytes := make([][]byte, 0)
	for i := 0; i < 2 && p.More(); i++ {
		entityBytes = append(entityBytes, p.pages[p.current])
		p.current++
	}

	return aztables.ListEntitiesResponse{Entities: entityBytes}, nil
}

// testClient implements azure.AzureTableClient for testing
type testClient struct {
	addEntity    func(context.Context, []byte, *aztables.AddEntityOptions) (aztables.AddEntityResponse, error)
	getEntity    func(context.Context, string, string, *aztables.GetEntityOptions) (aztables.GetEntityResponse, error)
	updateEntity func(context.Context, []byte, *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error)
	upsertEntity func(context.Context, []byte, *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error)
	deleteEntity func(context.Context, string, string, *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error)
	pager        *testPager
}

func (m *testClient) AddEntity(ctx context.Context, entity []byte, options *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
	if m.addEntity != nil {
		return m.addEntity(ctx, entity, options)
	}
	return aztables.AddEntityResponse{}, nil
}

func (m *testClient) GetEntity(ctx context.Context, partitionKey, rowKey string, options *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
	if m.getEntity != nil {
		return m.getEntity(ctx, partitionKey, rowKey, options)
	}
	return aztables.GetEntityResponse{}, nil
}

func (m *testClient) UpdateEntity(ctx context.Context, entity []byte, options *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
	if m.updateEntity != nil {
		return m.updateEntity(ctx, entity, options)
	}
	return aztables.UpdateEntityResponse{}, nil
}

func (m *testClient) UpsertEntity(ctx context.Context, entity []byte, options *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error) {
	if m.upsertEntity != nil {
		return m.upsertEntity(ctx, entity, options)
	}
	return aztables.UpsertEntityResponse{}, nil
}

func (m *testClient) DeleteEntity(ctx context.Context, partitionKey, rowKey string, options *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
	if m.deleteEntity != nil {
		return m.deleteEntity(ctx, partitionKey, rowKey, options)
	}
	return aztables.DeleteEntityResponse{}, nil
}

func (m *testClient) NewListEntitiesPager(options *aztables.ListEntitiesOptions) ListEntitiesPager {
	if m.pager != nil {
		return m.pager
	}
	return &testPager{} // Return empty pager by default
}
