package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/google/uuid"
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
		nil,
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

// collectEntities reads all pages from a pager, unmarshals each entity JSON, and applies
// an optional filter function. Returns the parsed entity maps.
func collectEntities(ctx context.Context, pager ListEntitiesPager, filterFn func(map[string]interface{}) bool) ([]map[string]interface{}, error) {
	var results []map[string]interface{}
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, entityBytes := range page.Entities {
			var entity map[string]interface{}
			if err := json.Unmarshal(entityBytes, &entity); err != nil {
				continue
			}
			if filterFn != nil && !filterFn(entity) {
				continue
			}
			results = append(results, entity)
		}
	}
	return results, nil
}

// getString safely extracts a string value from a map.
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// getStringPtr safely extracts a string value from a map, returning nil when
// the key is missing or the value is empty.
func getStringPtr(m map[string]interface{}, key string) *string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return &s
		}
	}
	return nil
}

// getStringDefault safely extracts a string value from a map, returning
// defaultVal when the key is missing or the value is empty.
func getStringDefault(m map[string]interface{}, key, defaultVal string) string {
	s := getString(m, key)
	if s == "" {
		return defaultVal
	}
	return s
}

// getFloat64 safely extracts a float64 value from a map.
func getFloat64(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case json.Number:
			f, _ := n.Float64()
			return f
		}
	}
	return 0
}

// getInt safely extracts an int value from a map.
func getInt(m map[string]interface{}, key string) int {
	return int(getFloat64(m, key))
}

// getBool safely extracts a bool value from a map.
func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// parseTime safely parses a time.Time from a map value.
func parseTime(m map[string]interface{}, key string) time.Time {
	s := getString(m, key)
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
