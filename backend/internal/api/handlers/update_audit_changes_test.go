package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditedPut sends a PUT through a router that runs the audit middleware and
// returns the parsed details of the one audit entry.
func auditedPut(t *testing.T, build func(gin.HandlerFunc) *gin.Engine, path, body string) (map[string]any, string) {
	t.Helper()
	details, raw, _ := auditedRequest(t, build, http.MethodPut, path, body)
	return details, raw
}

// auditedRequest sends a request through a router that runs the audit
// middleware and returns the parsed details, the raw details and the entry.
func auditedRequest(t *testing.T, build func(gin.HandlerFunc) *gin.Engine, method, path, body string) (map[string]any, string, models.AuditLog) {
	t.Helper()
	rec := &auditRecorder{}
	r := build(middleware.NewAuditMiddleware(rec))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Less(t, w.Code, 300, w.Body.String())
	e := rec.waitOne(t)
	details := map[string]any{}
	if e.Details != "" {
		require.NoError(t, json.Unmarshal([]byte(e.Details), &details), e.Details)
	}
	return details, e.Details, e
}

// changeMap returns details.changes keyed by field.
func changeMap(t *testing.T, details map[string]any) map[string]map[string]any {
	t.Helper()
	list, ok := details["changes"].([]any)
	require.True(t, ok, "details have no changes: %v", details)
	out := map[string]map[string]any{}
	for _, item := range list {
		m := item.(map[string]any)
		out[m["field"].(string)] = m
	}
	return out
}

func TestUpdateCluster_AuditChanges(t *testing.T) {
	t.Parallel()
	clusters := NewMockClusterRepository()
	seedCluster(clusters, "cl-1", "prod")
	details, _ := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupClusterRouter(clusters, NewMockStackInstanceRepository(), nil, "admin", mw)
	}, "/api/v1/clusters/cl-1", `{"allow_exec":true}`)
	ch := changeMap(t, details)
	assert.Equal(t, map[string]any{"field": "allow_exec", "old": false, "new": true}, ch["allow_exec"])
	assert.Len(t, ch, 1)
}

func TestUpdateCluster_AuditRedactsSecrets(t *testing.T) {
	t.Parallel()
	clusters := NewMockClusterRepository()
	seedCluster(clusters, "cl-1", "prod")
	body := `{"kubeconfig_path":"/kube/secret-kc-path","registry_url":"registry.example.com","registry_username":"bot","registry_password":"secret-pw"}`
	details, raw := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupClusterRouter(clusters, NewMockStackInstanceRepository(), nil, "admin", mw)
	}, "/api/v1/clusters/cl-1", body)
	ch := changeMap(t, details)
	assert.Equal(t, map[string]any{"field": "kubeconfig", "redacted": true}, ch["kubeconfig"])
	assert.Equal(t, map[string]any{"field": "registry_password", "redacted": true}, ch["registry_password"])
	assert.NotContains(t, raw, "secret-kc-path")
	assert.NotContains(t, raw, "secret-pw")
}

func TestUpdateChartConfig_AuditNoValues(t *testing.T) {
	t.Parallel()
	defs := NewMockStackDefinitionRepository()
	_ = defs.Create(context.Background(), &models.StackDefinition{ID: "def-1", Name: "shop", OwnerID: "u-1"})
	charts := NewMockChartConfigRepository()
	seedChartConfig(t, charts, "chart-1", "def-1", "app")
	body := `{"default_values":"password: hunter2","repository_url":"https://u:tok@charts.example.com/x"}`
	details, raw := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupChartConfigRouter(defs, charts, NewMockTemplateChartConfigRepository(), "u-1", "devops", mw)
	}, "/api/v1/stack-definitions/def-1/charts/chart-1", body)
	ch := changeMap(t, details)
	assert.Contains(t, ch, "default_values")
	assert.Contains(t, ch, "default_values.password")
	assert.Contains(t, ch, "repository_url")
	assert.NotContains(t, raw, "hunter2")
	assert.NotContains(t, raw, "tok@")
}

