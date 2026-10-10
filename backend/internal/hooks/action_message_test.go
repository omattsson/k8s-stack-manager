package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionResultMessage(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", maxDenyMessageLen+20)
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{"2xx has no message", 200, `{"message":"ok"}`, ""},
		{"3xx has no message", 302, `{"error":"moved"}`, ""},
		{"url is replaced", 409, `{"error":"job runs, see https://refresh.internal:8080/jobs/1?token=x for details"}`, "job runs, see [url] for details"},
		{"error field", 409, `{"error":"refresh already in flight"}`, "refresh already in flight"},
		{"message wins over error", 409, `{"error":"conflict","message":"refresh already in flight"}`, "refresh already in flight"},
		{"detail field", 422, `{"detail":"bad market"}`, "bad market"},
		{"nested error message", 409, `{"error":{"code":"busy","message":"job job-1 runs"}}`, "job job-1 runs"},
		{"json string body", 409, `"already in flight"`, "already in flight"},
		{"multi-line is one line", 409, `{"error":"line one\n  line two"}`, "line one line two"},
		{"long message is cut", 409, `{"error":"` + long + `"}`, long[:maxDenyMessageLen] + "…"},
		{"blank message", 409, `{"error":"  "}`, ""},
		{"no message field", 409, `{"job_id":"job-1"}`, ""},
		{"null body", 409, `null`, ""},
		{"array body", 409, `["x"]`, ""},
		{"number field is skipped", 409, `{"error":42,"reason":"busy"}`, "busy"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ActionResultMessage(tt.code, json.RawMessage(tt.body)))
		})
	}
}

func TestActionRegistry_Invoke_NonJSONRefusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		code        int
		contentType string
		body        string
		wantErr     bool
		wantBody    string
	}{
		{"plain text 409 becomes a JSON string", http.StatusConflict, "text/plain; charset=utf-8", "refresh already in flight\n", false, `"refresh already in flight"`},
		{"html 502 becomes null", http.StatusBadGateway, "text/html", "<html><body>Bad Gateway</body></html>", false, `null`},
		{"plain text 302 is still an error", http.StatusFound, "text/plain", "moved", true, ""},
		{"plain text 200 is still an error", http.StatusOK, "text/plain", "done", true, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.code)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			r, err := NewActionRegistry([]ActionSubscription{{Name: "x", URL: srv.URL, TimeoutSeconds: 5}}, srv.Client())
			require.NoError(t, err)

			res, err := r.Invoke(context.Background(), "x", nil, nil)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.code, res.StatusCode)
			assert.JSONEq(t, tt.wantBody, string(res.Body))
		})
	}
}
