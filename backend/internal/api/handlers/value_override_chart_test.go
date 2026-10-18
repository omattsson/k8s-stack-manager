package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOverrideTestChartRepo returns chart config chart-1 and chart-2 of
// definition def-1 and chart-other of definition def-2.
func newOverrideTestChartRepo(t *testing.T) *MockChartConfigRepository {
	t.Helper()
	repo := NewMockChartConfigRepository()
	seedChartConfig(t, repo, "chart-1", "def-1", "app")
	seedChartConfig(t, repo, "chart-2", "def-1", "worker")
	seedChartConfig(t, repo, "chart-other", "def-2", "other")
	return repo
}

// TestValueOverride_ChartValidationAndDelete covers issue 445 for value
// overrides: :chartId must be a chart of the instance's definition, empty
// values remove the override, GET and DELETE of a single override.
func TestValueOverride_ChartValidationAndDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		chartID    string
		body       string
		callerID   string
		seedRow    bool
		wantStatus int
		wantError  string
		wantRow    bool // whether a row for chartID must exist afterwards
	}{
		{name: "PUT unknown chart ID", method: http.MethodPut, chartID: "my-chart-name", body: `{"values":"a: 1"}`, wantStatus: http.StatusNotFound, wantError: msgChartNotInDefinition},
		{name: "PUT chart of another definition", method: http.MethodPut, chartID: "chart-other", body: `{"values":"a: 1"}`, wantStatus: http.StatusNotFound, wantError: msgChartNotInDefinition},
		{name: "PUT valid chart", method: http.MethodPut, chartID: "chart-1", body: `{"values":"a: 1"}`, wantStatus: http.StatusOK, wantRow: true},
		{name: "PUT empty values removes existing override", method: http.MethodPut, chartID: "chart-1", body: `{"values":""}`, seedRow: true, wantStatus: http.StatusNoContent},
		{name: "PUT whitespace values removes existing override", method: http.MethodPut, chartID: "chart-1", body: `{"values":"  \n\t"}`, seedRow: true, wantStatus: http.StatusNoContent},
		{name: "PUT empty values without override stores nothing", method: http.MethodPut, chartID: "chart-1", body: `{"values":""}`, wantStatus: http.StatusNoContent},
		{name: "GET existing override", method: http.MethodGet, chartID: "chart-1", seedRow: true, wantStatus: http.StatusOK, wantRow: true},
		{name: "GET missing override", method: http.MethodGet, chartID: "chart-2", wantStatus: http.StatusNotFound, wantError: "Value override not found"},
		{name: "GET unknown chart", method: http.MethodGet, chartID: "my-chart-name", wantStatus: http.StatusNotFound, wantError: msgChartNotInDefinition},
		{name: "GET by non-owner is forbidden", method: http.MethodGet, chartID: "chart-1", callerID: "uid-other", seedRow: true, wantStatus: http.StatusForbidden, wantRow: true},
		{name: "DELETE existing override", method: http.MethodDelete, chartID: "chart-1", seedRow: true, wantStatus: http.StatusNoContent},
		{name: "DELETE missing override", method: http.MethodDelete, chartID: "chart-2", wantStatus: http.StatusNotFound, wantError: "Value override not found"},
		{name: "DELETE unknown chart without override", method: http.MethodDelete, chartID: "my-chart-name", wantStatus: http.StatusNotFound, wantError: msgChartNotInDefinition},
		{name: "DELETE stale row with unknown chart ID", method: http.MethodDelete, chartID: "my-chart-name", seedRow: true, wantStatus: http.StatusNoContent},
		{name: "DELETE by non-owner is forbidden", method: http.MethodDelete, chartID: "chart-1", callerID: "uid-other", seedRow: true, wantStatus: http.StatusForbidden, wantRow: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instRepo := NewMockStackInstanceRepository()
			seedInstance(t, instRepo, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			overrideRepo := NewMockValueOverrideRepository()
			if tt.seedRow {
				seedValueOverride(t, overrideRepo, "vo-1", "inst-1", tt.chartID, "replicaCount: 2")
			}
			callerID := tt.callerID
			if callerID == "" {
				callerID = "uid-1"
			}

			router := setupValueOverrideRouter(
				instRepo, overrideRepo,
				NewMockStackDefinitionRepository(), newOverrideTestChartRepo(t),
				NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				callerID, "user",
			)

			req, _ := http.NewRequest(tt.method, "/api/v1/stack-instances/inst-1/overrides/"+tt.chartID, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				var resp map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tt.wantError, resp["error"])
			}
			if tt.wantStatus == http.StatusNoContent {
				assert.Empty(t, w.Body.String())
			}

			_, err := overrideRepo.FindByInstanceAndChart("inst-1", tt.chartID)
			if tt.wantRow {
				assert.NoError(t, err, "override row must exist")
			} else {
				assert.Error(t, err, "override row must not exist")
			}
		})
	}
}
