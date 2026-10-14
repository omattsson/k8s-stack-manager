package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"
)

// channelFormatResponseSchema checks the format of a channel response.
const channelFormatResponseSchema = `{
"type": "object",
"required": ["id", "name", "format"],
"properties": {
  "id": {"type": "string"},
  "name": {"type": "string"},
  "format": {"type": "string", "enum": ["generic", "teams"]}
}
}`

func assertChannelFormatResponse(t *testing.T, body []byte, wantFormat string) {
	t.Helper()
	result, err := gojsonschema.Validate(gojsonschema.NewStringLoader(channelFormatResponseSchema), gojsonschema.NewBytesLoader(body))
	require.NoError(t, err)
	assert.True(t, result.Valid(), "schema errors: %v", result.Errors())
	var got models.NotificationChannel
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, wantFormat, got.Format)
}

func TestNotificationChannelHandler_CreateChannel_Format(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		format     string // JSON fragment; empty omits the field
		wantCode   int
		wantFormat string
	}{
		{name: "omitted gives generic", wantCode: http.StatusCreated, wantFormat: models.NotificationChannelFormatGeneric},
		{name: "empty gives generic", format: `""`, wantCode: http.StatusCreated, wantFormat: models.NotificationChannelFormatGeneric},
		{name: "generic", format: `"generic"`, wantCode: http.StatusCreated, wantFormat: models.NotificationChannelFormatGeneric},
		{name: "teams", format: `"teams"`, wantCode: http.StatusCreated, wantFormat: models.NotificationChannelFormatTeams},
		{name: "teams in other case and spaces", format: `" Teams "`, wantCode: http.StatusCreated, wantFormat: models.NotificationChannelFormatTeams},
		{name: "unknown format gives 400", format: `"slack"`, wantCode: http.StatusBadRequest},
		{name: "wrong type gives 400", format: `42`, wantCode: http.StatusBadRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			body := `{"name":"ops","webhook_url":"https://hooks.example.com/a"`
			if tt.format != "" {
				body += `,"format":` + tt.format
			}
			body += `}`

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/notification-channels", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode != http.StatusCreated {
				assert.Contains(t, w.Body.String(), `"error"`)
				repo.mu.RLock()
				assert.Empty(t, repo.channels, "no channel is created")
				repo.mu.RUnlock()
				return
			}
			assertChannelFormatResponse(t, w.Body.Bytes(), tt.wantFormat)
		})
	}
}

func TestNotificationChannelHandler_CreateChannel_UnknownFormatMessage(t *testing.T) {
	t.Parallel()
	router, _ := setupNotificationChannelRouter()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/notification-channels",
		strings.NewReader(`{"name":"ops","webhook_url":"https://hooks.example.com/a","format":"slack"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "Invalid format: format must be one of: generic, teams", resp["error"])
}

func TestNotificationChannelHandler_UpdateChannel_Format(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantCode   int
		wantFormat string // the stored format after the request
	}{
		{name: "partial update keeps the format", body: `{"name":"renamed"}`, wantCode: http.StatusOK, wantFormat: models.NotificationChannelFormatTeams},
		{name: "change to generic", body: `{"format":"generic"}`, wantCode: http.StatusOK, wantFormat: models.NotificationChannelFormatGeneric},
		{name: "empty format gives generic", body: `{"format":""}`, wantCode: http.StatusOK, wantFormat: models.NotificationChannelFormatGeneric},
		{name: "unknown format gives 400 and keeps the format", body: `{"format":"slack"}`, wantCode: http.StatusBadRequest, wantFormat: models.NotificationChannelFormatTeams},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			now := time.Now().UTC()
			repo.mu.Lock()
			repo.channels["ch1"] = &models.NotificationChannel{
				ID: "ch1", Name: "ops", WebhookURL: "https://hooks.example.com/a",
				Format: models.NotificationChannelFormatTeams, Enabled: true, CreatedAt: now, UpdatedAt: now,
			}
			repo.mu.Unlock()

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/notification-channels/ch1", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode == http.StatusOK {
				assertChannelFormatResponse(t, w.Body.Bytes(), tt.wantFormat)
			} else {
				assert.Contains(t, w.Body.String(), "Invalid format")
			}
			repo.mu.RLock()
			assert.Equal(t, tt.wantFormat, repo.channels["ch1"].Format)
			repo.mu.RUnlock()
		})
	}
}

func TestNotificationChannelHandler_UpdateChannel_FormatNotFound(t *testing.T) {
	t.Parallel()
	router, _ := setupNotificationChannelRouter()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/notification-channels/missing", strings.NewReader(`{"format":"teams"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestNotificationChannelHandler_TestChannel_Format checks that the test
// send uses the format of the channel.
func TestNotificationChannelHandler_TestChannel_Format(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		format    string
		wantTeams bool
	}{
		{name: "teams sends an Adaptive Card envelope", format: models.NotificationChannelFormatTeams, wantTeams: true},
		{name: "generic sends the event JSON", format: models.NotificationChannelFormatGeneric},
		{name: "empty format sends the event JSON", format: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bodies := make(chan []byte, 1)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				bodies <- b
				w.WriteHeader(http.StatusAccepted)
			}))
			defer ts.Close()

			router, repo := setupNotificationChannelRouter()
			now := time.Now().UTC()
			repo.mu.Lock()
			repo.channels["ch1"] = &models.NotificationChannel{
				ID: "ch1", Name: "ops", WebhookURL: ts.URL, Format: tt.format, Enabled: true, CreatedAt: now, UpdatedAt: now,
			}
			repo.mu.Unlock()

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/notification-channels/ch1/test", nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), `"success":true`)

			body := <-bodies
			var generic map[string]interface{}
			require.NoError(t, json.Unmarshal(body, &generic))
			if !tt.wantTeams {
				assert.Equal(t, models.NotificationChannelTestEvent, generic["event_type"])
				assert.NotContains(t, generic, "attachments")
				return
			}
			assert.Equal(t, "message", generic["type"])
			attachments, ok := generic["attachments"].([]interface{})
			require.True(t, ok)
			require.Len(t, attachments, 1)
			content := attachments[0].(map[string]interface{})["content"].(map[string]interface{})
			assert.Equal(t, "AdaptiveCard", content["type"])
			blocks := content["body"].([]interface{})
			require.NotEmpty(t, blocks)
			assert.Equal(t, "Test notification", blocks[0].(map[string]interface{})["text"])
		})
	}
}
