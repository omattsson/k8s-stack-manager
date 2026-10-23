package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// templateLinkRequest is the body of PUT /stack-definitions/:id/template-link.
type templateLinkRequest struct {
	// TemplateID is the template to link. Empty removes the link.
	TemplateID string `json:"template_id" example:"6f1c2d3e-0000-0000-0000-000000000000"`
	// Version is a published version of the template. Empty means the
	// latest published version.
	Version string `json:"version" example:"1.2.0"`
}

// templateLinkError is a template link problem with its HTTP status and a
// user-safe message.
type templateLinkError struct {
	message string
	status  int
}

func (e *templateLinkError) Error() string { return e.message }

// resolveTemplateLink checks that the template exists and is published, and
// that version is one of its published versions (snapshots). An empty version
// means the latest published version. It returns the template and the version
// to store, or a *templateLinkError (404, 409 or 400) or another error.
func (h *DefinitionHandler) resolveTemplateLink(ctx context.Context, templateID, version string) (*models.StackTemplate, string, error) {
	tmpl, err := h.templateRepo.FindByID(ctx, templateID)
	if err != nil {
		if isNotFoundError(err) {
			return nil, "", &templateLinkError{status: http.StatusNotFound, message: "Template not found"}
		}
		return nil, "", fmt.Errorf("find template: %w", err)
	}
	if !tmpl.IsPublished || h.versionRepo == nil {
		return nil, "", &templateLinkError{status: http.StatusConflict, message: msgNoPublishedVersion}
	}
	if version == "" {
		latest, err := h.versionRepo.GetLatestByTemplate(ctx, tmpl.ID)
		if err != nil {
			if isNotFoundError(err) {
				return nil, "", &templateLinkError{status: http.StatusConflict, message: msgNoPublishedVersion}
			}
			return nil, "", fmt.Errorf("get latest template version: %w", err)
		}
		return tmpl, latest.Version, nil
	}
	versions, err := h.versionRepo.ListByTemplate(ctx, tmpl.ID)
	if err != nil {
		return nil, "", fmt.Errorf("list template versions: %w", err)
	}
	for _, v := range versions {
		if v.Version == version {
			return tmpl, version, nil
		}
	}
	return nil, "", &templateLinkError{
		status:  http.StatusBadRequest,
		message: fmt.Sprintf("Version %q is not a published version of template %q", version, tmpl.Name),
	}
}

// SetTemplateLink godoc
// @Summary     Link a stack definition to a template
// @Description Sets or removes the source template of an existing definition (for example an imported one). The link changes only source_template_id and source_template_version; the charts and the values of the definition do not change.
// @Description A linked definition gets the template rules: required charts cannot be removed, the locked values of the latest published snapshot apply at deploy time (also when the link names an older version; the version is the base of the upgrade check), the upgrade check compares with the template, and the analytics template usage counts the definition, its instances and its deploys. A definition whose charts differ from the template can be linked; the upgrade check then shows the difference.
// @Description template_id empty removes the link (both fields become empty; version must then be empty). The template must exist (404) and be published (409). version must be a published version of the template (400); empty means the latest published version.
// @Description Authorization: devops or admin (403).
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       id   path     string              true "Definition ID"
// @Param       body body     templateLinkRequest true "Template link"
// @Success     200  {object} models.StackDefinition
// @Failure     400  {object} map[string]string
// @Failure     403  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     409  {object} map[string]string
// @Failure     500  {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-definitions/{id}/template-link [put]
func (h *DefinitionHandler) SetTemplateLink(c *gin.Context) {
	id := c.Param("id")
	def, err := h.definitionRepo.FindByID(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}
	if !requireDefinitionModify(c, def) {
		return
	}

	var req templateLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}
	req.TemplateID = strings.TrimSpace(req.TemplateID)
	req.Version = strings.TrimSpace(req.Version)

	templateID, version := "", ""
	if req.TemplateID == "" {
		if req.Version != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "version needs a template_id"})
			return
		}
	} else {
		tmpl, resolved, err := h.resolveTemplateLink(c.Request.Context(), req.TemplateID, req.Version)
		if err != nil {
			var linkErr *templateLinkError
			if errors.As(err, &linkErr) {
				c.JSON(linkErr.status, gin.H{"error": linkErr.message})
				return
			}
			if respondCancelled(c, err) {
				return
			}
			slog.Error("failed to resolve template link", "definition_id", def.ID, "template_id", req.TemplateID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		templateID, version = tmpl.ID, resolved
	}

	// A targeted write: a concurrent edit of other fields is not lost.
	// Detached from here on: a committed link must answer 200 for its audit entry (#656).
	wctx := context.WithoutCancel(c.Request.Context())
	if err := h.definitionRepo.SetTemplateLink(wctx, def.ID, templateID, version); err != nil {
		status, message := mapError(err, entityStackDefinition)
		if status == http.StatusInternalServerError {
			slog.Error("failed to update template link", "definition_id", def.ID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}
	updated, err := h.definitionRepo.FindByID(wctx, def.ID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}
	setDefinitionOwnerNames(c.Request.Context(), h.userRepo, updated)
	c.JSON(http.StatusOK, updated)
}

// importTemplateLink resolves the optional source template of an import
// bundle. It looks up the template by ID, else by name among the published
// templates (one match only). It returns the template ID and version to store, or empty values and
// a warning when the link cannot be set. The name match is the one of the
// template list filter (not case-sensitive on MySQL). Lookup failures give a warning, not
// an error: the import never fails because of the link.
func (h *DefinitionHandler) importTemplateLink(ctx context.Context, src *SourceTemplateExportData) (string, string, string) {
	if src == nil || (strings.TrimSpace(src.ID) == "" && strings.TrimSpace(src.Name) == "") {
		return "", "", ""
	}
	id, name, version := strings.TrimSpace(src.ID), strings.TrimSpace(src.Name), strings.TrimSpace(src.Version)
	label := name
	if label == "" {
		label = id
	}

	templateID := ""
	if id != "" {
		if _, err := h.templateRepo.FindByID(ctx, id); err == nil {
			templateID = id
		} else if !isNotFoundError(err) {
			logLookupError(ctx, "import: template lookup failed", err, "template_id", id)
			return "", "", fmt.Sprintf("The source template %q could not be checked; the definition is not linked", label)
		}
	}
	if templateID == "" && name != "" {
		// Only a published template can be linked, so drafts with the
		// same name do not make the name ambiguous.
		matches, _, err := h.templateRepo.ListPublishedPaged(ctx, 2, 0, name)
		if err != nil {
			logLookupError(ctx, "import: template lookup by name failed", err, "template_name", name)
			return "", "", fmt.Sprintf("The source template %q could not be checked; the definition is not linked", label)
		}
		if len(matches) > 1 {
			return "", "", fmt.Sprintf("More than one template is named %q; the definition is not linked", name)
		}
		if len(matches) == 1 {
			templateID = matches[0].ID
		}
	}
	if templateID == "" {
		return "", "", fmt.Sprintf("The source template %q does not exist; the definition is not linked", label)
	}

	tmpl, resolved, err := h.resolveTemplateLink(ctx, templateID, version)
	if err != nil {
		var linkErr *templateLinkError
		if errors.As(err, &linkErr) {
			return "", "", fmt.Sprintf("The definition is not linked to template %q: %s", label, linkErr.message)
		}
		logLookupError(ctx, "import: template link check failed", err, "template_id", templateID)
		return "", "", fmt.Sprintf("The source template %q could not be checked; the definition is not linked", label)
	}
	return tmpl.ID, resolved, ""
}
