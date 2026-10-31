# API

The route groups of the REST API. The Swagger UI (`/swagger`, when `ENABLE_SWAGGER=true`) and `backend/docs/swagger.json` have every route. [stackctl](https://github.com/omattsson/stackctl) is the supported command-line client. See the [README](../README.md) for an overview and the [documentation map](../README.md#documentation).

Swagger documents all routes. All routes need authentication, except `/health/*`, `/api/v1/ping`, `/api/v1/ui-config`, and the login, refresh, logout, and OIDC routes. The Items handlers (`handlers/items.go`) are the reference implementation of a CRUD handler; no route registers them.

| Group | Prefix | Description |
|-------|--------|-------------|
| Auth | `/api/v1/auth` | Login, register, current user, refresh, logout, logout-all |
| OIDC | `/api/v1/auth/oidc` | OIDC configuration, authorize, callback, CLI login |
| Templates | `/api/v1/templates` | Template CRUD, publish, unpublish, instantiate, clone, versions, bulk operations |
| Quick Deploy | `/api/v1/templates/:id/quick-deploy` | One-click template deployment |
| Definitions | `/api/v1/stack-definitions` | Definition CRUD, chart configs, import, export, upgrade |
| Instances | `/api/v1/stack-instances` | Instance CRUD, clone, deploy, deploy preview, stop, clean, rollback, status, logs, compare, extend TTL, follow, custom actions, bulk operations |
| Port-forward | `/api/v1/stack-instances/:id/port-forward` | WebSocket tunnel to a service port of the instance (`service`, `port`); owner, `devops`, `admin`; off by default |
| Exec | `/api/v1/stack-instances/:id/exec` | WebSocket that runs one command in a container of a chart of the instance and streams its output (`chart`, optional `container`); owner, `devops`, `admin`; off by default |
| Overrides | `/api/v1/stack-instances/:id/overrides` | Per-chart value overrides |
| Branch Overrides | `/api/v1/stack-instances/:id/branches` | Per-chart branch overrides |
| Quota Overrides | `/api/v1/stack-instances/:id/quota-overrides` | Per-instance resource quota override |
| Git | `/api/v1/git` | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Filterable audit trail (`devops` or `admin`) and export (`admin`) |
| Users | `/api/v1/users` | User management (`admin`): list, delete, disable, enable, password reset, role change |
| API Keys | `/api/v1/users/:id/api-keys` | API key management |
| Admin | `/api/v1/admin` | Orphaned namespaces (`admin`), notification channels (`devops`) |
| Cleanup Policies | `/api/v1/admin/cleanup-policies` | Cron-based cleanup policy management (`admin`) |
| Clusters | `/api/v1/clusters` | Cluster registration, health, test connection, quotas, utilization |
| Shared Values | `/api/v1/clusters/:id/shared-values` | Per-cluster shared Helm values (`admin`) |
| Analytics | `/api/v1/analytics` | Usage overview and template statistics (`devops`), user statistics (`admin`) |
| Dashboard | `/api/v1/dashboard` | Overview for the landing page |
| Notifications | `/api/v1/notifications` | List, mark read, count, preferences |
| Favorites | `/api/v1/favorites` | Bookmarks |
| UI Config | `/api/v1/ui-config` | Public branding values |
| Health | `/health/*` | Liveness and readiness |
| WebSocket | `/ws` | Real-time status and log events |
