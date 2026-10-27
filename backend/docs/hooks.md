# Extensibility reference: Webhooks and Actions

> **Note:** To get started, read [EXTENDING.md](../../EXTENDING.md) in the
> repository root. It has a 10-minute tutorial, real-world recipes and
> troubleshooting. This file is the authoritative reference for schemas and
> fields. Use it when you implement a subscriber and need exact values.

k8s-stack-manager has two integration points for third-party behavior:

1. **Lifecycle events (webhooks).** The core sends a notification at defined
   points of the deploy and instance lifecycle. A subscriber can only observe,
   or it can abort the operation. Use events for audit logging, configuration
   management database (CMDB) sync and deploy policy enforcement.

2. **Actions.** A named, RPC-style handler that an API caller invokes for one
   instance. Use actions for user-initiated operations that the core does not
   implement (database refresh, snapshot restore, seed-data loading).

Neither mechanism needs a recompile or a fork. Both use plain HTTP, so you can
write a handler in any language.

The example [../examples/webhook-handler](../examples/webhook-handler/) is a
minimal Go starting point.

---

## Events

### Available events

| Event | Fires when | Semantics |
|---|---|---|
| `pre-deploy` | Just before a deployment starts, after the cluster is resolved. | Synchronous, with progress streaming (`LOG:` lines go to the deploy log). A subscriber with `failure_policy=fail` can abort. The deploy log then ends with status `error` and the hook reason. The instance gets status `error` with the reason in `error_message`. A stop during the call closes the request (see [Another operation during pre-deploy or pre-rollback](#another-operation-during-pre-deploy-or-pre-rollback)). |
| `post-deploy` | After a deployment completes **successfully** (also a partial deploy). | Fire-and-forget (default `failure_policy=ignore`). A subscription with `blocking: true` is called earlier, and the deploy waits for it. See [Blocking post-deploy subscribers](#blocking-post-deploy-subscribers). |
| `deploy-finalized` | After a deployment ends, **successfully or not**, and after the blocking post-deploy subscribers. | Fire-and-forget. `metadata.post_deploy_failed` lists the blocking subscribers with `failure_policy=ignore` that failed. |
| `deploy-timeout` | After a deployment failed because Helm or the deploy budget timed out. A failed blocking post-deploy subscriber does not fire it. | Fire-and-forget. |
| `stop-completed` | After a stop ends, successfully (`instance.status` is `stopped`) or not (`error`). | Fire-and-forget. |
| `clean-completed` | After a clean ends, successfully (`draft`) or not (`error`). It also fires for the clean of an API delete. Then `metadata.operation` is `delete` and `delete-completed` follows. A notifier can skip this event and post only the delete. | Fire-and-forget. |
| `delete-completed` | After an instance is deleted: by the API (at once for a draft, or after its clean) or by a cleanup policy. | Fire-and-forget. |
| `cleanup-policy-executed` | Once for each cleanup policy run (scheduled or manual) with at least one matching instance. It also fires for a dry run. It does not fire for a scheduled run that the end of a leadership term interrupted. | Fire-and-forget. See [Cleanup policy runs](#cleanup-policy-runs). |
| `pre-rollback` | Before a rollback runs (`POST /stack-instances/:id/rollback`). It runs in the background after the API answered 202. | Synchronous for the rollback, with progress streaming (`LOG:` lines go to the rollback log). `failure_policy=fail` aborts. The rollback log then ends with status `error` and the hook reason. The instance gets its previous status back. A stop during the call closes the request (see [Another operation during pre-deploy or pre-rollback](#another-operation-during-pre-deploy-or-pre-rollback)). |
| `post-rollback` | After a rollback completes **successfully**. | Fire-and-forget. |
| `rollback-completed` | After a rollback ends: succeeded, failed, rejected by `pre-rollback`, or cancelled because another operation (stop, clean, deploy) started meanwhile. `metadata.outcome` is `succeeded`, `failed`, `rejected` or `cancelled`. | Fire-and-forget. |
| `pre-instance-create` | After validation and before the instance is written to the database. | Synchronous. `failure_policy=fail` aborts the create (HTTP 403). |
| `post-instance-create` | After the instance is stored. | Fire-and-forget. |
| `pre-instance-delete` | Before a delete: a single API delete (after the instance ID is validated), each instance of a bulk delete, each instance of a cleanup policy delete. | Synchronous. `failure_policy=fail` aborts: HTTP 403 for the single delete, an `error` result for that instance in a bulk delete or a policy run. |
| `post-instance-delete` | After the instance is deleted: single or bulk API delete, API delete after its clean, cleanup policy delete. | Fire-and-forget. |
| `pre-namespace-create` | *Reserved. The core does not fire it yet.* | None. |
| `post-namespace-create` | *Reserved. The core does not fire it yet.* | None. |

The config also accepts `instance-created`, `stack-expiring`, `stack-expired`,
`quota-warning` and `secret-expiring`. The core does not fire them yet.

A `pre-*` event lets a subscriber block the operation. The `post-*` events and
`deploy-finalized` only notify. Use `failure_policy=ignore` for them, so a slow
or unavailable subscriber cannot stall the deploy goroutine.

### Subscription shape

The file loader (`HOOKS_CONFIG_FILE`) reads JSON. The top-level object has
`subscriptions` (event subscribers) and `actions` (RPC handlers). The loader
reads secrets from the environment variables that `secret_env` names. You can
therefore commit the file to version control.

```json
{
  "subscriptions": [
    {
      "name": "cmdb-sync",
      "events": ["post-instance-create", "post-instance-delete"],
      "url": "https://cmdb.example.com/hooks/stackmgr",
      "timeout_seconds": 5,
      "failure_policy": "ignore",
      "secret_env": "CMDB_HOOK_SECRET"
    }
  ]
}
```

- `name`: required. It must be unique.
- `events`: required. At least one known event.
- `url`: required. It must be an `http` or `https` URL with a host.
- `timeout_seconds`: optional. Default 5, maximum 3600 (60 minutes, for gates that wait for CI builds and free CI agents).
- `blocking`: optional. Default `false`. Only for `post-deploy`: the subscription must list `post-deploy`, or the startup fails. See [Blocking post-deploy subscribers](#blocking-post-deploy-subscribers).
- `failure_policy`: optional. Default `ignore`. Set `fail` to block on an error.
- `secret_env`: optional. It names an environment variable that holds the HMAC (hash-based message authentication code) secret. If you set it, the variable must be non-empty in the process environment. Otherwise the startup fails (fail closed).
- `applies_to`: optional. Calls the subscriber only for some instances. See [Limit to some instances](#limit-to-some-instances-applies_to). Not allowed with `cleanup-policy-executed` (one event for many instances): the startup fails.

### Limit to some instances (`applies_to`)

A subscription or an action can apply to some instances only, for example
the stacks of one team:

```json
{
  "actions": [
    {"name": "refresh-db-postgres", "url": "https://handlers.example.com/postgres", "label": "Refresh database",
     "applies_to": {"template_categories": ["team-a"]}},
    {"name": "refresh-db-mysql", "url": "https://handlers.example.com/mysql", "label": "Refresh database",
     "applies_to": {"template_categories": ["team-b"], "cluster_ids": ["cluster-dev"]}}
  ],
  "subscriptions": [
    {"name": "team-a-schema-gate", "events": ["pre-deploy"], "url": "https://handlers.example.com/gate",
     "failure_policy": "fail", "applies_to": {"template_ids": ["6c9f1e14-..."]}}
  ]
}
```

| Field | Matches |
|---|---|
| `template_ids` | The template of the instance's definition (`source_template_id`): instantiate, quick deploy, import or `PUT /stack-definitions/:id/template-link` set it. |
| `template_categories` | The `category` of that template. Trimmed, case-insensitive, exact (no globs). |
| `definition_ids` | The `stack_definition_id` of the instance. |
| `cluster_ids` | The `cluster_id` of the instance. |

Rules:
- The fields combine with AND, the values inside one field with OR.
- A missing or empty `applies_to` applies to every instance, as before.
- An instance whose definition has no template, or whose template was deleted, never matches `template_ids` or `template_categories`.
- The current template decides, not a published version. The server keeps the template of a definition in a cache for at most one minute: a changed category or template link applies after that time.
- At most 50 values per field. A value must not be empty; an ID has at most 36 characters, a category or a cluster ID at most 255. An unknown field in `applies_to` (for example a typo), or a misspelt `applies_to` key itself (for example `appliesTo` or `applies-to`), stops the startup, so a scope can never become empty by mistake.
- `cluster_ids` must not contain `all` (the cleanup policy value): no instance has it. Omit `cluster_ids` to match every cluster. An instance without a `cluster_id` matches no `cluster_ids`.
- The server does not check that the IDs exist.

When the scope needs the template and the template cannot be read (the
lookup fails, or takes longer than 2 seconds), the server does not guess:

| Path | Result |
|---|---|
| `GET .../actions` | The action is left out, and the response has `"scope_incomplete": true`. Actions without a template scope are listed. |
| `POST .../actions/:name` | 503 "Could not check where the action applies. Try again." The subscriber is not called, and no run is stored. |
| Event, `failure_policy: fail` | The operation fails with `<event> hook "<name>" failed (could not check where it applies; try again)`. A gate is never skipped silently. As for any failed `fail` subscriber, the later subscribers of the event are not called. `POST /stack-instances` and `DELETE /stack-instances/:id` answer 503 "Could not check where the pre-instance-create (or pre-instance-delete) hook applies. Try again." instead of the 403 of a denial. |
| Event, `failure_policy: ignore` | The subscriber is skipped, with a warning in the log. |

An action outside its scope:
- `GET .../actions` does not list it.
- `POST .../actions/:name` answers 404 `unknown action "<name>"`, the same answer as for an action that does not exist.
- The job log (`GET .../actions/:name/jobs/:job_id/log`) does not check the scope, so a run that started before a config change keeps its log. The modify rule and the instance check of the subscriber still apply.
- The [action run history](#action-run-history) is not filtered.

A blocking `post-deploy` subscriber counts only for the instances it
applies to: an instance without a matching blocking subscriber never becomes
`stabilizing`, also during a server shutdown. `post_deploy_hook_until` uses
the timeouts of the matching subscribers, and of the ones whose scope could
not be checked (the phase then fails or skips them as above). The deploy
deadline still counts all blocking subscribers (an upper bound).

**Server version.** A k8s-stack-manager version without `applies_to`
ignores the field: the subscription or the action then applies to every
instance. Upgrade the server before you add `applies_to` to a shared
config, and check the `hooks configured` startup log line.

### Request envelope

Every subscriber receives `POST <url>` with these headers:

```
Content-Type: application/json
Accept: application/json
X-StackManager-Event: <event-name>
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex>    (only when a secret is set)
Traceparent: ...
```

The `Accept` header is `application/x-ndjson, application/json` for calls with
progress streaming.

The body has `apiVersion: hooks.k8sstackmanager.io/v1`:

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
    "status": "draft",
    "template_id": "tpl-...",
    "template_name": "Team A web stack",
    "template_category": "team-a"
  },
  "deployment": {
    "id": "log-...",
    "started_at": "2026-04-18T10:15:32.820Z"
  },
  "charts":   [{"name": "web", "release_name": "web", "version": "1.2.3",
                "source_repo_url": "https://git.example.com/org/web",
                "build_pipeline_id": "42", "branch": "feature/Login_Fix",
                "image_tag": "feature-login-fix"}],
  "values":   {},
  "metadata": {},
  "extra":    {}
}
```

The core fills `deployment`, `charts` and `values` only when the event needs
them. A handler must not assume that every field is present.

`instance.cluster_name` is the name of the cluster of `instance.cluster_id`.
A subscriber (for example a chat notifier) can show it without an API call.
- The server reads it with the same lookup as the API field `cluster_name`.
- The server keeps it in a cache for at most one minute. A renamed cluster can show the old name for that time.
- The server waits at most 2 seconds for the lookup, so a slow database does not delay a gate such as `pre-deploy`.
- The field is omitted when `cluster_id` is empty, when the cluster no longer exists, or when the lookup fails or times out. The server sends the event in all cases.
- Action requests (`instance` in the request of `POST .../actions/:name`) have the same field.

`instance.template_id`, `instance.template_name` and `instance.template_category`
describe the template of the instance's definition (`source_template_id`), so
one subscriber can route by team without an API call.
- The category is sent as stored (not in lower case).
- The server keeps the template of a definition in a cache for at most one minute, and waits at most 2 seconds for the lookup. The template and the cluster name lookups run at the same time.
- The fields are omitted when the definition has no template, when the template no longer exists, or when the lookup fails or times out.
- Action requests have the same fields.

`trigger` tells what started the operation of the event:

```json
"trigger": {"type": "cleanup-policy", "id": "4f0c...", "name": "nightly-stop"}
```

| `type` | Meaning | `id` and `name` |
|---|---|---|
| `user` | An API call (also bulk operations and quick deploy). | User ID and username. Empty when the user is not known. |
| `cleanup-policy` | A cleanup policy run (scheduled or manual). | Policy ID and name. |
| `ttl` | The TTL (time to live) reaper stopped an expired instance. | Not set. |

These events carry `trigger`: the deploy, stop, clean and rollback events
(`pre-deploy` to `deploy-timeout`, `stop-completed`, `clean-completed`,
`pre-rollback` to `rollback-completed`), `delete-completed`, the
`*-instance-create` and `*-instance-delete` events, and
`cleanup-policy-executed`. A subscriber can show, for example, "stopped by
cleanup policy nightly-stop". Older envelopes have no `trigger`. Treat a
missing `trigger` as unknown.

`charts[].branch` is the effective branch of the chart: a per-chart override,
or else the instance branch. `charts[].image_tag` is the Docker-safe tag of
that branch. It is the same value as the `{{.ImageTag}}` template variable in
the chart values. A CI (continuous integration) gate checks and builds this
tag, so it matches what Helm deploys.

`metadata.registry_url` is the container registry URL of the cluster. The
`pre-deploy` and `pre-rollback` events carry it when the cluster has registry
credentials. A gate can use it for registry lookups.

`deploy-finalized` has `metadata.readiness` = `timeout` and
`metadata.readiness_warning` when the pods did not become ready in time but the
deploy did not fail.

`pre-rollback` carries the same `charts` list as `pre-deploy`. Subscribe a CI
image gate to `pre-rollback` too, because `pre-deploy` hooks do not run for a
rollback. The hook runs in the background like `pre-deploy`. It supports the
same streaming progress protocol. `metadata.rollback_mode` is
`previous_revision` or `target` (the API body had `target_log_id`).
`metadata.branch_source` tells where the chart branches come from: `current`,
`previous_deploy` or `target_deploy`.

For `previous_revision`, each release goes back one Helm revision. `charts`
lists all charts. Their branch and `image_tag` come from the deploy or
rollback log of the revision that `helm rollback` goes to
(`metadata.branch_source=previous_deploy`). The branch is the same for every
chart, because the core does not record per-chart branch overrides.
- If the newest log failed (a failed deploy still created a Helm revision), the core uses the newest successful log.
- If the newest log succeeded, the core uses the second newest successful log.

When that log has no branch (older logs) or does not exist, the branches are
the current ones (`metadata.branch_source=current`). This is a best guess for
each instance, not for each release. The rollback log stores the same branch.

A rejection writes a safe reason to the deployment log and to the
notification:
- A denial (`allowed: false`): `pre-rollback hook "<name>" denied the rollback: <subscriber message>`.
- Any other failure: `pre-rollback hook "<name>" failed (unreachable or timed out)`.

The full error can contain the subscriber URL. It goes only to the server log.
A `pre-deploy` denial shows the subscriber message in the same way:
`pre-deploy hook "<name>" denied the deployment: <subscriber message>`. For
`pre-deploy`, the reason goes to the `error_message` of the instance and of the
deploy log. It also goes as an `ERROR:` line to the deploy log output, after
the `LOG:` progress lines of the hook. The core flattens the subscriber message
to one line and cuts it at 500 characters. It cuts the instance
`error_message` at 256 characters.

The rollback continues after the hook only when no other operation started
meanwhile (its deployment log is still the newest one of the instance).
Otherwise the rollback log ends as cancelled and the core does not touch the
instance. The same check guards `pre-deploy`. A stop during the hook call
also closes the request to the subscriber. See
[Another operation during pre-deploy or pre-rollback](#another-operation-during-pre-deploy-or-pre-rollback).

For `target`:
- `charts` lists only the charts of the target deploy. Each chart has the version that the deploy recorded and the branch of the target deploy (`metadata.target_branch`). The core does not record per-chart branch overrides of that deploy. `metadata.branch_source` is `target_deploy`.
- `metadata.target_log_id` is the target deploy log.
- The rollback restores the stored **values** of that deploy (including the shared and locked values of that time), not the images. An image tag that is a branch name can now point to a newer image. A gate that must guarantee the old image has to check it itself.

### Another operation during pre-deploy or pre-rollback

A `pre-deploy` or `pre-rollback` subscriber (for example a CI gate) can run
for many minutes. A stop can take the instance meanwhile (API, bulk, TTL
reaper or a cleanup policy). A clean and a new deploy answer 409 while the
instance is deploying.

1. While the subscriber runs, the deployer checks the instance at every
   stabilize poll interval (`DEPLOY_STABILIZE_POLL_INTERVAL`, default 5 s).
2. When the status is no longer `deploying`, or a newer deployment log
   exists, the deployer closes the request to the subscriber. It does not
   wait for the `timeout_seconds` of the subscription.
3. The deploy or rollback log ends with status `error` and a
   `WARNING: deploy cancelled: another operation changed the instance ...`
   (or `rollback cancelled ...`) line.
4. Helm does not run. The core does not change the instance, so the status of
   the other operation stays.
5. This is a cancel, not a denial. The `failure_policy` does not apply.
   `rollback-completed` has `metadata.outcome` = `cancelled`.

A read error of the instance does not close the request. The core checks the
instance again after the subscriber answered. A change just before the
answer therefore gives the same result.

A server shutdown also closes the request. This is a hook failure, not a
cancel. With `failure_policy=fail`, the operation ends with
`<event> hook "<name>" failed (unreachable or timed out)`.

Closing the HTTP request does not stop the work of the subscriber. A
subscriber must stop its wait when the request closes (the client
disconnects). A CI gate can keep or cancel the builds that it started.

### Blocking post-deploy subscribers

A `post-deploy` subscriber can do long work after the Helm releases are ready.
Examples: restore a database snapshot, warm caches. With `"blocking": true`,
the deploy waits for the subscriber:

```json
{
  "name": "db-restore",
  "events": ["post-deploy"],
  "url": "http://db-restore.extensions:8080/hook",
  "blocking": true,
  "timeout_seconds": 900,
  "failure_policy": "fail"
}
```

1. After the Helm releases are ready (and after the readiness wait), the
   deployer calls the blocking subscribers that apply to the instance
   (`applies_to`) one after the other, in registration order. The instance
   has the status `stabilizing`. When none applies, the phase does not
   start.
2. The subscriber can stream `LOG: <message>` lines (the same protocol as
   `pre-deploy`). They go to the deploy log and to the WebSocket log stream.
   The deploy log keeps the last 16 KiB of these lines.
3. Each call is limited by its `timeout_seconds` (maximum 3600). The blocking
   phase has its own time limit: the sum of the timeouts of the blocking
   subscribers that apply to the instance plus one minute. It does not
   change the Helm deploy budget.
4. After all blocking subscribers returned, the instance gets `running`, the
   owner gets "Deployment succeeded", the core calls the non-blocking
   `post-deploy` subscribers, and `deploy-finalized` fires.
5. A failure, a denial (`allowed: false`) or a timeout has this result:
   - `failure_policy=fail`: the deploy fails. The instance gets `error`. The `error_message` of the instance and of the deploy log is `post-deploy hook "<name>" denied the deployment: <message>` or `post-deploy hook "<name>" failed (unreachable or timed out)`. For a 3xx answer it is `failed (the subscriber answered with a redirect)`. The deploy log output has the same `ERROR:` line. The core does not call later blocking subscribers or the non-blocking `post-deploy` subscribers. `deploy-finalized` fires. A timeout of the subscriber does not fire `deploy-timeout`.
   - `failure_policy=ignore`: the deploy succeeds (`running`). The deploy log gets a `WARNING:` line. The owner gets the notification "Post-deploy step <name> failed" (type `deployment.warning`). `deploy-finalized` has `metadata.post_deploy_failed` with the subscriber names.
6. These can end the wait early:
   - **Stop** (API, bulk, TTL reaper or a cleanup policy). The API refuses clean, delete, deploy and rollback (409) while the instance is `stabilizing`. The deployer checks the instance at every stabilize poll interval (`DEPLOY_STABILIZE_POLL_INTERVAL`, default 5 s). When the status is no longer `stabilizing` (or `deploying`), or a newer deployment log exists, it closes the request to the subscriber. The deploy log ends with status `error` and a `WARNING: deploy cancelled ...` line. The status of the stop stays. A read error of the instance does not end the wait.
   - **The k8s status watcher** does not end the wait for a pod error. While the hooks run, the instance has `post_deploy_hook_until` (a database column, so the watcher on the leader replica sees it, also when another replica runs the deploy). The watcher keeps the status. The same time is the deadline of the hook calls. After that time (for example after a crash, see below), the watcher sets `error` for a namespace that is still in error. The marker has one minute of margin after the sum of the hook timeouts. A clock difference of less than about one minute between the replicas is therefore safe. A stop, clean or rollback clears the marker.
   - **A server shutdown** (SIGTERM, rolling update). The deploy ends with status `error` and the message `Interrupted by a server restart. Deploy again.`, for every `failure_policy`. The result of the subscriber is unknown, so the core does not set the stack to `running`.

   Closing the HTTP request does not stop the work of the subscriber. A
   subscriber must stop its work when the request closes (the client
   disconnects). A stop starts `helm uninstall` at the same time. A subscriber
   that continues writes to a namespace that the core is removing.

   After a SIGKILL, an out-of-memory kill or the loss of the node, the leader
   ends the deploy (see
   [Interrupted operations](../../ARCHITECTURE.md#interrupted-operations)).
   The leader acts when both conditions are true:
   - The deploy passed the deadline of its log: the time budget of the deploy (which includes the sum of the blocking hook timeouts plus one minute), plus 5 minutes.
   - The replica that ran the deploy has no heartbeat for 2 minutes.

   The instance then gets `error` with the message
   `Interrupted: the server that ran this operation stopped. Deploy again.`
   The deploy log gets status `error`. The core clears `post_deploy_hook_until`.
   The owner and the followers get "Deployment failed". No hook event fires for
   this recovery (no `deploy-finalized`). The subscriber can still run, because
   the backend does not call it again. Until the recovery, a stop also ends the
   state.
7. When the core cannot read the stack at the start of the blocking phase (a
   database error), it calls no blocking subscriber. The deploy ends with
   status `error` and the message `The post-deploy steps did not run: the stack
   could not be read. Deploy again.` `deploy-finalized` fires.

The blocking wait does not hold a deploy concurrency slot
(`MAX_CONCURRENT_DEPLOYS`). The envelope of a blocking call has the `charts`
list of `pre-deploy`, `metadata.blocking` = `"true"` and the instance status
`stabilizing`. A subscription without `blocking` works as before: the core
calls it after the status update to `running`. A blocking subscription that
also lists other events gets those events as a normal subscriber.

### Cleanup policy runs

`cleanup-policy-executed` fires once for each run with at least one matching
instance. `instance` is not set. The run summary is in `cleanup_policy`:

```json
{
  "event": "cleanup-policy-executed",
  "trigger": {"type": "cleanup-policy", "id": "4f0c...", "name": "nightly-stop"},
  "cleanup_policy": {
    "id": "4f0c...",
    "name": "nightly-stop",
    "action": "stop",
    "cluster_id": "all",
    "condition": "idle_days:3",
    "dry_run": false,
    "run": "scheduled",
    "matched": 2,
    "succeeded": 1,
    "failed": 1,
    "instances": [
      {"id": "6c9f...", "name": "demo", "namespace": "stack-demo-alice",
       "owner_id": "uid-123", "result": "success"},
      {"id": "8a21...", "name": "old", "namespace": "stack-old-bob",
       "owner_id": "uid-456", "result": "error", "error": "resolving cluster: ..."}
    ]
  }
}
```

- `cluster_id` is the cluster of the policy, or `all`. `cluster_name` is the name of that cluster, the same as `instance.cluster_name`. It is omitted for `all` and for an unknown cluster.
- `run` is `scheduled` (cron) or `manual` (`POST /admin/cleanup-policies/:id/run`).
- `result` is `success`, `error` or `dry_run`. A dry run changes nothing.
- For `stop` and `clean`, `success` means that the operation started. The result comes with `stop-completed` or `clean-completed`, which carry the same `trigger`.
- A policy `delete` fires `pre-instance-delete` for each instance. A `failure_policy=fail` subscriber can stop it. The instance then gets `result` `error` with the hook reason. After the delete, `post-instance-delete` and `delete-completed` fire for each instance, and the owner gets `instance.deleted`.
- One delete (with its `pre-instance-delete` hooks) can take 5 minutes, or the sum of the `pre-instance-delete` timeouts plus one minute when that is longer.
- A manual run (`POST /admin/cleanup-policies/:id/run`) answers only when the run ends. A manual delete run with slow `pre-instance-delete` subscribers is therefore a long request: up to the number of matches times that time. Use a client and proxy timeout that allows it, or use a dry run first.
- `instances` has at most 200 entries (`instances_truncated: true` when the core cut the list). The counts cover all matching instances. `error` is one line, at most 500 characters.
- Only the leader runs scheduled policies. A run that the end of the leadership term interrupts fires no event. The next leader runs the policy again.

### Response

A subscriber returns a `HookResponse`:

```json
{ "allowed": true,  "message": "" }
```

To block a `pre-*` event that has `failure_policy=fail`:

```json
{ "allowed": false, "message": "quota exceeded on cluster cluster-1" }
```

- A non-2xx response is a failure.
- An empty 2xx body means `{"allowed": true}`.
- The core reads at most 1 MiB of the response. Do not send more.
- If `allowed` is `false` and `failure_policy=ignore`, the core logs a warning and the operation continues.

**Progress streaming.** For `pre-deploy`, `pre-rollback` and blocking
`post-deploy`, the subscriber can write progress lines before the final JSON.
- Start each progress line with `LOG:` plus one space. The core forwards the rest of the line to the deploy log (or rollback log) and to the WebSocket log stream.
- Flush after each line.
- The last non-empty line that does not start with `LOG:` must be the JSON `HookResponse`. A body without such a line means `{"allowed": true}`.
- A subscriber that returns plain JSON works without change.

The backend does not follow a redirect for an event hook (any dispatch path:
normal, progress streaming and blocking `post-deploy`). A 3xx answer (for
example 302, 307 or 308) is a subscriber failure, so a signed body never goes
to another URL. The `failure_policy` of the subscription applies:
- `fail`: the operation stops. The user sees `<event> hook "<name>" failed (the subscriber answered with a redirect)`. The message does not contain the subscriber URL or the `Location` header.
- `ignore`: the backend logs a warning and continues.

Configure the final URL of the subscriber in `url`.

---

## Actions

Actions generalize the former `/refresh-db` endpoint. You can invoke any named
action that you registered at startup:

```
POST /api/v1/stack-instances/:id/actions/:name
Content-Type: application/json

{ "parameters": { "image": "alpine", "reason": "reseed" } }
```

### Registration

```json
{
  "actions": [
    {
      "name": "refresh-db",
      "url": "https://handlers.example.com/actions/refresh-db",
      "description": "Wipe MySQL PVC and flush Redis for the instance",
      "timeout_seconds": 120,
      "secret_env": "REFRESH_DB_HOOK_SECRET",
      "label": "Refresh database",
      "confirm": "This replaces the database of the stack. Unsaved data is lost.",
      "parameters": [
        {"name": "image", "label": "Snapshot", "type": "string", "default": "golden"},
        {"name": "dry_run", "label": "Dry run", "type": "bool", "default": false},
        {"name": "scope", "type": "enum", "options": ["full", "schema-only"], "required": true},
        {"name": "db_password", "label": "Database password", "type": "string", "sensitive": true}
      ],
      "log_path": "/jobs/{job_id}/log"
    }
  ]
}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Action name in the URL (`/actions/:name`). It must be unique. |
| `url` | yes | Subscriber URL (`http` or `https`). The API never returns it. |
| `description` | no | Text for the UI and the list endpoint. |
| `timeout_seconds` | no | Default 30, maximum 600. |
| `secret_env` | no | Environment variable with the HMAC secret. It has the same fail-closed behavior as for subscriptions. The API never returns it. |
| `label` | no | Menu text in the web UI. Default: `name`. Maximum 100 characters. |
| `confirm` | no | Warning text in the run dialog. Use it for destructive actions. Maximum 1000 characters. |
| `parameters` | no | Parameter schema for the UI form (see below). Maximum 20 parameters. |
| `log_path` | no | Path template of the job log of an asynchronous action (see [Asynchronous actions](#asynchronous-actions-and-the-job-log)). |
| `applies_to` | no | Lists and runs the action only for some instances. See [Limit to some instances](#limit-to-some-instances-applies_to). |

The UI fields are optional. A config without them stays valid. The backend
checks them at startup. An invalid field stops the startup.

**Parameters.** Each parameter has these fields:

| Field | Meaning |
|---|---|
| `name` | Key in `parameters` of the ActionRequest. It must match `^[A-Za-z_][A-Za-z0-9_.-]{0,63}$` and be unique. |
| `label` | Form label. Default: `name`. |
| `description` | Help text under the field. |
| `type` | `string` (default), `bool` or `enum`. |
| `required` | A required `string` or `enum` must not be empty. |
| `default` | A string for `string` and `enum` (for `enum`, it must be one of `options`), a boolean for `bool`. The UI fills the form with it. The backend does not add defaults to the request. |
| `options` | The allowed values of an `enum` (1 to 50, unique). Only for `enum`. |
| `sensitive` | Optional, default `false`. The value goes to the subscriber unchanged, but the [action run history](#action-run-history) stores `[redacted]`, and the web UI uses a password field. Only for `type: string`. A sensitive parameter cannot have a `default`, because the defaults are public metadata. Another type or a `default` stops the startup. |

The invoke endpoint checks the declared parameters before it calls the
subscriber: type, `required` and the enum `options`. A mismatch gives 400.
Parameters that the action does not declare pass through unchanged, so
existing API and CLI callers keep working when you add a schema. Still,
validate all parameters in the subscriber.

### Listing actions

```
GET /api/v1/stack-instances/:id/actions
```

Any authenticated user who can view the instance gets the list. It holds the
actions that apply to the instance (`applies_to`). The response never
contains `url`, `secret_env`, `applies_to` or the secret:

```json
{
  "instance_id": "6c9f1e14-...",
  "can_invoke": true,
  "actions": [
    {
      "name": "refresh-db",
      "label": "Refresh database",
      "description": "Wipe MySQL PVC and flush Redis for the instance",
      "confirm": "This replaces the database of the stack. Unsaved data is lost.",
      "parameters": [
        {"name": "image", "label": "Snapshot", "type": "string", "required": false, "default": "golden"},
        {"name": "dry_run", "label": "Dry run", "type": "bool", "required": false, "default": false},
        {"name": "scope", "label": "scope", "type": "enum", "required": true, "options": ["full", "schema-only"]}
      ],
      "has_job_log": true,
      "can_invoke": true
    }
  ]
}
```

`can_invoke` is true for the instance owner, an admin and a devops user (the
same rule as deploy). The list is sorted by action name. Without an action
registry, the list is empty (200). A parameter with `sensitive: true` in the
config has `"sensitive": true` in the list (omitted when false).
`scope_incomplete: true` (omitted when false) means that actions limited to
templates were left out because the template of the instance could not be
read; the web UI shows "Some actions could not be loaded".

### Request envelope

```
POST <url>
Content-Type: application/json
Accept: application/json
X-StackManager-Event: action:<name>
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex>    (only when a secret is set)
Traceparent: ...
```

The body has `apiVersion: hooks.k8sstackmanager.io/v1` and `kind: ActionRequest`:

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

An action can return any JSON. The core forwards the body of the subscriber to
the API client, wrapped in this envelope:

```json
{
  "action": "refresh-db",
  "instance_id": "6c9f1e14-...",
  "status_code": 200,
  "result": { "wiped_pvcs": ["mysql-data"], "flushed_keys": 128 },
  "run_id": "1b2c3d4e-..."
}
```

`run_id` is the ID of the stored [action run](#action-run-history). It is
missing when the backend did not store the run (no history, or a database
error). A failed write never fails the action.

The backend does not follow a redirect from the subscriber, so a signed
request never reaches another host. A 3xx answer with an empty or JSON body is
returned as `status_code` (with `result` `null` for an empty body). A 3xx
answer with a non-JSON body (for example an HTML redirect page) gives 502, like
any other non-JSON body below status 400.

When the action has a `log_path`, the subscriber answered 2xx, and `result` is
an object with a valid `job_id` string, the envelope also has `"job_id"`. A
client then polls the job log route.

A refusal (status 400 or higher) keeps its reason. The envelope also has
`"message"`.
- The core takes it from a JSON string body, or from the first string field of `message`, `error`, `detail` and `reason` (also `error.message`).
- It is one line of at most 500 characters. A URL in the message becomes `[url]`.
- Example: a subscriber answers `409 {"error": "refresh-db already in flight"}`. The result is `{"status_code": 409, "result": {"error": "..."}, "message": "refresh-db already in flight"}`.
- A `text/plain` refusal body becomes a JSON string in `result` (and in `message`).
- Another non-JSON refusal body (for example an HTML error page of a proxy) gives `result: null`.
- A non-JSON body with a status below 400 still gives 502.
- The envelope never has the subscriber URL.

API error mappings:

| API status | Meaning |
|---|---|
| 200 | The subscriber responded. See `result`. `status_code` echoes the HTTP code of the subscriber. |
| 400 | Invalid parameters, malformed body, or a parameter that does not match the declared schema. |
| 403 | The caller is not the owner, an admin or a devops user. |
| 404 | Unknown instance, unknown action name, or an action whose `applies_to` does not match the instance. |
| 502 | The subscriber is unreachable, returned a transport error, or returned a non-JSON body (status below 400). |
| 503 | The action registry is not configured on this server, or the template of the instance could not be read to check the `applies_to` of the action. |

### Asynchronous actions and the job log

An action that runs longer than its timeout answers at once with a job ID and
does the work in the background:

```
HTTP/1.1 202 Accepted
Content-Type: application/json

{"job_id": "job-0123456789ab", "status": "started"}
```

The job ID must match `^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$` (1 to 128
characters, not starting with a dot). The subscriber MUST create random job
IDs. For example, use `job-` plus 12 or more random hex characters from a
secure random source. Do not use sequential numbers or timestamps. A guessable
job ID lets a user try the job IDs of other instances.

With `log_path` set, the backend serves the job log to API clients:

```
GET /api/v1/stack-instances/:id/actions/:name/jobs/:job_id/log?offset=<n>
```

- **Permission:** the same rule as invoke (owner, admin, devops). A job log can hold sensitive output (host names, database names, error text). A user who can only view the instance cannot read it.
- `offset` is a byte offset, a non-negative integer (default 0).
- Response (200):

```json
{
  "action": "refresh-db",
  "instance_id": "6c9f1e14-...",
  "job_id": "job-0123456789ab",
  "status": "running",
  "log": "step 1/4: scale down\nstep 2/4: restore snapshot\n",
  "offset": 0,
  "next_offset": 52,
  "done": false,
  "truncated": false
}
```

- A client polls with `offset=next_offset` until `done` is true. When `truncated` is true, the core cut the chunk at the size cap. Poll again at once.
- The web UI polls every 3 seconds.
  - On 429, 502, 503, 504 or a network error, it waits and continues. The wait is the `Retry-After` delay, or else a doubled delay up to 30 seconds.
  - On 401, 403 or 404, it stops with a message.
  - It stops after 60 minutes. The job can still run on the server.
  - A CLI that follows the log (`--follow`) should do the same.
  - The API rate limit (`RATE_LIMIT`, default 100 requests per minute for each IP address) applies.
- `status` is `running` while the job runs (also `pending`, `queued`, `started`). Otherwise it is a final state such as `succeeded` or `failed`. `done` is true for a final state, unless the chunk is truncated.

| API status | Meaning |
|---|---|
| 200 | Log chunk (can be empty). |
| 400 | Invalid `job_id` or `offset`. |
| 403 | The caller is not the owner, an admin or a devops user. |
| 404 | Unknown instance, unknown action, action without `log_path`, or the subscriber does not know the job (its 400, 404 or 410). |
| 502 | The subscriber is unreachable, timed out, or answered with another non-2xx status (also a 3xx redirect). The response has `request_id`, never the subscriber URL or body. |
| 503 | The action registry is not configured on this server. |

**Subscriber contract.** The backend calls:

```
GET <scheme>://<host of url><log_path with {job_id}>?instance_id=<instance id>&offset=<n>&ts=<unix seconds>
Accept: text/plain, */*
X-StackManager-Event: action-log:<name>
X-StackManager-Request-Id: req-xxxxxxxxxxxxxxxxxxxxxxxx
X-StackManager-Signature: sha256=<hex of HMAC-SHA256(secret, request URI)>   (when a secret is configured)
Traceparent: ...
```

- `log_path` is an absolute path on the host of the action `url`. Scheme, host and port always come from `url`. Example: for `url` `http://actions.example:8080/actions/refresh-db` and `log_path` `/jobs/{job_id}/log`, the backend calls `http://actions.example:8080/jobs/<job_id>/log?...`.
- `log_path` must start with `/` and contain `{job_id}` exactly once. It must not contain a query, a fragment, `%`, `//`, or `.` or `..` segments. It has at most 256 characters.
- The backend does not send the query string of the action `url` on log reads. A subscriber that authenticates invoke calls with a query key (for example `?code=...`) must authenticate log reads with the signature.
- The signature is the HMAC-SHA256 of the request URI as sent: path, then `?`, then query. Example: `/jobs/job-0123456789ab/log?instance_id=6c9f1e14-...&offset=0&ts=1791547200`. The GET has no body, so the request URI is the signed message. Verify it against the raw request URI, not a re-encoded one.
- `ts` is the request time in Unix seconds. It is part of the signed URI. A subscriber should refuse (401) a request whose `ts` is more than 5 minutes from its own clock. This limits the replay of a captured request.
- The subscriber MUST store the instance ID with each job. It MUST answer 404 when `instance_id` does not match the instance of the job. The backend checks only that the caller may modify the instance in the URL. It does not check that the job belongs to that instance.
- Answer 200 with the log bytes from `offset` to the end, as text. An offset past the end gives an empty body.
- Response headers:
  - `X-Job-Status` (recommended): `running`, `succeeded`, `failed` or another lowercase word (`^[a-z][a-z_]{0,31}$`).
  - `X-Log-Offset` (optional): the next offset. The backend uses it only when it is not smaller than `offset` and the chunk is not truncated. Otherwise it uses `offset` plus the body length.
- Prefer `X-Job-Status`. Only without it, the backend reads the status from the last end marker line in the chunk. The line looks like `===<NAME>-END=== status=<status>`, for example `===JOB-END=== status=succeeded`. The marker is a fallback. A job that prints untrusted output can print a fake marker and end the polling early. Without both, the status is `running`, and clients poll until they give up.
- Offsets are byte offsets. While the job runs and `X-Log-Offset` is not set, the backend drops an incomplete UTF-8 sequence at the end of a chunk. The next poll reads the whole character. When you set `X-Log-Offset`, end each chunk on a character boundary.
- Answer 404 for an unknown job.
- Keep the job logs readable for some time after the job ends (for example as long as `ACTION_RUN_RETENTION_DAYS`). Users open the log of a past run from the action run history. When a job is gone, answer 404: the web UI then shows "The job log is no longer available."
- The backend reads at most 256 KiB for each call. It cuts a larger chunk at its last line end. The client gets the rest with the next offset.
- The call times out after 10 seconds (or after the action timeout, when that is lower).
- The backend does not follow a redirect (a 3xx answer gives 502), so the signed request stays on the host.

### Action run history

The backend stores one action run for each invoke that reached the subscriber
call. That includes a refusal, an unreachable subscriber and an answer that it
cannot forward. A request that fails before the call (400, 403, 404, 503)
stores no run. The audit log keeps `invoke_action` for every call.

```
GET /api/v1/stack-instances/:id/action-runs?limit=<n>&cursor=<next_cursor>
```

- **Permission:** any authenticated user who can view the instance (the rule of the action list).
- The list is newest first. `limit` is 20 by default. A larger value than 100 counts as 100. A `limit` that is not a whole number above 0 gives 400 `Invalid limit parameter`. A bad `cursor` gives 400 `Invalid cursor parameter`.
- There is no total. `next_cursor` is missing on the last page.

```json
{
  "instance_id": "6c9f1e14-...",
  "can_view_logs": true,
  "data": [
    {
      "id": "1b2c3d4e-...",
      "instance_id": "6c9f1e14-...",
      "action": "refresh-db",
      "action_label": "Refresh database",
      "user_id": "u-123",
      "started_by": "alice",
      "started_at": "2026-10-20T10:00:00.123Z",
      "finished_at": "2026-10-20T10:01:02.456Z",
      "parameters": { "image": "golden", "dry_run": false, "db_password": "[redacted]" },
      "status_code": 202,
      "job_id": "job-0123456789ab",
      "job_status": "succeeded",
      "has_job_log": true,
      "request_id": "req-..."
    }
  ],
  "next_cursor": "MjAyNi0x..."
}
```

What a run holds:
- `user_id`: the caller (the user of the JWT or of the API key). `started_by` is the username. It is missing when the user no longer exists. A user delete keeps the runs of that user.
- `parameters`: only the declared parameters. The backend never stores an undeclared parameter. It stores a `sensitive` parameter as `[redacted]`. `parameters` is always an object (`{}` when there are none).
- `status_code`: the HTTP status of the subscriber. `0` means no answer (unreachable, timeout, or the client closed the request). An answer that the backend cannot forward keeps its status, for example `302` for a redirect with an HTML body.
- `message`: the reason of a refusal (status 400 or higher, the text of `message` in the invoke envelope), or a fixed text: `Subscriber unreachable`, `Subscriber returned a non-JSON body`, `Subscriber returned a body that is too large` or `The client closed the request before the answer`. It never holds a URL or the error detail.
- Every viewer reads `message`. Never put a parameter value or a secret in it. As a safety net, the backend replaces the value of each `sensitive` parameter of the call with `[redacted]` in the stored message. The invoke envelope keeps the message unchanged for the caller.
- `job_id`: the `job_id` of the invoke envelope (asynchronous actions with `log_path`).
- `request_id`: the `X-StackManager-Request-Id` of the invoke call, for the subscriber logs.
- A run never holds the result body, the subscriber URL, headers or the secret.

Finish time and final job status:
- A run without a job gets `finished_at` when the subscriber answers.
- A run with a job gets `job_status` and `finished_at` from the first job log poll that returns `done: true`, on any replica (one conditional update). Later polls change nothing.
- Only users with the modify rule read the job log. When nobody reads the log to the end, the run keeps no final status, and the web UI shows "Job started".
- `action_label` is the current `label` of the action, else its name. `has_job_log` is true when the run has a `job_id` and the action still has a `log_path`. `can_view_logs` is true for the owner, an admin and a devops user. Only they can open the job log of a run.

Retention: the leader worker `log-retention` deletes runs older than
`ACTION_RUN_RETENTION_DAYS` (default 180 days, by `started_at`). `0` keeps the
runs forever. An instance delete deletes the runs of the instance.

---

## Security

### HMAC signing

When a subscription (event or action) has a `secret`, the core signs the
request body with HMAC-SHA256:

```
X-StackManager-Signature: sha256=<hex-of-HMAC-SHA256(secret, body)>
```

For a job log request (a GET without a body), the signed message is the
request URI (path and query). See
[Asynchronous actions](#asynchronous-actions-and-the-job-log).

A handler must verify the signature before it trusts any envelope field. The
reference handler [../examples/webhook-handler/main.go](../examples/webhook-handler/main.go)
shows the verification.

Use a high-entropy shared secret (at least 32 bytes). To rotate a secret,
register a new subscription next to the old one, move the traffic, and then
remove the old entry.

### Network posture

The server opens outbound HTTP or HTTPS connections to subscriber URLs.
Handlers can run inside the cluster, behind a VPN or on a bastion host.
k8s-stack-manager only needs a URL that the server network can reach.

### Replay protection

The envelope has a unique `request_id` (`req-` plus 24 hex characters). A
handler that needs at-least-once delivery with deduplication should track
recently seen IDs. A 10-minute least recently used (LRU) cache is usually
enough.

The HMAC signature does not include a separate timestamp header. For events
and action invokes, the `timestamp` field is inside the signed body. Refuse an
envelope whose `timestamp` is more than 5 minutes old. Refuse a `request_id`
that you saw before. Without these checks, an attacker can replay a captured
signed request. Job log reads carry a signed `ts` query parameter (see
[Asynchronous actions](#asynchronous-actions-and-the-job-log)).

### Failure policy and blast radius

- `failure_policy=fail` on a `pre-*` event blocks the operation when the subscriber fails. Use it with care. A broken subscriber can halt all deploys.
- Every hook has a per-call timeout. The default is 5 s for events and 30 s for actions. The maximum is 60 minutes (3600 s) for events and 10 minutes (600 s) for actions.
- Dispatch is synchronous and follows the registration order. A `fail` subscription that errors prevents later subscriptions of the same event from running.
- A blocking `post-deploy` subscription with `failure_policy=fail` fails the deploy when it fails. Use `ignore` when the post-deploy work is optional.

---

## Contract versioning

`apiVersion` in every envelope identifies the contract revision:

- `hooks.k8sstackmanager.io/v1` is the current version. It does not mutate: subscribers can only allow or deny.
- A future `v2` can add mutating webhooks that rewrite parts of the payload (values, chart list, and so on) before the operation continues. A handler should ignore envelopes that it does not understand. It should return `{"allowed": true}` or HTTP 200 without a body.

---

## Observability

### Metrics

The `hooks` OpenTelemetry meter scope emits these metrics:

| Metric | Type | Labels |
|---|---|---|
| `hook.dispatches_total` | Counter | `hook.event`, `hook.subscription`, `hook.outcome` |
| `hook.dispatch_duration` | Histogram (s) | `hook.event`, `hook.subscription` |
| `hook.action_invocations_total` | Counter | `hook.action`, `hook.outcome` |
| `hook.action_invocation_duration` | Histogram (s) | `hook.action` |

`hook.outcome` is a small, stable set: `success`, `denied`, `http_error`,
`transport_error`, `timeout`, `unknown_action`, `scope_unknown`,
`marshal_error`. An action invoked for an instance outside its `applies_to`
counts as `unknown_action`; `scope_unknown` counts invokes whose scope could
not be checked.

### Traces

Spans:
- `hooks.dispatch`: event subscriber calls.
- `hooks.action`: action invocations.
- `hooks.action_log`: job log reads. It has no metrics, because clients poll often.

Span attributes: `hook.event` or `hook.action`, `hook.subscription`,
`hook.request_id`, `hook.outcome`, `hook.status_code`.

Outbound requests carry `Traceparent` (W3C TraceContext). A subscriber with
its own OpenTelemetry SDK adds its spans as children of the spans of the core.

### Structured logs

A dispatch failure writes a log line (`slog`) for quick triage:

```
level=warn subscription=cmdb-sync event=post-instance-create request_id=req-abc status=transport_error error="connection refused"
```

Use `request_id` to correlate metrics, spans, logs and subscriber-side
records. It has the same value as the `hook.request_id` attribute and the
`X-StackManager-Request-Id` header.
