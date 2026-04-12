package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTemplateVersionRouter creates a test gin engine with TemplateVersionHandler
// and TemplateHandler (for publish auto-snapshot) routes.
func setupTemplateVersionRouter(
	templateRepo *MockStackTemplateRepository,
	chartRepo *MockTemplateChartConfigRepository,
	definitionRepo *MockStackDefinitionRepository,
	chartConfigRepo *MockChartConfigRepository,
	versionRepo *MockTemplateVersionRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	th := NewTemplateHandlerWithVersions(templateRepo, chartRepo, definitionRepo, chartConfigRepo, versionRepo, &mockHandlerTxRunner{})
	vh := NewTemplateVersionHandler(versionRepo, templateRepo)

	tpl := r.Group("/api/v1/templates")
	{
		tpl.POST("/:id/publish", th.PublishTemplate)
		tpl.GET("/:id/versions", vh.ListVersions)
		tpl.GET("/:id/versions/diff", vh.DiffVersions)
		tpl.GET("/:id/versions/:versionId", vh.GetVersion)
	}
	return r
}

// setupDefinitionUpgradeRouter creates a test gin engine with DefinitionHandler upgrade routes.
func setupDefinitionUpgradeRouter(
	definitionRepo *MockStackDefinitionRepository,
	chartConfigRepo *MockChartConfigRepository,
	instanceRepo *MockStackInstanceRepository,
	templateRepo *MockStackTemplateRepository,
	templateChartRepo *MockTemplateChartConfigRepository,
	versionRepo *MockTemplateVersionRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))

	h := NewDefinitionHandlerWithVersions(definitionRepo, chartConfigRepo, instanceRepo, templateRepo, templateChartRepo, versionRepo, &mockHandlerTxRunner{})

	defs := r.Group("/api/v1/stack-definitions")
	{
		defs.GET("/:id/check-upgrade", h.CheckUpgrade)
		defs.POST("/:id/upgrade", h.ApplyUpgrade)
	}
	return r
}

func seedVersion(t *testing.T, repo *MockTemplateVersionRepository, id, templateID, version, snapshot string, createdAt time.Time) *models.TemplateVersion {
	t.Helper()
	v := &models.TemplateVersion{
		ID:         id,
		TemplateID: templateID,
		Version:    version,
		Snapshot:   snapshot,
		CreatedBy:  "test-user",
		CreatedAt:  createdAt,
	}
	require.NoError(t, repo.Create(nil, v))
	return v
}

func makeSnapshotJSON(t *testing.T, tmpl models.TemplateSnapshotData, charts []models.TemplateChartSnapshotData) string {
	t.Helper()
	snap := models.TemplateSnapshot{Template: tmpl, Charts: charts}
	b, err := json.Marshal(snap)
	require.NoError(t, err)
	return string(b)
}

// ---- ListVersions ----

func TestListVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		templateID string
		setup      func(*MockStackTemplateRepository, *MockTemplateVersionRepository)
		wantStatus int
		wantLen    int
	}{
		{
			name:       "empty versions list",
			templateID: "t1",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			},
			wantStatus: http.StatusOK,
			wantLen:    0,
		},
		{
			name:       "returns versions ordered newest first",
			templateID: "t1",
			setup: func(tmplRepo *MockStackTemplateRepository, verRepo *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "1.0"}, nil)
				seedVersion(t, verRepo, "v1", "t1", "1.0", snap, time.Now().Add(-1*time.Hour))
				seedVersion(t, verRepo, "v2", "t1", "2.0", snap, time.Now())
			},
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name:       "template not found returns 404",
			templateID: "missing",
			setup:      func(_ *MockStackTemplateRepository, _ *MockTemplateVersionRepository) {},
			wantStatus: http.StatusNotFound,
			wantLen:    -1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmplRepo := NewMockStackTemplateRepository()
			verRepo := NewMockTemplateVersionRepository()
			tt.setup(tmplRepo, verRepo)

			router := setupTemplateVersionRouter(tmplRepo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), verRepo, "uid-1", "devops")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates/"+tt.templateID+"/versions", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantLen >= 0 {
				var list []models.TemplateVersion
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
				assert.Len(t, list, tt.wantLen)
			}
		})
	}
}

// ---- GetVersion ----

func TestGetVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		templateID string
		versionID  string
		setup      func(*MockStackTemplateRepository, *MockTemplateVersionRepository)
		wantStatus int
		checkSnap  bool
	}{
		{
			name:       "found version with parsed snapshot",
			templateID: "t1",
			versionID:  "v1",
			setup: func(tmplRepo *MockStackTemplateRepository, verRepo *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "1.0"}, []models.TemplateChartSnapshotData{
					{ChartName: "frontend", RepoURL: "https://charts.example.com"},
				})
				seedVersion(t, verRepo, "v1", "t1", "1.0", snap, time.Now())
			},
			wantStatus: http.StatusOK,
			checkSnap:  true,
		},
		{
			name:       "version not found returns 404",
			templateID: "t1",
			versionID:  "missing",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "version belongs to different template returns 404",
			templateID: "t1",
			versionID:  "v1",
			setup: func(tmplRepo *MockStackTemplateRepository, verRepo *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "Template 1", "owner-1", true)
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T2", Version: "1.0"}, nil)
				seedVersion(t, verRepo, "v1", "t2", "1.0", snap, time.Now())
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmplRepo := NewMockStackTemplateRepository()
			verRepo := NewMockTemplateVersionRepository()
			tt.setup(tmplRepo, verRepo)

			router := setupTemplateVersionRouter(tmplRepo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), verRepo, "uid-1", "devops")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates/"+tt.templateID+"/versions/"+tt.versionID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkSnap {
				var resp map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Contains(t, string(resp["snapshot"]), "frontend")
			}
		})
	}
}

// ---- DiffVersions ----

func TestDiffVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		templateID string
		leftID     string
		rightID    string
		setup      func(*MockStackTemplateRepository, *MockTemplateVersionRepository)
		wantStatus int
		checkDiffs func(*testing.T, map[string]json.RawMessage)
	}{
		{
			name:       "diff with added and removed charts",
			templateID: "t1",
			leftID:     "v1",
			rightID:    "v2",
			setup: func(tmplRepo *MockStackTemplateRepository, verRepo *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				snap1 := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "1.0"}, []models.TemplateChartSnapshotData{
					{ChartName: "frontend", DefaultValues: "port: 80"},
					{ChartName: "old-service"},
				})
				snap2 := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "2.0"}, []models.TemplateChartSnapshotData{
					{ChartName: "frontend", DefaultValues: "port: 8080"},
					{ChartName: "new-service"},
				})
				seedVersion(t, verRepo, "v1", "t1", "1.0", snap1, time.Now().Add(-1*time.Hour))
				seedVersion(t, verRepo, "v2", "t1", "2.0", snap2, time.Now())
			},
			wantStatus: http.StatusOK,
			checkDiffs: func(t *testing.T, resp map[string]json.RawMessage) {
				t.Helper()
				var diffs []chartDiffEntry
				require.NoError(t, json.Unmarshal(resp["chart_diffs"], &diffs))
				assert.Len(t, diffs, 3)
				diffMap := make(map[string]chartDiffEntry)
				for _, d := range diffs {
					diffMap[d.ChartName] = d
				}
				assert.Equal(t, "modified", diffMap["frontend"].ChangeType)
				assert.Equal(t, "removed", diffMap["old-service"].ChangeType)
				assert.Equal(t, "added", diffMap["new-service"].ChangeType)
			},
		},
		{
			name:       "diff with identical charts",
			templateID: "t1",
			leftID:     "v1",
			rightID:    "v2",
			setup: func(tmplRepo *MockStackTemplateRepository, verRepo *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				charts := []models.TemplateChartSnapshotData{{ChartName: "frontend", DefaultValues: "port: 80"}}
				snap1 := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "1.0"}, charts)
				snap2 := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "1.1"}, charts)
				seedVersion(t, verRepo, "v1", "t1", "1.0", snap1, time.Now().Add(-1*time.Hour))
				seedVersion(t, verRepo, "v2", "t1", "1.1", snap2, time.Now())
			},
			wantStatus: http.StatusOK,
			checkDiffs: func(t *testing.T, resp map[string]json.RawMessage) {
				t.Helper()
				var diffs []chartDiffEntry
				require.NoError(t, json.Unmarshal(resp["chart_diffs"], &diffs))
				assert.Len(t, diffs, 1)
				assert.Equal(t, "unchanged", diffs[0].ChangeType)
				assert.False(t, diffs[0].HasDifferences)
			},
		},
		{
			name:       "left version not found returns 404",
			templateID: "t1",
			leftID:     "missing",
			rightID:    "v2",
			setup: func(tmplRepo *MockStackTemplateRepository, verRepo *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "1.0"}, nil)
				seedVersion(t, verRepo, "v2", "t1", "1.0", snap, time.Now())
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "right version not found returns 404",
			templateID: "t1",
			leftID:     "v1",
			rightID:    "missing",
			setup: func(tmplRepo *MockStackTemplateRepository, verRepo *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "1.0"}, nil)
				seedVersion(t, verRepo, "v1", "t1", "1.0", snap, time.Now())
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing query params returns 400",
			templateID: "t1",
			leftID:     "",
			rightID:    "",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockTemplateVersionRepository) {
				seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmplRepo := NewMockStackTemplateRepository()
			verRepo := NewMockTemplateVersionRepository()
			tt.setup(tmplRepo, verRepo)

			router := setupTemplateVersionRouter(tmplRepo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), verRepo, "uid-1", "devops")
			w := httptest.NewRecorder()
			url := "/api/v1/templates/" + tt.templateID + "/versions/diff"
			if tt.leftID != "" || tt.rightID != "" {
				url += "?left=" + tt.leftID + "&right=" + tt.rightID
			}
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkDiffs != nil {
				var resp map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				tt.checkDiffs(t, resp)
			}
		})
	}
}

