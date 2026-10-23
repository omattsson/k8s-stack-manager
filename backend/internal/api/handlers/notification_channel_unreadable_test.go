package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keylessChannelRepo refuses a secret like a repository without
// KUBECONFIG_ENCRYPTION_KEY.
type keylessChannelRepo struct {
	*MockNotificationChannelRepository
}

func (r *keylessChannelRepo) CreateChannel(ctx context.Context, ch *models.NotificationChannel) error {
	if ch.Secret != "" {
		return dberrors.NewDatabaseError("validation",
			errors.Join(dberrors.ErrValidation, errors.New("secret cannot be stored without KUBECONFIG_ENCRYPTION_KEY configured")))
	}
	return r.MockNotificationChannelRepository.CreateChannel(ctx, ch)
}

func channelRouter(repo models.NotificationChannelRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewNotificationChannelHandler(repo)
	r := gin.New()
	g := r.Group("/channels")
	g.POST("", h.CreateChannel)
	g.PUT("/:id", h.UpdateChannel)
	g.POST("/:id/test", h.TestChannel)
	return r
}

func doChannelRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCreateChannel_SecretRefusedWithoutKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want int
	}{
		{"secret is refused with 400 and a clear message", `{"name":"a","webhook_url":"https://hooks.example.com/a","secret":"s"}`, http.StatusBadRequest},
		{"no secret still works", `{"name":"a","webhook_url":"https://hooks.example.com/a"}`, http.StatusCreated},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := channelRouter(&keylessChannelRepo{NewMockNotificationChannelRepository()})
			w := doChannelRequest(r, http.MethodPost, "/channels", tt.body)
			assert.Equal(t, tt.want, w.Code)
			if tt.want == http.StatusBadRequest {
				assert.Contains(t, w.Body.String(), "KUBECONFIG_ENCRYPTION_KEY")
			}
		})
	}
}

func TestUpdateChannel_UnreadableValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		secretBroken  bool
		urlBroken     bool
		body          string
		want          int
		wantMsg       string
		wantRepoWrite bool
	}{
		{"rename of a channel with an unreadable secret is refused", true, false, `{"name":"x"}`, http.StatusBadRequest, "secret", false},
		{"rename of a channel with an unreadable URL is refused", false, true, `{"name":"x"}`, http.StatusBadRequest, "webhook_url", false},
		{"a new secret repairs the secret", true, false, `{"secret":"new"}`, http.StatusOK, "", true},
		{"removing the secret repairs it", true, false, `{"secret":""}`, http.StatusOK, "", true},
		{"a new URL repairs the URL", false, true, `{"webhook_url":"https://hooks.example.com/new"}`, http.StatusOK, "", true},
		{"both set again", true, true, `{"secret":"new","webhook_url":"https://hooks.example.com/new"}`, http.StatusOK, "", true},
		{"a secret alone does not repair a broken URL", true, true, `{"secret":"new"}`, http.StatusBadRequest, "webhook_url", false},
		{"a readable channel renames", false, false, `{"name":"x"}`, http.StatusOK, "", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockNotificationChannelRepository()
			seedChannel(t, repo, "ch1", "orig", "https://hooks.example.com/orig", true)
			repo.channels["ch1"].SecretUnreadable = tt.secretBroken
			repo.channels["ch1"].WebhookURLUnreadable = tt.urlBroken
			if tt.urlBroken {
				repo.channels["ch1"].WebhookURL = ""
			}
			r := channelRouter(repo)
			w := doChannelRequest(r, http.MethodPut, "/channels/ch1", tt.body)
			assert.Equal(t, tt.want, w.Code, w.Body.String())
			if tt.wantMsg != "" {
				assert.Contains(t, w.Body.String(), tt.wantMsg)
			}
			got, err := repo.GetChannel(context.Background(), "ch1")
			require.NoError(t, err)
			if !tt.wantRepoWrite {
				assert.Equal(t, "orig", got.Name, "a refused request changes nothing")
			}
		})
	}
}

func TestTestChannel_UnreadableValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		secretBroken bool
		urlBroken    bool
		wantMsg      string
	}{
		{"secret", true, false, "stored secret cannot be read"},
		{"url", false, true, "stored webhook URL cannot be read"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockNotificationChannelRepository()
			seedChannel(t, repo, "ch1", "orig", "https://127.0.0.1:1/hook", true)
			repo.channels["ch1"].SecretUnreadable = tt.secretBroken
			repo.channels["ch1"].WebhookURLUnreadable = tt.urlBroken
			w := doChannelRequest(channelRouter(repo), http.MethodPost, "/channels/ch1/test", "")
			require.Equal(t, http.StatusOK, w.Code)
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, false, resp["success"])
			assert.Contains(t, resp["message"], tt.wantMsg)
			logs, _, err := repo.ListDeliveryLogs(context.Background(), "ch1", 10, 0)
			require.NoError(t, err)
			require.Len(t, logs, 1)
			assert.Equal(t, "failed", logs[0].Status)
			assert.Contains(t, logs[0].ErrorMessage, "cannot be decrypted")
			assert.NotContains(t, logs[0].ErrorMessage, "127.0.0.1")
		})
	}
}
