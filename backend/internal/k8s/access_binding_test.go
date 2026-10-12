package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var testAccess = &AccessBinding{ClusterRole: "ksm-stack-access", ServiceAccount: "ksm-backend", ServiceAccountNamespace: "ksm"}

func TestEnsureNamespace_AccessBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		binding *AccessBinding
		wantRB  bool
	}{
		{name: "binding set creates RoleBinding", binding: testAccess, wantRB: true},
		{name: "no binding creates none", binding: nil, wantRB: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := fake.NewSimpleClientset()
			c := NewClientFromInterface(cs)
			c.SetAccessBinding(tt.binding)
			require.NoError(t, c.EnsureNamespace(context.Background(), "stack-a-u", "a"))

			rb, err := cs.RbacV1().RoleBindings("stack-a-u").Get(context.Background(), AccessRoleBindingName, metav1.GetOptions{})
			if !tt.wantRB {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "ClusterRole", rb.RoleRef.Kind)
			assert.Equal(t, "ksm-stack-access", rb.RoleRef.Name)
			require.Len(t, rb.Subjects, 1)
			assert.Equal(t, "ksm-backend", rb.Subjects[0].Name)
			assert.Equal(t, "ksm", rb.Subjects[0].Namespace)
		})
	}
}

func TestEnsureAccessBinding_FixesWrongRoleRef(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset(&rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: AccessRoleBindingName, Namespace: "stack-a-u"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "old-role"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "other", Namespace: "x"}},
	})
	c := NewClientFromInterface(cs)
	c.SetAccessBinding(testAccess)

	changed, err := c.ensureAccessBinding(context.Background(), "stack-a-u")
	require.NoError(t, err)
	assert.True(t, changed)
	rb, err := cs.RbacV1().RoleBindings("stack-a-u").Get(context.Background(), AccessRoleBindingName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "ksm-stack-access", rb.RoleRef.Name)
	assert.Equal(t, "ksm-backend", rb.Subjects[0].Name)

	changed, err = c.ensureAccessBinding(context.Background(), "stack-a-u")
	require.NoError(t, err)
	assert.False(t, changed, "a correct RoleBinding is not written again")
}

func TestEnsureAccessBindings_OnlyManagedNamespaces(t *testing.T) {
	t.Parallel()
	managed := map[string]string{ManagedByLabelKey: ManagedByLabelValue}
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-a-u", Labels: managed}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "stack-b-u", Labels: managed}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	c := NewClientFromInterface(cs)

	written, err := c.EnsureAccessBindings(context.Background())
	require.NoError(t, err)
	assert.Zero(t, written, "without a binding nothing is written")

	c.SetAccessBinding(testAccess)
	written, err = c.EnsureAccessBindings(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, written)
	_, err = cs.RbacV1().RoleBindings("kube-system").Get(context.Background(), AccessRoleBindingName, metav1.GetOptions{})
	assert.Error(t, err, "an unmanaged namespace gets no RoleBinding")

	written, err = c.EnsureAccessBindings(context.Background())
	require.NoError(t, err)
	assert.Zero(t, written)
}
