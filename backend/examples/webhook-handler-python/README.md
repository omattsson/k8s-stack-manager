# Python webhook handler (minimal)

This is a reference subscriber for k8s-stack-manager events and actions. It
needs Python 3.9 or later and the standard library only. You do not install
Flask, FastAPI or any other dependency. Use it as a starting point for
subscribers in Python, which is typical for operations tooling.

It covers the base protocol:

- HMAC-SHA256 (hash-based message authentication code) signature verification with a constant-time compare.
- Parsing of the EventEnvelope and the ActionRequest.
- The correct response shapes for events (`allowed` and `message`) and for actions (any JSON).
- A `/healthz` endpoint (no authentication, safe for liveness probes).

It does not implement progress streaming or the job log route. For those, see
[../../../EXTENDING.md](../../../EXTENDING.md).

## Run locally

```bash
export WEBHOOK_SECRET=$(openssl rand -hex 32)
python3 refresh-db-server-example.py
```

Environment variables: `WEBHOOK_SECRET` (empty disables the signature check),
`LISTEN_ADDR` (`host:port`, default `0.0.0.0:8080`) and `LOG_LEVEL` (default
`INFO`).

Expected output:
```
2026-04-19 10:15:22,045 INFO example-webhook listening on 0.0.0.0:8080 signature_verification=on
```

## Register with k8s-stack-manager

Add this to your `HOOKS_CONFIG_FILE`:

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

The environment of the k8s-stack-manager backend must have `WEBHOOK_SECRET`
with the same value as the subscriber.

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

- **Events:** edit `handle_event()`. Return `{"allowed": False, "message": "..."}` to block a `pre-*` event (the subscription needs `failure_policy: fail`). The example blocks a `pre-instance-delete` when the instance name contains `critical`.
- **Actions:** edit `handle_action()`. Return any JSON. The core forwards it unchanged under `result`.

For an operation that takes longer than about 30 seconds, return 202 at once
with a job ID and run the work on a background thread.
- The job ID MUST be random. It must not be sequential or a timestamp.
- To show progress in the web UI, serve the job log on the `log_path` of the action. Answer 404 when `instance_id` does not match the instance of the job. See [../../docs/hooks.md](../../docs/hooks.md#asynchronous-actions-and-the-job-log).

For a Go reference, see [../webhook-handler/](../webhook-handler/). A
production-grade Python handler usually adds threading, per-job progress logs
and `kubectl` orchestration to this skeleton.
