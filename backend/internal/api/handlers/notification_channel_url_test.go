package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webhookURLCases are the webhook URLs that create and update check. Each URL
// holds a token in its query. An error body must never contain the URL.
var webhookURLCases = []struct {
	name    string
	url     string
	wantErr string
}{
	{name: "valid URL", url: "https://hooks.example.com/hook?sig=TOKEN"},
	{name: "valid URL with port", url: "https://hooks.example.com:8443/hook?sig=TOKEN"},
	{name: "URL that does not parse", url: "https://hooks.example.com/%zz?sig=TOKEN", wantErr: msgWebhookURLInvalid},
	{name: "control character", url: `https://hooks.example.com/x\u007f?sig=TOKEN`, wantErr: msgWebhookURLInvalid},
	{name: "space in host", url: "https://hooks example.com/hook?sig=TOKEN", wantErr: msgWebhookURLInvalid},
	{name: "no host", url: "https:///hook?sig=TOKEN", wantErr: msgWebhookURLInvalid},
	{name: "port without host", url: "https://:443/hook?sig=TOKEN", wantErr: msgWebhookURLInvalid},
	{name: "http scheme", url: "http://hooks.example.com/hook?sig=TOKEN", wantErr: msgWebhookURLNotHTTPS},
	{name: "no scheme", url: "hooks.example.com/hook?sig=TOKEN", wantErr: msgWebhookURLNotHTTPS},
}

// assertWebhookURLError checks the 400 body of an invalid webhook URL.
func assertWebhookURLError(t *testing.T, w *httptest.ResponseRecorder, wantErr string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, wantErr, body["error"])
	assert.NotContains(t, w.Body.String(), "sig=TOKEN")
	assert.NotContains(t, w.Body.String(), "hooks")
}

func TestNotificationChannelHandler_CreateChannel_WebhookURL(t *testing.T) {
	t.Parallel()

	for _, tt := range webhookURLCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()

			body := `{"name":"chat","webhook_url":"` + tt.url + `"}`
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/notification-channels", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			repo.mu.RLock()
			stored := len(repo.channels)
			repo.mu.RUnlock()
			if tt.wantErr != "" {
				assertWebhookURLError(t, w, tt.wantErr)
				assert.Zero(t, stored, "an invalid URL is not stored")
				return
			}
			assert.Equal(t, http.StatusCreated, w.Code)
			assert.Equal(t, 1, stored)
		})
	}
}

func TestNotificationChannelHandler_UpdateChannel_WebhookURL(t *testing.T) {
	t.Parallel()

	const oldURL = "https://old.example.com/hook"
	for _, tt := range webhookURLCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			seedChannel(t, repo, "ch1", "chat", oldURL, true)

			body := `{"webhook_url":"` + tt.url + `"}`
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/admin/notification-channels/ch1", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			repo.mu.RLock()
			stored := repo.channels["ch1"].WebhookURL
			repo.mu.RUnlock()
			if tt.wantErr != "" {
				assertWebhookURLError(t, w, tt.wantErr)
				assert.Equal(t, oldURL, stored, "an invalid URL does not replace the stored URL")
				return
			}
			assert.Equal(t, http.StatusOK, w.Code)
			var decoded string
			require.NoError(t, json.Unmarshal([]byte(`"`+tt.url+`"`), &decoded))
			assert.Equal(t, decoded, stored)
		})
	}
}

// TestNotificationChannelHandler_UpdateChannel_WithoutWebhookURL checks that
// an update without webhook_url keeps the stored URL. The URL check runs only
// when the request has webhook_url.
func TestNotificationChannelHandler_UpdateChannel_WithoutWebhookURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "field missing", body: `{"name":"renamed"}`},
		{name: "field empty", body: `{"name":"renamed","webhook_url":""}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			// A row from before the check. The URL does not parse.
			const storedURL = "https://hooks.example.com/%zz?sig=TOKEN"
			seedChannel(t, repo, "ch1", "chat", storedURL, true)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/admin/notification-channels/ch1", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			repo.mu.RLock()
			defer repo.mu.RUnlock()
			assert.Equal(t, storedURL, repo.channels["ch1"].WebhookURL)
			assert.Equal(t, "renamed", repo.channels["ch1"].Name)
		})
	}
}

// TestNotificationChannelHandler_TestChannel_InvalidStoredURL checks that a
// test send to a stored URL that does not parse writes a delivery log without
// the URL. The response has no URL either.
func TestNotificationChannelHandler_TestChannel_InvalidStoredURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "invalid escape", url: "https://hooks.example.com/%zz?sig=TOKEN"},
		{name: "control character", url: "https://hooks.example.com/x\x7f?sig=TOKEN"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			seedChannel(t, repo, "ch1", "chat", tt.url, true)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/notification-channels/ch1/test", nil)
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			var result map[string]interface{}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
			assert.Equal(t, false, result["success"])
			assert.NotContains(t, w.Body.String(), "sig=TOKEN")
			assert.NotContains(t, w.Body.String(), "hooks.example.com")

			repo.mu.RLock()
			logs := repo.deliveryLogs["ch1"]
			repo.mu.RUnlock()
			require.Len(t, logs, 1)
			assert.Equal(t, "failed", logs[0].Status)
			assert.Equal(t, "creating request: invalid webhook URL", logs[0].ErrorMessage)
			assert.NotContains(t, logs[0].ErrorMessage, "sig=TOKEN")
			assert.NotContains(t, logs[0].ErrorMessage, "hooks.example.com")
		})
	}
}
