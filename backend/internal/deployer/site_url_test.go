package deployer

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/internal/hooks"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const siteTestNamespace = "stack-web-jane"

// siteIngress returns an Ingress with one rule for host in the site test
// namespace. primary sets the primary-url annotation.
func siteIngress(name, host string, primary bool) *networkingv1.Ingress {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: siteTestNamespace},
		Spec: networkingv1.IngressSpec{
			TLS: []networkingv1.IngressTLS{{Hosts: []string{host}}},
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{Path: "/"}},
				}},
			}},
		},
	}
	if primary {
		ing.Annotations = map[string]string{k8s.PrimaryURLAnnotation: "true"}
	}
	return ing
}

// siteClient returns a k8s client with the namespace, an Ingress "a-api"
// and the primary Ingress "z-web". failStatus makes every namespace status
// read fail (the deployment list of GetNamespaceStatus).
func siteClient(failStatus bool) *k8s.Client {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: siteTestNamespace}},
		siteIngress("a-api", "api.example.com", false),
		siteIngress("z-web", "web.example.com", true),
	)
	if failStatus {
		cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("api server unavailable")
		})
	}
	return k8s.NewClientFromInterface(cs)
}

func TestPrimaryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		st   *k8s.NamespaceStatus
		want string
	}{
		{name: "nil status", st: nil, want: ""},
		{name: "no ingress", st: &k8s.NamespaceStatus{}, want: ""},
		{name: "first ingress", st: &k8s.NamespaceStatus{Ingresses: []k8s.IngressInfo{
			{Host: "a.example.com", URL: "https://a.example.com"}, {Host: "b.example.com", URL: "https://b.example.com"},
		}}, want: "https://a.example.com"},
		{name: "rule without host", st: &k8s.NamespaceStatus{Ingresses: []k8s.IngressInfo{{URL: "http://"}}}, want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, primaryURL(tt.st))
		})
	}
}

func TestAwaitReadiness_ReturnsPrimaryURL(t *testing.T) {
	t.Parallel()

	instanceRepo := newMockInstanceRepo()
	logRepo := newMockDeployLogRepo()
	inst := seedInstance(t, instanceRepo, "inst-ready", "web", "owner-1")
	mgr := NewManager(ManagerConfig{
		Registry:              &mockClusterResolver{helm: &mockHelmExecutor{}},
		InstanceRepo:          instanceRepo,
		DeployLogRepo:         logRepo,
		TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
		Hub:                   &mockBroadcaster{},
		MaxConcurrent:         1,
		StabilizeTimeout:      5 * time.Second,
		StabilizePollInterval: 10 * time.Millisecond,
	})

	st, err := mgr.awaitReadiness(siteClient(false), inst.ID, siteTestNamespace, "log-1")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal(t, "https://web.example.com", primaryURL(st), "the annotated Ingress comes first")
}

func TestSiteURLFor(t *testing.T) {
	t.Parallel()

	last := &k8s.NamespaceStatus{Ingresses: []k8s.IngressInfo{{Host: "last.example.com", URL: "https://last.example.com"}}}
	tests := []struct {
		name         string
		noNotifier   bool
		noClient     bool
		failStatus   bool
		last         *k8s.NamespaceStatus
		readinessRan bool
		want         string
	}{
		{name: "readiness ran: its last status", last: last, readinessRan: true, want: "https://last.example.com"},
		{name: "readiness ran without a successful read: no extra read", readinessRan: true, want: ""},
		{name: "no readiness wait: one status read", want: "https://web.example.com"},
		{name: "failed status read: no URL", failStatus: true, want: ""},
		{name: "no notifier: no read", noNotifier: true, want: ""},
		{name: "no k8s client", noClient: true, want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := ManagerConfig{MaxConcurrent: 1}
			if !tt.noNotifier {
				cfg.Notifier = &mockNotifier{}
			}
			mgr := NewManager(cfg)
			var client *k8s.Client
			if !tt.noClient {
				client = siteClient(tt.failStatus)
			}
			assert.Equal(t, tt.want, mgr.siteURLFor(client, siteTestNamespace, tt.last, tt.readinessRan))
		})
	}
}

