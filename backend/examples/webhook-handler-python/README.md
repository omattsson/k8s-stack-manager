# Python webhook handler (minimal)

Reference subscriber for k8s-stack-manager events and actions. Python 3.9+,
stdlib only — no Flask, no FastAPI, no dependencies to install. Serves as a
starting point for subscribers written in Python, which is typical for
ops-adjacent tools (it's what Klaravik's RefreshDB handler is built on).

Covers the full protocol:

- HMAC-SHA256 signature verification (constant-time compare)
- EventEnvelope and ActionRequest parsing
- Correct response shapes for both events (Allowed/Message) and actions (arbitrary JSON)
- `/healthz` endpoint (unauthenticated, safe for liveness probes)

## Run locally

```bash
export WEBHOOK_SECRET=$(openssl rand -hex 32)
python3 refresh-db-server-example.py
```

Expected output:
```
2026-04-19 10:15:22,045 INFO example-webhook listening on 0.0.0.0:8080 signature_verification=on
```

## Register with k8s-stack-manager

Add to your `HOOKS_CONFIG_FILE`:

```json
{
  "subscriptions": [
    {
      "name": "audit-log",
      "events": ["post-instance-create", "post-instance-delete"],
      "url": "http://<this-host>:8080/events",
      "failure_policy": "ignore",
      "secret_env": "WEBHOOK_SECRET"
    }
  ],
  "actions": [
    {
      "name": "my-action",
      "url": "http://<this-host>:8080/actions/my-action",
      "secret_env": "WEBHOOK_SECRET"
    }
  ]
}
```

Make sure the k8s-stack-manager pod has `WEBHOOK_SECRET` set to the same
value as the subscriber.

## Test it manually

```bash
BODY='{"apiVersion":"hooks.k8sstackmanager.io/v1","kind":"ActionRequest",
       "action":"my-action","request_id":"req-test",
       "instance":{"id":"i-1","namespace":"stack-demo-alice"}}'
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -hex | awk '{print $2}')"

curl -s -X POST http://localhost:8080/actions/my-action \
     -H "Content-Type: application/json" \
     -H "X-StackManager-Signature: $SIG" \
     -d "$BODY" | jq
```

## Extending

- **Events** → edit `handle_event()`. Return `{"allowed": False, "message": "…"}`
  to block pre-* events (requires the subscription's `failure_policy: fail`).
- **Actions** → edit `handle_action()`. Return any JSON; it's forwarded to
  the stackctl caller verbatim under `result`.

For operations longer than ~30s, return 202 immediately with a job id and run
the work on a background thread. See [../../../examples/webhook-handler/](../webhook-handler/)
for a Go reference, and [kvk-devops-tools/refresh-db/refresh-db-server.py](https://dev.azure.com/tbauctions/Brand%20Platforms%20and%20Apps/_git/kvk-devops-tools?path=/refresh-db/refresh-db-server.py)
for a production-grade Python handler with threading, per-job progress logs,
and kubectl orchestration.
