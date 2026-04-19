# Extensibility reference: Webhooks and Actions

> **Looking to get started?** Read [EXTENDING.md](../../EXTENDING.md) at the
> repo root — it's tutorial-first with a 10-minute walkthrough, real-world
> recipes, and troubleshooting. This file is the authoritative schema + field
> reference, useful when you're implementing a subscriber and want precision.

k8s-stack-manager exposes two integration points for third-party behaviour:

1. **Lifecycle events (webhooks)** — fire-and-forget or abort-capable notifications
   at well-defined points in the deploy/instance lifecycle. Use these to observe
   or gate operations (audit logging, CMDB sync, deploy policy enforcement).

2. **Actions** — named, RPC-style handlers invoked by API callers against a
   specific instance. Use these for user-initiated operations the core does not
   implement (database refresh, snapshot restore, seed-data loading).

Neither mechanism requires recompiling or forking k8s-stack-manager. Both run
over plain HTTP, so handlers can be written in any language.

The reference implementation in [../examples/webhook-handler](../examples/webhook-handler/)
is a minimal Go starting point.

---

## Events

### Available events

| Event | Fires when | Semantics |
|---|---|---|
| `pre-deploy` | Just before a deployment starts, after cluster resolution. | Synchronous. A subscriber with `failure_policy=fail` can abort. |
| `post-deploy` | After a deployment completes **successfully**. | Fire-and-forget (default `failure_policy=ignore`). |
| `deploy-finalized` | After a deployment ends **successfully or not**. | Fire-and-forget. |
| `pre-instance-create` | After validation, before the instance is written to the DB. | Synchronous. `failure_policy=fail` aborts the create (HTTP 403). |
| `post-instance-create` | After the instance is persisted. | Fire-and-forget. |
| `pre-instance-delete` | After the instance ID is validated, before delete. | Synchronous. `failure_policy=fail` aborts (HTTP 403). |
| `post-instance-delete` | After the instance has been deleted. | Fire-and-forget. |
| `pre-namespace-create` | *(reserved — not yet wired)* | — |
| `post-namespace-create` | *(reserved — not yet wired)* | — |

Pre-* events let subscribers block the operation. Post-* and `deploy-finalized`
are notify-only; they should use `failure_policy=ignore` so a slow or down
subscriber cannot stall the deploy goroutine.

### Subscription shape

```yaml
hooks:
  subscriptions:
    - name: cmdb-sync
      events: [post-instance-create, post-instance-delete]
      url: https://cmdb.internal/hooks/stackmgr
      timeout_seconds: 5         # optional, default 5, max 30
      failure_policy: ignore     # optional, default ignore; set "fail" to block the operation on error
      # secret: <ref>            # shared HMAC secret (see Security below)
```

### Request envelope

Every subscriber receives `POST <url>` with:

```
Content-Type: application/json
X-StackManager-Event: <event-name>
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex>    (only when secret is set)
```

Body (`apiVersion: hooks.k8sstackmanager.io/v1`):

```json
{
  "apiVersion": "hooks.k8sstackmanager.io/v1",
  "kind": "EventEnvelope",
  "event": "pre-deploy",
  "timestamp": "2026-04-18T10:15:32.845Z",
  "request_id": "req-f2a1...",
  "instance": {
    "id": "6c9f1e14-...",
    "name": "demo",
    "namespace": "stack-demo-alice",
    "owner_id": "uid-123",
    "stack_definition_id": "def-...",
    "branch": "main",
    "cluster_id": "ple",
    "status": "draft"
  },
  "deployment": {
    "id": "log-...",
    "started_at": "2026-04-18T10:15:32.820Z"
  },
  "charts":   [{"name": "web", "release_name": "web", "version": "1.2.3"}],
  "values":   {},
  "metadata": {},
  "extra":    {}
}
```

Deployment/charts/values are populated only when relevant to the event. Handlers
should not assume every field is present.

### Response

Subscribers return a `HookResponse`:

```json
{ "allowed": true,  "message": "" }
```

To block a pre-* event that has `failure_policy=fail`:

