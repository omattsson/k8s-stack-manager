package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForLogDone polls until the deployment log is no longer running.
func waitForLogDone(t *testing.T, repo *mockDeployLogRepo, logID string) *models.DeploymentLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		log, err := repo.FindByID(context.Background(), logID)
		if err == nil && log.Status != models.DeployLogRunning {
			return log
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the deployment log to finish")
	return nil
}

func newRollbackTestManager(instanceRepo *mockInstanceRepo, logRepo *mockDeployLogRepo, helm HelmExecutor) *Manager {
	return NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: helm},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		MaxConcurrent: 2,
	})
}

func TestRollback_ToTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		versions     map[string]string
		installErr   error // returned for the "db" release only
		wantStatus   string
		wantVersions map[string]string // release -> version passed to helm
		wantValues   map[string]string // LastDeployedValues after the rollback
	}{
		{
			name:         "recorded versions and snapshot values are installed",
			versions:     map[string]string{"web": "1.0.0"},
			wantStatus:   models.StackStatusRunning,
			wantVersions: map[string]string{"web": "1.0.0", "db": "2.0.0"},
			wantValues:   map[string]string{"web": "replicas: 1\n", "db": "size: small\n", "worker": "running: worker\n"},
		},
		{
			name:         "without recorded versions the current chart version is used",
			wantStatus:   models.StackStatusRunning,
			wantVersions: map[string]string{"web": "1.1.0", "db": "2.0.0"},
			wantValues:   map[string]string{"web": "replicas: 1\n", "db": "size: small\n", "worker": "running: worker\n"},
		},
		{
			name:       "install error fails the rollback; charts upgraded before it are recorded",
			installErr: errors.New("boom"),
			wantStatus: models.StackStatusError,
			wantValues: map[string]string{"web": "replicas: 1\n", "db": "size: large\n", "worker": "running: worker\n"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()

			running, _ := json.Marshal(map[string]string{"web": "replicas: 3\n", "db": "size: large\n", "worker": "running: worker\n"})
			inst := &models.StackInstance{
				ID: "inst-rb", StackDefinitionID: "def-1", Name: "rb", Namespace: "stack-rb-alice",
				OwnerID: "user-1", Branch: "main", Status: models.StackStatusRunning,
				LastDeployedValues: string(running),
			}
			require.NoError(t, instanceRepo.Create(inst))

			var mu sync.Mutex
			installed := map[string]InstallRequest{}
			files := map[string]string{}
			helm := &mockHelmExecutor{
				installFunc: func(_ context.Context, req InstallRequest) (string, error) {
					mu.Lock()
					defer mu.Unlock()
					installed[req.ReleaseName] = req
					if req.ValuesFile != "" {
						data, err := os.ReadFile(req.ValuesFile)
						if err == nil {
							files[req.ReleaseName] = string(data)
						}
					}
					if req.ReleaseName == "db" {
						return "upgraded", tt.installErr
					}
					return "upgraded", nil
				},
				rollbackFunc: func(_ context.Context, _ string, _ string, _ int) (string, error) {
					t.Error("helm rollback must not run for a rollback to a target")
					return "", nil
				},
			}
			mgr := newRollbackTestManager(instanceRepo, logRepo, helm)

			logID, err := mgr.Rollback(context.Background(), RollbackRequest{
				Instance: inst,
				Charts: []ChartDeployInfo{
					{ChartConfig: models.ChartConfig{ChartName: "web", ChartVersion: "1.1.0", DeployOrder: 1}},
					{ChartConfig: models.ChartConfig{ChartName: "db", ChartVersion: "2.0.0", DeployOrder: 2, RepositoryURL: "oci://registry.example/charts"}},
					{ChartConfig: models.ChartConfig{ChartName: "worker", DeployOrder: 3}},
				},
				TargetLogID:         "dep-a",
				TargetValues:        map[string]string{"web": "replicas: 1\n", "db": "size: small\n", "removed": "x: 1\n"},
				TargetChartVersions: tt.versions,
				TargetBranch:        "release-1",
			})
			require.NoError(t, err)

			rbLog := waitForLogDone(t, logRepo, logID)
			assert.Equal(t, "dep-a", rbLog.TargetLogID)
			assert.Equal(t, "release-1", rbLog.Branch)

			final, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, final.Status)

			var gotValues map[string]string
			require.NoError(t, json.Unmarshal([]byte(final.LastDeployedValues), &gotValues))
			assert.Equal(t, tt.wantValues, gotValues)

			mu.Lock()
			defer mu.Unlock()
			_, workerInstalled := installed["worker"]
			assert.False(t, workerInstalled, "a chart outside the target deploy stays unchanged")
			if tt.installErr != nil {
				assert.Contains(t, rbLog.ErrorMessage, "db")
				assert.Empty(t, rbLog.ValuesSnapshot, "a failed rollback log keeps no values snapshot")
				return
			}
			assert.Equal(t, models.DeployLogSuccess, rbLog.Status)
			assert.Equal(t, final.LastDeployedValues, rbLog.ValuesSnapshot)
			for release, version := range tt.wantVersions {
				assert.Equal(t, version, installed[release].Version, release)
			}
			assert.Equal(t, "oci://registry.example/charts/db", installed["db"].ChartPath)
			assert.Equal(t, "replicas: 1\n", files["web"])
			assert.Equal(t, "size: small\n", files["db"])
			assert.Contains(t, rbLog.Output, "chart removed is in the target deploy but not in the stack definition")
		})
	}
}

