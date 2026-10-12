# Example webhook handler

This is a minimal reference implementation of a k8s-stack-manager event
subscriber. Use it as a starting point for custom handlers, for example
database refresh, configuration management database (CMDB) sync and
deploy-gate policies.

## What it does

The handler listens on `/events`. For each request it does these steps:

1. Reads the request body (at most 1 MiB).
2. If you start it with `-secret <value>`, verifies the `X-StackManager-Signature`
   header. The header is the HMAC-SHA256 (hash-based message authentication
   code) of the body, hex-encoded, with the prefix `sha256=`.
3. Parses the envelope (`apiVersion: hooks.k8sstackmanager.io/v1`).
4. Writes a structured log record (JSON).
5. Responds with `{"allowed": true}`.

Everything else is policy that you add.

## Run locally

Run the command from the `backend/` directory:

```bash
go run ./examples/webhook-handler -addr :8080 -secret topsecret
```

The flags are `-addr` (default `:8080`) and `-secret` (default: empty, no
signature check).

To register the handler, add a subscription that points at
`http://<host>:8080/events`. The same secret must be available to the backend
through the environment variable that `secret_env` names. The full envelope and
subscription schema is in [../../docs/hooks.md](../../docs/hooks.md).

## Contract summary

Request headers:
- `Content-Type: application/json`
- `X-StackManager-Event: <event-name>` (for example `pre-deploy`)
- `X-StackManager-Request-Id: <id>` (the same ID is also in the envelope)
- `X-StackManager-Signature: sha256=<hex>` (only when the subscription has a
  secret)

Response (any 2xx):
```json
{ "allowed": true,  "message": "" }
```
To deny a `pre-*` event that has `failure_policy=fail`:
```json
{ "allowed": false, "message": "quota exceeded" }
```

A non-2xx response signals a failure. The core treats it as a failure, and it
can abort the operation when `failure_policy=fail`.

## Tests

```bash
go test ./examples/webhook-handler/...
```

The tests cover: a valid request, a rejected bad signature, an accepted valid
signature and a malformed envelope.

## Next steps

- Subscribe to specific events only (filter on `X-StackManager-Event`).
- Implement policy. Examples: reject a `pre-deploy` during a maintenance
  window, reject a `pre-instance-delete` while dependencies exist.
- For longer work (for example a database refresh), do not use an event. Use an
  **action** instead: `POST /api/v1/stack-instances/:id/actions/:name`. An
  action is RPC-style and can return any JSON. See
  [../webhook-handler-python/](../webhook-handler-python/) for an action
  handler.
