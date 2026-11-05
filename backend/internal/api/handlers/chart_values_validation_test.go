package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	badTemplateValue = "tag: {{.Branch}}"
	badDuplicateKey  = "a: 1\na: 2"
	goodValue        = "tag: \"{{.Branch}}\"\nreplicas: 2"
)

func cvDoJSON(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestValidateChartValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, field, chart, value string
		wantPrefix                string
	}{
		{"valid", "default_values", "web", goodValue, ""},
		{"empty", "default_values", "web", "", ""},
		{"whitespace", "locked_values", "web", "  \n ", ""},
		{"invalid with chart", "default_values", "web", badDuplicateKey, `Invalid YAML in chart "web" default_values: `},
		{"invalid without chart", "locked_values", "", badDuplicateKey, `Invalid YAML in locked_values: `},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateChartValues(tt.field, tt.chart, tt.value)
			if tt.wantPrefix == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantPrefix)
		})
	}
}

func TestChartValuesValidation_DefinitionCharts(t *testing.T) {
	t.Parallel()

	newDef := func(t *testing.T) (*MockChartConfigRepository, *gin.Engine) {
		t.Helper()
		defRepo := NewMockStackDefinitionRepository()
		require.NoError(t, defRepo.Create(context.Background(), &models.StackDefinition{ID: "def-1", Name: "d", OwnerID: "uid-1", DefaultBranch: "master"}))
		chartRepo := NewMockChartConfigRepository()
		return chartRepo, setupChartConfigRouter(defRepo, chartRepo, NewMockTemplateChartConfigRepository(), "uid-1", "devops")
	}

	addTests := []struct {
		name       string
		values     string
		wantStatus int
		wantMsg    string
	}{
		{"valid", goodValue, http.StatusCreated, ""},
		{"empty", "", http.StatusCreated, ""},
		{"unquoted template", badTemplateValue, http.StatusBadRequest, "Invalid YAML in default_values: "},
		{"duplicate key", badDuplicateKey, http.StatusBadRequest, "Invalid YAML in default_values: "},
	}
	for _, tt := range addTests {
		tt := tt
		t.Run("add "+tt.name, func(t *testing.T) {
			t.Parallel()
			chartRepo, r := newDef(t)
			w := cvDoJSON(r, http.MethodPost, "/api/v1/stack-definitions/def-1/charts", map[string]any{"chart_name": "web", "default_values": tt.values})
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			list, _ := chartRepo.ListByDefinition(context.Background(), "def-1")
			if tt.wantMsg != "" {
				assert.Contains(t, cvErrMsg(w), tt.wantMsg)
				assert.Empty(t, list)
			} else {
				assert.Len(t, list, 1)
			}
		})
	}

	updateTests := []struct {
		name       string
		stored     string
		body       map[string]any
		wantStatus int
	}{
		{"new invalid value", "a: 1", map[string]any{"default_values": badDuplicateKey}, http.StatusBadRequest},
		{"new valid value", "a: 1", map[string]any{"default_values": goodValue}, http.StatusOK},
		{"unchanged invalid value", badDuplicateKey, map[string]any{"default_values": badDuplicateKey, "deploy_order": 3}, http.StatusOK},
		{"value absent, stored invalid", badDuplicateKey, map[string]any{"deploy_order": 3}, http.StatusOK},
		{"changed invalid value over invalid", badDuplicateKey, map[string]any{"default_values": badTemplateValue}, http.StatusBadRequest},
	}
	for _, tt := range updateTests {
		tt := tt
		t.Run("update "+tt.name, func(t *testing.T) {
			t.Parallel()
			chartRepo, r := newDef(t)
			cc := seedChartConfig(t, chartRepo, "c1", "def-1", "web")
			cc.DefaultValues = tt.stored
			require.NoError(t, chartRepo.Update(context.Background(), cc))
			w := cvDoJSON(r, http.MethodPut, "/api/v1/stack-definitions/def-1/charts/c1", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusBadRequest {
				assert.Contains(t, cvErrMsg(w), "Invalid YAML in default_values: ")
				got, _ := chartRepo.FindByID(context.Background(), "c1")
				assert.Equal(t, tt.stored, got.DefaultValues)
			}
		})
	}
}

func TestChartValuesValidation_TemplateCharts(t *testing.T) {
	t.Parallel()

	newTmpl := func(t *testing.T) (*MockTemplateChartConfigRepository, *gin.Engine) {
		t.Helper()
		tmplRepo := NewMockStackTemplateRepository()
		seedTemplate(t, tmplRepo, "t1", "T", "uid-1", false)
		chartRepo := NewMockTemplateChartConfigRepository()
		return chartRepo, setupTemplateChartRouter(tmplRepo, chartRepo, "uid-1", "devops")
	}

	addTests := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantMsg    string
	}{
		{"valid", map[string]any{"chart_name": "web", "default_values": goodValue, "locked_values": "a: 1"}, http.StatusCreated, ""},
		{"invalid default", map[string]any{"chart_name": "web", "default_values": badTemplateValue}, http.StatusBadRequest, "Invalid YAML in default_values: "},
		{"invalid locked", map[string]any{"chart_name": "web", "locked_values": badDuplicateKey}, http.StatusBadRequest, "Invalid YAML in locked_values: "},
	}
	for _, tt := range addTests {
		tt := tt
		t.Run("add "+tt.name, func(t *testing.T) {
			t.Parallel()
			chartRepo, r := newTmpl(t)
			w := cvDoJSON(r, http.MethodPost, "/api/v1/templates/t1/charts", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			list, _ := chartRepo.ListByTemplate(context.Background(), "t1")
			if tt.wantMsg != "" {
				assert.Contains(t, cvErrMsg(w), tt.wantMsg)
				assert.Empty(t, list)
			} else {
				assert.Len(t, list, 1)
			}
		})
	}

	updateTests := []struct {
		name          string
		storedDefault string
		storedLocked  string
		body          map[string]any
		wantStatus    int
		wantMsg       string
	}{
		{"new invalid default", "", "", map[string]any{"chart_name": "web", "default_values": badTemplateValue}, http.StatusBadRequest, "Invalid YAML in default_values: "},
		{"new invalid locked", "", "", map[string]any{"chart_name": "web", "locked_values": badDuplicateKey}, http.StatusBadRequest, "Invalid YAML in locked_values: "},
		{"new valid", "", "", map[string]any{"chart_name": "web", "default_values": goodValue, "locked_values": "a: 1"}, http.StatusOK, ""},
		{"unchanged invalid locked, other field changed", "", badDuplicateKey, map[string]any{"chart_name": "web", "locked_values": badDuplicateKey, "deploy_order": 4}, http.StatusOK, ""},
		{"unchanged invalid default, changed valid locked", badTemplateValue, "", map[string]any{"chart_name": "web", "default_values": badTemplateValue, "locked_values": "a: 1"}, http.StatusOK, ""},
		{"unchanged invalid default, changed invalid locked", badTemplateValue, "", map[string]any{"chart_name": "web", "default_values": badTemplateValue, "locked_values": badDuplicateKey}, http.StatusBadRequest, "Invalid YAML in locked_values: "},
	}
	for _, tt := range updateTests {
		tt := tt
		t.Run("update "+tt.name, func(t *testing.T) {
			t.Parallel()
			chartRepo, r := newTmpl(t)
			tc := seedTemplateChart(t, chartRepo, "c1", "t1", "web")
			tc.DefaultValues, tc.LockedValues = tt.storedDefault, tt.storedLocked
			require.NoError(t, chartRepo.Update(context.Background(), tc))
			w := cvDoJSON(r, http.MethodPut, "/api/v1/templates/t1/charts/c1", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantMsg != "" {
				assert.Contains(t, cvErrMsg(w), tt.wantMsg)
				got, _ := chartRepo.FindByID(context.Background(), "c1")
				assert.Equal(t, tt.storedDefault, got.DefaultValues)
				assert.Equal(t, tt.storedLocked, got.LockedValues)
			}
		})
	}
}

func TestChartValuesValidation_CreateTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		chart      map[string]any
		wantStatus int
		wantMsg    string
	}{
		{"valid", map[string]any{"chart_name": "web", "default_values": goodValue}, http.StatusCreated, ""},
		{"invalid default", map[string]any{"chart_name": "web", "default_values": badTemplateValue}, http.StatusBadRequest, `Invalid YAML in chart "web" default_values: `},
		{"invalid locked", map[string]any{"chart_name": "web", "locked_values": badDuplicateKey}, http.StatusBadRequest, `Invalid YAML in chart "web" locked_values: `},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmplRepo := NewMockStackTemplateRepository()
			chartRepo := NewMockTemplateChartConfigRepository()
			r := setupTemplateRouter(tmplRepo, chartRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), "uid-1", "devops")
			w := cvDoJSON(r, http.MethodPost, "/api/v1/templates", map[string]any{
				"name": "T", "category": "Full Stack", "version": "1.0.0",
				"charts": []any{tt.chart},
			})
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			list, _ := tmplRepo.List(context.Background())
			if tt.wantMsg != "" {
				assert.Contains(t, cvErrMsg(w), tt.wantMsg)
				assert.Empty(t, list)
			} else {
				assert.Len(t, list, 1)
			}
		})
	}
}