// ---- Auto-snapshot on Publish ----

func TestPublishTemplateCreatesVersion(t *testing.T) {
	t.Parallel()

	t.Run("publish creates version snapshot", func(t *testing.T) {
		t.Parallel()
		tmplRepo := NewMockStackTemplateRepository()
		chartRepo := NewMockTemplateChartConfigRepository()
		verRepo := NewMockTemplateVersionRepository()

		seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", false)
		require.NoError(t, chartRepo.Create(&models.TemplateChartConfig{
			ID:              "c1",
			StackTemplateID: "t1",
			ChartName:       "frontend",
			RepositoryURL:   "https://charts.example.com",
			DefaultValues:   "port: 80",
			Required:        true,
		}))

		router := setupTemplateVersionRouter(tmplRepo, chartRepo, NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), verRepo, "owner-1", "devops")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/publish", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		// Verify a version was created.
		versions, err := verRepo.ListByTemplate(nil, "t1")
		require.NoError(t, err)
		assert.Len(t, versions, 1)
		assert.Equal(t, "1.0.0", versions[0].Version) // From seedTemplate

		// Verify snapshot contains chart data.
		var snap models.TemplateSnapshot
		require.NoError(t, json.Unmarshal([]byte(versions[0].Snapshot), &snap))
		assert.Len(t, snap.Charts, 1)
		assert.Equal(t, "frontend", snap.Charts[0].ChartName)
	})

	t.Run("publish without version repo still succeeds", func(t *testing.T) {
		t.Parallel()
		tmplRepo := NewMockStackTemplateRepository()
		seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", false)

		// Use the non-version constructor.
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(injectAuthContext("owner-1", "devops"))
		h := NewTemplateHandler(tmplRepo, NewMockTemplateChartConfigRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository())
		r.POST("/api/v1/templates/:id/publish", h.PublishTemplate)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/t1/publish", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---- CheckUpgrade ----

func TestCheckUpgrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		defID            string
		setup            func(*MockStackDefinitionRepository, *MockChartConfigRepository, *MockTemplateVersionRepository)
		wantStatus       int
		upgradeAvailable bool
	}{
		{
			name:  "upgrade available",
			defID: "d1",
			setup: func(defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, verRepo *MockTemplateVersionRepository) {
				defRepo.Create(&models.StackDefinition{
					ID: "d1", Name: "My Def", OwnerID: "u1",
					SourceTemplateID: "t1", SourceTemplateVersion: "1.0",
				})
				ccRepo.Create(&models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "frontend", DefaultValues: "port: 80"})
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "2.0"}, []models.TemplateChartSnapshotData{
					{ChartName: "frontend", DefaultValues: "port: 8080"},
					{ChartName: "new-service"},
				})
				seedVersion(t, verRepo, "v1", "t1", "2.0", snap, time.Now())
			},
			wantStatus:       http.StatusOK,
			upgradeAvailable: true,
		},
		{
			name:  "no upgrade when at latest",
			defID: "d1",
			setup: func(defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository, verRepo *MockTemplateVersionRepository) {
				defRepo.Create(&models.StackDefinition{
					ID: "d1", Name: "My Def", OwnerID: "u1",
					SourceTemplateID: "t1", SourceTemplateVersion: "2.0",
				})
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "2.0"}, nil)
				seedVersion(t, verRepo, "v1", "t1", "2.0", snap, time.Now())
			},
			wantStatus:       http.StatusOK,
			upgradeAvailable: false,
		},
		{
			name:  "no source template returns false",
			defID: "d1",
			setup: func(defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockTemplateVersionRepository) {
				defRepo.Create(&models.StackDefinition{
					ID: "d1", Name: "My Def", OwnerID: "u1",
				})
			},
			wantStatus:       http.StatusOK,
			upgradeAvailable: false,
		},
		{
			name:  "definition not found returns 404",
			defID: "missing",
			setup: func(_ *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockTemplateVersionRepository) {
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			verRepo := NewMockTemplateVersionRepository()
			tt.setup(defRepo, ccRepo, verRepo)

			router := setupDefinitionUpgradeRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), verRepo, "uid-1", "devops")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/"+tt.defID+"/check-upgrade", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tt.upgradeAvailable, resp["upgrade_available"])
			}
		})
	}
}

