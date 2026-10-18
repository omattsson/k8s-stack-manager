package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefinitionModifyAccess checks that only the owner, devops and admin
// users can change a definition (update, delete).
func TestDefinitionModifyAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		body       string
		callerID   string
		callerRole string
		wantStatus int
	}{
		{name: "owner updates", method: http.MethodPut, body: `{"name":"New"}`, callerID: "owner", callerRole: "user", wantStatus: http.StatusOK},
		{name: "other user updates", method: http.MethodPut, body: `{"name":"New"}`, callerID: "other", callerRole: "user", wantStatus: http.StatusForbidden},
		{name: "devops updates", method: http.MethodPut, body: `{"name":"New"}`, callerID: "other", callerRole: "devops", wantStatus: http.StatusOK},
		{name: "admin updates", method: http.MethodPut, body: `{"name":"New"}`, callerID: "other", callerRole: "admin", wantStatus: http.StatusOK},
		{name: "owner deletes", method: http.MethodDelete, callerID: "owner", callerRole: "user", wantStatus: http.StatusNoContent},
		{name: "other user deletes", method: http.MethodDelete, callerID: "other", callerRole: "user", wantStatus: http.StatusForbidden},
		{name: "devops deletes", method: http.MethodDelete, callerID: "other", callerRole: "devops", wantStatus: http.StatusNoContent},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "Old", "owner")
			router := setupDefinitionRouter(defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(), tt.callerID, tt.callerRole)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, "/api/v1/stack-definitions/d1", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			_, err := defRepo.FindByID("d1")
			if tt.method == http.MethodDelete && tt.wantStatus == http.StatusForbidden {
				assert.NoError(t, err, "a refused delete must keep the definition")
			}
		})
	}
}

// TestUpdateChartConfigAccess checks the chart update rules for users
// without the devops or admin role: only the owner may change the chart, and
// only the default values and the order, not the chart source.
func TestUpdateChartConfigAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		callerID   string
		callerRole string
		wantStatus int
	}{
		{name: "owner changes default values", body: `{"default_values":"replicas: 2"}`, callerID: "owner", callerRole: "user", wantStatus: http.StatusOK},
		{name: "owner sends unchanged source", body: `{"chart_name":"web","repository_url":"oci://registry.example.com/charts","chart_version":"1.0.0","default_values":"a: 1"}`, callerID: "owner", callerRole: "user", wantStatus: http.StatusOK},
		{name: "owner changes repository", body: `{"repository_url":"https://charts.example.net"}`, callerID: "owner", callerRole: "user", wantStatus: http.StatusForbidden},
		{name: "owner changes chart path", body: `{"chart_path":"other"}`, callerID: "owner", callerRole: "user", wantStatus: http.StatusForbidden},
		{name: "owner changes chart version", body: `{"chart_version":"2.0.0"}`, callerID: "owner", callerRole: "user", wantStatus: http.StatusForbidden},
		{name: "other user changes values", body: `{"default_values":"replicas: 2"}`, callerID: "other", callerRole: "user", wantStatus: http.StatusForbidden},
		{name: "devops changes repository", body: `{"repository_url":"oci://registry.example.com/other"}`, callerID: "other", callerRole: "devops", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "def-1", "Def", "owner")
			chartRepo := NewMockChartConfigRepository()
			require.NoError(t, chartRepo.Create(&models.ChartConfig{
				ID: "chart-1", StackDefinitionID: "def-1", ChartName: "web",
				RepositoryURL: "oci://registry.example.com/charts", ChartVersion: "1.0.0",
				CreatedAt: time.Now().UTC(),
			}))
			router := setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), tt.callerID, tt.callerRole)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-definitions/def-1/charts/chart-1", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusForbidden {
				stored, err := chartRepo.FindByID("chart-1")
				require.NoError(t, err)
				assert.Equal(t, "oci://registry.example.com/charts", stored.RepositoryURL)
				assert.Empty(t, stored.DefaultValues)
			}
		})
	}
}

// TestApplyUpgradeAccess checks that a user who does not own the definition
// cannot upgrade it.
func TestApplyUpgradeAccess(t *testing.T) {
	t.Parallel()
	defRepo := NewMockStackDefinitionRepository()
	def := seedDefinition(t, defRepo, "d1", "Def", "owner")
	def.SourceTemplateID = "t1"
	require.NoError(t, defRepo.Update(def))

	router := setupDefinitionUpgradeRouter(t, defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(),
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), NewMockTemplateVersionRepository(), "other", "user")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-definitions/d1/upgrade", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}