func TestRollback_OneRevisionRecordsRunningValues(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()

	inst := &models.StackInstance{
		ID: "inst-rb1", StackDefinitionID: "def-1", Name: "rb1", Namespace: "stack-rb1-alice",
		OwnerID: "user-1", Branch: "main", Status: models.StackStatusRunning,
		LastDeployedValues: `{"web":"replicas: 3\n"}`,
	}
	require.NoError(t, instanceRepo.Create(inst))

	var rolledBack []int
	var mu sync.Mutex
	helm := &mockHelmExecutor{
		historyFunc: func(_ context.Context, _ string, _ string, _ int) ([]ReleaseRevision, error) {
			return []ReleaseRevision{{Revision: 3}, {Revision: 4}}, nil
		},
		rollbackFunc: func(_ context.Context, _ string, _ string, revision int) (string, error) {
			mu.Lock()
			rolledBack = append(rolledBack, revision)
			mu.Unlock()
			return "rolled back", nil
		},
		getValuesFunc: func(_ context.Context, release string, _ string, _ int) (string, error) {
			return "replicas: 2\n", nil
		},
	}
	mgr := newRollbackTestManager(instanceRepo, logRepo, helm)

	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts:   []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}}},
	})
	require.NoError(t, err)
	rbLog := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, models.DeployLogSuccess, rbLog.Status)
	assert.Equal(t, "main", rbLog.Branch)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"web":"replicas: 2\n"}`, final.LastDeployedValues, "deploy preview must compare against the running values")
	mu.Lock()
	assert.Equal(t, []int{3}, rolledBack)
	mu.Unlock()
}

func TestStoppedAt_SetOnStopClearedOnDeploy(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{
		ID: "inst-st", StackDefinitionID: "def-1", Name: "st", Namespace: "stack-st-alice",
		OwnerID: "user-1", Branch: "feature", Status: models.StackStatusRunning,
	}
	require.NoError(t, instanceRepo.Create(inst))
	mgr := newRollbackTestManager(instanceRepo, logRepo, &mockHelmExecutor{})
	charts := []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", ChartVersion: "1.2.3"}}}

	stopLog, err := mgr.StopWithCharts(context.Background(), inst, charts)
	require.NoError(t, err)
	waitForLogDone(t, logRepo, stopLog)
	waitForTerminalStatus(t, instanceRepo, inst.ID)
	stopped, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	require.Equal(t, models.StackStatusStopped, stopped.Status)
	require.NotNil(t, stopped.StoppedAt)
	assert.WithinDuration(t, time.Now(), *stopped.StoppedAt, 5*time.Second)

	deployLogID, err := mgr.Deploy(context.Background(), DeployRequest{Instance: stopped, Charts: charts})
	require.NoError(t, err)
	depLog := waitForLogDone(t, logRepo, deployLogID)
	assert.Equal(t, "feature", depLog.Branch)
	assert.JSONEq(t, `{"web":"1.2.3"}`, depLog.ChartVersions)
	deployed, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Nil(t, deployed.StoppedAt)
}

func TestChartVersionsAndRunningValuesHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", chartVersionsJSON(nil))
	assert.Equal(t, "", chartVersionsJSON([]ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "a"}}}))
	assert.JSONEq(t, `{"a":"1"}`, chartVersionsJSON([]ChartDeployInfo{
		{ChartConfig: models.ChartConfig{ChartName: "a", ChartVersion: "1"}},
		{ChartConfig: models.ChartConfig{ChartName: "b"}},
	}))

	assert.Equal(t, map[string]string{}, ParseChartVersions(""))
	assert.Equal(t, map[string]string{}, ParseChartVersions("not json"))
	assert.Equal(t, map[string]string{"a": "1"}, ParseChartVersions(`{"a":"1"}`))

	assert.Equal(t, `{"a":"x"}`, mergeRunningValues(`{"a":"x"}`, nil))
	assert.JSONEq(t, `{"a":"y","b":"z"}`, mergeRunningValues(`{"a":"x","b":"z"}`, map[string]string{"a": "y"}))
	assert.JSONEq(t, `{"a":"y"}`, mergeRunningValues("garbage", map[string]string{"a": "y"}))

	ref, repo := chartReference(models.ChartConfig{ChartName: "web", RepositoryURL: "https://charts.example"})
	assert.Equal(t, "web", ref)
	assert.Equal(t, "https://charts.example", repo)
	ref, repo = chartReference(models.ChartConfig{ChartName: "web", ChartPath: "app", RepositoryURL: "oci://r.example/c/"})
	assert.Equal(t, "oci://r.example/c/app", ref)
	assert.Equal(t, "", repo)
}

func TestRollback_PreRollbackHookPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     bool
		wantCharts map[string]hooks.ChartRef // name -> expected version/branch/image tag
		wantMeta   map[string]string
	}{
		{
			name:   "target rollback sends the target charts, versions and branch",
			target: true,
			wantCharts: map[string]hooks.ChartRef{
				"web": {Version: "1.0.0", Branch: "feature/Old_One", ImageTag: "feature-old-one"},
			},
			wantMeta: map[string]string{"rollback_mode": "target", "target_log_id": "dep-a", "target_branch": "feature/Old_One"},
		},
		{
			name:   "one revision rollback sends all charts with their branch",
			target: false,
			wantCharts: map[string]hooks.ChartRef{
				"web": {Version: "1.1.0", Branch: "main", ImageTag: "main"},
				"db":  {Version: "2.0.0", Branch: "hotfix", ImageTag: "hotfix"},
			},
			wantMeta: map[string]string{"rollback_mode": "previous_revision"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := newHookRecorder(t)
			d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
				Name: "gate", Events: []string{hooks.EventPreRollback}, URL: rec.server.URL, FailurePolicy: hooks.FailurePolicyFail,
			}}}, rec.server.Client())
			require.NoError(t, err)

			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := &models.StackInstance{ID: "inst-hk", Name: "hk", Namespace: "stack-hk-alice", OwnerID: "u", Branch: "main", Status: models.StackStatusRunning}
			require.NoError(t, instanceRepo.Create(inst))
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				MaxConcurrent: 2,
				Hooks:         d,
			})
			req := RollbackRequest{
				Instance: inst,
				Charts: []ChartDeployInfo{
					{ChartConfig: models.ChartConfig{ChartName: "web", ChartVersion: "1.1.0"}},
					{ChartConfig: models.ChartConfig{ChartName: "db", ChartVersion: "2.0.0"}, Branch: "hotfix"},
				},
			}
			if tt.target {
				req.TargetLogID = "dep-a"
				req.TargetValues = map[string]string{"web": "a: 1\n"}
				req.TargetChartVersions = map[string]string{"web": "1.0.0"}
				req.TargetBranch = "feature/Old_One"
			}
			logID, err := mgr.Rollback(context.Background(), req)
			require.NoError(t, err)
			waitForLogDone(t, logRepo, logID)

			snap := rec.snapshot()
			require.Len(t, snap, 1)
			env := snap[0].envelope
			assert.Equal(t, hooks.EventPreRollback, snap[0].event)
			require.Len(t, env.Charts, len(tt.wantCharts))
			for _, c := range env.Charts {
				want, ok := tt.wantCharts[c.Name]
				require.True(t, ok, c.Name)
				assert.Equal(t, want.Version, c.Version, c.Name)
				assert.Equal(t, want.Branch, c.Branch, c.Name)
				assert.Equal(t, want.ImageTag, c.ImageTag, c.Name)
			}
			for k, v := range tt.wantMeta {
				assert.Equal(t, v, env.Metadata[k], k)
			}
		})
	}
}

func TestFinalizeRollback_ConcurrentStopKeepsStatus(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	stoppedAt := time.Now().UTC().Add(-time.Minute)
	inst := &models.StackInstance{ID: "inst-cc", Name: "cc", Namespace: "stack-cc-a", OwnerID: "u",
		Status: models.StackStatusStopped, StoppedAt: &stoppedAt, LastDeployedValues: `{"web":"a: 1"}`}
	require.NoError(t, instanceRepo.Create(inst))
	log := &models.DeploymentLog{ID: "rb-1", StackInstanceID: inst.ID, Action: models.DeployActionRollback, Status: models.DeployLogRunning}
	require.NoError(t, logRepo.Create(context.Background(), log))
	mgr := newRollbackTestManager(instanceRepo, logRepo, &mockHelmExecutor{})

	mgr.finalizeRollback(inst.ID, log, "output", nil, map[string]string{"web": "a: 2"})

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStopped, final.Status, "a stop that finished meanwhile is not overwritten")
	require.NotNil(t, final.StoppedAt)
	assert.True(t, final.StoppedAt.Equal(stoppedAt))
	assert.Equal(t, `{"web":"a: 1"}`, final.LastDeployedValues)
	gotLog, err := logRepo.FindByID(context.Background(), "rb-1")
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogError, gotLog.Status, "the rollback result was not applied")
	assert.Contains(t, gotLog.ErrorMessage, "another operation")
	assert.NotNil(t, gotLog.CompletedAt)
}

func TestRollback_ReadinessGating(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{ID: "inst-rd", Name: "rd", Namespace: "stack-rd-a", OwnerID: "u", Branch: "main", Status: models.StackStatusRunning}
	require.NoError(t, instanceRepo.Create(inst))
	mgr := NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		MaxConcurrent:         2,
		StabilizeTimeout:      2 * time.Second,
		StabilizePollInterval: 10 * time.Millisecond,
	})
	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance:     inst,
		Charts:       []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}}},
		TargetLogID:  "dep-a",
		TargetValues: map[string]string{"web": "a: 1\n"},
	})
	require.NoError(t, err)
	rbLog := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, models.DeployLogSuccess, rbLog.Status)
	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusRunning, final.Status, "after the readiness wait the instance is running")
}

// streamingGate is a pre-rollback subscriber that writes progress lines
// ("LOG: ...") before its final JSON answer, like the CI trigger gate.
func streamingGate(t *testing.T, final string) *hooks.Dispatcher {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("LOG: checking image web:main\nLOG: image found\n" + final + "\n"))
	}))
	t.Cleanup(srv.Close)
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "ci-gate", Events: []string{hooks.EventPreRollback}, URL: srv.URL, FailurePolicy: hooks.FailurePolicyFail,
	}}}, srv.Client())
	require.NoError(t, err)
	return d
}

func TestRollback_StreamingPreRollbackGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		final         string
		wantLogStatus string
		wantInstance  string
		wantErrorMsg  string
		wantRolled    bool
	}{
		{name: "allowed gate with progress lines", final: `{"allowed":true}`,
			wantLogStatus: models.DeployLogSuccess, wantInstance: models.StackStatusRunning, wantRolled: true},
		{name: "denied gate restores the previous status", final: `{"allowed":false,"message":"image web:old is missing"}`,
			wantLogStatus: models.DeployLogError, wantInstance: models.StackStatusPartial, wantErrorMsg: "image web:old is missing"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := &models.StackInstance{ID: "inst-gate", Name: "gate", Namespace: "stack-gate-a", OwnerID: "u",
				Branch: "main", Status: models.StackStatusPartial, ErrorMessage: "partial deploy — failed charts: db"}
			require.NoError(t, instanceRepo.Create(inst))

			var mu sync.Mutex
			installs := 0
			helm := &mockHelmExecutor{installFunc: func(_ context.Context, _ InstallRequest) (string, error) {
				mu.Lock()
				installs++
				mu.Unlock()
				return "ok", nil
			}}
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: helm},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				MaxConcurrent: 2,
				Hooks:         streamingGate(t, tt.final),
			})

			logID, err := mgr.Rollback(context.Background(), RollbackRequest{
				Instance:     inst,
				Charts:       []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}}},
				TargetLogID:  "dep-a",
				TargetValues: map[string]string{"web": "a: 1\n"},
			})
			require.NoError(t, err, "the API accepts the rollback; the hook runs in the background")

			rbLog := waitForLogDone(t, logRepo, logID)
			assert.Equal(t, tt.wantLogStatus, rbLog.Status)
			assert.Contains(t, rbLog.Output, "checking image web:main")
			assert.Contains(t, rbLog.Output, "image found")
			require.Eventually(t, func() bool {
				got, err := instanceRepo.FindByID(inst.ID)
				return err == nil && got.Status == tt.wantInstance
			}, 5*time.Second, 10*time.Millisecond)

			mu.Lock()
			assert.Equal(t, tt.wantRolled, installs > 0)
			mu.Unlock()
			if tt.wantErrorMsg != "" {
				assert.Contains(t, rbLog.ErrorMessage, tt.wantErrorMsg)
				assert.Contains(t, rbLog.Output, tt.wantErrorMsg)
				got, err := instanceRepo.FindByID(inst.ID)
				require.NoError(t, err)
				assert.Equal(t, "partial deploy — failed charts: db", got.ErrorMessage, "the previous error message is restored")
			}
		})
	}
}

func TestRollback_PreviousRevisionUsesPreviousDeployBranch(t *testing.T) {
	t.Parallel()
	rec := newHookRecorder(t)
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "gate", Events: []string{hooks.EventPreRollback}, URL: rec.server.URL, FailurePolicy: hooks.FailurePolicyFail,
	}}}, rec.server.Client())
	require.NoError(t, err)

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{ID: "inst-prev", Name: "prev", Namespace: "stack-prev-a", OwnerID: "u", Branch: "feature/New", Status: models.StackStatusRunning}
	require.NoError(t, instanceRepo.Create(inst))
	base := time.Now().UTC().Add(-time.Hour)
	for i, l := range []models.DeploymentLog{
		{ID: "d1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, Branch: "release/Old"},
		{ID: "d2", Action: models.DeployActionDeploy, Status: models.DeployLogError, Branch: "broken"},
		{ID: "s1", Action: models.DeployActionStop, Status: models.DeployLogSuccess},
		{ID: "d3", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, Branch: "feature/New"},
	} {
		l.StackInstanceID = inst.ID
		l.StartedAt = base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, logRepo.Create(context.Background(), &l))
	}

	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		MaxConcurrent: 2,
		Hooks:         d,
	})
	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts:   []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}, Branch: "hotfix"}},
	})
	require.NoError(t, err)
	rbLog := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, "release/Old", rbLog.Branch)

	snap := rec.snapshot()
	require.Len(t, snap, 1)
	require.Len(t, snap[0].envelope.Charts, 1)
	assert.Equal(t, "release/Old", snap[0].envelope.Charts[0].Branch)
	assert.Equal(t, "release-old", snap[0].envelope.Charts[0].ImageTag)
	assert.Equal(t, "previous_deploy", snap[0].envelope.Metadata["branch_source"])
}

func TestRollback_PreviousRevisionPartialFailureRecordsValues(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{ID: "inst-pf", Name: "pf", Namespace: "stack-pf-a", OwnerID: "u", Branch: "main",
		Status: models.StackStatusRunning, LastDeployedValues: `{"web":"v: 2\n","db":"v: 2\n"}`}
	require.NoError(t, instanceRepo.Create(inst))
	helm := &mockHelmExecutor{
		historyFunc: func(_ context.Context, _ string, _ string, _ int) ([]ReleaseRevision, error) {
			return []ReleaseRevision{{Revision: 1}, {Revision: 2}}, nil
		},
		rollbackFunc: func(_ context.Context, release string, _ string, _ int) (string, error) {
			if release == "db" {
				return "", errors.New("boom")
			}
			return "ok", nil
		},
		getValuesFunc: func(_ context.Context, release string, _ string, _ int) (string, error) {
			return "v: 1\n", nil
		},
	}
	mgr := newRollbackTestManager(instanceRepo, logRepo, helm)
	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts: []ChartDeployInfo{
			{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}},
			{ChartConfig: models.ChartConfig{ChartName: "db", DeployOrder: 2}},
		},
	})
	require.NoError(t, err)
	rbLog := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, models.DeployLogError, rbLog.Status)
	assert.Empty(t, rbLog.ValuesSnapshot)

	final, err := instanceRepo.FindByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, final.Status)
	assert.JSONEq(t, `{"web":"v: 1\n","db":"v: 2\n"}`, final.LastDeployedValues, "the rolled back chart is recorded, the failed one keeps its values")
}

func TestPreviousRevisionBranch(t *testing.T) {
	t.Parallel()
	type entry struct {
		status, branch string
	}
	tests := []struct {
		name string
		logs []entry // oldest first
		want string
	}{
		{"newest failed: newest successful", []entry{{models.DeployLogSuccess, "A"}, {models.DeployLogError, "X"}}, "A"},
		{"newest failed after two successes: newest successful", []entry{{models.DeployLogSuccess, "A"}, {models.DeployLogSuccess, "B"}, {models.DeployLogError, "X"}}, "B"},
		{"newest succeeded: second newest successful", []entry{{models.DeployLogSuccess, "A"}, {models.DeployLogSuccess, "B"}}, "A"},
		{"only one success: unknown", []entry{{models.DeployLogSuccess, "A"}}, ""},
		{"no logs: unknown", nil, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logRepo := newMockDeployLogRepo()
			base := time.Now().UTC().Add(-time.Hour)
			for i, e := range tt.logs {
				require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
					ID: fmt.Sprintf("l%d", i), StackInstanceID: "i1", Action: models.DeployActionDeploy,
					Status: e.status, Branch: e.branch, StartedAt: base.Add(time.Duration(i) * time.Minute),
				}))
			}
			mgr := newRollbackTestManager(newMockInstanceRepo(), logRepo, &mockHelmExecutor{})
			assert.Equal(t, tt.want, mgr.previousRevisionBranch(context.Background(), "i1"))
		})
	}
}

func TestRollback_PreviousRevisionAfterPendingRecovery(t *testing.T) {
	t.Parallel()
	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{ID: "inst-pend", Name: "pend", Namespace: "stack-pend-a", OwnerID: "u", Branch: "main", Status: models.StackStatusRunning}
	require.NoError(t, instanceRepo.Create(inst))

	var mu sync.Mutex
	pendingRemoved := false
	var target int
	helm := &mockHelmExecutor{
		historyFunc: func(_ context.Context, _ string, _ string, max int) ([]ReleaseRevision, error) {
			mu.Lock()
			defer mu.Unlock()
			if !pendingRemoved {
				pendingRemoved = true // recoverPendingRelease reads the stuck revision first
				return []ReleaseRevision{{Revision: 5, Status: "pending-upgrade"}}, nil
			}
			return []ReleaseRevision{{Revision: 3, Status: "superseded"}, {Revision: 4, Status: "deployed"}}, nil
		},
		rollbackFunc: func(_ context.Context, _ string, _ string, revision int) (string, error) {
			mu.Lock()
			target = revision
			mu.Unlock()
			return "ok", nil
		},
	}
	mgr := newRollbackTestManager(instanceRepo, logRepo, helm)
	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts:   []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}}},
	})
	require.NoError(t, err)
	rbLog := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, models.DeployLogSuccess, rbLog.Status)
	assert.Contains(t, rbLog.Output, "stuck in pending-upgrade")
	mu.Lock()
	assert.Equal(t, 4, target, "after removing the stuck revision, the rollback goes to the now-current revision")
	mu.Unlock()
}

// blockingGate is a pre-rollback subscriber that waits for release before it
// answers final. started is closed when the hook call arrives.
func blockingGate(t *testing.T, final string) (*hooks.Dispatcher, chan struct{}, chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		_, _ = w.Write([]byte(final + "\n"))
	}))
	t.Cleanup(srv.Close)
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "slow-gate", Events: []string{hooks.EventPreRollback}, URL: srv.URL, FailurePolicy: hooks.FailurePolicyFail,
	}}}, srv.Client())
	require.NoError(t, err)
	return d, started, release
}

func TestRollback_NewerOperationDuringHookWins(t *testing.T) {
	t.Parallel()
	for _, final := range []string{`{"allowed":true}`, `{"allowed":false,"message":"no"}`} {
		final := final
		t.Run(final, func(t *testing.T) {
			t.Parallel()
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := &models.StackInstance{ID: "inst-race", Name: "race", Namespace: "stack-race-a", OwnerID: "u", Branch: "main", Status: models.StackStatusRunning}
			require.NoError(t, instanceRepo.Create(inst))
			gate, started, release := blockingGate(t, final)
			var mu sync.Mutex
			touched := 0
			helm := &mockHelmExecutor{installFunc: func(_ context.Context, _ InstallRequest) (string, error) {
				mu.Lock()
				touched++
				mu.Unlock()
				return "ok", nil
			}}
			mgr := NewManager(ManagerConfig{
				Registry:      &mockClusterResolver{helm: helm},
				InstanceRepo:  instanceRepo,
				DeployLogRepo: logRepo,
				TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				MaxConcurrent: 2,
				Hooks:         gate,
			})
			logID, err := mgr.Rollback(context.Background(), RollbackRequest{
				Instance:     inst,
				Charts:       []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}}},
				TargetLogID:  "dep-a",
				TargetValues: map[string]string{"web": "a: 1\n"},
			})
			require.NoError(t, err)
			<-started

			// While the hook waits: a stop and a new deploy start (newer logs).
			current, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			current.Status = models.StackStatusDeploying
			require.NoError(t, instanceRepo.Update(current))
			require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
				ID: "new-deploy", StackInstanceID: inst.ID, Action: models.DeployActionDeploy,
				Status: models.DeployLogRunning, StartedAt: time.Now().UTC().Add(time.Second),
			}))
			close(release)

			rbLog := waitForLogDone(t, logRepo, logID)
			assert.Equal(t, models.DeployLogError, rbLog.Status)
			got, err := instanceRepo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, models.StackStatusDeploying, got.Status, "the newer deploy keeps its status")
			mu.Lock()
			assert.Zero(t, touched, "the rollback does not touch the releases")
			mu.Unlock()
			newLog, err := logRepo.FindByID(context.Background(), "new-deploy")
			require.NoError(t, err)
			assert.Equal(t, models.DeployLogRunning, newLog.Status)
		})
	}
}

func TestRollback_RejectedReasonIsSafe(t *testing.T) {
	t.Parallel()
	// A subscriber that is down: the reason must not contain its URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	d, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name: "gate", Events: []string{hooks.EventPreRollback}, URL: srv.URL + "/hook?token=secret", FailurePolicy: hooks.FailurePolicyFail,
	}}}, srv.Client())
	require.NoError(t, err)

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := &models.StackInstance{ID: "inst-safe", Name: "safe", Namespace: "stack-safe-a", OwnerID: "u", Status: models.StackStatusRunning}
	require.NoError(t, instanceRepo.Create(inst))
	mgr := NewManager(ManagerConfig{
		Registry:      &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:  instanceRepo,
		DeployLogRepo: logRepo,
		TxRunner:      &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		MaxConcurrent: 2,
		Hooks:         d,
	})
	logID, err := mgr.Rollback(context.Background(), RollbackRequest{
		Instance: inst,
		Charts:   []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web"}}},
	})
	require.NoError(t, err)
	rbLog := waitForLogDone(t, logRepo, logID)
	assert.Equal(t, `pre-rollback hook "gate" failed (unreachable or timed out)`, rbLog.ErrorMessage)
	assert.NotContains(t, rbLog.Output, "token=secret")
	assert.NotContains(t, rbLog.Output, srv.URL)
	require.Eventually(t, func() bool {
		got, err := instanceRepo.FindByID(inst.ID)
		return err == nil && got.Status == models.StackStatusRunning
	}, 5*time.Second, 10*time.Millisecond)
}

func TestDeploymentStatusPayloadAction(t *testing.T) {
	t.Parallel()
	hub := &mockBroadcaster{}
	mgr := NewManager(ManagerConfig{Hub: hub, InstanceRepo: newMockInstanceRepo(), DeployLogRepo: newMockDeployLogRepo()})
	mgr.logActions.Store("log-1", models.DeployActionRollback)
	mgr.broadcastStatus("i1", models.StackStatusDeploying, "log-1")
	mgr.broadcastStatus("i1", models.StackStatusRunning, "log-1")
	mgr.broadcastStatus("i1", models.StackStatusRunning, "log-1")
	msgs := hub.getMessages()
	require.Len(t, msgs, 3)
	actions := make([]string, 0, 3)
	for _, raw := range msgs {
		var env struct {
			Payload struct {
				Action string `json:"action"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(raw, &env))
		actions = append(actions, env.Payload.Action)
	}
	assert.Equal(t, []string{"rollback", "rollback", ""}, actions, "the final status removes the entry")
}
