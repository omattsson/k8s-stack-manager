package k8s

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestNewClientFromInterface(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)
	assert.NotNil(t, client)
	assert.Equal(t, cs, client.Clientset())
}

func TestEnsureNamespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		existing  bool
		wantErr   bool
	}{
		{
			name:      "creates new namespace",
			namespace: "test-ns",
			existing:  false,
			wantErr:   false,
		},
		{
			name:      "idempotent for existing namespace",
			namespace: "existing-ns",
			existing:  true,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := fake.NewSimpleClientset()
			if tt.existing {
				_, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: tt.namespace},
				}, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			client := NewClientFromInterface(cs)
			err := client.EnsureNamespace(context.Background(), tt.namespace)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify namespace exists after call.
			exists, err := client.NamespaceExists(context.Background(), tt.namespace)
			assert.NoError(t, err)
			assert.True(t, exists)
		})
	}
}

func TestEnsureNamespace_AddsLabel(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset()
	client := NewClientFromInterface(cs)

	err := client.EnsureNamespace(context.Background(), "labeled-ns")
	assert.NoError(t, err)

	ns, err := cs.CoreV1().Namespaces().Get(context.Background(), "labeled-ns", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, "k8s-stack-manager", ns.Labels["managed-by"])
}

func TestDeleteNamespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		existing  bool
		wantErr   bool
	}{
		{
			name:      "deletes existing namespace",
			namespace: "to-delete",
			existing:  true,
			wantErr:   false,
		},
		{
			name:      "no error for nonexistent namespace",
			namespace: "ghost-ns",
			existing:  false,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cs *fake.Clientset
			if tt.existing {
				cs = fake.NewSimpleClientset(&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: tt.namespace},
				})
			} else {
				cs = fake.NewSimpleClientset()
			}

			client := NewClientFromInterface(cs)
			err := client.DeleteNamespace(context.Background(), tt.namespace)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNamespaceExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		existing  bool
	}{
		{name: "exists", namespace: "my-ns", existing: true},
		{name: "not exists", namespace: "ghost-ns", existing: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cs *fake.Clientset
			if tt.existing {
				cs = fake.NewSimpleClientset(&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: tt.namespace},
				})
			} else {
				cs = fake.NewSimpleClientset()
			}

			client := NewClientFromInterface(cs)
			exists, err := client.NamespaceExists(context.Background(), tt.namespace)
			assert.NoError(t, err)
			assert.Equal(t, tt.existing, exists)
		})
	}
}

