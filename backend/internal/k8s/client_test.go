package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
