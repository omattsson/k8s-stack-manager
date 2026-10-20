package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQuickDeploy_NotifiesInstanceCreated checks that quick deploy sends one
// instance.created to the caller, with the caller as the trigger (#650).
func TestQuickDeploy_NotifiesInstanceCreated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		published  bool
		noManager  bool
		createErr  error
		instance   string
		wantStatus int
		wantNotify bool
	}{
		{name: "quick deploy with a deploy notifies once", published: true, instance: "qd-ok",
			wantStatus: http.StatusAccepted, wantNotify: true},
		{name: "a failed deploy still notifies: the instance exists", published: true, noManager: true, instance: "qd-fail",
			wantStatus: http.StatusAccepted, wantNotify: true},
		{name: "an unpublished template sends nothing", instance: "qd-unpub",
			wantStatus: http.StatusConflict},
		{name: "a failed create sends nothing", published: true, instance: "qd-dup",
			createErr: dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey), wantStatus: http.StatusConflict},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)

			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			instRepo := NewMockStackInstanceRepository()
			boRepo := NewMockChartBranchOverrideRepository()
			ovRepo := NewMockValueOverrideRepository()
			seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", tt.published)
			require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
				ID: "tc1", StackTemplateID: "t1", ChartName: "nginx", RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
			}))
			if tt.createErr != nil {
				instRepo.SetCreateError(tt.createErr)
			}

			var mgr *deployer.Manager
			var registry *cluster.Registry
			if !tt.noManager {
				mgr = newTestManager(instRepo, NewMockDeploymentLogRepository())
				// Wait for the deploy goroutine before the test ends.
				t.Cleanup(mgr.Shutdown)
				registry = cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{})
			}

			h, err := NewQuickDeployHandler(
				tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo, boRepo, ovRepo, helm.NewValuesGenerator(),
				mgr, NewMockUserRepository(), nil, NewMockAuditLogRepository(),
				&MockBroadcastSender{}, registry, nil, 0,
				&mockHandlerTxRunner{repos: database.TxRepos{
					StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: instRepo,
					ValueOverride: ovRepo, BranchOverride: boRepo,
				}},
			)
			require.NoError(t, err)
			notif := &recordingLifecycleNotifier{}
			h.WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo)).WithNotifier(notif)

			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set("userID", "uid-1")
				c.Set("username", "alice")
				c.Set("role", "user")
				c.Next()
			})
			r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)

			body, _ := json.Marshal(quickDeployRequest{InstanceName: tt.instance})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/templates/t1/quick-deploy", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			notif.mu.Lock()
			defer notif.mu.Unlock()
			if !tt.wantNotify {
				assert.Empty(t, notif.types)
				return
			}
			var resp quickDeployResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.Equal(t, []string{"instance.created"}, notif.types)
			target := notif.targets[0]
			assert.Equal(t, resp.Instance.ID, target.InstanceID)
			assert.Equal(t, tt.instance, target.InstanceName)
			assert.Equal(t, "uid-1", target.OwnerID)
			assert.Equal(t, resp.Instance.Namespace, target.Namespace)
			assert.Equal(t, resp.Definition.ID, target.DefinitionID)
			assert.NotNil(t, target.FollowerIDs, "a new instance has no followers: no lookup")
			assert.Empty(t, target.FollowerIDs)
			require.NotNil(t, notif.triggers[0])
			assert.Equal(t, hooks.Trigger{Type: hooks.TriggerUser, ID: "uid-1", Name: "alice"}, *notif.triggers[0])
		})
	}
}
