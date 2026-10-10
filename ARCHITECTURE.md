# Architecture

Go (Gin) + React (TypeScript/Vite/MUI) web application for managing multi-service Helm stacks on Kubernetes. MySQL (GORM) persistence, JWT auth with optional OIDC, multi-cluster deployment.

See [WIKI.md](WIKI.md) for user-facing concepts, [EXTENDING.md](EXTENDING.md) for the hooks/actions extension system, and [README.md](README.md) for setup and screenshots.

## System Overview

```
┌──────────────────────────────────────────────────────────────┐
│                    Frontend (React 19)                        │
│  MUI · Vite · Monaco Editor · WebSocket · OIDC (PKCE)       │
├──────────────────────────────────────────────────────────────┤
│                   Backend (Go 1.26 + Gin)                     │
│  REST API · JWT · Swagger · Audit Middleware · OTel           │
├──────┬──────────┬──────────┬──────────┬──────────┬───────────┤
│MySQL │ Cluster  │ Git      │ Helm     │ Hook     │ K8s       │
│(GORM)│ Registry │ Provider │ Values   │ Dispatch │ Deployer  │
│      │ (multi)  │ (AzDO+GL)│ (merge)  │ (events) │ (helm)   │
└──────┴──────────┴──────────┴──────────┴──────────┴───────────┘
```

## Package Structure

```
backend/internal/
├── api/
│   ├── handlers/     # HTTP handlers (one file per resource; rate limiter lives here)
│   ├── middleware/    # auth, combined auth (JWT + API key), audit, role, security headers, HTTP + auth metrics, OTel span enrichment, WS token redaction
│   └── routes/       # routes.go — all route registration + middleware order
├── auth/             # OIDC provider (PKCE) — OIDC/CLI state persisted in sessionstore
├── cache/            # Generic in-memory TTL cache
├── cluster/          # ClusterRegistry, health poller, quota monitor, secret refresher
├── config/           # Env-based config loading
├── database/         # GORM repositories (one per model), migrations
├── deployer/         # Helm deploy/undeploy/rollback manager, expiry stopper, cleanup executor
├── gitprovider/      # Azure DevOps + GitLab branch listing, URL detection, cache
├── health/           # Liveness/readiness checks
├── helm/             # Values deep-merge, template variable substitution
├── hooks/            # Event dispatcher, action routing, HMAC signing
├── k8s/              # K8s client, namespace status, watcher, pod exec, scaling
├── leader/           # Leader election (Kubernetes Lease) + leader worker group
├── models/           # GORM model structs + repository interfaces
├── notifier/         # In-app notifications + outbound notification channels
├── replica/          # Process identity + heartbeat (replica_heartbeats)
├── scheduler/        # Cleanup policy scheduler (cron)
├── sessionstore/     # Token blocklist + OIDC state (MySQL or memory)
├── telemetry/        # OpenTelemetry setup, DB pool metrics, business metrics
├── ttl/              # Instance expiry reaper
└── websocket/        # Hub, clients, message types
```

## Data Model

Core entity relationships:

```
StackTemplate ──1:N──▶ TemplateChartConfig
      │                       │
      │ (instantiate)         │ (copies to)
      ▼                       ▼
StackDefinition ──1:N──▶ ChartConfig
      │
      │ (create instance)
      ▼
StackInstance ──1:N──▶ ValueOverride (per chart)
      │           └──▶ ChartBranchOverride (per chart)
      │
      └──▶ Cluster (deployment target)
```

Supporting models: `User`, `AuditLog`, `DeploymentLog`, `ReplicaHeartbeat`, `UserFavorite`, `Notification`, `NotificationPreference`, `NotificationChannel` (+ `NotificationChannelSubscription`, `NotificationDeliveryLog`), `RefreshToken`, `APIKey`, `CleanupPolicy`, `ResourceQuotaConfig`, `InstanceQuotaOverride`, `SharedValues`, `TemplateVersion`, `TemplateSnapshot`.

## Values Merge Precedence

Lowest to highest priority (`internal/helm/values_generator.go`):

