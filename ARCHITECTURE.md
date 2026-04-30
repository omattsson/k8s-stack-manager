# Architecture

Go (Gin) + React (TypeScript/Vite/MUI) web application for managing multi-service Helm stacks on Kubernetes. MySQL (GORM) persistence, JWT auth with optional OIDC, multi-cluster deployment.

See [WIKI.md](WIKI.md) for user-facing concepts, [EXTENDING.md](EXTENDING.md) for the hooks/actions extension system, and [README.md](README.md) for setup and screenshots.

## System Overview

```
┌──────────────────────────────────────────────────────────────┐
│                    Frontend (React 19)                        │
│  MUI · Vite · Monaco Editor · WebSocket · OIDC (PKCE)       │
├──────────────────────────────────────────────────────────────┤
│                   Backend (Go 1.25 + Gin)                     │
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
│   ├── handlers/     # HTTP handlers (one file per resource)
│   ├── middleware/    # auth, audit, role, rate-limiter
│   └── routes.go     # All route registration
├── auth/             # OIDC provider, state store
├── cluster/          # ClusterRegistry, health poller, quota monitor, secret refresher
├── config/           # Env-based config loading
├── database/         # GORM repositories (one per model)
├── deployer/         # Helm deploy/undeploy manager, expiry stopper, cleanup executor
├── gitprovider/      # Azure DevOps + GitLab branch listing, URL detection, cache
├── helm/             # Values deep-merge, template variable substitution
├── hooks/            # Event dispatcher, action routing, HMAC signing
├── k8s/              # K8s client, namespace status, watcher, pod exec, scaling
├── models/           # GORM model structs
├── scheduler/        # Cleanup policy scheduler (cron)
└── telemetry/        # OpenTelemetry setup, DB metrics
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

Supporting models: `User`, `AuditLog`, `DeploymentLog`, `UserFavorite`, `Notification`, `RefreshToken`, `ApiKey`, `CleanupPolicy`, `ResourceQuota`, `InstanceQuotaOverride`, `SharedValues`, `TemplateVersion`.

## Values Merge Precedence

Lowest to highest priority:

1. Shared values (cluster-scoped, by priority)
2. Template default values
3. **Template locked values** (cannot be overridden by anything below)
4. Definition default values
5. Instance value overrides

Template variables (`{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`) are substituted at export time.

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

## Authentication

- **Local**: username/password → bcrypt → JWT
- **OIDC**: Authorization Code Flow with PKCE → ID token → JIT user provisioning → local JWT
- Role hierarchy: `admin` > `devops` > `user`
- DevOps manages templates; admin manages clusters, users, cleanup policies

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
