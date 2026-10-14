package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeTeamsCard decodes a Teams envelope body and checks the fixed
// fields. It returns the card body elements.
func decodeTeamsCard(t *testing.T, body []byte) []map[string]interface{} {
	t.Helper()
	var env struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Schema  string                   `json:"$schema"`
				Type    string                   `json:"type"`
				Version string                   `json:"version"`
				Body    []map[string]interface{} `json:"body"`
				Actions []interface{}            `json:"actions"`
			} `json:"content"`
		} `json:"attachments"`
	}
	require.True(t, json.Valid(body), "body must be valid JSON: %s", body)
	require.NoError(t, json.Unmarshal(body, &env))
	assert.Equal(t, "message", env.Type)
	require.Len(t, env.Attachments, 1)
	att := env.Attachments[0]
	assert.Equal(t, "application/vnd.microsoft.card.adaptive", att.ContentType)
	assert.Equal(t, "http://adaptivecards.io/schemas/adaptive-card.json", att.Content.Schema)
	assert.Equal(t, "AdaptiveCard", att.Content.Type)
	assert.Equal(t, "1.4", att.Content.Version)
	assert.Nil(t, att.Content.Actions, "no actions without a public URL")
	return att.Content.Body
}

func factsOf(t *testing.T, block map[string]interface{}) map[string]string {
	t.Helper()
	assert.Equal(t, "FactSet", block["type"])
	raw, ok := block["facts"].([]interface{})
	require.True(t, ok)
	out := make(map[string]string, len(raw))
	for _, f := range raw {
		fact, ok := f.(map[string]interface{})
		require.True(t, ok)
		out[fact["title"].(string)] = fact["value"].(string)
	}
	return out
}

func TestEncodeBody_Teams(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 10, 14, 12, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	tests := []struct {
		name         string
		payload      EventPayload
		wantTitle    string
		wantMessage  string // empty: no message text block
		wantFacts    map[string]string
		wantBlockLen int
	}{
		{
			name: "all fields",
			payload: EventPayload{
				EventType: "deployment.stopped", Timestamp: ts, Title: "Stack stopped",
				Message: "my-stack was stopped", UserDisplayName: "Jane Doe",
			},
			wantTitle:    "Stack stopped",
			wantMessage:  "my-stack was stopped",
			wantFacts:    map[string]string{"Event": "deployment.stopped", "By": "Jane Doe", "Time": "2026-10-14T10:30:00Z"},
			wantBlockLen: 3,
		},
		{
			name: "quotes, newlines, unicode and HTML are escaped",
			payload: EventPayload{
				EventType: "deployment.error", Timestamp: ts,
				Title:           `Deploy "my-stack" failed`,
				Message:         "line 1\nline 2\t\"quoted\" \\ <b>&</b> åäö \U0001F680",
				UserDisplayName: `O'Brien "Ops"`,
			},
			wantTitle:    `Deploy "my-stack" failed`,
			wantMessage:  "line 1\nline 2\t\"quoted\" \\ <b>&</b> åäö \U0001F680",
			wantFacts:    map[string]string{"Event": "deployment.error", "By": `O'Brien "Ops"`, "Time": "2026-10-14T10:30:00Z"},
			wantBlockLen: 3,
		},
		{
			name:         "empty message and user are omitted",
			payload:      EventPayload{EventType: "quota.warning", Timestamp: ts, Title: "Quota warning"},
			wantTitle:    "Quota warning",
			wantFacts:    map[string]string{"Event": "quota.warning", "Time": "2026-10-14T10:30:00Z"},
			wantBlockLen: 2,
		},
		{
			name:         "no title and no facts gives no fact set",
			payload:      EventPayload{EventType: "", Title: "", Message: ""},
			wantTitle:    "",
			wantBlockLen: 1,
		},
		{
			name:         "empty title falls back to the event type",
			payload:      EventPayload{EventType: "test"},
			wantTitle:    "test",
			wantFacts:    map[string]string{"Event": "test"},
			wantBlockLen: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body, err := encodeBody(models.NotificationChannelFormatTeams, tt.payload)
			require.NoError(t, err)
			blocks := decodeTeamsCard(t, body)
			require.Len(t, blocks, tt.wantBlockLen)

			title := blocks[0]
			assert.Equal(t, "TextBlock", title["type"])
			assert.Equal(t, tt.wantTitle, title["text"])
			assert.Equal(t, "Bolder", title["weight"])
			assert.Equal(t, "Medium", title["size"])
			assert.Equal(t, true, title["wrap"])

			next := 1
			if tt.wantMessage != "" {
				msg := blocks[next]
				assert.Equal(t, "TextBlock", msg["type"])
				assert.Equal(t, tt.wantMessage, msg["text"])
				assert.Equal(t, true, msg["wrap"])
				assert.NotContains(t, msg, "weight")
				next++
			}
			if tt.wantFacts != nil {
				require.Greater(t, len(blocks), next)
				assert.Equal(t, tt.wantFacts, factsOf(t, blocks[next]))
			}
			if tt.wantFacts == nil {
				for _, b := range blocks {
					assert.NotEqual(t, "FactSet", b["type"], "no facts gives no fact set")
				}
			}
		})
	}
}

