package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManager_BlockingPostDeploy_AppliesTo checks that the blocking
// post-deploy phase runs only for the subscribers whose applies_to matches
// the instance (#720): an instance of another team is never marked
// stabilizing, and a scope that cannot be checked fails a fail-policy gate.
func TestManager_BlockingPostDeploy_AppliesTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		category      string
		lookupErr     error
		expectCalls   []string
		expectStatus  string
		expectOutput  []string
		expectNoPhase bool
		expectError   string
	}{
		{
			name:         "matching template runs the subscriber",
			category:     "Team-A",
			expectCalls:  []string{"restore-a:post-deploy", "notify:post-deploy"},
			expectStatus: models.StackStatusRunning,
			expectOutput: []string{`Running post-deploy step "restore-a"`, "Post-deploy steps finished"},
		},
		{
			name:          "other template skips the phase",
			category:      "team-b",
			expectCalls:   []string{"notify:post-deploy"},
			expectStatus:  models.StackStatusRunning,
			expectNoPhase: true,
		},
		{
			name:         "unknown template fails the fail-policy gate",
			lookupErr:    errors.New("db down"),
			expectCalls:  nil,
			expectStatus: models.StackStatusError,
			expectError:  `post-deploy hook "restore-a" failed (could not check where it applies; try again)`,
			expectOutput: []string{`ERROR: post-deploy hook "restore-a" failed (could not check where it applies; try again)`},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := seedInstance(t, instanceRepo, "inst-scope", "scope", "owner-1")

			var mu sync.Mutex
			var statusAtRestore string
			srv.handlers["restore-a"] = func(w http.ResponseWriter, _ *http.Request, _ hooks.EventEnvelope) {
				cur, _ := instanceRepo.FindByID(context.Background(), inst.ID)
				mu.Lock()
				statusAtRestore = cur.Status
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(hooks.HookResponse{Allowed: true})
			}

			d := srv.dispatcher(t,
				hooks.Subscription{Name: "restore-a", Events: []string{hooks.EventPostDeploy}, Blocking: true,
					FailurePolicy: hooks.FailurePolicyFail, TimeoutSeconds: 60,
					AppliesTo: hooks.Scope{TemplateCategories: []string{"team-a"}}},
				hooks.Subscription{Name: "notify", Events: []string{hooks.EventPostDeploy}},
			)
			templates := hooks.NewTemplateResolver(func(context.Context, []string) (map[string]hooks.TemplateRef, error) {
				if tt.lookupErr != nil {
					return nil, tt.lookupErr
				}
				return map[string]hooks.TemplateRef{inst.StackDefinitionID: {ID: "tpl-1", Category: tt.category}}, nil
			})
			t.Cleanup(templates.Stop)
			d.WithTemplates(templates)

			mgr := NewManager(ManagerConfig{
				Registry:              &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:          instanceRepo,
				DeployLogRepo:         logRepo,
				TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:                   &mockBroadcaster{},
				MaxConcurrent:         1,
				Notifier:              &mockNotifier{},
				StabilizePollInterval: 10 * time.Millisecond,
				Hooks:                 d,
			})
			deployLog := waitForLogDone(t, logRepo, deployOneChart(t, mgr, inst))
			if len(tt.expectCalls) > 0 {
				require.Eventually(t, func() bool { return len(srv.callNames()) >= len(tt.expectCalls) }, 5*time.Second, 10*time.Millisecond)
			}

			assert.Equal(t, tt.expectCalls, srv.callNames())
			final, err := instanceRepo.FindByID(context.Background(), inst.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.expectStatus, final.Status)
			assert.Nil(t, final.PostDeployHookUntil, "the marker is cleared or never set")
			for _, want := range tt.expectOutput {
				assert.Contains(t, deployLog.Output, want)
			}
			if tt.expectNoPhase {
				assert.NotContains(t, deployLog.Output, "post-deploy step")
			}
			if tt.expectError != "" {
				assert.Contains(t, final.ErrorMessage, tt.expectError)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(tt.expectCalls) > 1 {
				assert.Equal(t, models.StackStatusStabilizing, statusAtRestore)
			}
		})
	}
}

// TestManager_BlockingPostDeploy_ShutdownWithoutMatchingSubscriber checks
// that a shutdown at the blocking phase does not fail a deploy that no
// blocking subscriber applies to: the scope is decided before the shutdown
// check, so the deploy finalizes as without blocking subscribers.
func TestManager_BlockingPostDeploy_ShutdownWithoutMatchingSubscriber(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		category string
		wantErr  error
	}{
		{name: "no subscriber applies", category: "team-b", wantErr: nil},
		{name: "a subscriber applies", category: "team-a", wantErr: ErrDeployInterrupted},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newSubscriberServer(t)
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := seedInstance(t, instanceRepo, "inst-shutdown-scope", "shutdown-scope", "owner-1")
			inst.Status = models.StackStatusDeploying
			require.NoError(t, instanceRepo.Update(context.Background(), inst))

			d := srv.dispatcher(t, hooks.Subscription{Name: "restore-a", Events: []string{hooks.EventPostDeploy}, Blocking: true,
				FailurePolicy: hooks.FailurePolicyFail, TimeoutSeconds: 60,
				AppliesTo: hooks.Scope{TemplateCategories: []string{"team-a"}}})
			templates := hooks.NewTemplateResolver(func(context.Context, []string) (map[string]hooks.TemplateRef, error) {
				return map[string]hooks.TemplateRef{inst.StackDefinitionID: {ID: "tpl-1", Category: tt.category}}, nil
			})
			t.Cleanup(templates.Stop)
			d.WithTemplates(templates)
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:           &mockBroadcaster{},
				MaxConcurrent: 1,
				Hooks:         d,
			})
			t.Cleanup(mgr.Shutdown)
			deployLog := &models.DeploymentLog{ID: "log-1", StackInstanceID: inst.ID, Action: models.DeployActionDeploy,
				Status: models.DeployLogRunning, StartedAt: time.Now().UTC()}
			require.NoError(t, logRepo.Create(context.Background(), deployLog))
			mgr.shutdownCancel()

			res := mgr.runBlockingPostDeploy(inst.ID, deployLog, hookOpts{})
			if tt.wantErr == nil {
				assert.NoError(t, res.err)
				assert.Empty(t, res.output)
			} else {
				assert.ErrorIs(t, res.err, tt.wantErr)
			}
			assert.Empty(t, srv.callNames())
			stored, err := instanceRepo.FindByID(context.Background(), inst.ID)
			require.NoError(t, err)
			assert.Nil(t, stored.PostDeployHookUntil, "the phase is not marked during a shutdown")
		})
	}
}
