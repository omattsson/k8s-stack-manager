package models

import (
	"errors"
	"strings"
	"testing"

	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Item.Validate tests (currently at 0%)
// ---------------------------------------------------------------------------

func TestItemValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		item    Item
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid item",
			item:    Item{Base: Base{ID: 1}, Name: "Widget", Price: 9.99, Version: 1},
			wantErr: false,
		},
		{
			name:    "empty name",
			item:    Item{Price: 5.0},
			wantErr: true,
			errMsg:  ErrEmptyItemName.Error(),
		},
		{
			name:    "zero price",
			item:    Item{Name: "Widget", Price: 0},
			wantErr: true,
			errMsg:  ErrInvalidPrice.Error(),
		},
		{
			name:    "negative price",
			item:    Item{Name: "Widget", Price: -1.5},
			wantErr: true,
			errMsg:  ErrInvalidPrice.Error(),
		},
		{
			name:    "empty name takes precedence over bad price",
			item:    Item{Price: -1},
			wantErr: true,
			errMsg:  ErrEmptyItemName.Error(),
		},
		{
			name:    "valid item with minimal price",
			item:    Item{Name: "Tiny", Price: 0.01},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.item.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Versionable interface tests (GetVersion / SetVersion at 0%)
// ---------------------------------------------------------------------------

func TestItemGetVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version uint
	}{
		{name: "default version 0", version: 0},
		{name: "version 1", version: 1},
		{name: "large version", version: 99999},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			item := &Item{Version: tt.version}
			assert.Equal(t, tt.version, item.GetVersion())
		})
	}
}

func TestItemSetVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		initial    uint
		setTo      uint
		expectAfter uint
	}{
		{name: "set from 0 to 1", initial: 0, setTo: 1, expectAfter: 1},
		{name: "set from 1 to 2", initial: 1, setTo: 2, expectAfter: 2},
		{name: "set to 0", initial: 5, setTo: 0, expectAfter: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			item := &Item{Version: tt.initial}
			item.SetVersion(tt.setTo)
			assert.Equal(t, tt.expectAfter, item.GetVersion())
		})
	}
}

func TestItemVersionableInterface(t *testing.T) {
	t.Parallel()

	// Verify Item satisfies Versionable at compile time and at runtime.
	var v Versionable = &Item{Version: 3}
	assert.Equal(t, uint(3), v.GetVersion())
	v.SetVersion(4)
	assert.Equal(t, uint(4), v.GetVersion())
}

// ---------------------------------------------------------------------------
// Item satisfies Validator interface
// ---------------------------------------------------------------------------

func TestItemValidatorInterface(t *testing.T) {
	t.Parallel()

	var v Validator = &Item{Name: "Test", Price: 1.0}
	assert.NoError(t, v.Validate())
}

// ---------------------------------------------------------------------------
// handleError tests (currently at 0%) — only needs error inspection, no DB
// ---------------------------------------------------------------------------