1. Shared values (cluster-scoped, applied in priority order, lowest first)
2. Chart default values (from the definition's `ChartConfig`; copied from the template on instantiate)
3. Instance value overrides (per chart)
4. **Template locked values** (applied last; nothing can override them)

Template variables (`{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`) are substituted by `ValuesGenerator` whenever merged values are produced — deploy, preview and export.

## Multi-Cluster

`ClusterRegistry` in `internal/cluster/` manages per-cluster K8s and Helm clients. Lazy-initialized on first use. Background services per cluster:

- **Health poller** — periodic API server ping, broadcasts status via WebSocket
- **Quota monitor** — tracks namespace resource usage against ResourceQuota
- **Secret refresher** — rotates container registry pull secrets every 4h (for short-lived tokens like ACR)

Kubeconfig data encrypted at rest with AES-256-GCM (`KUBECONFIG_ENCRYPTION_KEY`).

## Deployment Pipeline

1. User triggers deploy → `POST /api/v1/stack-instances/:id/deploy`
2. `pre-deploy` hook fires (subscribers can abort with `failure_policy: fail`)
3. `deployer.Manager` resolves cluster via `ClusterRegistry`
4. Creates namespace if needed, provisions image pull secrets
5. `helm upgrade --install` per chart in deploy-order sequence
6. `post-deploy` hook fires
7. `k8s.Watcher` polls namespace for pod/deployment status
8. Status updates broadcast via WebSocket

`GET /api/v1/stack-instances/:id/deploy-preview` returns the merged values without deploying. `POST /api/v1/stack-instances/:id/rollback` reverses step 5 per chart and fires `pre-rollback`, then `rollback-completed` on either outcome and `post-rollback` only on success. Other dispatched hook events: `deploy-finalized`, `deploy-timeout`, `pre/post-instance-create`, `pre/post-instance-delete`, `stop-completed`, `clean-completed`, `delete-completed`. The constants `instance-created`, `stack-expiring`, `stack-expired`, `quota-warning`, `secret-expiring`, `cleanup-policy-executed` are defined but not yet fired, and `pre/post-namespace-create` are reserved (see `backend/docs/hooks.md` and `EXTENDING.md`).

## Authentication

- **Local**: username/password → bcrypt → JWT
- **OIDC**: Authorization Code Flow with PKCE → ID token → JIT user provisioning → local JWT
- Role hierarchy: `admin` > `devops` > `user`
- DevOps manages templates; admin manages clusters, users, cleanup policies
- **SessionStore**: Persistent token blocklist and OIDC state (MySQL default, in-memory for tests). Survives restarts — revoked tokens stay blocked, in-flight OIDC logins survive backend redeploys.
- **User.Disabled**: Admin can disable accounts. Blocks login, token refresh, OIDC login, and API key auth immediately.
- **Sessions**: a login starts a refresh-token family (`refresh_tokens.family_id`, also the access-token `sid` claim). Rotation keeps the family and its start time; a session ends after `SESSION_MAX_LIFETIME` or after `SESSION_IDLE_TIMEOUT` without requests (the JWT middleware records request activity, throttled to one write per minute per session). A just-rotated token gets an access token inside `REFRESH_REUSE_GRACE`; other reuse revokes the family. See [WIKI.md](WIKI.md#sessions).
- **Rate limits**: per-IP limiter on `/api/v1` (`RATE_LIMIT`, default 100/min) and a stricter login limiter (`LOGIN_RATE_LIMIT`, default 10/min).

## Replica Model

The backend can run with more than one replica. Every replica serves the HTTP API, the WebSocket hub and the WebSocket revalidation, and runs the deploy, stop, clean and rollback goroutines of the requests it handles. Only one replica, the leader, runs the background workers:

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
| Interrupted operation recovery, heartbeat cleanup | leader |
| HTTP API, WebSocket hub, revalidation and fan-out poller | every replica |
| Replica heartbeat (`replica_heartbeats`) | every replica |

- **Election**: `internal/leader` wraps client-go leader election with a `coordination.k8s.io/v1` Lease (`LEADER_ELECTION_LEASE_NAME`, default `k8s-stack-manager-workers`) in the pod namespace (`LEADER_ELECTION_NAMESPACE`, else `POD_NAMESPACE`, else the service account namespace file). The identity is `POD_NAME` (else the host name). Timings: lease 15s, renew deadline 10s, retry 2s (`LEADER_ELECTION_LEASE_DURATION`, `_RENEW_DEADLINE`, `_RETRY_PERIOD`). `LEADER_ELECTION_ENABLED=false` (default; docker-compose and local development) makes the process always the leader. With `true`, the backend needs the in-cluster service account configuration, or startup fails. The Helm chart enables it and creates a Role and RoleBinding for `leases` (get, create, update).
- **Terms**: `leader.Group` starts every worker with a term context (`Run(ctx)`, which can run again after it returned). When the term ends, the group cancels the context and waits for the workers (30s limit; a next term waits for workers that did not stop), and the replica campaigns again; it does not exit. A watchdog ends the term when the lease was not renewed for the renew deadline. The workers then stop at most renew deadline + retry period / 2 after the last renewal, which is before the lease expires for the other replicas (lease duration), when the workers stop within that margin (about 4s with the defaults). The backend releases the lease itself (client-go `ReleaseOnCancel` is off), and only after the workers stopped: on SIGTERM, on a watchdog stop and after a failed renewal. When the workers did not stop within the stop timeout, the backend does not release the lease; the other replicas wait for the lease duration. A Conflict on the release means that another replica already holds the lease. Another replica then takes over after about one retry period.
- **Cleanup scheduler**: only the leader has an active cron scheduler. A policy change on another replica does not start cron jobs there (`Reload` does nothing when the scheduler is not active); the leader reads the enabled policies when it becomes leader and every minute, and a scheduled run reads the policy again before it runs. When it becomes leader, the scheduler runs a policy once if its last scheduled time is within the last 5 minutes and after its last run (or its creation), so a run that fell into a leader change is not lost (cron expressions only, not `@every`; the dry-run flag applies). When the term ends during a run, the run stops before the next instance: no audit entry for the remaining instances, no `last_run_at` update and no summary notification. A run that the term end cancels after its actions but before it saved `last_run_at` can run again on the new leader (catch-up). Each run checks the condition again, so an instance that is already stopped or cleaned no longer matches a status condition, and stop and clean are idempotent. The catch-up looks only at scheduled times strictly before the time read before the cron instance starts, so the cron instance and the catch-up never run the same scheduled time. Manual runs (`POST /admin/cleanup-policies/:id/run`) work on every replica.
- **State in the database**: the expiry warner marks `stack_instances.expiry_warned_at` with a conditional update before it sends the warning: `expiry_warned_at IS NULL` and `expires_at` within 1 second of the listed value (the database can store less precision than Go). So a new leader or a short overlap of two leaders sends one warning. A repository `Update` never writes the column. In the same transaction it clears the column when the stored `expires_at` and the new value differ by 1 second or more (deploy, extend, TTL change), or when one of them is NULL and the other is not.
- **State in memory**: the quota monitor and the secret monitor keep their warning cooldowns in memory. After a leader change, the new leader can repeat one quota or secret warning.
- **k8s status cache**: the status watcher cache is on the leader only. On other replicas `GET /stack-instances/:id/status` reads the cluster directly.
- **Observability**: gauge `stackmanager_leader` (1 on the leader); a log line on each leadership change. Readiness does not depend on leadership.
- **WebSocket fan-out**: the hub is in memory per replica; the `ws_events` table shares its messages (`WS_FANOUT_ENABLED`; the Helm chart enables it, docker-compose and local development keep it off). Each hub send (`Broadcast`, `BroadcastToInstance`, `BroadcastToUser`) and each revocation (`DisconnectUser`, `DisconnectToken`) acts on the local clients at once and queues one row (target `all`, `instance:<id>`, `user:<id>`, `revoke:user:<id>` or `revoke:token:<jti>`). The origin is the replica identity (`POD_NAME`, else the host name) plus a random suffix per process; the leader election keeps the plain identity. A writer goroutine inserts the queued rows in batches (up to 200 rows or about 1 MiB). The queue holds 4096 rows; when it is full, or a message is larger than 512 KiB, the message reaches only the local clients (counter `websocket.fanout.events_dropped_total`, rate-limited warning). A hub send never waits for the database. Every replica polls `id > last id` every `WS_FANOUT_POLL_INTERVAL` (500ms; batches of 500, more pages at once when a batch is full) and delivers the rows of other origins to its clients; it skips its own rows. A revocation row from another replica closes the local sockets and writes no new row; a user revocation closes only sockets whose token was issued at or before the row time (the rule of the session store user block), so a session opened after the revoke stays open. Rows older than 30 seconds (for example after a database outage) are skipped (`websocket.fanout.events_skipped_total{reason="stale"}`). The age compares the clock of the writing replica (`created_at`) with the clock of the reading replica, so the pods need synchronized clocks (NTP on the nodes); a skew of more than a few seconds drops live updates. Alert on a rising `events_skipped_total{reason="stale"}`. So events of the leader workers (status, cluster health), deploy logs and revocations reach all replicas, with a delay of up to about one poll interval. The leader deletes rows older than `WS_FANOUT_RETENTION` (5m) every minute, in batches of 5000 rows. Disabled: no `ws_events` reads or writes.
- **Fan-out ordering**: MySQL assigns an auto-increment id at insert time, but the row becomes visible at commit. A row of one replica with a lower id can become visible after a row of another replica with a higher id was read. The poller records each id it skipped over and reads these ids again on each poll for 10 seconds. A late row is delivered when it appears (once); the gap of a rolled-back insert expires. Gaps larger than 1000 ids are not tracked (log line). One writer per replica inserts its batches one after the other, so the rows of one replica stay in order; only rows of different replicas can arrive out of order. At start the poller begins at `MAX(id)` (no replay) and also tracks the ids in `(MAX(id) - 1000, MAX(id)]` that are not visible yet: each replica has at most one uncommitted batch of 200 rows, so 1000 ids cover 5 other writers. Every 20th empty poll checks `MAX(id)`: when it is above 0 but below the last id, the ids restarted (table recreated, or an auto-increment reset of an empty table at a MySQL restart). The poller logs a warning and reads from id 0 again; the 30-second filter drops the old rows. Database errors in the poller are logged (rate limited) and the next tick tries again.
- **Binary log**: `ws_events` inserts and deletes go to the MySQL binary log like all writes. MySQL 8 (the bundled MySQL) keeps binary logs for 30 days by default (`binlog_expire_logs_seconds`), so deploy log lines add to the binary log size on the MySQL volume for that time. Plan the volume size, or set a shorter expiry in `mysql.config`.
- **Interrupted operations**: a deploy, rollback, stop or clean (and a blocking `post-deploy` hook) runs in a goroutine on the replica that took the request. See [Interrupted operations](#interrupted-operations).
- **Per-replica WebSocket state**: the token and user revalidation stays per replica as a fallback (see [WIKI.md](WIKI.md#revoking-a-user)): it closes sockets whose revocation row was dropped, skipped or written while fan-out was off. In-app notifications go only to the sockets of the notified user (`BroadcastToUser`).

### Interrupted operations

A replica that stops without a clean shutdown (SIGKILL, OOM kill, node loss) leaves its operations in progress: the instance stays `deploying`, `stabilizing`, `stopping` or `cleaning`, and the deploy log stays `running`. The leader ends these operations.

- **Process identity**: each backend process has an identity: the replica identity (`POD_NAME`, else the host name) plus a random suffix of 8 hex characters per process. A restarted process gets a new identity, also when the host name stays (docker-compose). The deploy manager stores the identity on each deploy log (`deployment_logs.replica_id`) when the operation starts. The identity has the same form as the fan-out origin, but a different suffix.
- **Heartbeat**: every process (not only the leader, and also with `LEADER_ELECTION_ENABLED=false`) writes its row in `replica_heartbeats` (`id`, `last_seen`) at start and every 30 seconds. A clean shutdown deletes the row after the deploy manager stopped. A process without a heartbeat for 2 minutes counts as stopped. The database server clock writes and compares `last_seen` (`UTC_TIMESTAMP(3)`), so the replica clocks do not matter for the heartbeat check. Heartbeat writes share the normal database connection pool: a pool that stays saturated (or a database outage) for more than 2 minutes can stop the heartbeat of a live replica. The deadline (below) keeps normal operations safe in that case.
- **Deadline**: the deploy manager also stores `deployment_logs.deadline_at` when the operation starts: the start time plus the time budget of the operation plus 5 minutes. The budget of a deploy is the `pre-deploy` hook timeouts + one helm timeout (`DEPLOYMENT_TIMEOUT`) per chart + 1 minute + `DEPLOY_STABILIZE_TIMEOUT` + the blocking `post-deploy` hook timeouts + 1 minute. A rollback: the `pre-rollback` hook timeouts + one helm timeout per chart + 1 minute + `DEPLOY_STABILIZE_TIMEOUT`. A stop or clean: one helm timeout + 1 minute. The wait for a concurrency slot (`MAX_CONCURRENT_DEPLOYS`) has no limit, so the deploy manager moves the deadline to now + the run budget (without the hooks before the slot) + 5 minutes when the operation gets its slot. `deadline_at` uses the clock of the replica; the leader compares it with its own clock (a skew of seconds does not matter for a margin of minutes).
- **Recovery worker** (leader, `interrupted-operation-recovery`): at the start of each term and every minute, it lists the running deploy logs that passed `deadline_at` and whose process identity has no heartbeat for 2 minutes (database time). The query excludes the own process and processes with a fresh heartbeat in SQL (`NOT EXISTS` on `replica_heartbeats`), so the logs of live replicas cannot fill the batch of 100. The log update checks the heartbeat again. It never ends an operation of a process with a fresh heartbeat, or of its own process. Logs without an identity or a deadline (written before migration 54) are not changed; use Stop for them.
- **Late writers**: every final write of a deploy log is conditional (`WHERE id = ? AND status = 'running'`, `models.ErrDeployLogNotRunning`). When a slow replica finishes an operation after the recovery ended it, the replica writes neither the log nor the instance, and it sends no status message, hook event or notification (a warning in the log).
- **End of an operation**: in one transaction, the deploy log gets `error` with `Interrupted: the server that ran this operation stopped. Deploy again.` (`Stop again.` / `Clean again.` for a stop or clean). When the instance is still in progress and no newer log exists, the instance gets `error` with the same message and `post_deploy_hook_until` is cleared. The instance update requires the status and `updated_at` that the worker read (optimistic lock; `stack_instances` has no version column). When another process changed the instance in between, nothing changes and the next check tries again. Known gap: `stack_instances` has no version column, and the other writers (k8s status watcher, TTL reaper, handlers) use a plain update. A writer that read the instance before the recovery and writes after it can overwrite the recovered `error` status. Then the worker writes an audit entry (action `interrupted`, user `system`), logs a warning, broadcasts the `deployment.status` message (fan-out reaches all replicas) and sends the error notification of the action (`deployment.error`, `rollback.error`, `stop.error` or `clean.error`) to the owner and the followers. No hook event fires.
- **Heartbeat cleanup**: the worker deletes heartbeat rows older than 1 hour. A missing row counts as a stopped process.

## Observability

`telemetry.Init` sets up OTLP traces and metrics (`OTEL_*`, `METRICS_ENABLED`). HTTP metrics middleware is active when `OTEL_ENABLED` or `METRICS_ENABLED` is true; tracing middleware and span enrichment only when `OTEL_ENABLED`. HTTP metrics and auth outcome counters come from middleware; DB pool and business KPIs from `telemetry`. `RedactWSToken` removes the WebSocket `?token=` query param before logging and tracing. The Helm chart can deploy an OTel collector (`otel.enabled`) and a ServiceMonitor (`metrics.serviceMonitor.enabled`). Local: `make dev-otel` (Prometheus `:9090`, Grafana `:3001`).

## Outbound Notifications

DevOps and admin users register webhook notification channels (`/api/v1/admin/notification-channels`, guarded by `RequireDevOps`) with per-event subscriptions. `notifier` dispatches lifecycle events to them and records `NotificationDeliveryLog` rows; a test-send endpoint validates a channel. In-app notifications stay per user.

## Design Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | MySQL (GORM) | Reliable relational store with migration support |
| 2 | Monaco Editor for YAML | Familiar to developers (VS Code's editor) |
| 3 | Service-level Git tokens | Simpler than per-user PATs; admin configures once |
| 4 | URL-based provider detection | No per-chart provider config needed; just look at the URL |
| 5 | Separate StackTemplate entity | Cleaner than overloading StackDefinition; separate permissions, publishing, versioning |
| 6 | Locked values + required charts | DevOps enforces guardrails; devs customize within bounds |
| 7 | Hooks over built-in features | Organization-specific ops (DB refresh, Slack, gates) stay out of core |
| 8 | No company-specific hardcoding | All branding via env/config for multi-org use |
| 9 | Namespace auto-generated | `stack-{name}-{owner}` prevents collisions in shared clusters |
| 10 | JWT for both auth flows | OIDC exchanges for a local JWT — single middleware path |
