package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteCluster_CountsInstancesWithoutCluster checks that an instance
// without a cluster ID blocks the delete of the default cluster, because it
// runs there, and does not block the delete of another cluster (#679).
func TestDeleteCluster_CountsInstancesWithoutCluster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		instCluster   string // cluster ID of the one instance; "-" means no instance
		deleteDefault bool
		wantCode      int
	}{
		{name: "default cluster with an instance without a cluster", instCluster: "", deleteDefault: true, wantCode: http.StatusConflict},
		{name: "default cluster with its own instance", instCluster: "cl-default", deleteDefault: true, wantCode: http.StatusConflict},
		{name: "default cluster without instances", instCluster: "-", deleteDefault: true, wantCode: http.StatusNoContent},
		{name: "other cluster with an instance without a cluster", instCluster: "", wantCode: http.StatusNoContent},
		{name: "other cluster with its own instance", instCluster: "cl-other", wantCode: http.StatusConflict},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clusters := NewMockClusterRepository()
			insts := NewMockStackInstanceRepository()
			require.NoError(t, clusters.Create(context.Background(), &models.Cluster{
				ID: "cl-default", Name: "default", APIServerURL: "https://k8s.example.com",
				KubeconfigPath: "/path/to/kubeconfig", IsDefault: true,
			}))
			seedCluster(clusters, "cl-other", "other")
			if tt.instCluster != "-" {
				require.NoError(t, insts.Create(context.Background(), &models.StackInstance{
					ID: "inst-1", Name: "legacy", ClusterID: tt.instCluster, OwnerID: "u1", Status: models.StackStatusStopped,
				}))
			}
			target := "cl-other"
			if tt.deleteDefault {
				target = "cl-default"
			}

			router := setupClusterRouter(clusters, insts, nil, "admin")
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/clusters/"+target, nil)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			_, err := clusters.FindByID(context.Background(), target)
			if tt.wantCode == http.StatusConflict {
				assert.NoError(t, err, "the cluster stays")
			} else {
				assert.Error(t, err, "the cluster is deleted")
			}
		})
	}
}
