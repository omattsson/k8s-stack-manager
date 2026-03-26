package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupBulkTemplateRouter creates a test gin engine with bulk template and list routes.
func setupBulkTemplateRouter(
	tmplRepo *MockStackTemplateRepository,
	defRepo *MockStackDefinitionRepository,
	userRepo *MockUserRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if callerID != "" {
			c.Set("userID", callerID)
		}
		if callerRole != "" {
			c.Set("role", callerRole)
		}
		c.Next()
	})

	h := NewTemplateHandler(tmplRepo, nil, defRepo, nil)
	h.SetUserRepo(userRepo)

	bulk := r.Group("/api/v1/templates/bulk")
	{
		bulk.POST("/delete", h.BulkDeleteTemplates)
		bulk.POST("/publish", h.BulkPublishTemplates)
		bulk.POST("/unpublish", h.BulkUnpublishTemplates)
	}
	r.GET("/api/v1/templates", h.ListTemplates)

	return r
}

func TestBulkDeleteTemplates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		callerID   string
		callerRole string
		setup      func(*MockStackTemplateRepository, *MockStackDefinitionRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — delete unpublished owned templates",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1", "t2"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {
				seedTemplate(t, tmplRepo, "t1", "Template A", "uid-1", false)
				seedTemplate(t, tmplRepo, "t2", "Template B", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 2, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
				for _, r := range resp.Results {
					assert.Equal(t, "success", r.Status)
				}
			},
		},
		{
			name:       "rejects published template — per-template error",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {
				seedTemplate(t, tmplRepo, "t1", "Published Tmpl", "uid-1", true)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, 0, resp.Succeeded)
				assert.Contains(t, resp.Results[0].Error, "published")
			},
		},
		{
			name:       "rejects template with linked definitions",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository, defRepo *MockStackDefinitionRepository) {
				seedTemplate(t, tmplRepo, "t1", "Template With Defs", "uid-1", false)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:               "d1",
					Name:             "Def A",
					OwnerID:          "uid-1",
					SourceTemplateID: "t1",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Contains(t, resp.Results[0].Error, "definition")
			},
		},
		{
			name:       "devops cannot delete others templates — not authorized",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "uid-2",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {
				seedTemplate(t, tmplRepo, "t1", "Other User Tmpl", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "not authorized", resp.Results[0].Error)
			},
		},
		{
			name:       "admin can delete any users templates",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "admin-1",
			callerRole: "admin",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {
				seedTemplate(t, tmplRepo, "t1", "Other User Tmpl", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
			},
		},
		{
			name:       "template not found — per-template error",
			body:       BulkTemplateRequest{TemplateIDs: []string{"nonexistent"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup:      func(_ *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "template not found", resp.Results[0].Error)
			},
		},
		{
			name:       "empty template_ids returns 400",
			body:       BulkTemplateRequest{TemplateIDs: []string{}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup:      func(_ *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "too many template IDs returns 400",
			body: BulkTemplateRequest{TemplateIDs: func() []string {
				ids := make([]string, 51)
				for i := range ids {
					ids[i] = "id"
				}
				return ids
			}()},
			callerID:   "uid-1",
			callerRole: "devops",
			setup:      func(_ *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON returns 400",
			body:       "not json",
			callerID:   "uid-1",
			callerRole: "devops",
			setup:      func(_ *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "partial success — one deleted one not found",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1", "missing"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository) {
				seedTemplate(t, tmplRepo, "t1", "Good Tmpl", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 1, resp.Failed)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			defRepo := NewMockStackDefinitionRepository()
			tt.setup(tmplRepo, defRepo)

			router := setupBulkTemplateRouter(tmplRepo, defRepo, NewMockUserRepository(), tt.callerID, tt.callerRole)

			var body []byte
			switch v := tt.body.(type) {
			case string:
				body = []byte(v)
			default:
				body, _ = json.Marshal(v)
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/bulk/delete", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

func TestBulkPublishTemplates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		callerID   string
		callerRole string
		setup      func(*MockStackTemplateRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — publish unpublished templates",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1", "t2"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Tmpl A", "uid-1", false)
				seedTemplate(t, tmplRepo, "t2", "Tmpl B", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 2, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
				for _, r := range resp.Results {
					assert.Equal(t, "success", r.Status)
				}
			},
		},
		{
			name:       "already published — treated as success",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Already Pub", "uid-1", true)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
			},
		},
		{
			name:       "unauthorized — user cannot publish others templates",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "uid-2",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Other Tmpl", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "not authorized", resp.Results[0].Error)
			},
		},
		{
			name:       "admin can publish any template",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "admin-1",
			callerRole: "admin",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Other Tmpl", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
			},
		},
		{
			name:       "template not found",
			body:       BulkTemplateRequest{TemplateIDs: []string{"nonexistent"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup:      func(_ *MockStackTemplateRepository) {},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "template not found", resp.Results[0].Error)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			tt.setup(tmplRepo)

			router := setupBulkTemplateRouter(tmplRepo, NewMockStackDefinitionRepository(), NewMockUserRepository(), tt.callerID, tt.callerRole)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/bulk/publish", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

func TestBulkUnpublishTemplates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       interface{}
		callerID   string
		callerRole string
		setup      func(*MockStackTemplateRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "happy path — unpublish published templates",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1", "t2"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Tmpl A", "uid-1", true)
				seedTemplate(t, tmplRepo, "t2", "Tmpl B", "uid-1", true)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 2, resp.Total)
				assert.Equal(t, 2, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
				for _, r := range resp.Results {
					assert.Equal(t, "success", r.Status)
				}
			},
		},
		{
			name:       "already unpublished — treated as success",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Not Pub", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
			},
		},
		{
			name:       "unauthorized — non-owner cannot unpublish",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "uid-2",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Other Tmpl", "uid-1", true)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "not authorized", resp.Results[0].Error)
			},
		},
		{
			name:       "admin can unpublish any template",
			body:       BulkTemplateRequest{TemplateIDs: []string{"t1"}},
			callerID:   "admin-1",
			callerRole: "admin",
			setup: func(tmplRepo *MockStackTemplateRepository) {
				seedTemplate(t, tmplRepo, "t1", "Other Tmpl", "uid-1", true)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 0, resp.Failed)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			tt.setup(tmplRepo)

			router := setupBulkTemplateRouter(tmplRepo, NewMockStackDefinitionRepository(), NewMockUserRepository(), tt.callerID, tt.callerRole)

			body, _ := json.Marshal(tt.body)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/templates/bulk/unpublish", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}

func TestListTemplates_Enriched(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		callerID   string
		callerRole string
		setup      func(*MockStackTemplateRepository, *MockStackDefinitionRepository, *MockUserRepository)
		wantStatus int
		checkFn    func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "returns definition_count and owner_username",
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(tmplRepo *MockStackTemplateRepository, defRepo *MockStackDefinitionRepository, userRepo *MockUserRepository) {
				seedTemplate(t, tmplRepo, "t1", "Tmpl A", "uid-1", true)
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:               "d1",
					Name:             "Def 1",
					OwnerID:          "uid-1",
					SourceTemplateID: "t1",
				}))
				require.NoError(t, defRepo.Create(&models.StackDefinition{
					ID:               "d2",
					Name:             "Def 2",
					OwnerID:          "uid-1",
					SourceTemplateID: "t1",
				}))
				require.NoError(t, userRepo.Create(&models.User{
					ID:       "uid-1",
					Username: "alice",
				}))
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var items []TemplateListItem
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
				require.Len(t, items, 1)
				assert.Equal(t, 2, items[0].DefinitionCount)
				assert.Equal(t, "alice", items[0].OwnerUsername)
			},
		},
		{
			name:       "regular user sees only published templates",
			callerID:   "uid-1",
			callerRole: "user",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockUserRepository) {
				seedTemplate(t, tmplRepo, "t1", "Published", "uid-2", true)
				seedTemplate(t, tmplRepo, "t2", "Unpublished", "uid-2", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var items []TemplateListItem
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
				require.Len(t, items, 1)
				assert.Equal(t, "Published", items[0].Name)
			},
		},
		{
			name:       "devops sees all templates",
			callerID:   "uid-1",
			callerRole: "devops",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockUserRepository) {
				seedTemplate(t, tmplRepo, "t1", "Published", "uid-1", true)
				seedTemplate(t, tmplRepo, "t2", "Unpublished", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var items []TemplateListItem
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
				assert.Len(t, items, 2)
			},
		},
		{
			name:       "admin sees all templates",
			callerID:   "admin-1",
			callerRole: "admin",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockUserRepository) {
				seedTemplate(t, tmplRepo, "t1", "Published", "uid-1", true)
				seedTemplate(t, tmplRepo, "t2", "Unpublished", "uid-1", false)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var items []TemplateListItem
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
				assert.Len(t, items, 2)
			},
		},
		{
			name:       "definition_count is zero when no definitions exist",
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockUserRepository) {
				seedTemplate(t, tmplRepo, "t1", "Lonely Tmpl", "uid-1", true)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var items []TemplateListItem
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
				require.Len(t, items, 1)
				assert.Equal(t, 0, items[0].DefinitionCount)
			},
		},
		{
			name:       "owner_username empty when userRepo has no matching user",
			callerID:   "uid-1",
			callerRole: "admin",
			setup: func(tmplRepo *MockStackTemplateRepository, _ *MockStackDefinitionRepository, _ *MockUserRepository) {
				seedTemplate(t, tmplRepo, "t1", "Tmpl A", "uid-unknown", true)
			},
			wantStatus: http.StatusOK,
			checkFn: func(t *testing.T, w *httptest.ResponseRecorder) {
				var items []TemplateListItem
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
				require.Len(t, items, 1)
				assert.Empty(t, items[0].OwnerUsername)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			defRepo := NewMockStackDefinitionRepository()
			userRepo := NewMockUserRepository()
			tt.setup(tmplRepo, defRepo, userRepo)

			router := setupBulkTemplateRouter(tmplRepo, defRepo, userRepo, tt.callerID, tt.callerRole)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/templates", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkFn != nil {
				tt.checkFn(t, w)
			}
		})
	}
}
