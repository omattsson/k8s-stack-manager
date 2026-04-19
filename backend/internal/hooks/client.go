package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	headerSignature = "X-StackManager-Signature"
	headerEvent     = "X-StackManager-Event"
	headerRequestID = "X-StackManager-Request-Id"
	signaturePrefix = "sha256="
)

// httpClient is the transport contract used by deliver. Tests inject a mock.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// deliver posts envelope to sub.URL with HMAC signing if a secret is set.
// Returns the parsed HookResponse and the HTTP status code observed (0 when
// the request never made it to a response). Network/HTTP errors and non-2xx
// responses are returned as errors; the caller decides whether to abort
// based on sub.FailurePolicy.
//
// The context carries W3C trace context (via the globally-registered
// propagator), which this function injects into the outbound request so
// subscribers can stitch their spans as children of ours.
func deliver(ctx context.Context, client httpClient, sub Subscription, envelope EventEnvelope) (HookResponse, int, error) {
	body, err := json.Marshal(envelope)
	if err != nil {
		return HookResponse{}, 0, fmt.Errorf("marshal envelope: %w", err)
	}

	timeout := time.Duration(sub.TimeoutSeconds) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return HookResponse{}, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(headerEvent, envelope.Event)
	req.Header.Set(headerRequestID, envelope.RequestID)
	if sub.Secret != "" {
		req.Header.Set(headerSignature, sign(body, sub.Secret))
	}
	injectTraceContext(reqCtx, req)

	resp, err := client.Do(req)
	if err != nil {
		return HookResponse{}, 0, fmt.Errorf("post hook: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHookResponseBytes))
	if err != nil {
		return HookResponse{}, resp.StatusCode, fmt.Errorf("read hook response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return HookResponse{}, resp.StatusCode, fmt.Errorf("hook returned status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var parsed HookResponse
	if len(bytes.TrimSpace(respBody)) == 0 {
		parsed.Allowed = true
		return parsed, resp.StatusCode, nil
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return HookResponse{}, resp.StatusCode, fmt.Errorf("decode hook response: %w", err)
	}
	return parsed, resp.StatusCode, nil
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
