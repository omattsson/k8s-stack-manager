package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleEvent_AllowsValidRequest(t *testing.T) {
	t.Parallel()

	handler := handleEvent(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	body, _ := json.Marshal(eventEnvelope{
		APIVersion: "hooks.k8sstackmanager.io/v1",
		Kind:       "EventEnvelope",
		Event:      "pre-deploy",
		RequestID:  "req-123",
		Instance:   &struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Branch    string `json:"branch,omitempty"`
		}{ID: "i-1", Name: "demo", Namespace: "stack-demo-alice"},
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp hookResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Allowed)
}

func TestHandleEvent_RejectsBadSignature(t *testing.T) {
	t.Parallel()

	secret := "topsecret"
	handler := handleEvent(slog.New(slog.NewTextHandler(io.Discard, nil)), secret)
	body := []byte(`{"event":"pre-deploy"}`)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
	r.Header.Set("X-StackManager-Signature", "sha256=deadbeef")
	handler(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleEvent_AcceptsValidSignature(t *testing.T) {
	t.Parallel()

	secret := "topsecret"
	handler := handleEvent(slog.New(slog.NewTextHandler(io.Discard, nil)), secret)
	body, _ := json.Marshal(eventEnvelope{Event: "pre-deploy", RequestID: "r1"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	r.Header.Set("X-StackManager-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestHandleEvent_BadEnvelopeReturns400(t *testing.T) {
	t.Parallel()

	handler := handleEvent(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	r := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	handler(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
