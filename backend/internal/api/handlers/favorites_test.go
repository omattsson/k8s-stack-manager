package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func setupFavoriteRouter() (*gin.Engine, *MockUserFavoriteRepository) {
	gin.SetMode(gin.TestMode)
	repo := NewMockUserFavoriteRepository()
	handler := NewFavoriteHandler(repo)

	r := gin.New()
	// Simulate auth middleware injecting userID.
	r.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("username", "testuser")
		c.Set("role", "developer")
		c.Next()
	})
	favorites := r.Group("/api/v1/favorites")
	{
		favorites.GET("", handler.ListFavorites)
		favorites.POST("", handler.AddFavorite)
		favorites.DELETE("/:entityType/:entityId", handler.RemoveFavorite)
		favorites.GET("/check", handler.CheckFavorite)
	}
	return r, repo
}

func TestFavoriteHandler_ListFavorites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		seed           []*models.UserFavorite
		expectedStatus int
		expectedCount  int
	}{
		{
			name:           "empty list",
			seed:           nil,
			expectedStatus: http.StatusOK,
			expectedCount:  0,
		},
		{
			name: "returns own favorites only",
			seed: []*models.UserFavorite{
				{ID: "f1", UserID: "user-1", EntityType: "instance", EntityID: "inst-1"},
				{ID: "f2", UserID: "user-1", EntityType: "definition", EntityID: "def-1"},
				{ID: "f3", UserID: "other-user", EntityType: "instance", EntityID: "inst-2"},
			},
			expectedStatus: http.StatusOK,
			expectedCount:  2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupFavoriteRouter()

			for _, fav := range tt.seed {
				_ = repo.Add(fav)
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/favorites", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var result []*models.UserFavorite
			err := json.Unmarshal(w.Body.Bytes(), &result)
			assert.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)
		})
	}
}