func TestChartValuesValidation_InstantiateAndClone(t *testing.T) {
	t.Parallel()

	seed := func(t *testing.T, stored string) (*MockStackDefinitionRepository, *gin.Engine) {
		t.Helper()
		tmplRepo := NewMockStackTemplateRepository()
		chartRepo := NewMockTemplateChartConfigRepository()
		defRepo := NewMockStackDefinitionRepository()
		seedTemplate(t, tmplRepo, "t1", "T", "owner-1", true)
		require.NoError(t, chartRepo.Create(context.Background(), &models.TemplateChartConfig{
			ID: "c1", StackTemplateID: "t1", ChartName: "web", DefaultValues: stored, LockedValues: stored,
		}))
		return defRepo, setupTemplateRouter(tmplRepo, chartRepo, defRepo, NewMockChartConfigRepository(), "owner-1", "devops")
	}

	overrideTests := []struct {
		name       string
		stored     string
		overrides  map[string]string
		wantStatus int
	}{
		{"invalid override", "a: 1", map[string]string{"web": badTemplateValue}, http.StatusBadRequest},
		{"invalid override by id", "a: 1", map[string]string{"c1": badDuplicateKey}, http.StatusBadRequest},
		{"valid override", "a: 1", map[string]string{"web": goodValue}, http.StatusCreated},
		{"empty override", "a: 1", map[string]string{"web": ""}, http.StatusCreated},
		{"invalid stored, no override", badDuplicateKey, nil, http.StatusCreated},
		{"invalid stored, valid override", badDuplicateKey, map[string]string{"web": "a: 1"}, http.StatusCreated},
	}
	for _, tt := range overrideTests {
		tt := tt
		t.Run("instantiate "+tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo, r := seed(t, tt.stored)
			w := cvDoJSON(r, http.MethodPost, "/api/v1/templates/t1/instantiate", map[string]any{"name": "d", "chart_overrides": tt.overrides})
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			n, _ := defRepo.Count(context.Background())
			if tt.wantStatus == http.StatusBadRequest {
				assert.Contains(t, cvErrMsg(w), `Invalid YAML in chart "web" default_values: `)
				assert.Zero(t, n, "no definition is stored")
			} else {
				assert.EqualValues(t, 1, n)
			}
		})
	}

	t.Run("clone copies an invalid stored value", func(t *testing.T) {
		t.Parallel()
		_, r := seed(t, badDuplicateKey)
		w := cvDoJSON(r, http.MethodPost, "/api/v1/templates/t1/clone", nil)
		assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	})
}

