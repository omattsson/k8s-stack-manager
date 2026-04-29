package hooks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const progressLinePrefix = "LOG: "

// readLimitedBody reads up to limit bytes from r. It returns (body, true, nil)
// when the source had more data than the limit so callers can fail explicitly
// instead of silently truncating.
func readLimitedBody(r io.Reader, limit int64) ([]byte, bool, error) {
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > limit {
		return buf[:limit], true, nil
	}
	return buf, false, nil
}

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

// buildHookRequest constructs the outbound HTTP request for a hook delivery,
// including HMAC signing and trace context injection. Shared by deliver and
// deliverStreaming to avoid drift.
func buildHookRequest(ctx context.Context, sub Subscription, envelope EventEnvelope, accept string) (*http.Request, context.CancelFunc, error) {
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal envelope: %w", err)
	}

	timeout := time.Duration(sub.TimeoutSeconds) * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set(headerEvent, envelope.Event)
	req.Header.Set(headerRequestID, envelope.RequestID)
	if sub.Secret != "" {
		req.Header.Set(headerSignature, sign(body, sub.Secret))
	}
	injectTraceContext(reqCtx, req)

	return req, cancel, nil
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
	req, cancel, err := buildHookRequest(ctx, sub, envelope, "application/json")
	if err != nil {
		return HookResponse{}, 0, err
	}
	defer cancel()

	resp, err := client.Do(req)
	if err != nil {
		return HookResponse{}, 0, fmt.Errorf("post hook: %w", err)
	}
	defer resp.Body.Close()

	respBody, truncated, err := readLimitedBody(resp.Body, maxHookResponseBytes)
	if err != nil {
		return HookResponse{}, resp.StatusCode, fmt.Errorf("read hook response: %w", err)
	}
	if truncated {
		return HookResponse{}, resp.StatusCode, fmt.Errorf("hook response exceeded %d-byte limit", maxHookResponseBytes)
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

// deliverStreaming is like deliver but reads the response body line by line,
// forwarding lines prefixed with "LOG: " to onProgress. The last non-progress
// line (or the entire body if no progress lines exist) is parsed as JSON
// HookResponse. This enables long-running hooks to stream status updates back
// to the caller while the HTTP connection stays open.
//
// Backward-compatible: subscribers that return plain JSON with no progress
// lines work identically to deliver().
func deliverStreaming(ctx context.Context, client httpClient, sub Subscription, envelope EventEnvelope, onProgress func(string)) (HookResponse, int, error) {
	req, cancel, err := buildHookRequest(ctx, sub, envelope, "application/x-ndjson, application/json")
	if err != nil {
		return HookResponse{}, 0, err
	}
	defer cancel()

	resp, err := client.Do(req)
	if err != nil {
		return HookResponse{}, 0, fmt.Errorf("post hook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _, _ := readLimitedBody(resp.Body, maxHookResponseBytes)
		return HookResponse{}, resp.StatusCode, fmt.Errorf("hook returned status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	// Read response line by line. Lines with the "LOG: " prefix are progress
	// updates forwarded to onProgress. The last non-empty line without the
	// prefix is the JSON HookResponse.
	var lastJSONLine string
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxHookResponseBytes))
	scanner.Buffer(make([]byte, 0, 64*1024), int(maxHookResponseBytes))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, progressLinePrefix) {
			onProgress(strings.TrimPrefix(line, progressLinePrefix))
			continue
		}
		if strings.TrimSpace(line) != "" {
			lastJSONLine = line
		}
	}
	if err := scanner.Err(); err != nil {
		return HookResponse{}, resp.StatusCode, fmt.Errorf("read hook response: %w", err)
	}

	var parsed HookResponse
	if strings.TrimSpace(lastJSONLine) == "" {
		parsed.Allowed = true
		return parsed, resp.StatusCode, nil
	}
	if err := json.Unmarshal([]byte(lastJSONLine), &parsed); err != nil {
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
