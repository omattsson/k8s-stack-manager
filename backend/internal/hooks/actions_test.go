package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewActionRegistry_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		subs      []ActionSubscription
		expectErr string
	}{
		{
			name:      "missing name",
			subs:      []ActionSubscription{{URL: "https://example.com"}},
			expectErr: "name is required",
		},
		{
			name:      "missing url",
			subs:      []ActionSubscription{{Name: "x"}},
			expectErr: "url is required",
		},
		{
			name:      "invalid url scheme",
			subs:      []ActionSubscription{{Name: "x", URL: "ftp://example.com"}},
			expectErr: "invalid url",
		},
		{
			name: "duplicate name",
			subs: []ActionSubscription{
				{Name: "dup", URL: "https://a/h"},
				{Name: "dup", URL: "https://b/h"},
			},
			expectErr: "duplicate name",
		},
		{
			name:      "negative timeout",
			subs:      []ActionSubscription{{Name: "x", URL: "https://e/h", TimeoutSeconds: -1}},
			expectErr: "timeout_seconds must be >= 0",
		},
		{
			name:      "timeout above ceiling",
			subs:      []ActionSubscription{{Name: "x", URL: "https://e/h", TimeoutSeconds: 301}},
			expectErr: "timeout_seconds must be <= 300",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewActionRegistry(tt.subs, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)
		})
	}
}

func TestActionRegistry_Names(t *testing.T) {
	t.Parallel()
	r, err := NewActionRegistry([]ActionSubscription{
		{Name: "refresh-db", URL: "https://e/h"},
		{Name: "seed-data", URL: "https://e/h"},
	}, nil)
	require.NoError(t, err)
	names := r.Names()
	assert.ElementsMatch(t, []string{"refresh-db", "seed-data"}, names)
}

func TestActionRegistry_Invoke_Unknown(t *testing.T) {
	t.Parallel()
	r, err := NewActionRegistry(nil, nil)
	require.NoError(t, err)
	_, err = r.Invoke(context.Background(), "missing", nil, nil)
	require.Error(t, err)
	var unk ErrUnknownAction
	require.True(t, errors.As(err, &unk))
	assert.Equal(t, "missing", unk.Name)
}

func TestActionRegistry_Invoke_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body ActionRequest
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		assert.Equal(t, "refresh-db", body.Action)
		assert.Equal(t, actionRequestKind, body.Kind)
		assert.Equal(t, envelopeAPIVersion, body.APIVersion)
		require.NotNil(t, body.Instance)
		assert.Equal(t, "i-1", body.Instance.ID)
		assert.Equal(t, "alpine", body.Parameters["image"])
		assert.Equal(t, "action:refresh-db", req.Header.Get(headerEvent))
		assert.NotEmpty(t, req.Header.Get(headerRequestID))

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "wiped_pvcs": []string{"mysql-data"}})
	}))
	defer srv.Close()

	r, err := NewActionRegistry([]ActionSubscription{{
		Name:           "refresh-db",
		URL:            srv.URL,
		TimeoutSeconds: 5,
	}}, srv.Client())
	require.NoError(t, err)

	res, err := r.Invoke(context.Background(), "refresh-db",
		&InstanceRef{ID: "i-1", Name: "demo", Namespace: "stack-demo-alice"},
		map[string]any{"image": "alpine"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Contains(t, string(res.Body), "wiped_pvcs")
}

func TestActionRegistry_Invoke_NonOKReturnedToCaller(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream"}`)
	}))
	defer srv.Close()

	r, err := NewActionRegistry([]ActionSubscription{{Name: "x", URL: srv.URL, TimeoutSeconds: 5}}, srv.Client())
	require.NoError(t, err)

	res, err := r.Invoke(context.Background(), "x", nil, nil)
	require.NoError(t, err, "non-2xx is delivered as ActionResult, not an error")
	assert.Equal(t, http.StatusBadGateway, res.StatusCode)
	assert.JSONEq(t, `{"error":"upstream"}`, string(res.Body))
}

func TestActionRegistry_Invoke_HMACSignatureSet(t *testing.T) {
	t.Parallel()

	var (
		gotSig  string
		gotBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotSig = req.Header.Get(headerSignature)
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	r, err := NewActionRegistry([]ActionSubscription{{Name: "x", URL: srv.URL, Secret: "topsecret", TimeoutSeconds: 5}}, srv.Client())
	require.NoError(t, err)

	_, err = r.Invoke(context.Background(), "x", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, sign(gotBody, "topsecret"), gotSig)
}