func TestSetOverride_AuditKeyPaths(t *testing.T) {
	t.Parallel()
	insts, overrides := NewMockStackInstanceRepository(), NewMockValueOverrideRepository()
	defs, charts := NewMockStackDefinitionRepository(), NewMockChartConfigRepository()
	_ = defs.Create(context.Background(), &models.StackDefinition{ID: "def-1", Name: "shop", OwnerID: "u-1"})
	seedChartConfig(t, charts, "chart-1", "def-1", "app")
	_ = insts.Create(context.Background(), &models.StackInstance{ID: "i-1", Name: "stack-a", OwnerID: "u-1", StackDefinitionID: "def-1", Status: models.StackStatusDraft})
	_ = overrides.Create(context.Background(), &models.ValueOverride{ID: "o-1", StackInstanceID: "i-1", ChartConfigID: "chart-1", Values: "replicas: 1"})
	details, raw := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupInstanceRouter(insts, overrides, defs, charts, NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "u-1", "alice", "user", mw)
	}, "/api/v1/stack-instances/i-1/overrides/chart-1", `{"values":"replicas: 2"}`)
	assert.Equal(t, map[string]any{"field": "replicas"}, changeMap(t, details)["replicas"])
	assert.Len(t, changeMap(t, details), 1)
	assert.NotContains(t, raw, "replicas: 2")
}

func TestSetBranchOverride_AuditOldNew(t *testing.T) {
	t.Parallel()
	insts, overrides := NewMockStackInstanceRepository(), NewMockChartBranchOverrideRepository()
	_ = insts.Create(context.Background(), &models.StackInstance{ID: "i-1", Name: "stack-a", OwnerID: "u-1", StackDefinitionID: "def-1", Status: models.StackStatusDraft})
	seedBranchOverride(t, overrides, "bo-1", "i-1", "chart-1", "main")
	details, _ := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupBranchOverrideRouter(insts, overrides, "u-1", "user", mw)
	}, "/api/v1/stack-instances/i-1/branches/chart-1", `{"branch":"feature-x"}`)
	assert.Equal(t, map[string]any{"field": "branch", "old": "main", "new": "feature-x"}, changeMap(t, details)["branch"])
}

func TestUpdateDefinition_AuditChanges(t *testing.T) {
	t.Parallel()
	defs := NewMockStackDefinitionRepository()
	_ = defs.Create(context.Background(), &models.StackDefinition{ID: "def-1", Name: "shop", Description: "d", DefaultBranch: "main", OwnerID: "u-1"})
	details, _ := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupDefinitionRouterWithTemplates(defs, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), nil, nil, "u-1", "user", mw)
	}, "/api/v1/stack-definitions/def-1", `{"name":"shop2","description":"d","default_branch":"main"}`)
	ch := changeMap(t, details)
	assert.Equal(t, map[string]any{"field": "name", "old": "shop", "new": "shop2"}, ch["name"])
	assert.Len(t, ch, 1, "computed owner names give no change")
}

func TestUpdateTemplate_AuditChanges(t *testing.T) {
	t.Parallel()
	tmpls := NewMockStackTemplateRepository()
	_ = tmpls.Create(context.Background(), &models.StackTemplate{ID: "t-1", Name: "base", Description: "old", OwnerID: "u-1"})
	details, _ := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupTemplateRouter(tmpls, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "u-1", "devops", mw)
	}, "/api/v1/templates/t-1", `{"description":"new"}`)
	ch := changeMap(t, details)
	assert.Equal(t, map[string]any{"field": "description"}, ch["description"])
	assert.Len(t, ch, 1)
}

