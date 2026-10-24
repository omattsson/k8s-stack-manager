package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wantNoKeyMessage = "A channel secret cannot be stored without KUBECONFIG_ENCRYPTION_KEY. Leave the secret empty, or set the key."

func channelRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestNotificationChannelHandler_SecretWithoutKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
		wantErr  string
	}{
		{"create with a secret", http.MethodPost, "/api/v1/admin/notification-channels",
			`{"name":"c","webhook_url":"https://h.example.com/x","secret":"s3cret"}`, http.StatusBadRequest, wantNoKeyMessage},
		{"create without a secret", http.MethodPost, "/api/v1/admin/notification-channels",
			`{"name":"c","webhook_url":"https://h.example.com/x"}`, http.StatusCreated, ""},
		{"update with a new secret", http.MethodPut, "/api/v1/admin/notification-channels/ch-1",
			`{"secret":"s3cret"}`, http.StatusBadRequest, wantNoKeyMessage},
		{"update removes the secret", http.MethodPut, "/api/v1/admin/notification-channels/ch-1",
			`{"secret":""}`, http.StatusOK, ""},
		{"update without a secret", http.MethodPut, "/api/v1/admin/notification-channels/ch-1",
			`{"name":"renamed"}`, http.StatusOK, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			seedChannel(t, repo, "ch-1", "one", "https://h.example.com/1", true)
			repo.refuseSecret = true

			w := channelRequest(router, tt.method, tt.path, tt.body)
			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantErr != "" {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, tt.wantErr, body["error"])
			}
		})
	}
}

func TestNotificationChannelHandler_WebhookURLTooLong(t *testing.T) {
	t.Parallel()
	prefix := "https://h.example.com/"
	tests := []struct {
		name string
		url  string
		want int
	}{
		{"2048 characters", prefix + strings.Repeat("a", 2048-len(prefix)), http.StatusCreated},
		{"2049 characters", prefix + strings.Repeat("a", 2049-len(prefix)), http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			seedChannel(t, repo, "ch-1", "one", "https://h.example.com/1", true)

			w := channelRequest(router, http.MethodPost, "/api/v1/admin/notification-channels", `{"name":"c","webhook_url":"`+tt.url+`"}`)
			assert.Equal(t, tt.want, w.Code)
			if tt.want == http.StatusBadRequest {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, msgWebhookURLTooLong, body["error"])
				assert.NotContains(t, w.Body.String(), "aaaa")
			}

			up := channelRequest(router, http.MethodPut, "/api/v1/admin/notification-channels/ch-1", `{"webhook_url":"`+tt.url+`"}`)
			if tt.want == http.StatusBadRequest {
				assert.Equal(t, http.StatusBadRequest, up.Code)
			} else {
				assert.Equal(t, http.StatusOK, up.Code)
			}
		})
	}
}

func TestNotificationChannelHandler_UnreadableChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*models.NotificationChannel)
		wantMsg  string
		wantJSON string
	}{
		{"secret", func(c *models.NotificationChannel) { c.SecretUnreadable = true }, models.ChannelSecretUnreadableMessage, "secret_unreadable"},
		{"webhook URL", func(c *models.NotificationChannel) { c.WebhookURL, c.WebhookURLUnreadable = "", true }, models.ChannelWebhookURLUnreadableMessage, "webhook_url_unreadable"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			seedChannel(t, repo, "ch-1", "one", "https://127.0.0.1:1/x", true)
			repo.mu.Lock()
			tt.mutate(repo.channels["ch-1"])
			repo.mu.Unlock()

			w := channelRequest(router, http.MethodPost, "/api/v1/admin/notification-channels/ch-1/test", ``)
			require.Equal(t, http.StatusOK, w.Code)
			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, false, body["success"])
			assert.Equal(t, tt.wantMsg, body["message"])
			assert.EqualValues(t, 0, body["status_code"])

			repo.mu.RLock()
			logs := repo.deliveryLogs["ch-1"]
			repo.mu.RUnlock()
			require.Len(t, logs, 1)
			assert.Equal(t, "failed", logs[0].Status)
			assert.Equal(t, tt.wantMsg, logs[0].ErrorMessage)

			get := channelRequest(router, http.MethodGet, "/api/v1/admin/notification-channels/ch-1", ``)
			require.Equal(t, http.StatusOK, get.Code)
			var ch map[string]interface{}
			require.NoError(t, json.Unmarshal(get.Body.Bytes(), &ch))
			assert.Equal(t, true, ch[tt.wantJSON])

			list := channelRequest(router, http.MethodGet, "/api/v1/admin/notification-channels", ``)
			assert.Contains(t, list.Body.String(), `"`+tt.wantJSON+`":true`)
		})
	}

	t.Run("flags are omitted when false", func(t *testing.T) {
		t.Parallel()
		router, repo := setupNotificationChannelRouter()
		seedChannel(t, repo, "ch-1", "one", "https://h.example.com/1", true)
		get := channelRequest(router, http.MethodGet, "/api/v1/admin/notification-channels/ch-1", ``)
		assert.NotContains(t, get.Body.String(), "unreadable")
	})
}