func TestFavoriteHandler_AddFavorite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		expectedStatus int
	}{
		{
			name:           "valid add",
			body:           `{"entity_type":"instance","entity_id":"inst-1"}`,
			expectedStatus: http.StatusCreated,
		},
		{
			name:           "missing entity_type",
			body:           `{"entity_id":"inst-1"}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "missing entity_id",
			body:           `{"entity_type":"instance"}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid entity_type",
			body:           `{"entity_type":"invalid","entity_id":"inst-1"}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid JSON",
			body:           `not json`,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, _ := setupFavoriteRouter()

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/favorites", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectedStatus == http.StatusCreated {
				var fav models.UserFavorite
				err := json.Unmarshal(w.Body.Bytes(), &fav)
				assert.NoError(t, err)
				assert.Equal(t, "user-1", fav.UserID)
				assert.Equal(t, "instance", fav.EntityType)
				assert.Equal(t, "inst-1", fav.EntityID)
			}
		})
	}
}

func TestFavoriteHandler_AddFavorite_Idempotent(t *testing.T) {
	t.Parallel()
	router, _ := setupFavoriteRouter()

	body := `{"entity_type":"instance","entity_id":"inst-1"}`

	// First add.
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/api/v1/favorites", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusCreated, w1.Code)

	// Second add — should still succeed (idempotent).
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/api/v1/favorites", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusCreated, w2.Code)
}

func TestFavoriteHandler_AddFavorite_UserIDFromContext(t *testing.T) {
	t.Parallel()
	router, _ := setupFavoriteRouter()

	// Body intentionally does not include user_id — it comes from context.
	body := `{"entity_type":"template","entity_id":"tpl-5"}`

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/favorites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var fav models.UserFavorite
	_ = json.Unmarshal(w.Body.Bytes(), &fav)
	assert.Equal(t, "user-1", fav.UserID, "user_id should come from context, not request body")
}

func TestFavoriteHandler_RemoveFavorite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		entityType     string
		entityID       string
		seed           bool
		expectedStatus int
	}{
		{
			name:           "remove existing",
			entityType:     "instance",
			entityID:       "inst-1",
			seed:           true,
			expectedStatus: http.StatusNoContent,
		},
		{
			name:           "remove non-existing",
			entityType:     "instance",
			entityID:       "inst-999",
			seed:           false,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "invalid entity_type",
			entityType:     "invalid",
			entityID:       "inst-1",
			seed:           false,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupFavoriteRouter()

			if tt.seed {
				_ = repo.Add(&models.UserFavorite{
					UserID: "user-1", EntityType: tt.entityType, EntityID: tt.entityID,
				})
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("DELETE", "/api/v1/favorites/"+tt.entityType+"/"+tt.entityID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

func TestFavoriteHandler_CheckFavorite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		query          string
		seed           bool
		expectedStatus int
		expectedFav    bool
	}{
		{
			name:           "is favorite",
			query:          "?entity_type=instance&entity_id=inst-1",
			seed:           true,
			expectedStatus: http.StatusOK,
			expectedFav:    true,
		},
		{
			name:           "not favorite",
			query:          "?entity_type=instance&entity_id=inst-999",
			seed:           false,
			expectedStatus: http.StatusOK,
			expectedFav:    false,
		},
		{
			name:           "missing params",
			query:          "",
			seed:           false,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid entity_type",
			query:          "?entity_type=invalid&entity_id=inst-1",
			seed:           false,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupFavoriteRouter()

			if tt.seed {
				_ = repo.Add(&models.UserFavorite{
					UserID: "user-1", EntityType: "instance", EntityID: "inst-1",
				})
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/favorites/check"+tt.query, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectedStatus == http.StatusOK {
				var result map[string]bool
				err := json.Unmarshal(w.Body.Bytes(), &result)
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedFav, result["is_favorite"])
			}
		})
	}
}

func setupRecentInstancesRouter() (*gin.Engine, *MockStackInstanceRepository) {
	gin.SetMode(gin.TestMode)
	instanceRepo := NewMockStackInstanceRepository()
	handler := NewInstanceHandler(
		instanceRepo,
		NewMockValueOverrideRepository(),
		NewMockChartBranchOverrideRepository(),
		NewMockStackDefinitionRepository(),
		NewMockChartConfigRepository(),
		NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(),
		nil, // valuesGen
		NewMockUserRepository(),
		60, // defaultTTLMinutes
	)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("username", "testuser")
		c.Set("role", "developer")
		c.Next()
	})
	instances := r.Group("/api/v1/stack-instances")
	{
		instances.GET("/recent", handler.GetRecentInstances)
	}
	return r, instanceRepo
}

func TestInstanceHandler_GetRecentInstances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		instances      []*models.StackInstance
		expectedCount  int
		expectedStatus int
	}{
		{
			name:           "empty",
			instances:      nil,
			expectedCount:  0,
			expectedStatus: http.StatusOK,
		},
		{
			name: "returns max 5 sorted by updated_at",
			instances: func() []*models.StackInstance {
				base := time.Now()
				var out []*models.StackInstance
				for i := 0; i < 8; i++ {
					out = append(out, &models.StackInstance{
						ID:        strings.Replace("inst-X", "X", string(rune('0'+i)), 1),
						Name:      "test",
						OwnerID:   "user-1",
						Status:    models.StackStatusRunning,
						UpdatedAt: base.Add(time.Duration(i) * time.Minute),
					})
				}
				return out
			}(),
			expectedCount:  5,
			expectedStatus: http.StatusOK,
		},
		{
			name: "only own instances",
			instances: []*models.StackInstance{
				{ID: "inst-mine", Name: "mine", OwnerID: "user-1", Status: "running", UpdatedAt: time.Now()},
				{ID: "inst-other", Name: "other", OwnerID: "user-2", Status: "running", UpdatedAt: time.Now()},
			},
			expectedCount:  1,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupRecentInstancesRouter()

			for _, inst := range tt.instances {
				_ = repo.Create(inst)
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/stack-instances/recent", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var result []models.StackInstance
			err := json.Unmarshal(w.Body.Bytes(), &result)
			assert.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)

			// Verify sorted descending.
			for i := 1; i < len(result); i++ {
				assert.True(t, !result[i-1].UpdatedAt.Before(result[i].UpdatedAt),
					"results should be sorted by updated_at descending")
			}
		})
	}
}

// ---- Error path tests for coverage ----

func TestFavoriteHandler_ListFavorites_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupFavoriteRouter()
	repo.SetError(errors.New("db connection lost"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/favorites", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	assert.Equal(t, "Internal server error", body["error"])
}

func TestFavoriteHandler_AddFavorite_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupFavoriteRouter()
	repo.SetError(errors.New("write failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/favorites",
		strings.NewReader(`{"entity_type":"instance","entity_id":"inst-1"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestFavoriteHandler_RemoveFavorite_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupFavoriteRouter()
	_ = repo.Add(&models.UserFavorite{UserID: "user-1", EntityType: "instance", EntityID: "inst-1"})
	repo.SetError(errors.New("delete failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/favorites/instance/inst-1", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestFavoriteHandler_CheckFavorite_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupFavoriteRouter()
	repo.SetError(errors.New("read failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/favorites/check?entity_type=instance&entity_id=inst-1", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	assert.Equal(t, "Internal server error", body["error"])
}
