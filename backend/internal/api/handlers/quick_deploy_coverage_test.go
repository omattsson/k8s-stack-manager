package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/cluster"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQuickDeploy_DefinitionCreateError covers lines 171-175 (definitionRepo.Create error).
func TestQuickDeploy_DefinitionCreateError(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
	defRepo.SetError(errors.New("db write failed"))

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, nil,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "my-deploy"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestQuickDeploy_TemplateChartsError covers lines 187-192 (templateChartRepo.ListByTemplate error).
func TestQuickDeploy_TemplateChartsError(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
	tmplChartRepo.SetError(errors.New("table storage unavailable"))

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, nil,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "my-deploy"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestQuickDeploy_ChartConfigCreateError covers lines 208-219 (chartConfigRepo.Create error + rollback).
func TestQuickDeploy_ChartConfigCreateError(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
	require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
		ID:              "tc1",
		StackTemplateID: "t1",
		ChartName:       "frontend",
		RepositoryURL:   "oci://charts.example.com/frontend",
		DeployOrder:     1,
	}))

	// Make chart config creation fail.
	ccRepo.SetError(errors.New("db write failed"))

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, nil,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "my-deploy"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestQuickDeploy_FindByNamespaceError covers lines 299-307 (FindByNamespace non-ErrNotFound error).
func TestQuickDeploy_FindByNamespaceError(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)

	// Make FindByNamespace return a non-ErrNotFound error.
	instRepo.SetFetchError(errors.New("connection refused"))

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, nil,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "my-deploy"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestQuickDeploy_BranchOverrideSetError covers lines 336-342 (branchOverrideRepo.Set error — logged, not fatal).
func TestQuickDeploy_BranchOverrideSetError(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)

	// Inject error on branch override Set.
	boRepo.mu.Lock()
	boRepo.err = errors.New("branch override write failed")
	boRepo.mu.Unlock()

	logRepo := NewMockDeploymentLogRepository()
	mgr := newTestManager(instRepo, logRepo)
	registry := cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		mgr, registry,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{
		InstanceName:    "bo-error-test",
		BranchOverrides: map[string]string{"cc1": "feature/test"},
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// Should still succeed — branch override errors are logged but not fatal.
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestQuickDeploy_AuditLogError covers lines 370-372 (auditRepo.Create error — logged, not fatal).
func TestQuickDeploy_AuditLogError(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
	auditRepo.SetError(errors.New("audit write failed"))

	logRepo := NewMockDeploymentLogRepository()
	mgr := newTestManager(instRepo, logRepo)
	registry := cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		mgr, registry,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "audit-error-test"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// Should still succeed — audit log errors are logged but not fatal.
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestQuickDeploy_NoChartsConfigured covers lines 395-397 in triggerDeploy (empty chartConfigs → error).
func TestQuickDeploy_NoChartsConfigured(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
	// No template charts → triggerDeploy gets empty chartConfigs.

	logRepo := NewMockDeploymentLogRepository()
	mgr := newTestManager(instRepo, logRepo)
	registry := cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		mgr, registry,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "no-charts"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// Deploy should still return 202 (instance created, deploy fails gracefully).
	assert.Equal(t, http.StatusAccepted, w.Code)
	var resp quickDeployResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Instance.ID)
}

// TestQuickDeploy_InstanceCreateError covers lines 319-324 (instanceRepo.Create error).
func TestQuickDeploy_InstanceCreateError(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
	instRepo.SetCreateError(dberrors.NewDatabaseError("Create", errors.New("db write failed")))

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, nil,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "create-fail"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestTruncate_QuickDeploy covers the truncate() utility function (lines 468-473).
func TestTruncate_QuickDeploy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string unchanged",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length unchanged",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "long string truncated",
			input:  "hello world",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "unicode truncation",
			input:  "日本語テスト",
			maxLen: 3,
			want:   "日本語",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestQuickDeploy_NegativeTTL covers the TTL < 0 path (lines 255-258).
func TestQuickDeploy_NegativeTTL(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, nil,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{
		InstanceName: "neg-ttl",
		TTLMinutes:   intPtr(-5),
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "TTL")
}

// TestQuickDeploy_ExcessiveTTL covers the TTL > MaxTTLMinutes path (lines 260-264).
func TestQuickDeploy_ExcessiveTTL(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, nil,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{
		InstanceName: "big-ttl",
		TTLMinutes:   intPtr(MaxTTLMinutes + 1),
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "TTL")
}

// TestQuickDeploy_UnknownCluster covers the ClusterExists=false path (lines 281-285).
func TestQuickDeploy_UnknownCluster(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)

	clusterRepo := NewMockClusterRepository()
	seedCluster(clusterRepo, "cl-1", "test-cluster")
	registry := cluster.NewRegistry(cluster.RegistryConfig{ClusterRepo: clusterRepo})

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, registry,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{
		InstanceName: "my-deploy",
		ClusterID:    "nonexistent-cluster",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "Unknown cluster_id")
}

// TestQuickDeploy_NoDefaultCluster covers the ResolveClusterID error path (lines 275-279).
func TestQuickDeploy_NoDefaultCluster(t *testing.T) {
	t.Parallel()

	tmplRepo := NewMockStackTemplateRepository()
	tmplChartRepo := NewMockTemplateChartConfigRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	auditRepo := NewMockAuditLogRepository()

	seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)

	clusterRepo := NewMockClusterRepository()
	registry := cluster.NewRegistry(cluster.RegistryConfig{ClusterRepo: clusterRepo})

	r := setupQuickDeployRouter(t,
		tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo,
		boRepo, ovRepo, auditRepo,
		nil, registry,
		"user1", "alice", "developer", 0,
	)

	body, _ := json.Marshal(quickDeployRequest{InstanceName: "my-deploy"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates/t1/quick-deploy", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "default cluster")
}