// nsObj returns a minimal Namespace object for the fake clientset. Seeding
// namespaces alongside the secrets they contain matches real-cluster shape —
// a Secret can't exist without a parent Namespace.
func nsObj(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestEnsureDockerRegistrySecret_Creates(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(nsObj("stack-ns"))
	c := NewClientFromInterface(cs)

	err := c.EnsureDockerRegistrySecret(
		context.Background(), "stack-ns", "acr-pull-secret",
		"myregistry.azurecr.io", "user", "pass123",
	)
	assert.NoError(t, err)

	got, err := cs.CoreV1().Secrets("stack-ns").Get(context.Background(), "acr-pull-secret", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, got.Type)
	assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
	assert.Equal(t, "true", got.Labels["k8s-stack-manager.io/image-pull-secret"])
	assert.Equal(t, "myregistry.azurecr.io", got.Annotations["k8s-stack-manager.io/registry"])

	dockerCfg := string(got.Data[corev1.DockerConfigJsonKey])
	assert.Contains(t, dockerCfg, "myregistry.azurecr.io")
	assert.Contains(t, dockerCfg, "user")
	assert.Contains(t, dockerCfg, "pass123")
	assert.Contains(t, dockerCfg, base64.StdEncoding.EncodeToString([]byte("user:pass123")))
}

func TestEnsureDockerRegistrySecret_UpdatesExisting(t *testing.T) {
	t.Parallel()

	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "acr-pull-secret", Namespace: "stack-ns",
			Labels: map[string]string{"custom": "label"},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"old.registry":{}}}`)},
	}
	cs := fake.NewSimpleClientset(nsObj("stack-ns"), existing)
	c := NewClientFromInterface(cs)

	err := c.EnsureDockerRegistrySecret(
		context.Background(), "stack-ns", "acr-pull-secret",
		"new.registry.io", "newuser", "newpass",
	)
	assert.NoError(t, err)

	got, err := cs.CoreV1().Secrets("stack-ns").Get(context.Background(), "acr-pull-secret", metav1.GetOptions{})
	assert.NoError(t, err)
	// New credentials
	dockerCfg := string(got.Data[corev1.DockerConfigJsonKey])
	assert.Contains(t, dockerCfg, "new.registry.io")
	assert.Contains(t, dockerCfg, "newuser")
	// Pre-existing labels preserved
	assert.Equal(t, "label", got.Labels["custom"])
	// Our labels added
	assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
	assert.Equal(t, "new.registry.io", got.Annotations["k8s-stack-manager.io/registry"])
}

func TestEnsureDockerRegistrySecret_Idempotent(t *testing.T) {
	t.Parallel()

	cs := fake.NewSimpleClientset(nsObj("stack-ns"))
	c := NewClientFromInterface(cs)

	// Create twice with same params — should not error.
	err := c.EnsureDockerRegistrySecret(context.Background(), "stack-ns", "pull-secret", "reg.io", "u", "p")
	assert.NoError(t, err)
	err = c.EnsureDockerRegistrySecret(context.Background(), "stack-ns", "pull-secret", "reg.io", "u", "p")
	assert.NoError(t, err)

	// Only one secret should exist.
	list, err := cs.CoreV1().Secrets("stack-ns").List(context.Background(), metav1.ListOptions{})
	assert.NoError(t, err)
	assert.Len(t, list.Items, 1)
}

func TestCopySecret_CreatesInTarget(t *testing.T) {
	t.Parallel()

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wildcard-tls", Namespace: "source-ns"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("CERT"), "tls.key": []byte("KEY")},
	}
	cs := fake.NewSimpleClientset(nsObj("source-ns"), nsObj("target-ns"), src)
	c := NewClientFromInterface(cs)

	err := c.CopySecret(context.Background(), "source-ns", "wildcard-tls", "target-ns", "wildcard-tls")
	assert.NoError(t, err)

	got, err := cs.CoreV1().Secrets("target-ns").Get(context.Background(), "wildcard-tls", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeTLS, got.Type)
	assert.Equal(t, []byte("CERT"), got.Data["tls.crt"])
	assert.Equal(t, []byte("KEY"), got.Data["tls.key"])
	assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
	assert.Equal(t, "source-ns", got.Labels["k8s-stack-manager.io/copied-from-namespace"])
	assert.Equal(t, "wildcard-tls", got.Labels["k8s-stack-manager.io/copied-from-secret"])
}

func TestCopySecret_UpdatesExistingTarget(t *testing.T) {
	t.Parallel()

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wildcard-tls", Namespace: "source-ns"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("NEW_CERT"), "tls.key": []byte("NEW_KEY")},
	}
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wildcard-tls",
			Namespace: "target-ns",
			Labels:    map[string]string{"keep-me": "yes"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"tls.crt": []byte("OLD_CERT")},
	}
	cs := fake.NewSimpleClientset(nsObj("source-ns"), nsObj("target-ns"), src, stale)
	c := NewClientFromInterface(cs)

	err := c.CopySecret(context.Background(), "source-ns", "wildcard-tls", "target-ns", "wildcard-tls")
	assert.NoError(t, err)

	got, err := cs.CoreV1().Secrets("target-ns").Get(context.Background(), "wildcard-tls", metav1.GetOptions{})
	assert.NoError(t, err)
	// Data + type converge to source.
	assert.Equal(t, corev1.SecretTypeTLS, got.Type)
	assert.Equal(t, []byte("NEW_CERT"), got.Data["tls.crt"])
	assert.Equal(t, []byte("NEW_KEY"), got.Data["tls.key"])
	// Pre-existing labels are preserved, ours are layered on top.
	assert.Equal(t, "yes", got.Labels["keep-me"])
	assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
	assert.Equal(t, "source-ns", got.Labels["k8s-stack-manager.io/copied-from-namespace"])
}

func TestCopySecret_RenamesIntoTarget(t *testing.T) {
	t.Parallel()

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "source-name", Namespace: "source-ns"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("CERT")},
	}
	cs := fake.NewSimpleClientset(nsObj("source-ns"), nsObj("target-ns"), src)
	c := NewClientFromInterface(cs)

	err := c.CopySecret(context.Background(), "source-ns", "source-name", "target-ns", "different-target-name")
	assert.NoError(t, err)

	got, err := cs.CoreV1().Secrets("target-ns").Get(context.Background(), "different-target-name", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, "different-target-name", got.Name)
	assert.Equal(t, "target-ns", got.Namespace)
	// The copied-from labels still reference the SOURCE name/namespace.
	assert.Equal(t, "source-name", got.Labels["k8s-stack-manager.io/copied-from-secret"])
}

func TestCopySecret_SourceNotFound(t *testing.T) {
	t.Parallel()

	// Namespaces exist but the source secret does not — mirrors the real-cluster
	// case where an operator enabled the feature but forgot to create the secret.
	cs := fake.NewSimpleClientset(nsObj("source-ns"), nsObj("target-ns"))
	c := NewClientFromInterface(cs)

	err := c.CopySecret(context.Background(), "source-ns", "missing-secret", "target-ns", "target-secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "get source secret")
	assert.Contains(t, err.Error(), "missing-secret")

	// Nothing should have been created in the target namespace.
	_, getErr := cs.CoreV1().Secrets("target-ns").Get(context.Background(), "target-secret", metav1.GetOptions{})
	assert.Error(t, getErr)
}

// ── EnsureRoleBinding ────────────────────────────────────────────────────────

func TestEnsureRoleBinding(t *testing.T) {
	t.Parallel()

	const ns = "stack-test"

	type args struct {
		rbName, clusterRole, saName, saNs string
	}

	tests := []struct {
		name     string
		seed     []runtime.Object // beyond the namespace itself
		args     args
		wantErr  bool
		errSubstr string
		check    func(t *testing.T, cs *fake.Clientset)
	}{
		{
			name: "creates when absent",
			args: args{rbName: "refresh-db-snap-manager", clusterRole: "refresh-db-snap-manager", saName: "refresh-db", saNs: "refresh-db"},
			check: func(t *testing.T, cs *fake.Clientset) {
				got, err := cs.RbacV1().RoleBindings(ns).Get(context.Background(), "refresh-db-snap-manager", metav1.GetOptions{})
				require.NoError(t, err)
				assert.Equal(t, "ClusterRole", got.RoleRef.Kind)
				assert.Equal(t, rbacv1.GroupName, got.RoleRef.APIGroup)
				assert.Equal(t, "refresh-db-snap-manager", got.RoleRef.Name)
				require.Len(t, got.Subjects, 1)
				assert.Equal(t, "ServiceAccount", got.Subjects[0].Kind)
				assert.Equal(t, "refresh-db", got.Subjects[0].Name)
				assert.Equal(t, "refresh-db", got.Subjects[0].Namespace)
				assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
				assert.Equal(t, "refresh-db-snap-manager", got.Annotations["k8s-stack-manager.io/cluster-role"])
			},
		},
		{
			name: "defaults rbName to clusterRole when empty",
			args: args{rbName: "", clusterRole: "my-cluster-role", saName: "the-sa", saNs: "addons"},
			check: func(t *testing.T, cs *fake.Clientset) {
				got, err := cs.RbacV1().RoleBindings(ns).Get(context.Background(), "my-cluster-role", metav1.GetOptions{})
				require.NoError(t, err)
				assert.Equal(t, "my-cluster-role", got.Name)
			},
		},
		{
			name: "updates subjects when roleRef matches and preserves foreign labels",
			seed: []runtime.Object{
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "rb", Namespace: ns,
						Labels: map[string]string{"keep-me": "yes"},
					},
					Subjects: []rbacv1.Subject{
						{Kind: rbacv1.ServiceAccountKind, Name: "old-sa", Namespace: "old-ns"},
					},
					RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "shared-cr"},
				},
			},
			args: args{rbName: "rb", clusterRole: "shared-cr", saName: "new-sa", saNs: "new-ns"},
			check: func(t *testing.T, cs *fake.Clientset) {
				got, err := cs.RbacV1().RoleBindings(ns).Get(context.Background(), "rb", metav1.GetOptions{})
				require.NoError(t, err)
				require.Len(t, got.Subjects, 1)
				assert.Equal(t, "new-sa", got.Subjects[0].Name)
				assert.Equal(t, "new-ns", got.Subjects[0].Namespace)
				assert.Equal(t, "yes", got.Labels["keep-me"])
				assert.Equal(t, "k8s-stack-manager", got.Labels["managed-by"])
			},
		},
		{
			name: "recreates when roleRef.Name changes",
			seed: []runtime.Object{
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: ns},
					Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "sa", Namespace: "addons"}},
					RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "old-cr"},
				},
			},
			args: args{rbName: "rb", clusterRole: "new-cr", saName: "sa", saNs: "addons"},
			check: func(t *testing.T, cs *fake.Clientset) {
				got, err := cs.RbacV1().RoleBindings(ns).Get(context.Background(), "rb", metav1.GetOptions{})
				require.NoError(t, err)
				assert.Equal(t, "new-cr", got.RoleRef.Name)
			},
		},
		{
			name: "recreates when roleRef.APIGroup differs",
			seed: []runtime.Object{
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: "rb", Namespace: ns},
					Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "sa", Namespace: "addons"}},
					// Invalid APIGroup that does not match rbacv1.GroupName.
					RoleRef: rbacv1.RoleRef{APIGroup: "stale.example.com", Kind: "ClusterRole", Name: "cr"},
				},
			},
			args: args{rbName: "rb", clusterRole: "cr", saName: "sa", saNs: "addons"},
			check: func(t *testing.T, cs *fake.Clientset) {
				got, err := cs.RbacV1().RoleBindings(ns).Get(context.Background(), "rb", metav1.GetOptions{})
				require.NoError(t, err)
				assert.Equal(t, rbacv1.GroupName, got.RoleRef.APIGroup)
			},
		},
		{
			name: "idempotent second call leaves a single RoleBinding",
			args: args{rbName: "rb", clusterRole: "cr", saName: "sa", saNs: "addons"},
			check: func(t *testing.T, cs *fake.Clientset) {
				err := NewClientFromInterface(cs).EnsureRoleBinding(
					context.Background(), ns, "rb", "cr", "sa", "addons",
				)
				require.NoError(t, err)
				list, err := cs.RbacV1().RoleBindings(ns).List(context.Background(), metav1.ListOptions{})
				require.NoError(t, err)
				assert.Len(t, list.Items, 1)
			},
		},
		{
			name:      "rejects empty namespace",
			args:      args{rbName: "rb", clusterRole: "cr", saName: "sa", saNs: "addons"},
			wantErr:   true,
			errSubstr: "namespace",
		},
		{
			name:      "rejects empty clusterRole",
			args:      args{rbName: "rb", clusterRole: "", saName: "sa", saNs: "addons"},
			wantErr:   true,
			errSubstr: "clusterRoleName",
		},
		{
			name:      "rejects empty serviceAccount fields",
			args:      args{rbName: "rb", clusterRole: "cr", saName: "", saNs: ""},
			wantErr:   true,
			errSubstr: "required",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			seed := []runtime.Object{nsObj(ns)}
			seed = append(seed, tt.seed...)
			cs := fake.NewSimpleClientset(seed...)
			c := NewClientFromInterface(cs)

			callNs := ns
			// "rejects empty namespace" exercises the validation path with an
			// empty namespace; every other case uses the seeded namespace.
			if tt.name == "rejects empty namespace" {
				callNs = ""
			}

			err := c.EnsureRoleBinding(
				context.Background(), callNs, tt.args.rbName,
				tt.args.clusterRole, tt.args.saName, tt.args.saNs,
			)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, cs)
			}
		})
	}
}

// TestEnsureRoleBinding_CreateRaceFallsThroughToUpdate exercises the
// IsAlreadyExists branch where another writer created the same RoleBinding
// between our Get-not-found and our Create. The helper must fall through to
// the update path rather than fail.
func TestEnsureRoleBinding_CreateRaceFallsThroughToUpdate(t *testing.T) {
	t.Parallel()

	const ns = "stack-test"
	cs := fake.NewSimpleClientset(nsObj(ns))

	// React to the Create call with AlreadyExists exactly once, then inject
	// the conflicting RoleBinding so the post-race Get can find it.
	gvr := schema.GroupResource{Group: rbacv1.GroupName, Resource: "rolebindings"}
	createCount := 0
	cs.PrependReactor("create", "rolebindings", func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		rb, ok := ca.GetObject().(*rbacv1.RoleBinding)
		if !ok {
			return false, nil, nil
		}
		createCount++
		if createCount == 1 {
			// Inject the conflicting object into the tracker so the post-race
			// Get sees it, then return AlreadyExists from the Create.
			pre := rb.DeepCopy()
			pre.Subjects = []rbacv1.Subject{
				{Kind: rbacv1.ServiceAccountKind, Name: "stale-sa", Namespace: "stale-ns"},
			}
			if err := cs.Tracker().Create(
				schema.GroupVersionResource{Group: rbacv1.GroupName, Version: "v1", Resource: "rolebindings"},
				pre, ns,
			); err != nil {
				return true, nil, err
			}
			return true, nil, k8serrors.NewAlreadyExists(gvr, rb.Name)
		}
		return false, nil, nil
	})

	c := NewClientFromInterface(cs)
	err := c.EnsureRoleBinding(
		context.Background(), ns, "rb", "cr", "sa", "addons",
	)
	require.NoError(t, err)

	got, err := cs.RbacV1().RoleBindings(ns).Get(context.Background(), "rb", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, got.Subjects, 1)
	assert.Equal(t, "sa", got.Subjects[0].Name)
	assert.Equal(t, "addons", got.Subjects[0].Namespace)
}
