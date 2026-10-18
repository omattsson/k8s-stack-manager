# Extending k8s-stack-manager

k8s-stack-manager has no built-in logic for the specific operations of your organization. Database refreshes, configuration management database (CMDB) sync, deploy gates, snapshot restores and Slack notifications are not built in. You can add all of them **without a fork**, in any language, as a small out-of-process service.

The extension mechanism has two primitives:

- **Event hooks.** The core fires named events (`pre-deploy`, `post-instance-create`, ...) and sends them with HTTP POST to subscriber URLs. A subscriber with `failure_policy: fail` can abort the operation. Use event hooks to **observe or gate**.
- **Actions.** The core has a generic route `POST /api/v1/stack-instances/:id/actions/:name`. It sends the request to a subscriber that you register. The subscriber does the work, and the core forwards its response to the caller. Use actions for **user-initiated custom operations**.

Both use plain HTTP with HMAC (hash-based message authentication code) signing. A subscriber is any server that the backend can reach over the network: Python, Go, Node, Rust, a shell script, a serverless function or an Argo Workflow. You choose.

---

## Tutorial (10 minutes): your first custom action

In this tutorial you build a `snapshot-pvc` action. It takes a PVC (PersistentVolumeClaim) snapshot for a stack instance. A production version uses VolumeSnapshots. This tutorial uses a stub, so it fits on one page.

### 1. Write the handler

```python
#!/usr/bin/env python3
# snapshot-server.py: minimal action subscriber
import hashlib, hmac, json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SECRET = os.environ["SNAPSHOT_WEBHOOK_SECRET"]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))

        # 1. Verify the HMAC-SHA256 signature
        want = "sha256=" + hmac.new(SECRET.encode(), body, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(self.headers.get("X-StackManager-Signature", ""), want):
            self.send_response(401); self.end_headers(); return

        # 2. Parse the ActionRequest envelope
        req = json.loads(body)
        inst = req["instance"]
        print(f"snapshot-pvc for {inst['namespace']}/{inst['name']}")

        # 3. Do the work (replace this with a real VolumeSnapshot create)
        snapshot_name = f"{inst['name']}-{req['request_id']}"

        # 4. Respond with any JSON. The core forwards it unchanged.
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

Point the backend at the file:

```bash
export HOOKS_CONFIG_FILE=/path/to/actions.json
export SNAPSHOT_WEBHOOK_SECRET=...same value as above...
# restart the backend
```

At startup, the backend writes a log line like this:
```
INFO hooks configured config_file=... actions=[snapshot-pvc]
```

With the Helm chart, set `hooks.enabled=true` and put the subscriptions and actions in `hooks.subscriptions` and `hooks.actions`. The chart creates the file and sets `HOOKS_CONFIG_FILE`.

### 4. Invoke it

```bash
curl -X POST https://stack-manager.example.com/api/v1/stack-instances/$INSTANCE_ID/actions/snapshot-pvc \
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

You added a custom action to your deployment without a change to the source code.

---

## Shapes of extension

### Actions: RPC-style, user-initiated

```
user -> CLI or UI -> k8s-stack-manager -> subscriber
        POST /api/v1/stack-instances/:id/actions/:name
the subscriber responds with any JSON; the core forwards it unchanged to the user
```

