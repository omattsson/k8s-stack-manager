package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTemplateLinkEnv returns a draft release env with template t1 published
// as 1.0.0 and 1.1.0, the unpublished template t2, and definition d1 (owner
// uid-owner, chart "web", no link).
func newTemplateLinkEnv(t *testing.T) *draftReleaseEnv {
	t.Helper()
	e := newDraftReleaseEnv(t)
	e.seedDraft(t)
	w, _ := e.publish(t, `{}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	e.setWorkingVersion(t, "1.1.0")
	w, _ = e.publish(t, `{}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	now := time.Now().UTC()
	require.NoError(t, e.templates.Create(&models.StackTemplate{
		ID: "t2", Name: "Draft stack", Version: "0.1.0", OwnerID: "uid-dev", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, e.defs.Create(context.Background(), &models.StackDefinition{
		ID: "d1", Name: "Imported", OwnerID: "uid-owner", DefaultBranch: "master", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, e.defCharts.Create(context.Background(), &models.ChartConfig{
		ID: "c1", StackDefinitionID: "d1", ChartName: "web", RepositoryURL: "oci://charts/web",
		ChartVersion: "0.9.0", DefaultValues: "replicas: 3", DeployOrder: 1, CreatedAt: now,
	}))
	return e
}

// templateLinkRouter adds the template link, import and export routes to the
// env router of the caller.
func templateLinkRouter(e *draftReleaseEnv, userID, role string) *gin.Engine {
	r := e.routerAs(userID, role)
	r.PUT("/api/v1/stack-definitions/:id/template-link", e.dh.SetTemplateLink)
	r.POST("/api/v1/stack-definitions/import", e.dh.ImportDefinition)
	r.GET("/api/v1/stack-definitions/:id/export", e.dh.ExportDefinition)
	return r
}

func TestSetTemplateLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		userID      string
		role        string
		definition  string
		preLink     bool
		body        string
		wantStatus  int
		wantID      string
		wantVersion string
		wantError   string
	}{
		{name: "latest version by default", userID: "uid-dev", role: "devops", definition: "d1",
			body: `{"template_id":"t1"}`, wantStatus: http.StatusOK, wantID: "t1", wantVersion: "1.1.0"},
		{name: "explicit older version", userID: "uid-dev", role: "devops", definition: "d1",
			body: `{"template_id":"t1","version":"1.0.0"}`, wantStatus: http.StatusOK, wantID: "t1", wantVersion: "1.0.0"},
		{name: "owner passes the handler check (the route also needs devops)", userID: "uid-owner", role: "user", definition: "d1",
			body: `{"template_id":" t1 "}`, wantStatus: http.StatusOK, wantID: "t1", wantVersion: "1.1.0"},
		{name: "empty template_id removes the link", userID: "uid-dev", role: "admin", definition: "d1", preLink: true,
			body: `{"template_id":""}`, wantStatus: http.StatusOK},
		{name: "unknown template", userID: "uid-dev", role: "devops", definition: "d1",
			body: `{"template_id":"nope"}`, wantStatus: http.StatusNotFound, wantError: "Template not found"},
		{name: "unpublished template", userID: "uid-dev", role: "devops", definition: "d1",
			body: `{"template_id":"t2"}`, wantStatus: http.StatusConflict, wantError: msgNoPublishedVersion},
		{name: "unknown version", userID: "uid-dev", role: "devops", definition: "d1",
			body: `{"template_id":"t1","version":"9.9.9"}`, wantStatus: http.StatusBadRequest,
			wantError: `Version "9.9.9" is not a published version of template "Web stack"`},
		{name: "version without template", userID: "uid-dev", role: "devops", definition: "d1",
			body: `{"version":"1.0.0"}`, wantStatus: http.StatusBadRequest, wantError: "version needs a template_id"},
		{name: "other user is refused", userID: "uid-other", role: "user", definition: "d1",
			body: `{"template_id":"t1"}`, wantStatus: http.StatusForbidden, wantError: msgDefinitionModifyForbidden},
		{name: "unknown definition", userID: "uid-dev", role: "devops", definition: "missing",
			body: `{"template_id":"t1"}`, wantStatus: http.StatusNotFound},
		{name: "invalid body", userID: "uid-dev", role: "devops", definition: "d1",
			body: `{`, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTemplateLinkEnv(t)
			if tt.preLink {
				def, err := e.defs.FindByID(context.Background(), "d1")
				require.NoError(t, err)
				def.SourceTemplateID, def.SourceTemplateVersion = "t1", "1.0.0"
				require.NoError(t, e.defs.Update(context.Background(), def))
			}
			chartsBefore, err := e.defCharts.ListByDefinition(context.Background(), "d1")
			require.NoError(t, err)

			w := serve(templateLinkRouter(e, tt.userID, tt.role), http.MethodPut,
				"/api/v1/stack-definitions/"+tt.definition+"/template-link", tt.body)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, tt.wantError, body["error"])
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			var resp models.StackDefinition
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantID, resp.SourceTemplateID)
			assert.Equal(t, tt.wantVersion, resp.SourceTemplateVersion)

			stored, err := e.defs.FindByID(context.Background(), "d1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, stored.SourceTemplateID)
			assert.Equal(t, tt.wantVersion, stored.SourceTemplateVersion)

			// The link never changes the charts or the values.
			chartsAfter, err := e.defCharts.ListByDefinition(context.Background(), "d1")
			require.NoError(t, err)
			assert.Equal(t, chartsBefore, chartsAfter)

			// The analytics template usage counts the linked definition.
			counts, err := e.defs.CountByTemplateIDs(context.Background(), []string{"t1"})
			require.NoError(t, err)
			want := 0
			if tt.wantID == "t1" {
				want = 1
			}
			assert.Equal(t, want, counts["t1"])
		})
	}
}

func TestImportDefinition_SourceTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		// duplicate adds template t3 with the name "Web stack": "published"
		// or "draft". Empty adds nothing.
		duplicate   string
		wantID      string
		wantVersion string
		wantWarning string
		noWarnings  bool
	}{
		{name: "by name, latest version", source: `{"name":"Web stack"}`, wantID: "t1", wantVersion: "1.1.0", noWarnings: true},
		{name: "by ID and version", source: `{"id":"t1","version":"1.0.0"}`, wantID: "t1", wantVersion: "1.0.0", noWarnings: true},
		{name: "unknown ID falls back to the name", source: `{"id":"other-install-id","name":"Web stack","version":"1.0.0"}`,
			wantID: "t1", wantVersion: "1.0.0", noWarnings: true},
		{name: "unknown template", source: `{"name":"Nope"}`,
			wantWarning: `The source template "Nope" does not exist; the definition is not linked`},
		{name: "unpublished template", source: `{"id":"t2"}`,
			wantWarning: `The definition is not linked to template "t2": ` + msgNoPublishedVersion},
		{name: "unknown version", source: `{"name":"Web stack","version":"9.9.9"}`,
			wantWarning: `The definition is not linked to template "Web stack": Version "9.9.9" is not a published version of template "Web stack"`},
		{name: "ambiguous name", source: `{"name":"Web stack"}`, duplicate: "published",
			wantWarning: `More than one template is named "Web stack"; the definition is not linked`},
		{name: "a draft with the same name does not count", source: `{"name":"Web stack"}`, duplicate: "draft",
			wantID: "t1", wantVersion: "1.1.0", noWarnings: true},
		{name: "no source template", source: ``, noWarnings: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTemplateLinkEnv(t)
			if tt.duplicate != "" {
				now := time.Now().UTC()
				require.NoError(t, e.templates.Create(&models.StackTemplate{
					ID: "t3", Name: "Web stack", Version: "1.0.0", OwnerID: "uid-dev", CreatedAt: now, UpdatedAt: now,
					IsPublished: tt.duplicate == "published",
				}))
			}
			source := ""
			if tt.source != "" {
				source = `,"source_template":` + tt.source
			}
			body := `{"schema_version":"1.0","definition":{"name":"Imported stack"` + source +
				`},"charts":[{"chart_name":"web","repository_url":"oci://charts/web","deploy_order":1}]}`

			w := serve(templateLinkRouter(e, "uid-dev", "devops"), http.MethodPost, "/api/v1/stack-definitions/import", body)

			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			var resp definitionImportResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantID, resp.SourceTemplateID)
			assert.Equal(t, tt.wantVersion, resp.SourceTemplateVersion)
			assert.Len(t, resp.Charts, 1)
			if tt.noWarnings {
				assert.Empty(t, resp.Warnings)
			} else {
				assert.Equal(t, []string{tt.wantWarning}, resp.Warnings)
			}
			stored, err := e.defs.FindByID(context.Background(), resp.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, stored.SourceTemplateID)
		})
	}
}