func TestEncodeBody_GenericAndUnknownFormat(t *testing.T) {
	t.Parallel()

	payload := EventPayload{EventType: "deployment.success", Title: "Deployed", Message: "ok"}
	tests := []struct {
		name   string
		format string
	}{
		{name: "generic", format: models.NotificationChannelFormatGeneric},
		{name: "empty", format: ""},
		{name: "unknown", format: "slack"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body, err := encodeBody(tt.format, payload)
			require.NoError(t, err)
			var got EventPayload
			require.NoError(t, json.Unmarshal(body, &got))
			assert.Equal(t, "deployment.success", got.EventType)
			assert.Equal(t, "Deployed", got.Title)
			assert.NotContains(t, string(body), "AdaptiveCard")
		})
	}
}

type capturedRequest struct {
	body      []byte
	signature string
	event     string
}

// TestDispatch_BodyPerChannelFormat checks that each channel gets the body
// of its format, that the signature covers the sent body, and that the
// event header stays.
func TestDispatch_BodyPerChannelFormat(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	got := map[string]capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		mu.Lock()
		got[r.URL.Path] = capturedRequest{body: b, signature: r.Header.Get("X-StackManager-Signature"), event: r.Header.Get("X-StackManager-Event")}
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	repo := &mockChannelRepo{channels: []models.NotificationChannel{
		{ID: "g", Name: "generic", WebhookURL: server.URL + "/generic", Secret: "s1", Enabled: true, Format: models.NotificationChannelFormatGeneric},
		{ID: "t", Name: "teams", WebhookURL: server.URL + "/teams", Secret: "s2", Enabled: true, Format: models.NotificationChannelFormatTeams},
		{ID: "t2", Name: "teams-unsigned", WebhookURL: server.URL + "/teams2", Enabled: true, Format: models.NotificationChannelFormatTeams},
		{ID: "e", Name: "empty-format", WebhookURL: server.URL + "/empty", Enabled: true},
	}}
	payload := EventPayload{
		EventType: "deployment.stopped", Timestamp: time.Now(), Title: `Stack "a" stopped`,
		Message: "stopped\nby TTL", UserDisplayName: "System",
	}
	NewDispatcher(repo).Dispatch(context.Background(), payload)

	require.Len(t, repo.deliveries, 4)
	for _, d := range repo.deliveries {
		assert.Equal(t, "success", d.Status, d.ChannelName)
	}
	require.Len(t, got, 4)

	tests := []struct {
		path   string
		secret string
		teams  bool
	}{
		{path: "/generic", secret: "s1"},
		{path: "/teams", secret: "s2", teams: true},
		{path: "/teams2", teams: true},
		{path: "/empty"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			req := got[tt.path]
			assert.Equal(t, "deployment.stopped", req.event)
			if tt.teams {
				blocks := decodeTeamsCard(t, req.body)
				assert.Equal(t, `Stack "a" stopped`, blocks[0]["text"])
				assert.Equal(t, "stopped\nby TTL", blocks[1]["text"])
			} else {
				var p EventPayload
				require.NoError(t, json.Unmarshal(req.body, &p))
				assert.Equal(t, "deployment.stopped", p.EventType)
			}
			if tt.secret == "" {
				assert.Empty(t, req.signature)
				return
			}
			mac := hmac.New(sha256.New, []byte(tt.secret))
			mac.Write(req.body)
			assert.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), req.signature, "the signature covers the sent body")
		})
	}
	assert.Equal(t, got["/teams"].body, got["/teams2"].body, "the teams body is the same for each channel")
}

// TestDeliverOnce_UsesChannelFormat checks the format of the test send path.
func TestDeliverOnce_UsesChannelFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format string
		teams  bool
	}{
		{name: "teams", format: models.NotificationChannelFormatTeams, teams: true},
		{name: "generic", format: models.NotificationChannelFormatGeneric},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bodies := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				bodies <- b
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ch := models.NotificationChannel{ID: "c", Name: "c", WebhookURL: server.URL, Format: tt.format}
			status, code, msg := NewDispatcher(&mockChannelRepo{}).DeliverOnce(context.Background(), ch,
				EventPayload{EventType: models.NotificationChannelTestEvent, Title: "Test notification"})
			assert.Equal(t, "success", status)
			assert.Equal(t, http.StatusOK, code)
			assert.Empty(t, msg)
			body := <-bodies
			assert.True(t, json.Valid(body))
			assert.Equal(t, tt.teams, containsAdaptiveCard(body))
		})
	}
}

func containsAdaptiveCard(body []byte) bool {
	var env teamsEnvelope
	if err := json.Unmarshal(body, &env); err != nil || len(env.Attachments) != 1 {
		return false
	}
	return env.Attachments[0].Content.Type == teamsCardType
}