```json
{ "allowed": false, "message": "quota exceeded on cluster ple" }
```

Any non-2xx response is also treated as a failure. Empty 2xx bodies are
interpreted as `{"allowed": true}`.

---

## Actions

Actions generalise the former `/refresh-db` endpoint. Any named action
registered at startup is invocable at:

```
POST /api/v1/stack-instances/:id/actions/:name
Content-Type: application/json

{ "parameters": { "image": "alpine", "reason": "reseed" } }
```

### Registration

```yaml
hooks:
  actions:
    - name: refresh-db
      url: https://kvk-devops-tools.internal/actions/refresh-db
      description: Wipe MySQL PVC and flush Redis for the instance
      timeout_seconds: 120       # optional, default 30, max 300
      # secret: <ref>
```

### Request envelope

```
POST <url>
Content-Type: application/json
X-StackManager-Event: action:<name>
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex>
```

Body (`apiVersion: hooks.k8sstackmanager.io/v1`, `kind: ActionRequest`):

```json
{
  "apiVersion": "hooks.k8sstackmanager.io/v1",
  "kind": "ActionRequest",
  "action": "refresh-db",
  "timestamp": "...",
  "request_id": "...",
  "instance": { ... },
  "parameters": { ... }
}
```

### Response

Actions may return any JSON. The core forwards the subscriber's body verbatim
to the API client, wrapped in:

```json
{
  "action": "refresh-db",
  "instance_id": "6c9f1e14-...",
  "status_code": 200,
  "result": { "wiped_pvcs": ["mysql-data"], "flushed_keys": 128 }
}
```

API error mappings:

| API status | Meaning |
|---|---|
| 200 | Subscriber responded (see `result` for details, `status_code` echoes the subscriber's HTTP code) |
| 400 | Invalid parameters / malformed body |
| 404 | Unknown instance OR unknown action name |
| 502 | Subscriber unreachable or returned a transport error |
| 503 | Action registry not configured on this server |

---

## Security

### HMAC signing

When a subscription (event or action) has a `secret` configured, the request
body is signed with HMAC-SHA256:

```
X-StackManager-Signature: sha256=<hex-of-HMAC-SHA256(secret, body)>
```

Handlers must verify the signature before trusting any envelope fields. The
reference handler at [../examples/webhook-handler/main.go](../examples/webhook-handler/main.go)
demonstrates the verification.

Use a high-entropy shared secret (≥32 bytes). Rotate secrets by registering a
new subscription alongside the old, cutting traffic over, then removing the
old entry.

### Network posture

The server establishes outbound HTTPS to subscriber URLs. Handlers live where
they belong (inside the cluster, behind a VPN, on a bastion) — k8s-stack-manager
does not care, as long as the URL is reachable from the server's network.

### Replay protection

The envelope includes a unique `request_id` (`req-` + 24 hex chars). Handlers
that need at-least-once + dedup semantics should track recently-seen IDs
(a 10-minute LRU is usually enough).

### Failure policy and blast radius

- `failure_policy=fail` on a pre-* event blocks the operation when the subscriber
  fails. Use it sparingly — a broken subscriber can halt all deploys.
- Every hook has a per-call timeout (default 5s for events, 30s for actions),
  capped at 30s / 5min respectively.
- Dispatch is synchronous and in subscription registration order; a `fail`
  subscription that errors prevents later subscriptions on the same event from
  being invoked.

---

## Contract versioning

`apiVersion` in every envelope identifies the contract revision:

- `hooks.k8sstackmanager.io/v1` — non-mutating; subscribers can only allow or
  deny. This is the current version.
- A future `v2` may add mutating webhooks that rewrite parts of the payload
  (values, chart list, etc.) before the operation continues. Handlers should
  ignore envelopes they do not understand and return `{"allowed": true}` or
  HTTP 200 without a body.

---

## Observability

Every dispatch logs structured events to the server's log stream with these
fields:

- `event`
- `subscription` (name from config)
- `request_id`
- `status` (success | denied | transport_error | timeout)
- `duration_ms`

Correlate across systems via `request_id`.
