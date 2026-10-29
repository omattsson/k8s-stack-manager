package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClusterAllowPortForward(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	t.Run("create sets the flag and defaults to false", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name string
			req  CreateClusterRequest
			want bool
		}{
			{"default off", CreateClusterRequest{Name: "a", APIServerURL: "https://k8s.example.com", KubeconfigData: "data"}, false},
			{"on", CreateClusterRequest{Name: "b", APIServerURL: "https://k8s.example.com", KubeconfigData: "data", AllowPortForward: true}, true},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				router := setupClusterRouter(NewMockClusterRepository(), NewMockStackInstanceRepository(), nil, "admin")
				body, _ := json.Marshal(tt.req)
				w := httptest.NewRecorder()
				req, _ := http.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusCreated, w.Code)
				var out map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
				assert.Equal(t, tt.want, out["allow_port_forward"])
			})
		}
	})

	t.Run("update changes the flag only when sent", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			start bool
			send  *bool
			want  bool
		}{
			{"turn on", false, boolPtr(true), true},
			{"turn off", true, boolPtr(false), false},
			{"omitted keeps", true, nil, true},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				repo := NewMockClusterRepository()
				cl := seedCluster(repo, "cl-1", "prod")
				cl.AllowPortForward = tt.start
				_ = repo.Update(context.Background(), cl)
				router := setupClusterRouter(repo, NewMockStackInstanceRepository(), nil, "admin")
				body, _ := json.Marshal(UpdateClusterRequest{Region: strPtr("x"), AllowPortForward: tt.send})
				w := httptest.NewRecorder()
				req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code)
				var out map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
				assert.Equal(t, tt.want, out["allow_port_forward"])
			})
		}
	})

	t.Run("non-admin cannot set the flag", func(t *testing.T) {
		t.Parallel()
		repo := NewMockClusterRepository()
		seedCluster(repo, "cl-1", "prod")
		router := setupClusterRouter(repo, NewMockStackInstanceRepository(), nil, "devops")
		body, _ := json.Marshal(UpdateClusterRequest{AllowPortForward: boolPtr(true)})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}
