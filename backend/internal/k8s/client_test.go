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
