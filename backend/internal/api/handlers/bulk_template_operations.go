package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
)

// MaxBulkTemplates is the maximum number of templates allowed per bulk template operation.
const MaxBulkTemplates = 50

const logKeyBulkTemplateID = "template_id"

// BulkTemplateRequest is the request body for bulk template operations.
type BulkTemplateRequest struct {
	TemplateIDs []string `json:"template_ids" binding:"required"`
}

// BulkTemplateResultItem represents the result of a single template in a bulk operation.
type BulkTemplateResultItem struct {
	TemplateID   string `json:"template_id"`
	TemplateName string `json:"template_name"`
	Status       string `json:"status"` // "success" or "error"
	Error        string `json:"error,omitempty"`
}

// BulkTemplateResponse is the response body for bulk template operations.
type BulkTemplateResponse struct {
	Total     int                      `json:"total"`
	Succeeded int                      `json:"succeeded"`
	Failed    int                      `json:"failed"`
	Results   []BulkTemplateResultItem `json:"results"`
}

// bulkTemplateOperationFunc is the signature for a function that operates on a single template.
type bulkTemplateOperationFunc func(c *gin.Context, tmpl *models.StackTemplate) error

// executeBulkTemplateOperation is a shared helper that validates the request, checks
// authorization per template, and invokes the given operation for each template.
func (h *TemplateHandler) executeBulkTemplateOperation(c *gin.Context, opName string, op bulkTemplateOperationFunc) {
	var req BulkTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format: template_ids is required"})
		return
	}

	if len(req.TemplateIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template_ids must not be empty"})
		return
	}

	// De-duplicate template IDs (and filter empty strings) while preserving order.
	seen := make(map[string]struct{}, len(req.TemplateIDs))
	uniqueIDs := make([]string, 0, len(req.TemplateIDs))
	for _, id := range req.TemplateIDs {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}

	if len(uniqueIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template_ids must not be empty"})
		return
	}

	if len(uniqueIDs) > MaxBulkTemplates {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Too many templates: maximum is %d", MaxBulkTemplates)})
		return
	}

	resp := BulkTemplateResponse{
		Total:   len(uniqueIDs),
		Results: make([]BulkTemplateResultItem, 0, len(uniqueIDs)),
	}

	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)

	for _, id := range uniqueIDs {
		result := BulkTemplateResultItem{
			TemplateID: id,
		}

		tmpl, err := h.templateRepo.FindByID(id)
		if err != nil {
			result.Status = "error"
			if errors.Is(err, dberrors.ErrNotFound) {
				result.Error = "template not found"
			} else {
				result.Error = msgInternalServerError
				slog.Error("bulk "+opName+" FindByID failed",
					logKeyBulkTemplateID, id,
					"error", err,
				)
			}
			resp.Failed++
			resp.Results = append(resp.Results, result)
			continue
		}

		result.TemplateName = tmpl.Name

		// Authorization: admin can operate on any template; others only their own.
		if role != "admin" && tmpl.OwnerID != userID {
			result.Status = "error"
			result.Error = "not authorized"
			resp.Failed++
			resp.Results = append(resp.Results, result)
			continue
		}

		if err := op(c, tmpl); err != nil {
			result.Status = "error"
			result.Error = err.Error()
			resp.Failed++
			slog.Warn("bulk "+opName+" failed for template",
				logKeyBulkTemplateID, id,
				"error", err,
			)
		} else {
			result.Status = "success"
			resp.Succeeded++
		}

		resp.Results = append(resp.Results, result)
	}

	c.JSON(http.StatusOK, resp)
}

// BulkDeleteTemplates godoc
// @Summary     Bulk delete stack templates
// @Description Delete multiple stack templates in a single request. Only unpublished templates with no linked definitions can be deleted.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       request body     BulkTemplateRequest true "Template IDs to delete"
// @Success     200     {object} BulkTemplateResponse
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Router      /api/v1/templates/bulk/delete [post]
func (h *TemplateHandler) BulkDeleteTemplates(c *gin.Context) {
	h.executeBulkTemplateOperation(c, "delete", func(_ *gin.Context, tmpl *models.StackTemplate) error {
		if tmpl.IsPublished {
			return fmt.Errorf("template is published")
		}

		// Check that no definitions reference this template.
		if h.definitionRepo != nil {
			defs, err := h.definitionRepo.ListByTemplate(tmpl.ID)
			if err != nil {
				slog.Error("bulk delete: failed to check definitions", logKeyBulkTemplateID, tmpl.ID, "error", err)
				return fmt.Errorf("failed to check linked definitions")
			}
			if len(defs) > 0 {
				return fmt.Errorf("template is used by %d definition(s)", len(defs))
			}
		}

		if err := h.templateRepo.Delete(tmpl.ID); err != nil {
			slog.Error("bulk delete: failed to delete template", logKeyBulkTemplateID, tmpl.ID, "error", err)
			return fmt.Errorf("failed to delete template")
		}

		return nil
	})
}

// BulkPublishTemplates godoc
// @Summary     Bulk publish stack templates
// @Description Publish multiple stack templates in a single request, making them visible to all users.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       request body     BulkTemplateRequest true "Template IDs to publish"
// @Success     200     {object} BulkTemplateResponse
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Router      /api/v1/templates/bulk/publish [post]
func (h *TemplateHandler) BulkPublishTemplates(c *gin.Context) {
	h.executeBulkTemplateOperation(c, "publish", func(c *gin.Context, tmpl *models.StackTemplate) error {
		if tmpl.IsPublished {
			return nil // already published, treat as success
		}

		tmpl.IsPublished = true
		tmpl.UpdatedAt = timeNow()

		if err := h.templateRepo.Update(tmpl); err != nil {
			slog.Error("bulk publish: failed to update template", logKeyBulkTemplateID, tmpl.ID, "error", err)
			return fmt.Errorf("failed to publish template")
		}

		// Auto-create a version snapshot on publish.
		if h.versionRepo != nil {
			h.createVersionSnapshot(c, tmpl)
		}

		return nil
	})
}

// BulkUnpublishTemplates godoc
// @Summary     Bulk unpublish stack templates
// @Description Unpublish multiple stack templates in a single request, hiding them from regular users.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       request body     BulkTemplateRequest true "Template IDs to unpublish"
// @Success     200     {object} BulkTemplateResponse
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Router      /api/v1/templates/bulk/unpublish [post]
func (h *TemplateHandler) BulkUnpublishTemplates(c *gin.Context) {
	h.executeBulkTemplateOperation(c, "unpublish", func(_ *gin.Context, tmpl *models.StackTemplate) error {
		if !tmpl.IsPublished {
			return nil // already unpublished, treat as success
		}

		tmpl.IsPublished = false
		tmpl.UpdatedAt = timeNow()

		if err := h.templateRepo.Update(tmpl); err != nil {
			slog.Error("bulk unpublish: failed to update template", logKeyBulkTemplateID, tmpl.ID, "error", err)
			return fmt.Errorf("failed to unpublish template")
		}

		return nil
	})
}

// timeNow is a package-level var so tests can override the clock if needed.
var timeNow = timeNowUTC

func timeNowUTC() time.Time {
	return time.Now().UTC()
}
