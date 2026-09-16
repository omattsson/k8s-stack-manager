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
├── models/           # GORM model structs + repository interfaces
├── notifier/         # In-app notifications + outbound notification channels
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

Supporting models: `User`, `AuditLog`, `DeploymentLog`, `UserFavorite`, `Notification`, `NotificationPreference`, `NotificationChannel` (+ `NotificationChannelSubscription`, `NotificationDeliveryLog`), `RefreshToken`, `APIKey`, `CleanupPolicy`, `ResourceQuotaConfig`, `InstanceQuotaOverride`, `SharedValues`, `TemplateVersion`, `TemplateSnapshot`.

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
- **Rate limits**: per-IP limiter on `/api/v1` (`RATE_LIMIT`, default 100/min) and a stricter login limiter (`LOGIN_RATE_LIMIT`, default 10/min).

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