func TestHandleError(t *testing.T) {
	t.Parallel()

	repo := &GenericRepository{db: nil, allowedFilterFields: nil}

	tests := []struct {
		name      string
		op        string
		err       error
		expectNil bool
		wantSentinel error
		wantOp    string
	}{
		{
			name:      "nil error returns nil",
			op:        "find",
			err:       nil,
			expectNil: true,
		},
		{
			name:         "gorm ErrRecordNotFound maps to ErrNotFound",
			op:           "find",
			err:          gorm.ErrRecordNotFound,
			wantSentinel: dberrors.ErrNotFound,
			wantOp:       "find",
		},
		{
			name:         "duplicate entry maps to ErrDuplicateKey",
			op:           "create",
			err:          errors.New("Duplicate entry 'foo' for key 'name'"),
			wantSentinel: dberrors.ErrDuplicateKey,
			wantOp:       "create",
		},
		{
			name:         "validation failed maps to ErrValidation",
			op:           "update",
			err:          errors.New("validation failed: some field"),
			wantSentinel: dberrors.ErrValidation,
			wantOp:       "update",
		},
		{
			name:   "unknown error passes through",
			op:     "delete",
			err:    errors.New("connection timed out"),
			wantOp: "delete",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := repo.handleError(tt.op, tt.err)
			if tt.expectNil {
				assert.NoError(t, result)
				return
			}
			assert.Error(t, result)
			var dbErr *dberrors.DatabaseError
			assert.True(t, errors.As(result, &dbErr), "expected *dberrors.DatabaseError")
			assert.Equal(t, tt.wantOp, dbErr.Op)
			if tt.wantSentinel != nil {
				assert.True(t, errors.Is(dbErr.Err, tt.wantSentinel),
					"expected sentinel %v, got %v", tt.wantSentinel, dbErr.Err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Filter and Pagination struct tests
// ---------------------------------------------------------------------------

func TestFilterStruct(t *testing.T) {
	t.Parallel()

	f := Filter{Field: "name", Op: "exact", Value: "foo"}
	assert.Equal(t, "name", f.Field)
	assert.Equal(t, "exact", f.Op)
	assert.Equal(t, "foo", f.Value)
}

func TestPaginationStruct(t *testing.T) {
	t.Parallel()

	p := Pagination{Limit: 10, Offset: 20}
	assert.Equal(t, 10, p.Limit)
	assert.Equal(t, 20, p.Offset)
}

// ---------------------------------------------------------------------------
// NewRepository / NewRepositoryWithFilterFields — constructor tests
// ---------------------------------------------------------------------------

func TestNewRepository(t *testing.T) {
	t.Parallel()

	repo := NewRepository(nil)
	assert.NotNil(t, repo)
	gr, ok := repo.(*GenericRepository)
	assert.True(t, ok, "expected *GenericRepository")
	// Default filter fields for Item: "name" and "price"
	assert.True(t, gr.allowedFilterFields["name"])
	assert.True(t, gr.allowedFilterFields["price"])
	assert.False(t, gr.allowedFilterFields["id"])
	assert.Len(t, gr.allowedFilterFields, 2)
}

func TestNewRepositoryWithFilterFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fields []string
		expect map[string]bool
	}{
		{
			name:   "custom fields",
			fields: []string{"status", "owner_id", "cluster_id"},
			expect: map[string]bool{"status": true, "owner_id": true, "cluster_id": true},
		},
		{
			name:   "empty fields",
			fields: []string{},
			expect: map[string]bool{},
		},
		{
			name:   "single field",
			fields: []string{"name"},
			expect: map[string]bool{"name": true},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewRepositoryWithFilterFields(nil, tt.fields)
			gr, ok := repo.(*GenericRepository)
			assert.True(t, ok)
			assert.Equal(t, tt.expect, gr.allowedFilterFields)
		})
	}
}

// ---------------------------------------------------------------------------
// StackInstance.Validate — additional edge cases not in validation_test.go
// ---------------------------------------------------------------------------

func TestStackInstanceValidate_NameLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inst    StackInstance
		wantErr bool
		errContains string
	}{
		{
			name: "name exactly at max length",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              strings.Repeat("a", MaxInstanceNameLength),
				OwnerID:           "o1",
			},
		},
		{
			name: "name exceeds max length",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "a" + string(make([]byte, MaxInstanceNameLength)),
				OwnerID:           "o1",
			},
			wantErr:     true,
			errContains: "at most 50 characters",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.inst.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStackInstanceValidate_Namespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inst    StackInstance
		wantErr bool
		errContains string
	}{
		{
			name: "valid namespace",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "stack-my-stack-owner",
			},
			wantErr: false,
		},
		{
			name: "empty namespace is valid (auto-generated)",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "",
			},
			wantErr: false,
		},
		{
			name: "namespace too long",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "a234567890123456789012345678901234567890123456789012345678901234", // 64 chars
			},
			wantErr:     true,
			errContains: "at most 63 characters",
		},
		{
			name: "namespace at max length 63",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "a23456789012345678901234567890123456789012345678901234567890123", // 63 chars
			},
			wantErr: false,
		},
		{
			name: "namespace starts with dash",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "-invalid",
			},
			wantErr:     true,
			errContains: "RFC 1123",
		},
		{
			name: "namespace ends with dash",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "invalid-",
			},
			wantErr:     true,
			errContains: "RFC 1123",
		},
		{
			name: "namespace with uppercase",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "Invalid",
			},
			wantErr:     true,
			errContains: "RFC 1123",
		},
		{
			name: "namespace with underscore",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "invalid_ns",
			},
			wantErr:     true,
			errContains: "RFC 1123",
		},
		{
			name: "single char namespace",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "a",
			},
			wantErr: false,
		},
		{
			name: "namespace with dots",
			inst: StackInstance{
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "o1",
				Namespace:         "has.dot",
			},
			wantErr:     true,
			errContains: "RFC 1123",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.inst.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cluster.Validate — additional edge cases
// ---------------------------------------------------------------------------

func TestClusterValidate_MaxNamespaces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		maxNamespaces int
		wantErr       bool
	}{
		{name: "zero is valid", maxNamespaces: 0, wantErr: false},
		{name: "positive is valid", maxNamespaces: 100, wantErr: false},
		{name: "negative is invalid", maxNamespaces: -1, wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := Cluster{
				Name:           "test",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
				MaxNamespaces:  tt.maxNamespaces,
			}
			err := c.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, "max_namespaces must be non-negative")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestClusterValidate_AllHealthStatuses(t *testing.T) {
	t.Parallel()

	validStatuses := []string{"", ClusterHealthy, ClusterDegraded, ClusterUnreachable}
	for _, status := range validStatuses {
		status := status
		t.Run("valid_"+status, func(t *testing.T) {
			t.Parallel()
			c := Cluster{
				Name:           "test",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
				HealthStatus:   status,
			}
			assert.NoError(t, c.Validate())
		})
	}
}

// ---------------------------------------------------------------------------
// SharedValues.Validate — additional YAML edge cases
// ---------------------------------------------------------------------------

func TestSharedValuesValidate_YAMLEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  string
		wantErr bool
	}{
		{
			name:    "valid nested YAML",
			values:  "global:\n  image:\n    tag: latest\n  replicas: 3",
			wantErr: false,
		},
		{
			name:    "valid simple key-value",
			values:  "key: value",
			wantErr: false,
		},
		{
			name:    "invalid YAML with tabs in wrong places",
			values:  "key:\n\t\tbad: yaml: [",
			wantErr: true,
		},
		{
			name:    "scalar YAML is invalid mapping",
			values:  "just a string",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sv := SharedValues{
				Name:      "test",
				ClusterID: "c1",
				Values:    tt.values,
			}
			err := sv.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CleanupPolicy.Validate — additional cron and action edge cases
// ---------------------------------------------------------------------------

func TestCleanupPolicyValidate_AllValidActions(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"stop", "clean", "delete"} {
		action := action
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			cp := CleanupPolicy{
				Name:      "test-" + action,
				Action:    action,
				Condition: "idle_days:7",
				Schedule:  "0 2 * * *",
				ClusterID: "c1",
			}
			assert.NoError(t, cp.Validate())
		})
	}
}