// ---- ApplyUpgrade ----

func TestApplyUpgrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		defID      string
		setup      func(*MockStackDefinitionRepository, *MockChartConfigRepository, *MockTemplateVersionRepository)
		wantStatus int
		check      func(*testing.T, *httptest.ResponseRecorder, *MockChartConfigRepository)
	}{
		{
			name:  "adds new charts and updates existing",
			defID: "d1",
			setup: func(defRepo *MockStackDefinitionRepository, ccRepo *MockChartConfigRepository, verRepo *MockTemplateVersionRepository) {
				defRepo.Create(&models.StackDefinition{
					ID: "d1", Name: "My Def", OwnerID: "u1",
					SourceTemplateID: "t1", SourceTemplateVersion: "1.0",
				})
				ccRepo.Create(&models.ChartConfig{
					ID: "cc1", StackDefinitionID: "d1", ChartName: "frontend",
					RepositoryURL: "https://old.example.com", DefaultValues: "port: 80",
				})
				// User-added chart not in template should be preserved.
				ccRepo.Create(&models.ChartConfig{
					ID: "cc2", StackDefinitionID: "d1", ChartName: "user-custom",
					DefaultValues: "custom: true",
				})
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "2.0"}, []models.TemplateChartSnapshotData{
					{ChartName: "frontend", RepoURL: "https://new.example.com", DefaultValues: "port: 8080"},
					{ChartName: "backend", RepoURL: "https://backend.example.com", DefaultValues: "port: 3000", IsRequired: true},
				})
				seedVersion(t, verRepo, "v1", "t1", "2.0", snap, time.Now())
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, w *httptest.ResponseRecorder, ccRepo *MockChartConfigRepository) {
				t.Helper()
				var resp map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

				var def models.StackDefinition
				require.NoError(t, json.Unmarshal(resp["definition"], &def))
				assert.Equal(t, "2.0", def.SourceTemplateVersion)

				var charts []models.ChartConfig
				require.NoError(t, json.Unmarshal(resp["charts"], &charts))
				// Should have: frontend (updated), user-custom (preserved), backend (added)
				assert.Len(t, charts, 3)

				chartMap := make(map[string]models.ChartConfig)
				for _, ch := range charts {
					chartMap[ch.ChartName] = ch
				}
				assert.Equal(t, "port: 8080", chartMap["frontend"].DefaultValues)
				assert.Equal(t, "https://new.example.com", chartMap["frontend"].RepositoryURL)
				assert.Equal(t, "custom: true", chartMap["user-custom"].DefaultValues)
				assert.Equal(t, "port: 3000", chartMap["backend"].DefaultValues)
			},
		},
		{
			name:  "no source template returns 400",
			defID: "d1",
			setup: func(defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockTemplateVersionRepository) {
				defRepo.Create(&models.StackDefinition{
					ID: "d1", Name: "My Def", OwnerID: "u1",
				})
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:  "already at latest returns 409",
			defID: "d1",
			setup: func(defRepo *MockStackDefinitionRepository, _ *MockChartConfigRepository, verRepo *MockTemplateVersionRepository) {
				defRepo.Create(&models.StackDefinition{
					ID: "d1", Name: "My Def", OwnerID: "u1",
					SourceTemplateID: "t1", SourceTemplateVersion: "2.0",
				})
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "T", Version: "2.0"}, nil)
				seedVersion(t, verRepo, "v1", "t1", "2.0", snap, time.Now())
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:  "definition not found returns 404",
			defID: "missing",
			setup: func(_ *MockStackDefinitionRepository, _ *MockChartConfigRepository, _ *MockTemplateVersionRepository) {
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			verRepo := NewMockTemplateVersionRepository()
			tt.setup(defRepo, ccRepo, verRepo)

			router := setupDefinitionUpgradeRouter(defRepo, ccRepo, NewMockStackInstanceRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), verRepo, "uid-1", "devops")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-definitions/"+tt.defID+"/upgrade", bytes.NewBufferString("{}"))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.check != nil {
				tt.check(t, w, ccRepo)
			}
		})
	}
}
