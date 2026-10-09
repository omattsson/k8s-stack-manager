package handlers

import (
	"net/http"
	"testing"

	"backend/internal/database"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestCreatePublished_RollbackWithGORMTx checks with a real (SQLite)
// transaction that a failed publish in POST /templates (is_published=true)
// rolls the create back: neither the template nor its charts are stored.
func TestCreatePublished_RollbackWithGORMTx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		migrateVers   bool
		wantStatus    int
		wantTemplates int64
	}{
		{"publish fails: create rolled back", false, http.StatusInternalServerError, 0},
		{"publish works: template and snapshot stored", true, http.StatusCreated, 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1) // one connection: one in-memory database
			require.NoError(t, db.AutoMigrate(&models.StackTemplate{}, &models.TemplateChartConfig{}))
			if tt.migrateVers {
				require.NoError(t, db.AutoMigrate(&models.TemplateVersion{}))
			}

			th, err := NewTemplateHandlerWithVersions(
				database.NewGORMStackTemplateRepository(db),
				database.NewGORMTemplateChartConfigRepository(db),
				nil, nil,
				database.NewGORMTemplateVersionRepository(db),
				database.NewGORMTxRunner(db),
			)
			require.NoError(t, err)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("uid-dev", "devops"))
			r.POST("/api/v1/templates", th.CreateTemplate)

			w := serve(r, http.MethodPost, "/api/v1/templates",
				`{"name":"New","version":"1.0.0","is_published":true,"charts":[{"chart_name":"web"}]}`)
			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			var templates, charts int64
			require.NoError(t, db.Model(&models.StackTemplate{}).Count(&templates).Error)
			require.NoError(t, db.Model(&models.TemplateChartConfig{}).Count(&charts).Error)
			assert.Equal(t, tt.wantTemplates, templates)
			assert.Equal(t, tt.wantTemplates, charts)
			if tt.migrateVers {
				var versions int64
				require.NoError(t, db.Model(&models.TemplateVersion{}).Count(&versions).Error)
				assert.Equal(t, int64(1), versions)
			}
		})
	}
}