func TestCleanupPolicyValidate_CronEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		schedule string
		wantErr  bool
	}{
		{name: "every minute", schedule: "* * * * *", wantErr: false},
		{name: "every 5 minutes", schedule: "*/5 * * * *", wantErr: false},
		{name: "at midnight daily", schedule: "0 0 * * *", wantErr: false},
		{name: "weekdays at 9am", schedule: "0 9 * * 1-5", wantErr: false},
		{name: "empty schedule caught before cron parse", schedule: "", wantErr: true},
		{name: "six-field cron rejected", schedule: "0 0 0 * * *", wantErr: true},
		{name: "garbage string", schedule: "hello world", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cp := CleanupPolicy{
				Name:      "test",
				Action:    "stop",
				Condition: "idle_days:7",
				Schedule:  tt.schedule,
				ClusterID: "c1",
			}
			err := cp.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateFavoriteEntityType — standalone function coverage
// ---------------------------------------------------------------------------

func TestValidateFavoriteEntityType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		entityType string
		want       bool
	}{
		{"definition", true},
		{"instance", true},
		{"template", true},
		{"cluster", false},
		{"user", false},
		{"", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.entityType, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ValidateFavoriteEntityType(tt.entityType))
		})
	}
}

// ---------------------------------------------------------------------------
// Constants sanity checks
// ---------------------------------------------------------------------------

func TestStackStatusConstants(t *testing.T) {
	t.Parallel()

	statuses := []string{
		StackStatusDraft, StackStatusQueued, StackStatusDeploying,
		StackStatusRunning, StackStatusStopping, StackStatusStopped,
		StackStatusCleaning, StackStatusError,
	}
	seen := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		assert.NotEmpty(t, s, "status constant should not be empty")
		assert.False(t, seen[s], "duplicate status constant: %s", s)
		seen[s] = true
	}
}

func TestClusterHealthConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "healthy", ClusterHealthy)
	assert.Equal(t, "degraded", ClusterDegraded)
	assert.Equal(t, "unreachable", ClusterUnreachable)
}

func TestDeploymentLogConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "deploy", DeployActionDeploy)
	assert.Equal(t, "stop", DeployActionStop)
	assert.Equal(t, "clean", DeployActionClean)
	assert.Equal(t, "running", DeployLogRunning)
	assert.Equal(t, "success", DeployLogSuccess)
	assert.Equal(t, "error", DeployLogError)
}

func TestMaxLengthConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 50, MaxInstanceNameLength)
	assert.Equal(t, 63, MaxNamespaceLength)
}