func TestExportDefinition_SourceTemplateRoundTrip(t *testing.T) {
	t.Parallel()
	e := newTemplateLinkEnv(t)
	r := templateLinkRouter(e, "uid-dev", "devops")
	w := serve(r, http.MethodPut, "/api/v1/stack-definitions/d1/template-link", `{"template_id":"t1","version":"1.0.0"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	w = serve(r, http.MethodGet, "/api/v1/stack-definitions/d1/export", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var bundle DefinitionExportBundle
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &bundle))
	require.NotNil(t, bundle.Definition.SourceTemplate)
	assert.Equal(t, SourceTemplateExportData{ID: "t1", Name: "Web stack", Version: "1.0.0"}, *bundle.Definition.SourceTemplate)

	w = serve(r, http.MethodPost, "/api/v1/stack-definitions/import", w.Body.String())
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp definitionImportResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "t1", resp.SourceTemplateID)
	assert.Equal(t, "1.0.0", resp.SourceTemplateVersion)
	assert.Empty(t, resp.Warnings)

	// A definition without a link exports no source_template.
	w = serve(r, http.MethodPut, "/api/v1/stack-definitions/d1/template-link", `{"template_id":""}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = serve(r, http.MethodGet, "/api/v1/stack-definitions/d1/export", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "source_template")
}