Use an action when:
- The user explicitly starts the operation (for example a CLI plugin such as `stackctl-refresh-db`, or the Actions menu in the web UI).
- The caller must see the result.
- The work is bounded. The default timeout is 30 s and the maximum is 600 s. For longer work, answer with a job ID and run the work in the background (see [Asynchronous actions](#asynchronous-actions-job-id-and-job-log)).

Examples: database refresh, snapshot restore, seed data load, force redeploy, cache warm-up.

### Event hooks: fire-and-forget or gate-keeping

```
the core fires event X, then sends the envelope to every subscriber of X:
  if the subscriber returns allowed:false AND failure_policy=fail, the operation aborts
  otherwise the operation continues
```

Use an event hook when:
- You want to observe but not start an operation (audit log, CMDB sync, Slack notification).
- You want to block a policy violation (maintenance window, quota exceeded).
- Several subscribers must react to the same event.

Examples: a Slack message after a deploy, a `pre-instance-delete` check for dependencies, a CMDB record after `post-instance-create`.

### Template functions: in-process helpers

```
YAML values: host: "{{ .Owner | dnsify }}.example.com"
```

Use a template function when:
- You need a small computed value in chart values at render time.
- The behavior is a pure function and belongs in the process.

A template function needs a code change. The default build registers none. Register a function in `buildDomainServices` in [backend/api/bootstrap.go](backend/api/bootstrap.go) with `valuesGen.RegisterFunc("dnsify", fn)`. See [backend/internal/helm/values_generator.go](backend/internal/helm/values_generator.go). The built-in variables are `{{.Branch}}`, `{{.ImageTag}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}` and `{{.Owner}}`.

---

## Events reference

| Event | Fires when | Semantics |
|---|---|---|
| `pre-deploy` | Just before a deployment starts, after the cluster is resolved | Synchronous. `failure_policy: fail` aborts. |
| `post-deploy` | After a deployment completes **successfully** | Fire-and-forget. With `blocking: true`, the deploy waits for the subscriber (see [Blocking post-deploy hooks](#blocking-post-deploy-hooks)). |
| `deploy-finalized` | After a deployment ends, successfully **or not** (after the blocking post-deploy hooks) | Fire-and-forget. |
| `deploy-timeout` | After a deployment failed on a Helm or deploy budget timeout | Fire-and-forget. |
| `stop-completed` | After a stop ends (`instance.status` is `stopped` or `error`) | Fire-and-forget. |
| `clean-completed` | After a clean ends (`draft` or `error`). For the clean of a delete, `metadata.operation` is `delete` and `delete-completed` follows | Fire-and-forget. |
| `delete-completed` | After any delete: API (draft, or after the clean) or cleanup policy | Fire-and-forget. |
| `cleanup-policy-executed` | Once for each cleanup policy run with at least one match (also a dry run) | Fire-and-forget. The run summary is in `cleanup_policy` (see [backend/docs/hooks.md](backend/docs/hooks.md#cleanup-policy-runs)). |
| `pre-rollback` | Before a rollback runs (in the background, after the API answered 202) | Synchronous for the rollback, with progress streaming. `failure_policy: fail` aborts. The instance gets its previous status back. Same `charts` list as `pre-deploy`. |
| `post-rollback` | After a rollback completes **successfully** | Fire-and-forget. |
| `rollback-completed` | After a rollback ends. `metadata.outcome` is `succeeded`, `failed`, `rejected` (by `pre-rollback`) or `cancelled` (another operation started) | Fire-and-forget. |
| `pre-instance-create` | After validation, before the database write | Synchronous. `failure_policy: fail` gives HTTP 403. |
| `post-instance-create` | After the instance is stored | Fire-and-forget. |
| `pre-instance-delete` | Before each delete: single, bulk and cleanup policy | Synchronous. `failure_policy: fail` gives HTTP 403 (single delete) or an `error` result for that instance (bulk, policy). |
| `post-instance-delete` | After each delete completes (single, bulk, after a clean, cleanup policy) | Fire-and-forget. |

Reserved for the future: `pre-namespace-create`, `post-namespace-create`. The config accepts these events, but the core does not fire them yet: `instance-created`, `stack-expiring`, `stack-expired`, `quota-warning`, `secret-expiring`.

Each deploy, stop, clean, rollback and delete envelope has a `trigger`:
- `{"type": "user", "id": "<user id>", "name": "<username>"}` for an API call.
- `{"type": "cleanup-policy", "id": "<policy id>", "name": "<policy name>"}` for a cleanup policy.
- `{"type": "ttl"}` for the TTL (time to live) reaper.

A notifier can use it to show "stopped by cleanup policy nightly-stop".

A `pre-*` subscriber can block. A `post-*` subscriber should use `failure_policy: ignore` (the default), so a slow or unavailable subscriber cannot stall the deploy goroutine.

## Subscription configuration

Event subscriptions and action subscriptions are both in the `HOOKS_CONFIG_FILE` JSON:

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

- **`secret_env`** names an environment variable that holds the HMAC secret. You can commit the file to version control. The secrets stay in the environment (mounted from a Kubernetes Secret, Vault, and so on).
- If you set `secret_env`, the variable must be non-empty. Otherwise the backend refuses to start.
- An empty `secret_env` disables HMAC signing for that subscriber. Do this only for internal communication on a trust boundary, for example localhost.
- `timeout_seconds`: events default to 5 s (maximum 1800 s). Actions default to 30 s (maximum 600 s).
- `failure_policy`: `fail` or `ignore`. The default is `ignore`.
- `name` must be unique, and `url` must be an `http` or `https` URL.
- Actions also accept optional UI fields: `label`, `confirm`, `parameters` and `log_path`. See [Actions in the web UI](#actions-in-the-web-ui).

## Request envelope: EventEnvelope

Every event subscriber receives:

```
POST <url>
Content-Type: application/json
X-StackManager-Event: pre-deploy
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex>       (when a secret is configured)
```

The body has `apiVersion: hooks.k8sstackmanager.io/v1` and `kind: EventEnvelope`:

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
    "cluster_id": "cluster-1",
    "cluster_name": "Production",
    "status": "draft"
  },
  "deployment": { "id": "log-...", "started_at": "..." },
  "charts":   [{"name": "web", "release_name": "web", "version": "1.2.3", "source_repo_url": "https://git.example.com/org/web", "build_pipeline_id": "42", "branch": "main", "image_tag": "main"}],
  "values":   {},
  "metadata": {},
  "extra":    {}
}
```

The core does not fill every field for every event. A handler must check that a field is present before it uses the field.

`instance.cluster_name` (and `cleanup_policy.cluster_name`) is the name of the cluster of `cluster_id`. The core omits it when the cluster is unknown. See [backend/docs/hooks.md](backend/docs/hooks.md).

### Response

```json
{ "allowed": true }
```

To block a `pre-*` event that has `failure_policy: fail`:

```json
{ "allowed": false, "message": "quota exceeded on cluster cluster-1" }
```

A non-2xx response is a failure. An empty 2xx body means `{"allowed": true}`.

### Streaming progress (long-running hooks)

A hook can take a long time, for example when it triggers CI (continuous integration) builds and waits for them. The subscriber can stream progress lines before the final JSON response. The deployment log viewer shows these lines in real time with the LIVE indicator.

**Protocol:** write `LOG:` lines (`LOG: <message>` and a newline) to the response body. Flush after each line. The final line must be the JSON `HookResponse`. The core treats a line without the `LOG:` prefix as the JSON response.

```
HTTP/1.1 200 OK
Content-Type: application/x-ndjson
Transfer-Encoding: chunked

LOG: Checking image app-api:feature-x...
LOG: Image not found, triggering CI build #1234
LOG: Build #1234: queued
LOG: Build #1234: in progress
LOG: Build #1234: completed successfully (3m12s)
LOG: All images ready, proceeding with deployment
{"allowed": true, "message": "all builds ready"}
```

Go example with `http.Flusher`:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    flusher, _ := w.(http.Flusher)
    w.Header().Set("Content-Type", "application/x-ndjson")
    w.WriteHeader(http.StatusOK)

    fmt.Fprintln(w, "LOG: Starting checks...")
    flusher.Flush()

    // ... do the work, write more LOG: lines ...

    fmt.Fprintln(w, `{"allowed": true}`)
    flusher.Flush()
}
```

**Backward compatible:** a subscriber that returns plain JSON (no `LOG:` lines) works as before. Streaming is opt-in for each subscriber. It applies to `pre-deploy`, `pre-rollback` and blocking `post-deploy`.

### Blocking post-deploy hooks

A normal `post-deploy` subscriber is fire-and-forget. The stack is `running` and the user gets "Deployment succeeded" while the subscriber still works. For long post-deploy work (for example a database restore into the new stack), set `blocking: true`:

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
- `LOG:` lines (the streaming protocol above) go to the deploy log and to the live log view.
- `timeout_seconds` (maximum 1800) limits the call. The wait has its own time limit: the sum of the blocking timeouts plus one minute. The Helm deploy budget does not change.
- Only after the subscriber returned: status `running`, "Deployment succeeded", the other `post-deploy` subscribers, `deploy-finalized`.
- A failure, a denial or a timeout has this result:
  - With `failure_policy: fail`, the deploy fails. The status is `error`, and `error_message` has the hook reason (never the URL).
  - With `ignore`, the stack gets `running`, and the owner gets the warning "Post-deploy step db-restore failed".
- Only a stop ends the wait early. The API refuses clean, delete, deploy and rollback while the stack is `stabilizing`. The k8s status watcher does not set `error` during the wait. When a stop ends the wait, the deployer closes the request. This does not stop remote work, so your subscriber must stop its work when the request closes. The stop runs `helm uninstall` at the same time.
- A server shutdown ends the deploy with `error` ("Interrupted by a server restart. Deploy again.").
- After a SIGKILL or a node loss, the leader sets the stack to `error` ("Interrupted: the server that ran this operation stopped. Deploy again.") when both conditions are true: the deploy passed its deadline (its time budget, including the blocking timeouts, plus 5 minutes), and the replica has no heartbeat for 2 minutes. A stop ends the state earlier. No hook event fires for this recovery.

`blocking` is valid only for a subscription that lists `post-deploy`. The full contract is in [backend/docs/hooks.md](backend/docs/hooks.md#blocking-post-deploy-subscribers).

The backend does not follow a redirect from an event subscriber. A 3xx answer is a failure, and `failure_policy` applies. Configure the final URL.

## Request envelope: ActionRequest

API callers (or a CLI) invoke actions with `POST /api/v1/stack-instances/:id/actions/:name`. The core sends the subscriber:

```
POST <url>
Content-Type: application/json
X-StackManager-Event: action:refresh-db
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex>
```

The body has `apiVersion: hooks.k8sstackmanager.io/v1` and `kind: ActionRequest`:

```json
{
  "apiVersion": "hooks.k8sstackmanager.io/v1",
  "kind": "ActionRequest",
  "action": "refresh-db",
  "timestamp": "...",
  "request_id": "...",
  "instance": { /* same shape as EventEnvelope.instance */ },
  "parameters": { /* supplied by the caller, any JSON */ }
}
```

### Response

An action can return **any JSON**. The core wraps it in a response envelope:

```json
{
  "action": "refresh-db",
  "instance_id": "6c9f1e14-...",
  "status_code": 200,
  "result": { ... the JSON body of your subscriber, unchanged ... }
}
```

To refuse a call, answer with a status of 400 or higher and a short reason. Example: `409 {"error": "refresh-db already in flight"}`. The envelope then also has `"message": "refresh-db already in flight"`. The web UI can show it.
- The core takes the message from a JSON string body, or from the field `message`, `error`, `detail` or `reason`.
- The message is one line with at most 500 characters. The core replaces URLs with `[url]`.
- A `text/plain` refusal body also works.

See [backend/docs/hooks.md](backend/docs/hooks.md).

API error mappings when you invoke an action:

| HTTP status | Meaning |
|---|---|
| `200` | The subscriber responded. See `result` and the echoed `status_code`. |
| `400` | Invalid parameters or malformed body. |
| `403` | The caller is not the owner, an admin or a devops user. |
| `404` | Unknown instance, or unknown action name. |
| `502` | The subscriber is unreachable, or there is a transport error, or the body is not JSON. |
| `503` | The action registry is not configured on this server. |

---

## Actions in the web UI

The instance detail page has an **Actions** menu next to Deploy, Stop and Clean. The first group of the menu lists the actions from `GET /api/v1/stack-instances/:id/actions`. The menu also has export values, clone, clean namespace and delete. Every user who can view the instance sees the menu. Only the owner, an admin or a devops user can run an action (`can_invoke`). For other users, the action items are disabled.

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

- `label`: menu text (default: `name`).
- `confirm`: warning text in the run dialog. Use it for destructive actions.
- `parameters`: the UI renders a form. `string` is a text field, `bool` is a switch, and `enum` is a select with `options`. `required` and `default` are optional. The backend checks the declared parameters (type, required, options) and returns 400 on a mismatch. Undeclared parameters pass through. The values reach the subscriber in `parameters`.
- `log_path`: see below.

The list endpoint never returns `url` or `secret_env`. The dialog shows the result: `status_code` and `result`. A non-2xx `status_code` shows as an error.

### Asynchronous actions: job ID and job log

If the work takes longer than `timeout_seconds`, answer at once with a job ID and run the work in the background:

```json
{"job_id": "job-0123456789ab", "status": "started"}
```

Set `log_path` (a path on the host of `url`, with `{job_id}` once). The backend then serves the job log at:

```
GET /api/v1/stack-instances/:id/actions/:name/jobs/:job_id/log?offset=<n>
```

It answers JSON `{status, log, offset, next_offset, done, truncated}`.
- The web UI polls the route every 3 seconds until `done` is true.
- It waits longer on 429, 502, 503, 504 and network errors (`Retry-After`, or else a doubling delay up to 30 seconds).
- It stops on 401, 403 and 404, and after 60 minutes.
- A CLI can use the same route for `--follow`.
- Only users who can run the action can read the job log, because a job log can hold sensitive output.

Your subscriber serves the log on `GET <log_path>?instance_id=<id>&offset=<n>&ts=<unix seconds>`:

- Job IDs MUST be random (for example `"job-" + uuid4().hex`). Never use sequential numbers or timestamps.
- Store the instance ID with each job. You MUST answer 404 when `instance_id` does not match the instance of the job.
- Return the log bytes from `offset` to the end as text (200), or 404 for an unknown job.
- Send `X-Job-Status` (`running`, `succeeded`, `failed`, ...). Optional: `X-Log-Offset` (the next offset; default: `offset` plus the body length; the backend ignores it when it is smaller than `offset`).
- An end marker line such as `===JOB-END=== status=succeeded` is only a fallback for when you cannot send `X-Job-Status`. Job output can print a fake marker.
- Verify `X-StackManager-Signature`. For this GET, it is the HMAC-SHA256 of the request URI (path and query, exactly as you received it). Refuse a request whose `ts` is more than 5 minutes from your clock.
- The backend does not send the query string of the action `url` on log reads. If you authenticate invokes with a query key, authenticate log reads with the signature.

```python
def do_GET(self):
    # Verify the signature over the request URI (path and query).
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

The backend never follows a redirect. It reads at most 256 KiB for each call (the client gets the rest with the next offset) and waits at most 10 seconds. The full contract is in [backend/docs/hooks.md](backend/docs/hooks.md#asynchronous-actions-and-the-job-log).

---

## Security

### HMAC signing

With a `secret_env` configured, the core signs every request:

```
X-StackManager-Signature: sha256=<hex of HMAC-SHA256(secret, raw body)>
```

**Subscribers must verify** the signature before they trust any envelope field. Use a constant-time compare (`hmac.compare_digest` in Python, `hmac.Equal` in Go). Reject a mismatch with 401.

To rotate a secret, register a new subscription next to the old one, move the traffic, and then remove the old entry.

### Replay protection

Every envelope has a unique `request_id` (`req-` plus 24 hex characters). If you need at-least-once delivery with deduplication, keep a small least recently used (LRU) cache of seen IDs (10 minutes is usually enough) and reject replays.

The signature has no separate timestamp header. For events and action invokes, the `timestamp` field is inside the signed body. Reject an envelope that is older than 5 minutes, and reject a `request_id` that you saw before. Otherwise an attacker can replay a captured request. Job log reads carry a signed `ts` query parameter for the same check.

### Network posture

The server makes **outbound** HTTPS (or HTTP) connections to subscriber URLs. A subscriber can run anywhere that the server can reach:
- **In the cluster** (`svc.cluster.local`): the easiest and most common choice.
- **Over a VPN (virtual private network):** this works.
- **Over the public internet** with mutual TLS or a signed payload: this works for SaaS integrations.

### Failure blast radius

`failure_policy: fail` on a `pre-*` event blocks the operation when the subscriber fails. Use it with care. A broken subscriber can halt every deploy. For anything non-critical, use `ignore` and add alerting.

Per-call timeouts limit the worst case. The default is 5 s for events and 30 s for actions. The maximum is 30 minutes for events and 10 minutes for actions.

### Do not trust outbound URLs in a hostile environment

Untrusted users must not be able to create subscriptions. If untrusted actors can reach your k8s-stack-manager instance, keep the action registry config at deployment time only. Do not expose subscription management as an API until you have mitigations for server-side request forgery (SSRF).

---

## Observability

### Metrics (OpenTelemetry)

Every dispatch and every action invocation increments counters and histograms in the `hooks` meter scope. All instruments are always on. If OpenTelemetry is disabled, they are no-ops. Names and labels:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `hook.dispatches_total` | Counter | `hook.event`, `hook.subscription`, `hook.outcome` | One for each event and subscriber attempt |
| `hook.dispatch_duration` | Histogram (seconds) | `hook.event`, `hook.subscription` | Wall-clock time for the POST and the response |
| `hook.action_invocations_total` | Counter | `hook.action`, `hook.outcome` | One for each `/actions/{name}` call |
| `hook.action_invocation_duration` | Histogram (seconds) | `hook.action` | Wall-clock time for the action POST |

`hook.outcome` is a small, stable set, so a dashboard can list it: `success`, `denied`, `http_error`, `transport_error`, `timeout`, `unknown_action`, `marshal_error`.

**Prometheus queries that you probably want:**

```promql
# dispatch error rate for each subscription (last 5 minutes)
sum by (hook_subscription, hook_outcome) (
  rate(hook_dispatches_total{hook_outcome!="success"}[5m])
)

# p95 dispatch latency for each event
histogram_quantile(0.95, sum by (le, hook_event) (rate(hook_dispatch_duration_bucket[5m])))

# actions that return non-2xx again and again
sum by (hook_action) (increase(hook_action_invocations_total{hook_outcome="http_error"}[15m])) > 0
```

### Traces (OpenTelemetry)

Every dispatch opens a span: `hooks.dispatch` for event subscriber calls, `hooks.action` for action invocations, and `hooks.action_log` for job log reads. Each span has these attributes:

- `hook.event` or `hook.action`: the event or action that fires.
- `hook.subscription`: the subscriber name (for events).
- `hook.request_id`: the same value as `X-StackManager-Request-Id`. Use it to correlate logs, traces and subscriber-side records.
- `hook.outcome`: the final label. It matches the metric outcome.
- `hook.status_code`: the HTTP status, when the core got a response.
- `span.status`: `Ok` on success, `Error` on any other outcome.

**Trace context propagation:** k8s-stack-manager installs the W3C TraceContext propagator globally. Every outbound hook or action request has a `Traceparent` header. A subscriber that runs its own OpenTelemetry SDK adds its spans as children. The Python reference [backend/examples/webhook-handler-python/](backend/examples/webhook-handler-python/) and the Go reference [backend/examples/webhook-handler/](backend/examples/webhook-handler/) show the protocol and signature check. They are minimal and do not include tracing code.

In Jaeger or Tempo, you see a trace like this:

```
| stack-manager: deployer.deploy        (root)
|  +- hooks.dispatch                    pre-deploy to cmdb-sync        outcome=success
|  +- hooks.dispatch                    pre-deploy to maintenance-gate outcome=denied
|  +- cmdb-sync-server: POST /events    (from the OpenTelemetry SDK of the subscriber)
```

### Structured logs

A dispatch failure writes a log line for quick triage:

```
level=warn subscription=cmdb-sync event=post-instance-create request_id=req-abc status=transport_error error="connection refused"
```

Use `request_id` to correlate across systems. It is in the metric and span attributes above, and in the `X-StackManager-Request-Id` header of every outbound request.

---

## Real-world examples

### Database refresh

A "refresh-db" operation is the typical action use case. It wipes the per-instance MySQL PVC, flushes Redis and restarts the application pods. It needs cluster credentials. It runs a multi-step `kubectl` sequence. It is logically an RPC on the instance. A reference pattern:

- A Python webhook server (standard library only: `http.server.ThreadingHTTPServer`, and `subprocess` for `kubectl`).
- `kubectl` orchestration for the restore sequence: scale down, truncate, flush, extract the golden database again, scale up.
- A per-job progress log on disk for real-time tailing. The server serves it on `log_path` (for example `/jobs/{job_id}/log`), so the web UI shows the progress.
- HMAC signature verification on every request.
- Deployment as a Kubernetes Deployment with a ClusterRole and a Service.

The time from the webhook request to the end of the `kubectl` orchestration depends on the database size. It is typically several minutes.

### Policy gate: block deploys outside business hours

A small `pre-deploy` subscriber returns `{"allowed": false}` between 17:00 and 09:00 on weekdays. It needs about 20 lines of Python and no state. Deploy it as a Kubernetes Deployment. Register it with `failure_policy: fail`.

### CMDB sync: mirror every stack instance

`post-instance-create` and `post-instance-delete` subscribers update an external asset inventory. Use `failure_policy: ignore`, so a CMDB outage cannot block instance management.

### Slack notification: surface failures to an ops channel

A `deploy-finalized` subscriber posts to Slack when the deploy failed. It returns at once on the success path. Register it with a short timeout, so a short Slack outage does not affect dispatch. (`post-deploy` fires only after a successful deploy.)

### CI trigger gate: build images before deploy

A `pre-deploy` subscriber has `failure_policy: fail` and a long timeout (up to 1800 s). It checks whether container images exist for the branch of the deploy. When images are missing, it triggers CI builds (Azure DevOps, GitHub Actions, GitLab CI, and so on) and polls until they complete.

The core sends `charts` with `source_repo_url`, `build_pipeline_id`, `branch` and `image_tag` (the `{{.ImageTag}}` value of that chart) in the `pre-deploy` envelope. It also sends `metadata.registry_url` for registry lookups. The subscriber uses the streaming progress protocol to show the build status in the deployment log.

`pre-deploy` hooks do not run for a rollback. To gate rollbacks too, add `pre-rollback` to `events`.
- The envelope has the same `charts` list, plus `metadata.rollback_mode` (`previous_revision` or `target`), `target_log_id`, `target_branch` and `branch_source`.
- A rollback to a target restores the stored values of that deploy, not the images. An image tag that is a branch name can point to a newer image.
- `pre-rollback` runs in the background like `pre-deploy`. It supports the same streaming progress protocol, so the `LOG:` lines of the gate appear in the rollback log.
- For a one-revision rollback, the chart branches come from the previous successful deploy log when it recorded a branch (`metadata.branch_source`).

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

Skip logic keeps the gate fast:
- It skips charts without `build_pipeline_id` (public or pre-built images).
- It skips semver tags (release images).
- An in-memory cache avoids a second check of images that it verified recently.

A typical deploy with pre-built images adds less than 1 s. A deploy that triggers builds blocks for 3 to 10 minutes, with periodic status updates in the log viewer. If a stop or clean changes the instance status while the gate runs, the core cancels the deploy and installs nothing. A backend restart during the gate cancels the deploy (the CI build continues). Deploy again afterwards.

For a reference implementation with Azure DevOps support, see [k8s-stack-manager-extensions/hooks/ci-trigger-gate](https://github.com/omattsson/k8s-stack-manager-extensions).

### Deploy gate for expensive clusters

A `pre-deploy` subscriber calls your cost-management backend. It rejects deploys to a specific cluster when the predicted monthly cost exceeds the budget.

### Security scan gate: block deploys with critical vulnerabilities

A `pre-deploy` subscriber with `failure_policy: fail` calls a vulnerability scanner (Trivy, Grype or the scan API of your registry) for every chart image before it allows the deploy. The subscriber extracts image references from `charts` and `values` and queries the scan backend. It returns `{"allowed": false, "message": "CVE-2026-1234 (critical) in web:v1.3.7"}` if any image exceeds the severity threshold. CVE means Common Vulnerabilities and Exposures.

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

The handler is usually a thin Go or Python service. It runs `trivy image --severity CRITICAL --exit-code 1 <image>` or queries a registry API. A short timeout (15 s) keeps deploys fast. The scanner should cache results for each image digest. The handler returns `{"allowed": true}` when all images are clean.

### Generate a debug bundle: collect diagnostics on demand

An action gathers pod logs, `kubectl describe` output, events and resource metrics for every release in the namespace of the instance. It compresses them into a tarball, uploads the tarball to object storage and returns a pre-signed download URL. Use it for support workflows where developers cannot access the cluster directly.

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
curl -X POST https://stack-manager.example.com/api/v1/stack-instances/$ID/actions/generate-debug-bundle \
     -H "Authorization: Bearer $TOKEN"
```

Response:
```json
{
  "action": "generate-debug-bundle",
  "instance_id": "6c9f1e14-...",
  "status_code": 200,
  "result": {
    "bundle_url": "https://storage.example.com/debug-bundles/stack-demo-alice-20260419T1430.tar.gz",
    "expires_in": "24h",
    "pod_count": 5,
    "namespace": "stack-demo-alice"
  }
}
```

The handler needs a ServiceAccount with `get` and `list` rights on pods, events and logs in the target namespace. The upload destination is configurable (S3, Azure Blob, MinIO).

### Pre-delete backup verification: confirm a backup exists before teardown

A `pre-instance-delete` subscriber with `failure_policy: fail` queries your backup system (Velero, a custom snapshot API or a database backup service). It confirms that a recent successful backup exists for the instance. This prevents data loss from the delete of a stack whose last backup failed without notice.

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

The handler receives the instance envelope. It queries the backup system for the namespace (for example `velero backup get --selector namespace=stack-demo-alice`) and returns:

```json
{ "allowed": false, "message": "No successful backup in the last 24h for stack-demo-alice; last backup failed at 2026-04-18T03:00Z" }
```

When it confirms a recent backup, it returns `{"allowed": true}`. To force a delete, an operator can remove the subscription from the config for a short time. This friction is intentional.

---

## Reference implementations (examples/)

| Language | Location | What it shows |
|---|---|---|
| Go | [backend/examples/webhook-handler/](backend/examples/webhook-handler/) | Envelope parsing, HMAC verification, typed response |
| Python | [backend/examples/webhook-handler-python/](backend/examples/webhook-handler-python/) | Minimal handler with the standard library only |

All examples show the **protocol** (envelope shape, signature, response). They are starting points. Replace the echo and allow logic with your policy and business rules.

For production-ready extensions with Dockerfiles, Kubernetes manifests and real-world logic, see the [community extensions repository](https://github.com/omattsson/k8s-stack-manager-extensions).

---

## Write production-grade handlers

- **Verify signatures first, parse the envelope second.** Do not deserialize untrusted JSON before you confirm that it came from k8s-stack-manager.
- **Respect the timeout.** If your work can exceed the `timeout_seconds` of the subscription, return 202 with a `job_id` at once and run the work on a background thread or process. Serve the job log on `log_path`, so the web UI and the CLI can show progress (see [Asynchronous actions](#asynchronous-actions-job-id-and-job-log)).
- **Idempotency.** The caller can retry an action. Make your operation idempotent (the common case), or deduplicate by `request_id`.
- **Log structured fields.** At minimum: `request_id`, `event` or `action`, outcome, duration. Correlate with the k8s-stack-manager logs through `request_id`.
- **Health check endpoint.** Expose `GET /healthz` (or similar) without signature verification. Kubernetes probes and operators can then confirm that the handler is reachable.
- **Do not hard-code secrets.** Read them from the environment, not from the config file. Fill the environment from a Kubernetes Secret or Vault.

---

## Troubleshooting

### "503 action registry not configured"

The backend started without `HOOKS_CONFIG_FILE`. Check the startup logs for `hooks configured config_file=...`. If the value is empty, set the variable and restart.

### The subscriber returns 401 on every request

The HMAC does not match. Check these points:
- The `secret_env` name in the config file matches an environment variable of the backend pod.
- The value of the variable matches the secret that the subscriber expects.
- The subscriber verifies against the **raw body**, not a pretty-printed re-serialization.
- You use HMAC-**SHA256** with hex encoding and the `sha256=` prefix.

### Deploys hang or have long delays

A `pre-*` subscriber is probably slow. Check these points:
- The timeout settings of the subscription (`timeout_seconds`).
- The network path to the subscriber (port-forward, DNS, firewall).
- The subscriber-side logs for slow handling.

To unblock urgently, remove the subscription from the config file and restart the backend.

### "unknown action" (404)

The action name in the URL path does not match a registered subscription. Compare the registered names with the name that your CLI plugin or `curl` uses:
```bash
kubectl -n k8s-stack-manager logs deployment/... | grep "hooks configured"
```

### The Actions menu has no custom actions, or the job log stays empty

- The custom action items come from `GET /api/v1/stack-instances/:id/actions`. If it returns no actions, check `HOOKS_CONFIG_FILE` and the `hooks configured` log line.
- The action items are disabled for users who are not the owner, an admin or a devops user.
- The job log needs `log_path` in the action config and a `job_id` in the subscriber response. When the subscriber answers 400, 404 or 410 for the job, the route gives 404, and the web UI stops with a message. Make sure that the log is available as soon as you return the `job_id`.
- A 502 on the job log route means that the subscriber failed (timeout, 5xx, or 401 for a bad signature). The backend log has the details with the `request_id` of the response.

### A post-deploy subscriber never fires

Check that you registered the subscriber for `post-deploy` (not `pre-deploy`). Check that `failure_policy` is `ignore` (the default). The dispatcher logs failures but does not abort the deploy for `post-*` events.

---

## See also

- [backend/docs/hooks.md](backend/docs/hooks.md): the authoritative reference (schema tables, field by field).
- [backend/examples/webhook-handler/README.md](backend/examples/webhook-handler/README.md): the Go reference implementation.
- [Community extensions](https://github.com/omattsson/k8s-stack-manager-extensions): production-ready hooks (Slack notifier, maintenance gate, security scan gate, debug bundle) with Dockerfiles and Kubernetes manifests.
- [Extending stackctl](https://github.com/omattsson/stackctl/blob/main/EXTENDING.md): the CLI side. It shows how to add `stackctl my-action` as a plugin.
