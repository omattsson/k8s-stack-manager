# Extending k8s-stack-manager

k8s-stack-manager ships with no opinion about your organisation's specific operations. Database refreshes, CMDB sync, deploy gates, snapshot restores, Slack notifications — none of that is built in. But all of it can be added **without forking**, in any language, as a small out-of-process service.

The extension mechanism is two primitives:

- **Event hooks** — the core fires named events (`pre-deploy`, `post-instance-create`, …) and POSTs them to subscriber URLs. A `failure_policy: fail` subscriber can abort the operation. Use these to **observe or gate**.
- **Actions** — the core exposes a generic `POST /api/v1/stack-instances/:id/actions/:name` route that dispatches to a subscriber you register. The subscriber does real work and its response is forwarded to the caller. Use these for **user-initiated custom operations**.

Both run over plain HTTP with HMAC signing. A subscriber is any server you can reach over the network — Python, Go, Node, Rust, Bash+netcat, a Lambda, an Argo Workflow. You pick.

---

## The 10-minute tutorial — your first custom action

You'll build a `snapshot-pvc` action that takes a PVC snapshot for a stack instance. Full production would use VolumeSnapshots; we'll stub it with a shell command so the tutorial fits on one page.

### 1. Write the handler

```python
#!/usr/bin/env python3
# snapshot-server.py — minimal action subscriber
import hashlib, hmac, json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SECRET = os.environ["SNAPSHOT_WEBHOOK_SECRET"]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))

        # 1. Verify HMAC-SHA256 signature
        want = "sha256=" + hmac.new(SECRET.encode(), body, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(self.headers.get("X-StackManager-Signature", ""), want):
            self.send_response(401); self.end_headers(); return

        # 2. Parse the ActionRequest envelope
        req = json.loads(body)
        inst = req["instance"]
        print(f"snapshot-pvc for {inst['namespace']}/{inst['name']}")

        # 3. Do the work (replace with a real VolumeSnapshot create)
        snapshot_name = f"{inst['name']}-{req['request_id']}"

        # 4. Respond with arbitrary JSON — the core forwards it verbatim
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(json.dumps({"snapshot": snapshot_name, "namespace": inst["namespace"]}).encode())

ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
```

### 2. Run it

```bash
export SNAPSHOT_WEBHOOK_SECRET=$(openssl rand -hex 32)
python3 snapshot-server.py
```

### 3. Register it with k8s-stack-manager

Create a JSON config file:

```json
{
  "actions": [
    {
      "name": "snapshot-pvc",
      "url": "http://<your-host>:8080/",
      "description": "Take a VolumeSnapshot of a stack's MySQL PVC",
      "timeout_seconds": 60,
      "secret_env": "SNAPSHOT_WEBHOOK_SECRET"
    }
  ]
}
```

Point the backend at it:

```bash
export HOOKS_CONFIG_FILE=/path/to/actions.json
export SNAPSHOT_WEBHOOK_SECRET=...same value as above...
# restart the backend
```

You should see on boot:
```
INFO hooks configured config_file=... actions=[snapshot-pvc]
```

### 4. Invoke it

```bash
curl -X POST https://stack-manager.example/api/v1/stack-instances/$INSTANCE_ID/actions/snapshot-pvc \
     -H "X-API-Key: $API_KEY"
```

Response:
```json
{
  "action": "snapshot-pvc",
  "instance_id": "...",
  "status_code": 200,
  "result": { "snapshot": "demo-req-abc123", "namespace": "stack-demo-alice" }
}
```

That's it. You've added a custom action to your stack-manager deployment without touching its source code.

---

## Shapes of extension

### Actions — RPC-style, user-initiated

```
user → stackctl → k8s-stack-manager → subscriber
                       POST /api/v1/stack-instances/:id/actions/:name
subscriber responds with arbitrary JSON, forwarded verbatim to the user
```

Use when:
- The user explicitly triggers something (`stackctl refresh-db <id>`)
- You need the caller to see the result
- The work is bounded (≤5min by default; subscriber-side async for longer)

Examples: database refresh, snapshot restore, seed data load, force redeploy, cache warm-up.

### Event hooks — fire-and-forget or gate-keeping

```
core fires event X → POSTs envelope to every subscriber of X →
  if subscriber returns Allowed:false AND failure_policy=fail → abort
  otherwise → continue
```

Use when:
- You want to observe but not initiate (audit log, CMDB sync, Slack notify)
- You want to block policy violations (maintenance window, quota exceeded)
- Multiple subscribers want to react to the same event

Examples: post-deploy Slack message, pre-instance-delete "still has dependencies" check, post-instance-create CMDB record.

### Template functions — in-process helpers