// TestNotification_DeploySuccessCarriesSiteURL runs a deploy and checks that
// deployment.success has the primary URL, with and without the readiness
// wait, and none when the status read fails.
func TestNotification_DeploySuccessCarriesSiteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stabilize  time.Duration
		failStatus bool
		want       string
	}{
		{name: "from the readiness wait", stabilize: 5 * time.Second, want: "https://web.example.com"},
		{name: "one read without a readiness wait", want: "https://web.example.com"},
		{name: "failed status read gives no URL", failStatus: true, want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			notif := &mockNotifier{}
			instanceRepo := newMockInstanceRepo()
			logRepo := newMockDeployLogRepo()
			inst := &models.StackInstance{
				ID: "inst-site", StackDefinitionID: "def-1", Name: "web", Namespace: siteTestNamespace,
				OwnerID: "owner-1", Branch: "main", ClusterID: "cluster-1", Status: models.StackStatusDraft,
			}
			require.NoError(t, instanceRepo.Create(inst))
			mgr := NewManager(ManagerConfig{
				Registry:              &mockClusterResolver{helm: &mockHelmExecutor{}, k8sClient: siteClient(tt.failStatus)},
				InstanceRepo:          instanceRepo,
				DeployLogRepo:         logRepo,
				TxRunner:              &mockTxRunner{instanceRepo: instanceRepo, logRepo: logRepo},
				Hub:                   &mockBroadcaster{},
				MaxConcurrent:         2,
				Notifier:              notif,
				StabilizeTimeout:      tt.stabilize,
				StabilizePollInterval: 10 * time.Millisecond,
			})

			trigger := hooks.Trigger{Type: hooks.TriggerUser, ID: "devops-1", Name: "dan"}
			_, err := mgr.Deploy(hooks.WithTrigger(context.Background(), trigger), DeployRequest{
				Instance:   inst,
				Definition: &models.StackDefinition{ID: "def-1", Name: "test-def"},
				Charts:     []ChartDeployInfo{{ChartConfig: models.ChartConfig{ChartName: "web", DeployOrder: 1}}},
			})
			require.NoError(t, err)

			notif.waitForCalls(t, 1)
			call := notif.getCalls()[0]
			require.Equal(t, "deployment.success", call.Type)
			assert.Equal(t, tt.want, call.SiteURL)
			require.NotNil(t, call.Trigger)
			assert.Equal(t, trigger, *call.Trigger)
		})
	}
}

// TestFinalize_SiteURLOnlyOnSuccess checks that only deployment.success,
// deployment.partial and rollback.completed carry the site URL.
func TestFinalize_SiteURLOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	const site = "https://web.example.com"
	tests := []struct {
		name     string
		run      func(mgr *Manager, instID string, log *models.DeploymentLog)
		wantType string
		wantURL  string
	}{
		{name: "deployment.success", wantType: "deployment.success", wantURL: site,
			run: func(mgr *Manager, id string, log *models.DeploymentLog) {
				mgr.finalizeDeployWith(id, log, "", nil, false, "", "", nil, site)
			}},
		{name: "deployment.partial", wantType: "deployment.partial", wantURL: site,
			run: func(mgr *Manager, id string, log *models.DeploymentLog) {
				mgr.finalizeDeployWith(id, log, "", errors.New("partial deploy — failed charts: b"), true, "", "", nil, site)
			}},
		{name: "deployment.error has none", wantType: "deployment.error",
			run: func(mgr *Manager, id string, log *models.DeploymentLog) {
				mgr.finalizeDeployWith(id, log, "", errors.New("all charts failed to deploy: a"), false, "", "", nil, site)
			}},
		{name: "rollback.completed", wantType: "rollback.completed", wantURL: site,
			run: func(mgr *Manager, id string, log *models.DeploymentLog) {
				mgr.finalizeRollback(id, log, "", nil, nil, site)
			}},
		{name: "rollback.error has none", wantType: "rollback.error",
			run: func(mgr *Manager, id string, log *models.DeploymentLog) {
				mgr.finalizeRollback(id, log, "", errors.New("rollback failed"), nil, site)
			}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			notif := &mockNotifier{}
			mgr, instanceRepo, logRepo := newNotificationTestManager(notif)
			inst := seedInstance(t, instanceRepo, "inst-fin", "web", "owner-1")
			inst.Status = models.StackStatusDeploying
			require.NoError(t, instanceRepo.Update(inst))
			log := &models.DeploymentLog{
				ID: "log-fin", StackInstanceID: inst.ID, Action: models.DeployActionDeploy,
				Status: models.DeployLogRunning, StartedAt: time.Now().UTC(),
			}
			require.NoError(t, logRepo.Create(context.Background(), log))
			mgr.rememberTrigger(hooks.WithTrigger(context.Background(), hooks.Trigger{Type: hooks.TriggerTTL}), log.ID)

			tt.run(mgr, inst.ID, log)

			calls := notif.getCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, tt.wantType, calls[0].Type)
			assert.Equal(t, tt.wantURL, calls[0].SiteURL)
			require.NotNil(t, calls[0].Trigger)
			assert.Equal(t, hooks.TriggerTTL, calls[0].Trigger.Type)
		})
	}
}
