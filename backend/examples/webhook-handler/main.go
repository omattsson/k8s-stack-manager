// Example webhook handler for k8s-stack-manager lifecycle events.
//
// This is a minimal reference implementation showing:
//   - how to parse the event envelope
//   - how to verify the X-StackManager-Signature HMAC
//   - how to respond with Allowed / Message (the hook contract)
//
// Run it locally:
//
//	go run . -addr :8080 -secret topsecret
//
// Register it against a k8s-stack-manager instance by adding a Subscription
// pointing at http://<host>:8080/events with failure_policy: ignore (for
// post-*) or fail (for pre-*).
//
// Use this as a starting point for real handlers (e.g. RefreshDB) — don't
// deploy it to production as-is.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
)

const signaturePrefix = "sha256="

// eventEnvelope is a local copy of backend/internal/hooks.EventEnvelope.
// Inlined so this example can live alongside the backend without importing
// internal/. A real handler in your own repo would define its own struct
// matching the contract in docs/hooks.md.
type eventEnvelope struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Event      string `json:"event"`
	RequestID  string `json:"request_id"`
	Instance   *struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Branch    string `json:"branch,omitempty"`
	} `json:"instance,omitempty"`
}

type hookResponse struct {
	Allowed bool   `json:"allowed"`
	Message string `json:"message,omitempty"`
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	secret := flag.String("secret", "", "shared secret for HMAC verification (optional)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	http.HandleFunc("/events", handleEvent(log, *secret))
	log.Info("starting example webhook handler", "addr", *addr, "hmac_enabled", *secret != "")
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func handleEvent(log *slog.Logger, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		if secret != "" {
			got := r.Header.Get("X-StackManager-Signature")
			want := signaturePrefix + hex.EncodeToString(hmacSHA256(body, secret))
			if !hmac.Equal([]byte(got), []byte(want)) {
				log.Warn("bad signature", "got", got)
				http.Error(w, "bad signature", http.StatusUnauthorized)
				return
			}
		}

		var env eventEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "bad envelope: "+err.Error(), http.StatusBadRequest)
			return
		}

		log.Info("received event",
			"event", env.Event,
			"request_id", env.RequestID,
			"instance_id", instanceID(&env),
			"api_version", env.APIVersion,
		)

		// Default reference behaviour: allow every event. Replace this with
		// whatever policy your handler enforces — e.g. check CMDB, verify quota,
		// refuse deploys during maintenance windows.
		writeJSON(w, http.StatusOK, hookResponse{Allowed: true})
	}
}

func hmacSHA256(body []byte, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return mac.Sum(nil)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func instanceID(env *eventEnvelope) string {
	if env.Instance == nil {
		return ""
	}
	return env.Instance.ID
}
