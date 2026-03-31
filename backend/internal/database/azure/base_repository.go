package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/google/uuid"
)

// Common OData filter prefix and partition key constants.
const (
	odataPartitionKeyEq = "PartitionKey eq '"
	pkGlobal            = "global"
	pkUsers             = "users"
	pkClusters          = "clusters"
)

// Common database operation names used in error wrapping.
const (
	opCreate        = "create"
	opFind          = "find"
	opUpdate        = "update"
	opDelete        = "delete"
	opList          = "list"
	opMarshal       = "marshal"
	opUnmarshal     = "unmarshal"
	opTypeAssertion = "type_assertion"
)


// newID generates a new UUID string for use as an entity ID.
func newID() string {
	return uuid.New().String()
}

// createTableClient creates an Azure Table Storage client for the given table.
func createTableClient(accountName, accountKey, endpoint, tableName string, useAzurite bool) (AzureTableClient, error) {
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
	serviceClient, err := aztables.NewServiceClientFromConnectionString(
		fmt.Sprintf("DefaultEndpointsProtocol=%s;AccountName=%s;AccountKey=%s;TableEndpoint=%s",
			protocol,
			accountName,
			accountKey,
			serviceURL),
		SharedClientOptions(DefaultTransportConfig()),
	)
	if err != nil {
		return nil, dberrors.NewDatabaseError("azure_client", err)
	}

	tableClient := serviceClient.NewClient(tableName)

	_, err = serviceClient.CreateTable(context.Background(), tableName, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) {
			if respErr.ErrorCode == "TableAlreadyExists" {
				return newAzureClientAdapter(tableClient), nil
			}
			return nil, fmt.Errorf("create_table: %v", respErr.RawResponse.Status)
		}
		return nil, dberrors.NewDatabaseError("create_table", err)
	}

	return newAzureClientAdapter(tableClient), nil
}

// mapAzureError maps Azure SDK errors to domain dberrors.
func mapAzureError(op string, err error) error {
	if err == nil {
		return nil
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.ErrorCode {
		case "EntityAlreadyExists":
			return dberrors.NewDatabaseError(op, dberrors.ErrDuplicateKey)
		case "ResourceNotFound", "EntityNotFound":
			return dberrors.NewDatabaseError(op, dberrors.ErrNotFound)
		}
		if respErr.StatusCode == 404 {
			return dberrors.NewDatabaseError(op, dberrors.ErrNotFound)
		}
	}
	return dberrors.NewDatabaseError(op, err)
}

// collectEntitiesTyped reads pages from a pager, unmarshals each entity JSON
// directly into a typed struct T (avoiding the intermediate map[string]interface{}),
// and applies an optional filter function. When maxResults > 0, iteration stops
// as soon as that many matching entities have been collected (0 means unlimited).
func collectEntitiesTyped[T any](ctx context.Context, pager ListEntitiesPager, filterFn func(*T) bool, maxResults int) ([]T, error) {
	var results []T
outer:
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, entityBytes := range page.Entities {
			var item T
			if err := json.Unmarshal(entityBytes, &item); err != nil {
				continue
			}
			if filterFn != nil && !filterFn(&item) {
				continue
			}
			results = append(results, item)
			if maxResults > 0 && len(results) >= maxResults {
				break outer
			}
		}
	}
	return results, nil
}

