# Example webhook handler

Minimal reference implementation for a k8s-stack-manager event subscriber. Use
this as a starting point for custom handlers (e.g. database-refresh, CMDB
sync, deploy-gate policies).

## What it does

Listens on `/events` and:

1. Reads the request body (capped at 1 MiB).
2. If started with `-secret <value>`, verifies the `X-StackManager-Signature`
   header — HMAC-SHA256 of the body, hex-encoded, `sha256=` prefix.
3. Unmarshals the envelope (`apiVersion: hooks.k8sstackmanager.io/v1`).
4. Logs a structured record.
5. Responds `{"allowed": true}`.

That's it. Everything else is policy you add.

## Run locally

```bash
go run ./examples/webhook-handler -addr :8080 -secret topsecret
```

Register it against a k8s-stack-manager instance by adding a subscription
pointing at `http://<host>:8080/events`. See the hooks contract doc for the
full envelope and subscription schema.

## Contract summary

Request headers:
- `Content-Type: application/json`
- `X-StackManager-Event: <event-name>` (e.g. `pre-deploy`)
- `X-StackManager-Request-Id: <id>` (same id also in the envelope)
- `X-StackManager-Signature: sha256=<hex>` (only when the subscription has a
  secret configured)

Response (any 2xx):
```json
{ "allowed": true,  "message": "" }
```
or, to deny a pre-\* event with `failure_policy=fail`:
```json
{ "allowed": false, "message": "quota exceeded" }
```

Respond with a non-2xx to signal transport failure; that is also treated as a
failure (and may abort the operation when `failure_policy=fail`).

## Tests

```bash
go test ./examples/webhook-handler/...
```

Covers: valid request, bad signature rejected, valid signature accepted, malformed envelope.

## Next steps

- Subscribe to specific events only (filter on `X-StackManager-Event`).
- Implement policy: reject pre-deploys during a maintenance window, reject
  pre-instance-deletes while dependencies exist, etc.
- For longer-running work (RefreshDB-style), respond quickly with
  `{"allowed": true}` and do the real work as an **action** via
  `POST /api/v1/stack-instances/:id/actions/:name` — actions are RPC-style
  and can return arbitrary JSON payloads.
