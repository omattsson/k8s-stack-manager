# Architecture

K8s Stack Manager is a web application that manages multi-service Helm stacks on Kubernetes. It has a Go (Gin) backend and a React (TypeScript, Vite, MUI) frontend. It uses MySQL (GORM) for persistence, JWT authentication with optional OpenID Connect (OIDC), and multi-cluster deployment.

See [WIKI.md](WIKI.md) for the concepts and rules, [EXTENDING.md](EXTENDING.md) for the hooks and actions extension system, and [README.md](README.md) for the setup and screenshots.

## System Overview

```
+--------------------------------------------------------------+
|                    Frontend (React 19)                       |
|  MUI, Vite, Monaco Editor, WebSocket, OIDC (PKCE)            |
+--------------------------------------------------------------+
|                   Backend (Go 1.26 + Gin)                    |
|  REST API, JWT, Swagger, Audit Middleware, OpenTelemetry     |
+------+----------+----------+----------+----------+-----------+
|MySQL | Cluster  | Git      | Helm     | Hook     | K8s       |
|(GORM)| Registry | Provider | Values   | Dispatch | Deployer  |
|      | (multi)  | (ADO+GL) | (merge)  | (events) | (helm)    |
+------+----------+----------+----------+----------+-----------+
```

ADO is Azure DevOps. GL is GitLab. PKCE is Proof Key for Code Exchange.

## Package Structure

```
backend/internal/
|-- api/
|   |-- handlers/     # HTTP handlers (one file for each resource; the rate limiter is here)
|   |-- middleware/   # auth, combined auth (JWT + API key), audit, role, security headers,
|   |                 # HTTP and auth metrics, OTel span enrichment, WebSocket token redaction
|   `-- routes/       # routes.go: all route registration and the middleware order
|-- auth/             # OIDC provider (PKCE); OIDC and CLI state are stored in sessionstore
|-- cache/            # Generic in-memory TTL cache
|-- cluster/          # ClusterRegistry, health poller, quota monitor, secret refresher, access reconciler
|-- config/           # Configuration loading from environment variables
|-- database/         # GORM repositories (one for each model), migrations
|-- deployer/         # Helm deploy, undeploy, rollback manager; expiry stopper; cleanup executor
|-- gitprovider/      # Azure DevOps and GitLab branch listing, URL detection, cache
|-- health/           # Liveness and readiness checks
|-- helm/             # Values deep merge, template variable substitution
|-- hooks/            # Event dispatcher, action routing, HMAC signing
|-- k8s/              # Kubernetes client, namespace status, watcher, pod exec, scaling, quotas
|-- leader/           # Leader election (Kubernetes Lease) and leader worker group
|-- models/           # GORM model structs and repository interfaces
|-- notifier/         # In-app notifications and outbound notification channels
|-- replica/          # Process identity and heartbeat (replica_heartbeats)
|-- scheduler/        # Cleanup policy scheduler (cron)
|-- sessionstore/     # Token blocklist and OIDC state (MySQL or memory)
|-- telemetry/        # OpenTelemetry setup, database pool metrics, business metrics
|-- ttl/              # Instance expiry reaper
`-- websocket/        # Hub, clients, message types, fan-out between replicas
```

## Data Model

The core entity relationships:

```
StackTemplate --1:N--> TemplateChartConfig
      |                       |
      | (instantiate)         | (copies to)
      v                       v
StackDefinition --1:N--> ChartConfig
      |
      | (create instance)
      v
StackInstance --1:N--> ValueOverride (per chart)
      |           `--> ChartBranchOverride (per chart)
      |
      `--> Cluster (deployment target)
```

The supporting models are: `User`, `AuditLog`, `DeploymentLog`, `ReplicaHeartbeat`, `WSEvent`, `UserFavorite`, `InstanceFollower`, `Notification`, `NotificationPreference`, `NotificationChannel` (with `NotificationChannelSubscription` and `NotificationDeliveryLog`), `RefreshToken`, `APIKey`, `CleanupPolicy`, `ResourceQuotaConfig`, `InstanceQuotaOverride`, `SharedValues`, `TemplateVersion`, and `TemplateSnapshot`.

## Values Merge Precedence

The order from lowest to highest priority (`internal/helm/values_generator.go`):

1. Shared values (cluster-scoped, applied in priority order, lowest first).
2. Chart default values (from the `ChartConfig` of the definition; the application copies them from the template when you instantiate it).
3. Instance value overrides (per chart).
4. **Template locked values.** The application applies them last. Nothing can override them.