```
YAML values: host: "{{ .Owner | dnsify }}.example.com"
```

Use when:
- You need small computed values in chart values at render time
- Behaviour is pure-function and belongs in-process

Register at startup: `valuesGen.RegisterFunc("dnsify", fn)`. See
[backend/internal/helm/values_generator.go](backend/internal/helm/values_generator.go).

---

## Events reference

| Event | Fires when | Semantics |
|---|---|---|
| `pre-deploy` | Just before a deployment starts, after cluster resolution | Sync. `failure_policy: fail` aborts. |
| `post-deploy` | After a deployment completes **successfully** | Fire-and-forget. With `blocking: true` the deploy waits for the subscriber (see [Blocking post-deploy hooks](#blocking-post-deploy-hooks)). |
| `deploy-finalized` | After a deployment ends, success **or** failure (after the blocking post-deploy hooks) | Fire-and-forget. |
| `deploy-timeout` | After a deployment failed on a Helm or deploy budget timeout | Fire-and-forget. |
| `stop-completed` | After a stop ends (`instance.status` `stopped` or `error`) | Fire-and-forget. |
| `clean-completed` | After a clean ends (`draft` or `error`). For the clean of a delete, `metadata.operation` is `delete` and `delete-completed` follows | Fire-and-forget. |
| `delete-completed` | After any delete: API (draft, or after the clean) or cleanup policy | Fire-and-forget. |
| `cleanup-policy-executed` | Once per cleanup policy run with at least one match (also a dry run) | Fire-and-forget. The run summary is in `cleanup_policy` (see [backend/docs/hooks.md](backend/docs/hooks.md#cleanup-policy-runs)). |
| `pre-rollback` | Before a rollback runs (in the background, after the API answered 202) | Sync for the rollback, with progress streaming. `failure_policy: fail` aborts; the instance gets its previous status back. Same `charts` list as `pre-deploy`. |
| `post-rollback` | After a rollback completes **successfully** | Fire-and-forget. |
| `rollback-completed` | After a rollback ends: `metadata.outcome` = `succeeded`, `failed`, `rejected` (by `pre-rollback`) or `cancelled` (another operation started) | Fire-and-forget. |
| `pre-instance-create` | After validation, before DB write | Sync. `failure_policy: fail` → HTTP 403. |
| `post-instance-create` | After the instance is persisted | Fire-and-forget. |
| `pre-instance-delete` | Before each delete: single, bulk and cleanup policy | Sync. `failure_policy: fail` → HTTP 403 (single delete) or an `error` result for that instance (bulk, policy). |
| `post-instance-delete` | After each delete completes (single, bulk, after a clean, cleanup policy) | Fire-and-forget. |

Reserved for future: `pre-namespace-create`, `post-namespace-create`. Accepted in the config but not fired yet: `instance-created`, `stack-expiring`, `stack-expired`, `quota-warning`, `secret-expiring`.

Each deploy, stop, clean, rollback and delete envelope has a `trigger`: `{"type": "user", "id": "<user id>", "name": "<username>"}` for an API call, `{"type": "cleanup-policy", "id": "<policy id>", "name": "<policy name>"}` for a cleanup policy, `{"type": "ttl"}` for the TTL reaper. A notifier can show "stopped by cleanup policy nightly-stop".

Pre-* subscribers can block; post-* subscribers should default to `failure_policy: ignore` so a slow or down subscriber can't stall the deploy goroutine.

## Subscription configuration

Both event subscriptions and action subscriptions live in the `HOOKS_CONFIG_FILE` JSON:

```json
{
  "subscriptions": [
    {
      "name": "cmdb-sync",
      "events": ["post-instance-create", "post-instance-delete"],
      "url": "https://cmdb.internal/hooks/stackmgr",
      "timeout_seconds": 5,
      "failure_policy": "ignore",
      "secret_env": "CMDB_WEBHOOK_SECRET"
    },
    {
      "name": "maintenance-gate",
      "events": ["pre-deploy"],
      "url": "http://maintenance-gate.ops:8080/check",
      "timeout_seconds": 3,
      "failure_policy": "fail",
      "secret_env": "MAINT_WEBHOOK_SECRET"
    }
  ],
  "actions": [
    {
      "name": "refresh-db",
      "url": "http://refresh-db.refresh-db.svc.cluster.local/",
      "description": "Wipe MySQL PVC and flush Redis",
      "timeout_seconds": 30,
      "secret_env": "REFRESH_DB_WEBHOOK_SECRET"
    }
  ]
}
```

- **`secret_env`** names an environment variable holding the HMAC secret. The file itself is safe to commit to version control; secrets stay in env (mounted from a Kubernetes Secret, Vault, …).
- Empty `secret_env` disables HMAC signing for that subscriber — safe only for internal localhost communication on a trust boundary.
- `timeout_seconds`: events default 5s (max 1800s); actions default 30s (max 600s).
- `failure_policy`: `fail` or `ignore`; defaults to `ignore` if omitted.
- Actions also accept optional UI fields: `label`, `confirm`, `parameters` and `log_path`. See [Actions in the web UI](#actions-in-the-web-ui).

## Request envelope — EventEnvelope

Every event subscriber receives:

```
POST <url>
Content-Type: application/json
X-StackManager-Event: pre-deploy
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex>       (when secret configured)
```

Body (`apiVersion: hooks.k8sstackmanager.io/v1`, `kind: EventEnvelope`):

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
  "deployment": { "id": "log-...", "started_at": "..." },
  "charts":   [{"name": "web", "release_name": "web", "version": "1.2.3", "source_repo_url": "https://dev.azure.com/org/proj/_git/web", "build_pipeline_id": "42"}],
  "values":   {},
  "metadata": {},
  "extra":    {}
}
```

Not every field is populated for every event — handlers should check presence and not assume.

### Response

```json
{ "allowed": true }
```

or to block a pre-* with `failure_policy: fail`:

```json
{ "allowed": false, "message": "quota exceeded on cluster ple" }
```

Any non-2xx response is treated as failure. Empty 200 bodies are interpreted as `{"allowed": true}`.

### Streaming progress (long-running hooks)

For hooks that take a long time (e.g. triggering CI builds and waiting for completion), subscribers can stream progress lines before the final JSON response. The deployment log viewer shows these lines in real-time with the LIVE indicator.

**Protocol:** Write `LOG:` `<message>\n` lines to the response body, flushing after each line. The final line must be the JSON `HookResponse`. Lines without the `LOG:` prefix are treated as the JSON response.

```
HTTP/1.1 200 OK
Content-Type: application/x-ndjson
Transfer-Encoding: chunked

LOG: Checking image app-api:feature-x...
LOG: Image not found, triggering ADO build #1234
LOG: Build #1234: queued
LOG: Build #1234: in progress
LOG: Build #1234: completed successfully (3m12s)
LOG: All images ready, proceeding with deployment
{"allowed": true, "message": "all builds ready"}
```

Go example using `http.Flusher`:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    flusher, _ := w.(http.Flusher)
    w.Header().Set("Content-Type", "application/x-ndjson")
    w.WriteHeader(http.StatusOK)

    fmt.Fprintln(w, "LOG: Starting checks...")
    flusher.Flush()

    // ... do work, write more LOG: lines ...

    fmt.Fprintln(w, `{"allowed": true}`)
    flusher.Flush()
}
```

**Backward-compatible:** Subscribers that return plain JSON (no `LOG:` lines) work identically to before. The streaming protocol is opt-in per subscriber.

### Blocking post-deploy hooks

A normal `post-deploy` subscriber is fire-and-forget: the stack is `running` and the user gets "Deployment succeeded" while the subscriber still works. For long post-deploy work (for example a database restore into the new stack), set `blocking: true`:

```json
{
  "name": "db-restore",
  "events": ["post-deploy"],
  "url": "http://db-restore.extensions:8080/hook",
  "blocking": true,
  "timeout_seconds": 900,
  "failure_policy": "ignore",
  "secret_env": "DB_RESTORE_HOOK_SECRET"
}
```

- The deployer calls the blocking subscriber after the Helm releases are ready and waits for it. The stack stays `stabilizing`.
- `LOG:` lines (the streaming protocol above) go to the deploy log and the live log view.
- `timeout_seconds` (max 1800) limits the call. The wait has its own time limit (the sum of the blocking timeouts plus one minute); the Helm deploy budget does not change.
- Only after the subscriber returned: status `running`, "Deployment succeeded", the other `post-deploy` subscribers, `deploy-finalized`.
- Failure, denial or timeout: with `failure_policy: fail` the deploy fails (status `error`, the hook reason in `error_message`, never the URL); with `ignore` the stack gets `running` and the owner gets the warning "Post-deploy step db-restore failed".
- Only a Stop ends the wait early (clean, delete and deploy are refused while the stack is `stabilizing`); the k8s status watcher does not set `error` during the wait. The deployer then closes the request, but that does not stop remote work: stop your work when the request closes. The stop runs `helm uninstall` at the same time.
- A server shutdown ends the deploy with `error` ("Interrupted by a server restart. Deploy again."). After a SIGKILL or a node loss the leader sets the stack to `error` ("Interrupted: the server that ran this operation stopped. Deploy again.") when the deploy passed its deadline (its time budget, including the blocking timeouts, plus 5 minutes) and the replica has no heartbeat for 2 minutes. A Stop ends the state earlier. No hook event fires for this recovery.

`blocking` is only valid for a subscription that lists `post-deploy`. The full contract is in [backend/docs/hooks.md](backend/docs/hooks.md#blocking-post-deploy-subscribers).

The backend does not follow a redirect from an event subscriber: a 3xx answer is a failure, and `failure_policy` applies. Configure the final URL.

## Request envelope — ActionRequest

Actions are invoked by API callers (or stackctl) at `POST /api/v1/stack-instances/:id/actions/:name`:

```
POST <url>
Content-Type: application/json
X-StackManager-Event: action:refresh-db
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
  "instance": { /* same shape as EventEnvelope.instance */ },
  "parameters": { /* caller-supplied, arbitrary JSON */ }
}
```

### Response

Actions can return **any JSON**. The core wraps it in a response envelope:

```json
{
  "action": "refresh-db",
  "instance_id": "6c9f1e14-...",
  "status_code": 200,
  "result": { ... your subscriber's JSON body, verbatim ... }
}
```

API error mappings when invoking actions:

| HTTP status | Meaning |
|---|---|
| `200` | Subscriber responded — see `result` + echoed `status_code` |
| `400` | Invalid parameters / malformed body |
| `404` | Unknown instance OR unknown action name |
| `502` | Subscriber unreachable or transport error |
| `503` | Action registry not configured on this server |

---

## Actions in the web UI

The instance detail page has an **Actions** menu next to Deploy, Stop and Clean. It lists the actions from `GET /api/v1/stack-instances/:id/actions`. The menu is hidden when no action is registered. Every user who can view the instance sees the menu; only the owner, an admin or a devops user can run an action (`can_invoke`), so for other users the items are disabled.

Add optional UI fields to the action config:

```json
{
  "actions": [
    {
      "name": "refresh-db",
      "url": "http://refresh-db.refresh-db.svc.cluster.local/",
      "description": "Replace the stack database with the latest snapshot",
      "secret_env": "REFRESH_DB_WEBHOOK_SECRET",
      "label": "Refresh database",
      "confirm": "This replaces the database of the stack. Unsaved data is lost.",
      "parameters": [
        {"name": "image", "label": "Snapshot", "type": "string", "default": "golden"},
        {"name": "dry_run", "label": "Dry run", "type": "bool"},
        {"name": "scope", "type": "enum", "options": ["full", "schema-only"], "required": true}
      ],
      "log_path": "/jobs/{job_id}/log"
    }
  ]
}
```

- `label` — menu text (default: `name`).
- `confirm` — warning text in the run dialog. Use it for destructive actions.
- `parameters` — the UI renders a form: `string` → text field, `bool` → switch, `enum` → select with `options`. `required` and `default` are optional. The backend checks declared parameters (type, required, options) and returns 400 on a mismatch; undeclared parameters pass through. The values reach the subscriber in `parameters`.
- `log_path` — see below.

The list endpoint never returns `url`, `secret_env` or headers. The dialog shows the result: `status_code` and `result`. A non-2xx `status_code` shows as an error.

### Asynchronous actions: job ID and job log

If the work takes longer than `timeout_seconds`, answer at once with a job ID and run the work in the background:

```json
{"job_id": "job-0123456789ab", "status": "started"}
```

Set `log_path` (a path on the host of `url`, with `{job_id}` once). The backend then serves the job log at:

```
GET /api/v1/stack-instances/:id/actions/:name/jobs/:job_id/log?offset=<n>
```

It answers JSON `{status, log, offset, next_offset, done, truncated}`. The web UI polls it every 3 seconds until `done` is true, and backs off on 429, 5xx gateway errors and network errors (`Retry-After`, else up to 30 seconds); a CLI can use the same route for `--follow`. Only users who may run the action may read the job log, because a job log can hold sensitive output.

Your subscriber serves the log on `GET <log_path>?instance_id=<id>&offset=<n>&ts=<unix seconds>`:

- Job IDs MUST be random (for example `"job-" + uuid4().hex`). Never use sequential numbers or timestamps.
- Store the instance ID with each job. You MUST answer 404 when `instance_id` does not match the job's instance.
- Return the log bytes from `offset` to the end as text (200), or 404 for an unknown job.
- Send `X-Job-Status` (`running`, `succeeded`, `failed`, ...). Optional: `X-Log-Offset` (next offset; default `offset` + body length; ignored when smaller than `offset`).
- An end marker line such as `===JOB-END=== status=succeeded` is only a fallback when you cannot send `X-Job-Status`: job output can print a fake marker.
- Verify `X-StackManager-Signature`: for this GET it is the HMAC-SHA256 of the request URI (path and query, exactly as received). Refuse a request whose `ts` is more than 5 minutes from your clock.
- The query string of the action `url` is not sent on log reads. If you authenticate invokes with a query key, authenticate log reads with the signature.

```python
def do_GET(self):
    # Verify the signature over the request URI (path + query).
    want = "sha256=" + hmac.new(SECRET.encode(), self.path.encode(), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(self.headers.get("X-StackManager-Signature", ""), want):
        self.send_response(401); self.end_headers(); return
    ts = parse_qs(urlsplit(self.path).query).get("ts", ["0"])[0]
    if not ts.isdigit() or abs(time.time() - int(ts)) > 300:   # 5-minute window
        self.send_response(401); self.end_headers(); return
    m = re.match(r"^/jobs/([A-Za-z0-9_-][A-Za-z0-9._-]{0,127})/log$", urlsplit(self.path).path)
    job = JOBS.get(m.group(1)) if m else None
    qs = parse_qs(urlsplit(self.path).query)
    if job is None or qs.get("instance_id", [""])[0] != job.instance_id:
        self.send_response(404); self.end_headers(); return
    offset = max(0, int(qs.get("offset", ["0"])[0]))
    data = job.read_log(offset)
    self.send_response(200)
    self.send_header("Content-Type", "text/plain; charset=utf-8")
    self.send_header("X-Log-Offset", str(offset + len(data)))
    self.send_header("X-Job-Status", job.status)   # running | succeeded | failed
    self.end_headers()
    self.wfile.write(data)
```

The backend never follows a redirect, reads at most 256 KiB per call (the client gets the rest with the next offset) and waits at most 10 seconds. The full contract is in [backend/docs/hooks.md](backend/docs/hooks.md#asynchronous-actions-and-the-job-log).

---

## Security

### HMAC signing

With a `secret_env` configured, every request is signed:

```
X-StackManager-Signature: sha256=<hex of HMAC-SHA256(secret, raw body)>
```

**Subscribers must verify** before trusting any envelope field. Constant-time compare (`hmac.compare_digest` in Python, `hmac.Equal` in Go). Reject on mismatch with 401.

Rotate secrets by registering a new subscription alongside the old, cutting traffic over, then removing the old entry.

### Replay protection

Every envelope includes a unique `request_id` (`req-` + 24 hex chars). If at-least-once delivery + dedup matters, keep a small LRU of seen IDs (10 minutes is usually enough) and reject replays.

The signature has no separate timestamp header. For events and action invokes, the `timestamp` field is inside the signed body: reject an envelope older than 5 minutes and a `request_id` that you saw before, else a captured request can be replayed. Job log reads carry a signed `ts` query parameter for the same check.

### Network posture

The server makes **outbound** HTTPS (or HTTP) to subscriber URLs. Subscribers live wherever is reachable:
- **In-cluster** (`svc.cluster.local`) — easiest, most common
- **Over the VPN** — fine
- **Over the public internet** with mTLS or signed payload — viable for SaaS integrations

### Failure blast radius

`failure_policy: fail` on a pre-* event blocks the operation when the subscriber fails. Use sparingly — a broken subscriber can halt every deploy. Prefer `ignore` + alerting for anything non-critical.

Per-call timeouts (default 5s for events, 30s for actions; max 10min / 5min respectively) bound the worst case.

### Don't trust outbound URLs in a hostile environment

If your k8s-stack-manager instance is reachable by untrusted actors who could create subscriptions, restrict the action registry config to deployment-time only — don't expose subscription management as an API until SSRF mitigations are in place.

---

## Observability

### Metrics (OpenTelemetry)

Every dispatch and every action invocation increments counters + histograms in the `hooks` meter scope. All instruments are always on — if OTel is disabled they fall back to no-op. Names and labels:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `hook.dispatches_total` | Counter | `hook.event`, `hook.subscription`, `hook.outcome` | One per event → subscriber attempt |
| `hook.dispatch_duration` | Histogram (seconds) | `hook.event`, `hook.subscription` | Wall-clock time for the POST + response |
| `hook.action_invocations_total` | Counter | `hook.action`, `hook.outcome` | One per `/actions/{name}` call |
| `hook.action_invocation_duration` | Histogram (seconds) | `hook.action` | Wall-clock time for the action POST |

`hook.outcome` is a small, stable set so dashboards can enumerate it: `success`, `denied`, `http_error`, `transport_error`, `timeout`, `unknown_action`, `marshal_error`.

**Prometheus queries you probably want:**

```promql
# dispatch error rate per subscription (last 5m)
sum by (hook_subscription, hook_outcome) (
  rate(hook_dispatches_total{hook_outcome!="success"}[5m])
)

# p95 dispatch latency per event
histogram_quantile(0.95, sum by (le, hook_event) (rate(hook_dispatch_duration_bucket[5m])))

# actions that repeatedly return non-2xx
sum by (hook_action) (increase(hook_action_invocations_total{hook_outcome="http_error"}[15m])) > 0
```

### Traces (OpenTelemetry)

Every dispatch opens a span: `hooks.dispatch` for event subscriber calls, `hooks.action` for action invocations. Each span carries:

- `hook.event` / `hook.action` — which event or action is firing
- `hook.subscription` — subscriber name (for events)
- `hook.request_id` — same value as `X-StackManager-Request-Id`, so you can correlate logs ↔ traces ↔ subscriber-side records
- `hook.outcome` — terminal label matching the metric outcome
- `hook.status_code` — HTTP status when a response was observed
- `span.status` — `Ok` on success, `Error` on any non-success outcome

**Trace context propagation:** k8s-stack-manager installs the W3C TraceContext propagator globally, and every outbound hook/action request carries a `Traceparent` header. Subscribers that run their own OTel SDK automatically stitch as children. The Python reference at [backend/examples/webhook-handler-python/](backend/examples/webhook-handler-python/) shows the minimal server side; the Go reference at [backend/examples/webhook-handler/](backend/examples/webhook-handler/) does the same.

In Jaeger / Tempo you'll see a trace like:

```
│ stack-manager: deployer.deploy        (root)
│  ├─ hooks.dispatch                    pre-deploy → cmdb-sync        outcome=success
│  ├─ hooks.dispatch                    pre-deploy → maintenance-gate outcome=denied
│  └─ cmdb-sync-server: POST /events    (from subscriber's OTel SDK)
```

### Structured logs

Dispatch failures still emit slog lines for quick triage:

```
level=warn subscription=cmdb-sync event=post-instance-create request_id=req-abc status=transport_error error="connection refused"
```

Correlate across systems via `request_id` — it appears on the metric/span attributes above and in the `X-StackManager-Request-Id` header on every outbound request.

---

## Real-world examples

### Database refresh

A "refresh-db" operation (wipe the per-instance MySQL PVC, flush Redis, restart app pods) is the canonical action-webhook use case: it needs cluster credentials, runs a multi-step kubectl sequence, and is logically an RPC on the instance. A reference implementation pattern:

- Python webhook server (~340 lines, stdlib only — `http.server.ThreadingHTTPServer` + `subprocess` for kubectl)
- kubectl orchestration for the restore sequence (scale down, truncate, flush, re-extract golden DB, scale up)
- Per-job progress log on disk for real-time tailing, served on `log_path` (for example `/jobs/{job_id}/log`) so the web UI shows the progress
- HMAC signature verification on every request
- Ships as a Kubernetes Deployment + ClusterRole + Service
- ~40 MB container (alpine + python + kubectl)

End-to-end (webhook receives request → kubectl orchestration completes → response) is typically in the minutes range depending on DB size.

### Policy gate — block deploys outside business hours

A tiny pre-deploy subscriber that returns `{"allowed": false}` between 5pm and 9am on weekdays. 20 lines of Python, deployed as a Kubernetes Deployment with no state. Registered with `failure_policy: fail`.

### CMDB sync — mirror every stack instance

Post-instance-create + post-instance-delete subscribers that update an external asset inventory. `failure_policy: ignore` so a CMDB outage can't block instance management.

### Slack notification — surface failures to #ops

A post-deploy subscriber that posts to Slack on `status=error` only. Drops on 2xx fast path. Registered with a short timeout so transient Slack outages don't affect dispatch.

### CI trigger gate — build images before deploy

A `pre-deploy` subscriber with `failure_policy: fail` and a high timeout (up to 1800s) that checks if container images exist for the branch being deployed. When images are missing, it triggers CI builds (Azure DevOps, GitHub Actions, GitLab CI, etc.) and polls until they complete.

The core sends `charts` with `source_repo_url`, `build_pipeline_id`, `branch` and `image_tag` (the `{{.ImageTag}}` value of that chart) in the pre-deploy envelope, plus `metadata.registry_url` for registry lookups. The subscriber uses the streaming progress protocol to show build status in the deployment log.

Pre-deploy hooks do not run for a rollback. To gate rollbacks too, add `pre-rollback` to `events`: the envelope has the same `charts` list, plus `metadata.rollback_mode` (`previous_revision` or `target`), `target_log_id` and `target_branch`. A rollback to a target restores the stored values of that deploy, not the images: an image tag that is a branch name can point to a newer image. `pre-rollback` runs in the background like `pre-deploy` and supports the same streaming progress protocol, so the gate's `LOG:` lines appear in the rollback log. For a one-revision rollback, the chart branches come from the previous successful deploy log when it recorded a branch (`metadata.branch_source`).

```json
{
  "subscriptions": [{
    "name": "ci-trigger-gate",
    "events": ["pre-deploy"],
    "url": "http://ci-trigger-gate.extensions.svc.cluster.local:8080/hook",
    "timeout_seconds": 1500,
    "failure_policy": "fail",
    "secret_env": "CI_TRIGGER_WEBHOOK_SECRET"
  }]
}
```

Skip logic keeps it fast: charts without `build_pipeline_id` are skipped (public/pre-built images), semver tags are skipped (release images), and an in-memory cache avoids re-checking images that were verified recently. A typical deploy with pre-built images adds <1s; a deploy that triggers builds blocks for 3-10 minutes with periodic status updates in the log viewer. If a stop or clean changes the instance status while the gate runs, the deploy is cancelled and installs nothing. A backend restart during the gate cancels the deploy (the CI build continues); deploy again afterwards.

See [k8s-stack-manager-extensions/hooks/ci-trigger-gate](https://github.com/omattsson/k8s-stack-manager-extensions) for a reference implementation with Azure DevOps support.

### Deploy-gate for expensive clusters

pre-deploy subscriber that calls your FinOps backend: rejects deploys to a specific cluster when predicted monthly cost exceeds budget.

### Security scan gate — block deploys with critical CVEs

A `pre-deploy` subscriber with `failure_policy: fail` that calls a vulnerability scanner (Trivy, Grype, or your registry's scan API) for every chart image before allowing the deploy to proceed. The subscriber extracts image references from `charts` and `values`, queries the scan backend, and returns `{"allowed": false, "message": "CVE-2026-1234 (critical) in web:v1.3.7"}` if any image exceeds the configured severity threshold.

```json
{
  "subscriptions": [
    {
      "name": "security-scan-gate",
      "events": ["pre-deploy"],
      "url": "http://image-scanner.security.svc.cluster.local:8080/scan",
      "timeout_seconds": 15,
      "failure_policy": "fail",
      "secret_env": "SCANNER_WEBHOOK_SECRET"
    }
  ]
}
```

The handler is typically a thin Go or Python service that shells out to `trivy image --severity CRITICAL --exit-code 1 <image>` or queries a registry API. Short timeout (15s) keeps deploys snappy; the scanner should cache results per image digest. Returns `{"allowed": true}` when all images are clean.

### Generate debug bundle — collect diagnostics on demand

An action that gathers pod logs, `kubectl describe`, events, and resource metrics for every release in the instance's namespace, compresses them into a tarball, uploads to object storage, and returns a pre-signed download URL. Useful for support workflows where developers can't access the cluster directly.

```json
{
  "actions": [
    {
      "name": "generate-debug-bundle",
      "url": "http://debug-bundler.ops.svc.cluster.local:8080/bundle",
      "description": "Collect pod logs, events, and resource usage into a downloadable archive",
      "timeout_seconds": 60,
      "secret_env": "DEBUG_BUNDLE_WEBHOOK_SECRET"
    }
  ]
}
```

Invoke:
```bash
curl -X POST https://stack-manager.example/api/v1/stack-instances/$ID/actions/generate-debug-bundle \
     -H "Authorization: Bearer $TOKEN"
```

Response:
```json
{
  "action": "generate-debug-bundle",
  "instance_id": "6c9f1e14-...",
  "status_code": 200,
  "result": {
    "bundle_url": "https://storage.example/debug-bundles/stack-demo-alice-20260419T1430.tar.gz",
    "expires_in": "24h",
    "pod_count": 5,
    "namespace": "stack-demo-alice"
  }
}
```

The handler needs a ServiceAccount with `get`/`list` on pods, events, and logs in the target namespace. Upload destination is configurable (S3, Azure Blob, MinIO).

### Pre-delete backup verification — ensure backups exist before teardown

A `pre-instance-delete` subscriber with `failure_policy: fail` that queries your backup system (Velero, custom snapshot API, or a database backup service) to confirm a recent successful backup exists for the instance. Prevents accidental data loss from deleting a stack whose last backup failed silently.

```json
{
  "subscriptions": [
    {
      "name": "backup-verification",
      "events": ["pre-instance-delete"],
      "url": "http://backup-verifier.ops.svc.cluster.local:8080/verify",
      "timeout_seconds": 10,
      "failure_policy": "fail",
      "secret_env": "BACKUP_VERIFY_SECRET"
    }
  ]
}
```

The handler receives the instance envelope, queries the backup system for the namespace (e.g., `velero backup get --selector namespace=stack-demo-alice`), and returns:

```json
{ "allowed": false, "message": "No successful backup in the last 24h for stack-demo-alice; last backup failed at 2026-04-18T03:00Z" }
```

or `{"allowed": true}` when a recent backup is confirmed. Operators can force-delete by temporarily removing the subscription from the config if needed — this is intentional friction.

---

## Reference implementations (examples/)

| Language | Location | What it demonstrates |
|---|---|---|
| Go | [backend/examples/webhook-handler/](backend/examples/webhook-handler/) | Envelope parsing, HMAC verify, typed response |
| Python | [backend/examples/webhook-handler-python/](backend/examples/webhook-handler-python/) | Stdlib-only minimal handler |

All examples focus on the **protocol** (envelope shape, signature, response). They're starting points — replace the echo/allow logic with your real policy and business rules.

For production-ready extensions with Dockerfiles, k8s manifests, and real-world logic, see the [community extensions repo](https://github.com/omattsson/k8s-stack-manager-extensions).

---

## Writing production-grade handlers

- **Verify signatures first, parse envelope second.** Don't deserialise untrusted JSON before you've confirmed it came from k8s-stack-manager.
- **Respect the timeout.** If your work can exceed the subscription's `timeout_seconds`, return 202 with a `job_id` immediately and run the actual work on a background thread/process. Serve the job log on `log_path` so the web UI and the CLI can show progress (see [Asynchronous actions](#asynchronous-actions-job-id-and-job-log)).
- **Idempotency.** Actions can be retried by the caller. Either make your operation idempotent (common case) or dedup by `request_id`.
- **Log structured fields.** At minimum: `request_id`, `event` or `action`, outcome, duration. Correlate with k8s-stack-manager logs via `request_id`.
- **Healthcheck endpoint.** Expose `GET /healthz` (or similar) without signature verification — lets k8s liveness/readiness probes + operators sanity-check the handler is reachable.
- **Don't hardcode secrets.** Read from env, not from the config file. Pull env vars from a Kubernetes Secret or Vault.

---

## Troubleshooting

### "503 action registry not configured"

The backend booted without `HOOKS_CONFIG_FILE`. Check startup logs for `hooks configured config_file=...`. If empty, set the env var and restart.

### Subscriber returns 401 on every request

HMAC mismatch. Confirm:
- The `secret_env` name in the config file matches an env var on the backend pod.
- The env var value matches the secret the subscriber expects.
- The subscriber is verifying against the **raw body** (not a pretty-printed re-serialisation).
- You're using HMAC-**SHA256** with hex encoding and the `sha256=` prefix.

### Deploy hangs / long delays

A pre-* subscriber is probably slow. Check:
- Subscription timeout settings (`timeout_seconds`)
- Network path to the subscriber (port-forward, DNS, firewall)
- Subscriber-side logs for slow handling

Drop the subscription temporarily by removing it from the config file + restarting if you need to unblock urgently.

### "unexpected action" 400 responses

The action name in the URL path doesn't match any registered subscription. Compare:
```bash
kubectl -n k8s-stack-manager logs deployment/... | grep "hooks configured"
```
with the name your stackctl plugin / curl is using.

### The Actions menu is not shown, or the job log stays empty

- The menu is hidden when `GET /api/v1/stack-instances/:id/actions` returns no actions. Check `HOOKS_CONFIG_FILE` and the `hooks configured` log line.
- The menu items are disabled for users who are not the owner, an admin or a devops user.
- The job log needs `log_path` in the action config and a `job_id` in the subscriber response. A 404 from the subscriber gives 404; the web UI retries a few times, because the log file can appear a moment after the job starts.
- A 502 on the job log route means the subscriber failed (timeout, 5xx, 401 for a bad signature). The backend log has the details with the `request_id` of the response.

### Post-deploy subscriber never fires

Check you registered for `post-deploy` (not `pre-deploy`) and that `failure_policy` is `ignore` (default) — the dispatcher logs failures but won't abort the deploy for post-* events.

---

## Also see

- [backend/docs/hooks.md](backend/docs/hooks.md) — authoritative reference (schema tables, field-by-field)
- [backend/examples/webhook-handler/README.md](backend/examples/webhook-handler/README.md) — Go reference implementation
- [Community extensions](https://github.com/omattsson/k8s-stack-manager-extensions) — production-ready hooks (Slack notifier, maintenance gate, security scan gate, debug bundle) with Dockerfiles and k8s manifests
- [Extending stackctl](https://github.com/omattsson/stackctl/blob/main/EXTENDING.md) — the CLI side: how to add `stackctl my-action` as a plugin