func TestUpdateInstance_AuditChanges(t *testing.T) {
	t.Parallel()
	insts := NewMockStackInstanceRepository()
	_ = insts.Create(context.Background(), &models.StackInstance{ID: "i-1", Name: "stack-a", OwnerID: "u-1", StackDefinitionID: "def-1", Branch: "main", Status: models.StackStatusDraft})
	details, _ := auditedPut(t, func(mw gin.HandlerFunc) *gin.Engine {
		return setupInstanceRouter(insts, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(),
			NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "u-1", "alice", "user", mw)
	}, "/api/v1/stack-instances/i-1", `{"ttl_minutes":60}`)
	ch := changeMap(t, details)
	assert.Equal(t, map[string]any{"field": "ttl_minutes", "old": float64(0), "new": float64(60)}, ch["ttl_minutes"])
	for _, computed := range []string{"owner_username", "definition_name", "cluster_name", "values_drift"} {
		assert.NotContains(t, ch, computed)
	}
}

// TestOverrideHandlers_AuditEntityName checks that override entries name the
// instance: their entity ID is the instance ID.
func TestOverrideHandlers_AuditEntityName(t *testing.T) {
	t.Parallel()
	seed := func() (*MockStackInstanceRepository, *MockValueOverrideRepository, *MockStackDefinitionRepository, *MockChartConfigRepository) {
		insts, overrides := NewMockStackInstanceRepository(), NewMockValueOverrideRepository()
		defs, charts := NewMockStackDefinitionRepository(), NewMockChartConfigRepository()
		_ = defs.Create(context.Background(), &models.StackDefinition{ID: "def-1", Name: "shop", OwnerID: "u-1"})
		_ = charts.Create(context.Background(), &models.ChartConfig{ID: "chart-1", StackDefinitionID: "def-1", ChartName: "app"})
		_ = insts.Create(context.Background(), &models.StackInstance{ID: "i-1", Name: "stack-a", OwnerID: "u-1", StackDefinitionID: "def-1", Status: models.StackStatusDraft})
		_ = overrides.Create(context.Background(), &models.ValueOverride{ID: "o-1", StackInstanceID: "i-1", ChartConfigID: "chart-1", Values: "replicas: 1"})
		return insts, overrides, defs, charts
	}
	instanceRouter := func(mw gin.HandlerFunc) *gin.Engine {
		insts, overrides, defs, charts := seed()
		r := setupInstanceRouter(insts, overrides, defs, charts, NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "u-1", "alice", "user", mw)
		h := NewInstanceHandler(insts, overrides, NewMockChartBranchOverrideRepository(), defs, charts, nil, nil, nil, NewMockUserRepository(), 0)
		r.DELETE("/api/v1/stack-instances/:id/overrides/:chartId", h.DeleteOverride)
		return r
	}
	branchRouter := func(mw gin.HandlerFunc) *gin.Engine {
		insts, bos := NewMockStackInstanceRepository(), NewMockChartBranchOverrideRepository()
		_ = insts.Create(context.Background(), &models.StackInstance{ID: "i-1", Name: "stack-a", OwnerID: "u-1", StackDefinitionID: "def-1", Status: models.StackStatusDraft})
		seedBranchOverride(t, bos, "bo-1", "i-1", "chart-1", "main")
		return setupBranchOverrideRouter(insts, bos, "u-1", "user", mw)
	}
	tests := []struct {
		name   string
		build  func(gin.HandlerFunc) *gin.Engine
		method string
		path   string
		body   string
	}{
		{"set value override", instanceRouter, http.MethodPut, "/api/v1/stack-instances/i-1/overrides/chart-1", `{"values":"replicas: 2"}`},
		{"delete value override", instanceRouter, http.MethodDelete, "/api/v1/stack-instances/i-1/overrides/chart-1", ""},
		{"set branch override", branchRouter, http.MethodPut, "/api/v1/stack-instances/i-1/branches/chart-1", `{"branch":"feature-x"}`},
		{"delete branch override", branchRouter, http.MethodDelete, "/api/v1/stack-instances/i-1/branches/chart-1", ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, e := auditedRequest(t, tt.build, tt.method, tt.path, tt.body)
			assert.Equal(t, "i-1", e.EntityID)
			assert.Equal(t, "stack-a", e.EntityName)
		})
	}
}