`ValuesGenerator` substitutes the template variables (`{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`) each time it produces merged values: deploy, preview, and export.

## Multi-Cluster

`ClusterRegistry` in `internal/cluster/` manages the Kubernetes and Helm clients of each cluster. It creates a client on first use. These background services run for the clusters:

- **Health poller.** It pings the API server periodically and broadcasts the status through WebSocket.
- **Quota monitor.** It tracks the resource use of the namespaces against the ResourceQuota.
- **Secret refresher.** It rotates the container registry pull secrets every 4 hours (for short-lived tokens).
- **Access reconciler.** It applies the access RoleBinding to the stack namespaces of in-cluster clusters (see [Namespace-scoped backend access](#namespace-scoped-backend-access)).

The application encrypts kubeconfig data at rest with AES-256-GCM. The key comes from `KUBECONFIG_ENCRYPTION_KEY`. Without this key, the backend rejects kubeconfig data.

## Deployment Pipeline

1. The user triggers a deploy: `POST /api/v1/stack-instances/:id/deploy`. The API checks that the caller is the owner, `devops`, or `admin`.
2. The `pre-deploy` hook fires. A subscriber can stop the deploy with `failure_policy: fail`.
3. `deployer.Manager` resolves the cluster through `ClusterRegistry`.
4. The backend creates or checks the namespace. It sets the labels. On an in-cluster cluster it also creates the access RoleBinding.
5. The backend applies the namespace ResourceQuota and LimitRange (the cluster quota merged with the instance override).
6. The backend copies the wildcard TLS secret (when configured), provisions the image pull secrets, and applies the configured per-namespace RoleBindings.
7. The backend checks each chart against `ALLOWED_CHART_REPOS`. A chart that is not allowed counts as failed.
8. The backend runs `helm upgrade --install` for each chart, in deploy-order sequence. A pod event watcher writes the pod problems to the deploy log.
9. The instance status changes to `stabilizing`. The backend polls the namespace until the pods are ready, or until `DEPLOY_STABILIZE_TIMEOUT` ends.
10. Blocking `post-deploy` hooks run. The instance stays `stabilizing` during this time. The backend then writes the final status (`running`, `partial`, or `error`). It fires the `post-deploy` hook (only when the deploy succeeded or was partial) and the `deploy-finalized` hook.
11. `k8s.Watcher` polls the namespace for pod and deployment status. The backend broadcasts status updates through WebSocket.

`GET /api/v1/stack-instances/:id/deploy-preview` returns the merged values without a deploy.

`POST /api/v1/stack-instances/:id/rollback` reverses step 8 for each chart. It fires `pre-rollback`, then `rollback-completed` on either result, and `post-rollback` only on success.

Other hook events that the backend dispatches are: `deploy-finalized`, `deploy-timeout`, `pre-instance-create`, `post-instance-create`, `pre-instance-delete`, `post-instance-delete`, `stop-completed`, `clean-completed`, `delete-completed`, and `cleanup-policy-executed`. The constants `instance-created`, `stack-expiring`, `stack-expired`, `quota-warning`, and `secret-expiring` are defined but not yet fired. `pre-namespace-create` and `post-namespace-create` are reserved. See [backend/docs/hooks.md](backend/docs/hooks.md) and [EXTENDING.md](EXTENDING.md).

## Authentication

- **Local.** Username and password, checked with bcrypt, give a JWT.
- **OIDC.** The Authorization Code flow with PKCE gives an ID token. The backend creates the user on the first login (just-in-time provisioning) and issues a local JWT.
- **API keys.** A key in the `X-API-Key` header authenticates a request. The role comes from the database on each request.
- **Role hierarchy.** `admin` > `devops` > `user`. `devops` users manage templates and definition chart sources. `admin` users manage clusters, users, shared values, and cleanup policies.
- **SessionStore.** A persistent token blocklist and OIDC state (MySQL by default, in-memory for tests). It survives restarts: revoked tokens stay blocked, and OIDC logins in progress survive backend redeploys.
- **User.Disabled.** An `admin` can disable accounts. This blocks login, token refresh, OIDC login, and API-key auth at once.
- **Sessions.** A login starts a refresh-token family (`refresh_tokens.family_id`, also the `sid` claim of the access token).
  - A rotation keeps the family and its start time.
  - A session ends after `SESSION_MAX_LIFETIME`, or after `SESSION_IDLE_TIMEOUT` without requests. The JWT middleware records the request activity, at most one write each minute for each session.
  - A just-rotated token gets an access token inside `REFRESH_REUSE_GRACE`. Other reuse revokes the family.
  - See [WIKI.md](WIKI.md#sessions).
- **Rate limits.** A per-IP limiter works on `/api/v1` (`RATE_LIMIT`, default 100 each minute). A stricter login limiter works on the login and refresh routes (`LOGIN_RATE_LIMIT`, default 10 each minute).

## Namespace-scoped backend access

Helm runs with the identity of the backend. With `backend.rbac.scope: namespace` (the Helm default), the chart limits what a stack chart can do on an in-cluster cluster:

- The backend has a RoleBinding `k8s-stack-manager-access` in each stack namespace. It binds the backend service account to the ClusterRole `<fullname>-stack-access`.
- `k8s.Client.EnsureNamespace` creates the RoleBinding before any other object. Configuration: `STACK_ACCESS_CLUSTER_ROLE` and `STACK_ACCESS_SERVICE_ACCOUNT`.
- The leader worker `stack-access-reconcile` applies the RoleBinding to existing namespaces with the label `managed-by=k8s-stack-manager`. It runs at the start of each term and then every 10 minutes.
- A ValidatingAdmissionPolicy (`backend.rbac.admissionPolicy.enabled`, Kubernetes 1.30 or later) limits the backend service account. It can write Roles and RoleBindings only in labeled namespaces. It can create only labeled namespaces. It can change or delete only namespaces whose name starts with `stack-` or that have the label.

`ALLOWED_CHART_REPOS` limits the chart sources. The backend checks it when a chart config is saved, at deploy time, and at rollback time. See [WIKI.md](WIKI.md#chart-sources) and [WIKI.md](WIKI.md#backend-rights-on-the-cluster).

## Replica Model

The backend can run with more than one replica. Every replica serves the HTTP API, the WebSocket hub, and the WebSocket revalidation. Every replica also runs the deploy, stop, clean, and rollback goroutines of the requests that it handles. Only one replica, the leader, runs the background workers:

| Worker | Runs in |
|---|---|
| TTL reaper, expiry warner | leader |
| Cleanup scheduler (cron jobs) | leader |
| Quota monitor, secret monitor | leader |
| Pull secret refresher | leader |
| Cluster health poller | leader |
| k8s status watcher | leader |
| Refresh token cleanup | leader |
| WebSocket event cleanup (`ws_events`) | leader |
| Stack access reconcile | leader |
| Interrupted operation recovery, heartbeat cleanup | leader |
| HTTP API, WebSocket hub, revalidation, and fan-out poller | every replica |
| Replica heartbeat (`replica_heartbeats`) | every replica |

- **Election.** `internal/leader` wraps the client-go leader election with a `coordination.k8s.io/v1` Lease.
  - The Lease name is `LEADER_ELECTION_LEASE_NAME` (default `k8s-stack-manager-workers`). The namespace is `LEADER_ELECTION_NAMESPACE`, else `POD_NAMESPACE`, else the namespace file of the service account.
  - The identity is `POD_NAME`, else the host name.
  - The timings are: lease 15s, renew deadline 10s, retry 2s (`LEADER_ELECTION_LEASE_DURATION`, `LEADER_ELECTION_RENEW_DEADLINE`, `LEADER_ELECTION_RETRY_PERIOD`).
  - `LEADER_ELECTION_ENABLED=false` (the default, used by Docker Compose and local development) makes the process always the leader.
  - With `true`, the backend needs the in-cluster service account configuration. Otherwise the startup fails.
  - The Helm chart enables the election and creates a Role and RoleBinding for `leases` (get, create, update).
- **Terms.** `leader.Group` starts every worker with a term context (`Run(ctx)`, which can run again after it returned).
  - When the term ends, the group cancels the context and waits for the workers (30s limit; a next term waits for workers that did not stop). The replica then campaigns again. It does not exit.
  - A watchdog ends the term when the lease was not renewed for the renew deadline. The workers then stop at most "renew deadline + retry period / 2" after the last renewal. This is before the lease expires for the other replicas (lease duration), if the workers stop within that margin (about 4s with the defaults).
  - The backend releases the lease itself (client-go `ReleaseOnCancel` is off), and only after the workers stopped: on SIGTERM, on a watchdog stop, and after a failed renewal.
  - When the workers did not stop within the stop timeout, the backend does not release the lease. The other replicas wait for the lease duration.
  - A Conflict on the release means that another replica already holds the lease. Another replica then takes over after about one retry period.
- **Cleanup scheduler.** Only the leader has an active cron scheduler.
  - A policy change on another replica does not start cron jobs there (`Reload` does nothing when the scheduler is not active).
  - The leader reads the enabled policies when it becomes leader and every minute. A scheduled run reads the policy again before it runs.
  - When it becomes leader, the scheduler runs a policy once if its last scheduled time is within the last 5 minutes and after its last run (or its creation). So a run that fell into a leader change is not lost. This applies to cron expressions only, not to `@every`. The dry-run flag applies.
  - When the term ends during a run, the run stops before the next instance. There is no audit entry for the remaining instances, no `last_run_at` update, and no summary notification.
  - A run that the term end cancels after its actions but before it saved `last_run_at` can run again on the new leader (catch-up).
  - Each run checks the condition again. So an instance that is already stopped or cleaned no longer matches a status condition, and stop and clean are idempotent.
  - The catch-up looks only at scheduled times strictly before the time that the backend read before the cron instance starts. So the cron instance and the catch-up never run the same scheduled time.
  - Manual runs (`POST /admin/cleanup-policies/:id/run`) work on every replica.
- **State in the database.** The expiry warner marks `stack_instances.expiry_warned_at` with a conditional update before it sends the warning.
  - The condition is `expiry_warned_at IS NULL` and `expires_at` within 1 second of the listed value (the database can store less precision than Go). So a new leader, or a short overlap of two leaders, sends one warning.
  - A repository `Update` never writes the column. In the same transaction, it clears the column in two cases. The first case is that the stored `expires_at` and the new value differ by 1 second or more (deploy, extend, TTL change). The second case is that one of them is NULL and the other is not.
- **State in memory.** The quota monitor and the secret monitor keep their warning cooldowns in memory. After a leader change, the new leader can repeat one quota or secret warning.
- **k8s status cache.** The status watcher cache is on the leader only. On other replicas, `GET /stack-instances/:id/status` reads the cluster directly.
- **Observability.** The gauge `stackmanager_leader` is 1 on the leader. The backend writes a log line on each leadership change. Readiness does not depend on leadership.
- **WebSocket fan-out.** The hub is in memory for each replica. The table `ws_events` shares its messages (`WS_FANOUT_ENABLED`; the Helm chart enables it, Docker Compose and local development keep it off).
  - Each hub send (`Broadcast`, `BroadcastToInstance`, `BroadcastToUser`) and each revocation (`DisconnectUser`, `DisconnectToken`, `DisconnectAPIKey`) acts on the local clients at once. It also queues one row. The target of the row is `all`, `instance:<id>`, `user:<id>`, `revoke:user:<id>`, `revoke:token:<jti>`, or `revoke:apikey:<key id>`.
  - The origin of a row is the replica identity (`POD_NAME`, else the host name) plus a random suffix for each process. The leader election keeps the plain identity.
  - A writer goroutine inserts the queued rows in batches (up to 200 rows or about 1 MiB).
  - The queue holds 4096 rows. When it is full, or a message is larger than 512 KiB, the message reaches only the local clients (counter `websocket.fanout.events_dropped_total`, rate-limited warning). A hub send never waits for the database.
  - Every replica polls `id > last id` every `WS_FANOUT_POLL_INTERVAL` (500ms; batches of 500, more pages at once when a batch is full). It delivers the rows of other origins to its clients and skips its own rows.
  - A revocation row from another replica closes the local sockets and writes no new row. A user revocation closes only sockets whose token was issued at or before the row time (the rule of the session store user block). So a session that opened after the revoke stays open. It always closes the sockets that were opened with an API key of the user.
  - The poller skips rows older than 30 seconds (for example after a database outage): `websocket.fanout.events_skipped_total{reason="stale"}`. The age compares the clock of the writing replica (`created_at`) with the clock of the reading replica. So the pods need synchronized clocks (NTP on the nodes). A skew of more than a few seconds drops live updates. Set an alert on a rising `events_skipped_total{reason="stale"}`.
  - So events of the leader workers (status, cluster health), deploy logs, and revocations reach all replicas, with a delay of up to about one poll interval.
  - The leader deletes rows older than `WS_FANOUT_RETENTION` (5m) every minute, in batches of 5000 rows.
  - When fan-out is off, there are no `ws_events` reads or writes.
- **Fan-out ordering.** MySQL assigns an auto-increment ID at insert time, but the row becomes visible at commit. A row of one replica with a lower ID can become visible after a row of another replica with a higher ID was read.
  - The poller records each ID that it skipped over and reads these IDs again on each poll for 10 seconds. A late row is delivered when it appears (once). The gap of a rolled-back insert expires.
  - Gaps larger than 1000 IDs are not tracked (log line).
  - One writer for each replica inserts its batches one after the other. So the rows of one replica stay in order. Only rows of different replicas can arrive out of order.
  - At start, the poller begins at `MAX(id)` (no replay). It also tracks the IDs in `(MAX(id) - 1000, MAX(id)]` that are not visible yet. Each replica has at most one uncommitted batch of 200 rows, so 1000 IDs cover 5 other writers.
  - Every 20th empty poll checks `MAX(id)`. When it is above 0 but below the last ID, the IDs restarted (the table was created again, or an auto-increment reset of an empty table at a MySQL restart). The poller logs a warning and reads from ID 0 again. The 30-second filter drops the old rows.
  - Database errors in the poller are logged (rate limited), and the next tick tries again.
- **Binary log.** `ws_events` inserts and deletes go to the MySQL binary log like all writes. MySQL 8 (the bundled MySQL) keeps binary logs for 30 days by default (`binlog_expire_logs_seconds`). So the deploy log lines add to the binary log size on the MySQL volume for that time. Plan the volume size, or set a shorter expiry in `mysql.config`.
- **Interrupted operations.** A deploy, rollback, stop, or clean (and a blocking `post-deploy` hook) runs in a goroutine on the replica that took the request. See [Interrupted operations](#interrupted-operations).
- **Per-replica WebSocket state.** The token, API key, and user revalidation stays for each replica as a fallback (see [WIKI.md](WIKI.md#revoking-a-user)). It closes sockets whose revocation row was dropped, skipped, or written while fan-out was off. In-app notifications go only to the sockets of the notified user (`BroadcastToUser`).

### Interrupted operations

A replica can stop without a clean shutdown (SIGKILL, OOM kill, node loss). It then leaves its operations in progress: the instance stays `deploying`, `stabilizing`, `stopping`, or `cleaning`, and the deploy log stays `running`. The leader ends these operations.

- **Process identity.** Each backend process has an identity: the replica identity (`POD_NAME`, else the host name) plus a random suffix of 8 hexadecimal characters for each process. A restarted process gets a new identity, also when the host name stays (Docker Compose). The deploy manager stores the identity on each deploy log (`deployment_logs.replica_id`) when the operation starts. The identity has the same form as the fan-out origin, but a different suffix.
- **Heartbeat.** Every process (not only the leader, and also with `LEADER_ELECTION_ENABLED=false`) writes its row in `replica_heartbeats` (`id`, `last_seen`) at start and every 30 seconds.
  - A clean shutdown deletes the row after the deploy manager stopped.
  - A process without a heartbeat for 2 minutes counts as stopped.
  - The database server clock writes and compares `last_seen` (`UTC_TIMESTAMP(3)`), so the replica clocks do not matter for the heartbeat check.
  - The heartbeat writes use the normal database connection pool. A pool that stays saturated (or a database outage) for more than 2 minutes can stop the heartbeat of a live replica. The deadline (below) keeps normal operations safe in that case.
- **Deadline.** The deploy manager also stores `deployment_logs.deadline_at` when the operation starts. It is the start time plus the time budget of the operation plus 5 minutes.
  - The budget of a deploy is: the `pre-deploy` hook timeouts + one helm timeout (`DEPLOYMENT_TIMEOUT`) for each chart + 1 minute + `DEPLOY_STABILIZE_TIMEOUT` + the blocking `post-deploy` hook timeouts + 1 minute.
  - The budget of a rollback is: the `pre-rollback` hook timeouts + one helm timeout for each chart + 1 minute + `DEPLOY_STABILIZE_TIMEOUT`.
  - The budget of a stop or clean is: one helm timeout + 1 minute.
  - The wait for a concurrency slot (`MAX_CONCURRENT_DEPLOYS`) has no limit. So the deploy manager moves the deadline to "now + the run budget (without the hooks before the slot) + 5 minutes" when the operation gets its slot.
  - `deadline_at` uses the clock of the replica. The leader compares it with its own clock. A skew of seconds does not matter for a margin of minutes.
- **Recovery worker** (leader, `interrupted-operation-recovery`). At the start of each term and every minute, it lists the running deploy logs that passed `deadline_at` and whose process identity has no heartbeat for 2 minutes (database time).
  - The query excludes the own process and processes with a fresh heartbeat in SQL (`NOT EXISTS` on `replica_heartbeats`). So the logs of live replicas cannot fill the batch of 100.
  - The log update checks the heartbeat again. The worker never ends an operation of a process with a fresh heartbeat, or of its own process.
  - Logs without an identity or a deadline (written before migration 54) are not changed. Use Stop for them.
- **Late writers.** Every final write of a deploy log is conditional (`WHERE id = ? AND status = 'running'`, `models.ErrDeployLogNotRunning`). When a slow replica finishes an operation after the recovery ended it, the replica writes neither the log nor the instance. It sends no status message, hook event, or notification (it writes a warning in the log).
- **End of an operation.** In one transaction, the deploy log gets `error` with `Interrupted: the server that ran this operation stopped. Deploy again.` (`Stop again.` or `Clean again.` for a stop or clean).
  - When the instance is still in progress and no newer log exists, the instance gets `error` with the same message, and `post_deploy_hook_until` is cleared.
  - The instance update requires the status and `updated_at` that the worker read (optimistic lock; `stack_instances` has no version column). When another process changed the instance in between, nothing changes, and the next check tries again.
  - Known gap: `stack_instances` has no version column, and the other writers (k8s status watcher, TTL reaper, handlers) use a plain update. A writer that read the instance before the recovery and writes after it can overwrite the recovered `error` status.
  - Then the worker writes an audit entry (action `interrupted`, user `system`), logs a warning, and broadcasts the `deployment.status` message (fan-out reaches all replicas). It sends the error notification of the action (`deployment.error`, `rollback.error`, `stop.error`, or `clean.error`) to the owner and the followers. No hook event fires.
- **Heartbeat cleanup.** The worker deletes heartbeat rows older than 1 hour. A missing row counts as a stopped process.

## Observability

`telemetry.Init` sets up OTLP (OpenTelemetry Protocol) traces and metrics (`OTEL_*`, `METRICS_ENABLED`).

- The HTTP metrics middleware is active when `OTEL_ENABLED` or `METRICS_ENABLED` is true. The tracing middleware and the span enrichment are active only when `OTEL_ENABLED` is true.
- HTTP metrics and auth outcome counters come from the middleware. Database pool metrics and business KPIs come from `telemetry`.
- `RedactWSToken` removes the WebSocket `?token=` query parameter before logging and tracing.
- The Helm chart can deploy an OpenTelemetry collector (`otel.enabled`) and a ServiceMonitor (`metrics.serviceMonitor.enabled`).
- Local development: `make dev-otel` starts a collector, Tempo, Prometheus (`:9090`), and Grafana (`:3001`).

## Outbound Notifications

`devops` and `admin` users register webhook notification channels (`/api/v1/admin/notification-channels`, guarded by `RequireDevOps`) with per-event subscriptions. `notifier` dispatches lifecycle events to them and records `NotificationDeliveryLog` rows. A test-send endpoint checks a channel. In-app notifications stay for each user.

## Design Decisions

| # | Decision | Reason |
|---|----------|-----------|
| 1 | MySQL (GORM) | A reliable relational store with migration support |
| 2 | Monaco Editor for YAML | Developers know it (it is the editor of VS Code) |
| 3 | Service-level Git tokens | Simpler than a token for each user. An `admin` configures it once. |
| 4 | URL-based provider detection | No provider configuration for each chart. The application reads the URL. |
| 5 | Separate StackTemplate entity | Cleaner than an overloaded StackDefinition. It has separate permissions, publishing, and versioning. |
| 6 | Locked values and required charts | `devops` users set guardrails. Developers customize inside them. |
| 7 | Hooks instead of built-in features | Organization-specific operations (database refresh, chat messages, gates) stay out of the core. |
| 8 | No company-specific hard coding | All branding comes from environment variables and configuration, for use by many organizations. |
| 9 | Automatic namespace name | `stack-{name}-{owner}` prevents collisions in shared clusters. |
| 10 | JWT for both login flows | OIDC exchanges its token for a local JWT. There is one middleware path. |
| 11 | Chart source allowlist and namespace-scoped RBAC | Helm runs with the backend identity. These controls limit what a chart can reach. |
