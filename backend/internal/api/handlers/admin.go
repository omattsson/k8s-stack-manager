package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"backend/internal/cluster"
	"backend/internal/deployer"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
)

// Admin handler message constants.
const (
	msgK8sClientUnavailable = "Kubernetes client is not available"
)

const logKeyNamespace = "namespace"



// OrphanedNamespaceResponse is the JSON representation of an orphaned namespace.
type OrphanedNamespaceResponse struct {
	CreatedAt      time.Time           `json:"created_at"`
	Name           string              `json:"name"`
	Phase          string              `json:"phase"`
	HelmReleases   []string            `json:"helm_releases"`
	ResourceCounts *k8s.ResourceCounts `json:"resource_counts,omitempty"`
}

// rfc1123LabelRegex matches a valid RFC1123 DNS label: starts and ends with alnum, only alnum and dashes, max 63 chars.
var rfc1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// AdminHandler provides administrative endpoints for cluster maintenance.
type AdminHandler struct {
	registry     *cluster.Registry
	instanceRepo models.StackInstanceRepository
}

// NewAdminHandler creates a new AdminHandler with the given dependencies.
func NewAdminHandler(
	registry *cluster.Registry,
	instanceRepo models.StackInstanceRepository,
) *AdminHandler {
	return &AdminHandler{
		registry:     registry,
		instanceRepo: instanceRepo,
	}
}

// ListOrphanedNamespaces returns all stack-* namespaces that have no matching StackInstance.
// @Summary      List orphaned namespaces
// @Description  Lists all Kubernetes namespaces matching the stack-* pattern that have no corresponding stack instance in the database. Pass ?details=true to include resource counts and helm releases per namespace (expensive).
// @Tags         Admin
// @Produce      json
// @Param        details  query  string  false  "Include resource counts and helm releases (true/false)"
// @Success      200  {array}   OrphanedNamespaceResponse
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/v1/admin/orphaned-namespaces [get]
// @Security     BearerAuth
func (h *AdminHandler) ListOrphanedNamespaces(c *gin.Context) {
	ctx := c.Request.Context()

	if h.registry == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgK8sClientUnavailable})
		return
	}

	clients, err := h.registry.GetDefaultClients()
	if err != nil {
		slog.Error("Failed to get default cluster clients", "error", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgK8sClientUnavailable})
		return
	}
	k8sClient := clients.K8s
	helmExecutor := clients.Helm

	includeDetails := c.Query("details") == "true"

	namespaces, err := k8sClient.ListStackNamespaces(ctx)
	if err != nil {
		slog.Error("Failed to list stack namespaces", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	var orphaned []OrphanedNamespaceResponse
	for _, ns := range namespaces {
		_, err := h.instanceRepo.FindByNamespace(ns.Name)
		if err != nil {
			var dbErr *dberrors.DatabaseError
			if errors.As(err, &dbErr) && errors.Is(dbErr.Err, dberrors.ErrNotFound) {
				// Namespace is orphaned — no matching instance in DB.
				resp := OrphanedNamespaceResponse{
					Name:      ns.Name,
					CreatedAt: ns.CreatedAt,
					Phase:     ns.Phase,
				}

				if includeDetails {
					// Best-effort: get resource counts.
					counts, countErr := k8sClient.GetResourceCounts(ctx, ns.Name)
					if countErr != nil {
						slog.Warn("Failed to get resource counts for orphaned namespace",
							logKeyNamespace, ns.Name, "error", countErr)
					} else {
						resp.ResourceCounts = counts
					}

					// Best-effort: list helm releases.
					if helmExecutor != nil {
						releases, relErr := helmExecutor.ListReleases(ctx, ns.Name)
						if relErr != nil {
							slog.Warn("Failed to list helm releases in orphaned namespace",
								logKeyNamespace, ns.Name, "error", relErr)
							resp.HelmReleases = []string{}
						} else {
							resp.HelmReleases = releases
						}
					} else {
						resp.HelmReleases = []string{}
					}
				} else {
					resp.HelmReleases = []string{}
				}

				orphaned = append(orphaned, resp)
			} else {
				slog.Warn("Error checking namespace against DB",
					logKeyNamespace, ns.Name, "error", err)
			}
		}
		// If FindByNamespace succeeds, the namespace has a matching instance — skip it.
	}

	// Return empty array instead of null.
	if orphaned == nil {
		orphaned = []OrphanedNamespaceResponse{}
	}

	c.JSON(http.StatusOK, orphaned)
}

// DeleteOrphanedNamespace removes an orphaned namespace after verifying it has no matching instance.
// It uninstalls all Helm releases in the namespace, then deletes the K8s namespace.
// @Summary      Delete an orphaned namespace
// @Description  Verifies the namespace is orphaned, uninstalls all Helm releases, and deletes the Kubernetes namespace
// @Tags         Admin
// @Produce      json
// @Param        namespace  path  string  true  "Namespace name"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      409  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/v1/admin/orphaned-namespaces/{namespace} [delete]
// @Security     BearerAuth
func (h *AdminHandler) DeleteOrphanedNamespace(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := c.Param(logKeyNamespace)

	if h.registry == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgK8sClientUnavailable})
		return
	}

	clients, err := h.registry.GetDefaultClients()
	if err != nil {
		slog.Error("Failed to get default cluster clients", "error", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgK8sClientUnavailable})
		return
	}
	k8sClient := clients.K8s
	helmExecutor := clients.Helm

	// Safety check: only allow deletion of valid stack-* namespaces.
	if !strings.HasPrefix(namespace, "stack-") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only namespaces with the 'stack-' prefix can be deleted"})
		return
	}
	// Validate full RFC1123: starts/ends with alnum, only alnum and dashes, max 63 chars.
	if !rfc1123LabelRegex.MatchString(namespace) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid namespace format"})
		return
	}

	// Verify the namespace is truly orphaned.
	_, err = h.instanceRepo.FindByNamespace(namespace)
	if err == nil {
		// Instance exists — namespace is NOT orphaned.
		c.JSON(http.StatusConflict, gin.H{"error": "Namespace is not orphaned — a matching stack instance exists"})
		return
	}
	var dbErr *dberrors.DatabaseError
	if !errors.As(err, &dbErr) || !errors.Is(dbErr.Err, dberrors.ErrNotFound) {
		// Unexpected error querying the DB.
		slog.Error("Failed to verify namespace orphan status", logKeyNamespace, namespace, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Uninstall all Helm releases in the namespace (best-effort).
	if helmExecutor != nil {
		releases, listErr := helmExecutor.ListReleases(ctx, namespace)
		if listErr != nil {
			slog.Warn("Failed to list releases for cleanup, proceeding with namespace deletion",
				logKeyNamespace, namespace, "error", listErr)
		} else {
			for _, release := range releases {
				output, unErr := helmExecutor.Uninstall(ctx, deployer.UninstallRequest{
					ReleaseName: release,
					Namespace:   namespace,
				})
				if unErr != nil {
					slog.Warn("Failed to uninstall release during orphan cleanup",
						logKeyNamespace, namespace, "release", release, "output", output, "error", unErr)
				} else {
					slog.Info("Uninstalled orphaned release",
						logKeyNamespace, namespace, "release", release)
				}
			}
		}
	}

	// Delete the K8s namespace.
	if err := k8sClient.DeleteNamespace(ctx, namespace); err != nil {
		slog.Error("Failed to delete orphaned namespace", logKeyNamespace, namespace, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete namespace",
		})
		return
	}

	slog.Info("Orphaned namespace deleted", logKeyNamespace, namespace)
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Namespace %q deleted successfully", namespace)})
}