func TestChartValuesValidation_ImportDefinition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		charts     []map[string]any
		wantStatus int
		wantMsg    string
	}{
		{"valid", []map[string]any{{"chart_name": "web", "default_values": goodValue}}, http.StatusCreated, ""},
		{"invalid first", []map[string]any{{"chart_name": "web", "default_values": badTemplateValue}}, http.StatusBadRequest, `Invalid YAML in chart "web" default_values: `},
		{"invalid second", []map[string]any{{"chart_name": "web", "default_values": "a: 1"}, {"chart_name": "db", "default_values": badDuplicateKey}}, http.StatusBadRequest, `Invalid YAML in chart "db" default_values: `},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			chartRepo := NewMockChartConfigRepository()
			r := setupDefinitionRouter(defRepo, chartRepo, NewMockStackInstanceRepository(), "uid-1", "devops")
			w := cvDoJSON(r, http.MethodPost, "/api/v1/stack-definitions/import", map[string]any{
				"schema_version": "1.0",
				"definition":     map[string]any{"name": "Imp", "default_branch": "main"},
				"charts":         tt.charts,
			})
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			n, _ := defRepo.Count(context.Background())
			if tt.wantMsg != "" {
				assert.Contains(t, cvErrMsg(w), tt.wantMsg)
				assert.Zero(t, n, "nothing is stored")
			} else {
				assert.EqualValues(t, 1, n)
			}
		})
	}
}

func cvErrMsg(w *httptest.ResponseRecorder) string {
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out["error"]
}
