# K8s Stack Manager — Wiki

## Concepts

### Stack Template
A reusable blueprint created by DevOps engineers. Contains a set of Helm chart configurations with default values. Templates can be **published** for developers to use, and individual chart values can be **locked** to prevent modification.

### Stack Definition
A concrete collection of Helm chart configurations. Created by instantiating a template or from scratch. Owns the chart configs (chart name, repository, version, default values).

### Stack Instance
A developer's working copy of a stack definition. Each instance has:
- An **owner** (the developer)
- A **branch** (Git branch for the deployment)
- **Value overrides** per chart (merged on top of chart defaults)
- An auto-generated **namespace** (`stack-{instance-name}-{owner}`)

### Value Override
Per-chart configuration overrides on a stack instance. Deep-merged with chart defaults during Helm values export. Template variables (`{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, etc.) are substituted at export time.

### Audit Log
Every mutating API call (POST, PUT, DELETE) is recorded with user, action, entity type, entity ID, and timestamp.

### Cluster
A registered Kubernetes cluster that stack instances can be deployed to. Each cluster stores connection details (kubeconfig path or encrypted kubeconfig data) and is monitored via periodic health checks. One cluster can be designated as the **default** target. Clusters are managed by admins through `/admin/clusters`.

Clusters can optionally store **container registry credentials** (`registry_url`, `registry_username`, `registry_password`, `image_pull_secret_name`) for automatic image pull secret provisioning. When configured, a `kubernetes.io/dockerconfigjson` secret is created in each stack namespace before chart installs and refreshed periodically (every 4 hours) by a background service to handle short-lived tokens (e.g. ACR). The registry password is encrypted at rest using the same AES-GCM scheme as kubeconfig data.

## Architecture

### Data Flow
```
Template → (instantiate) → Definition + ChartConfigs → (create instance) → Instance + ValueOverrides
```

### Storage
- **MySQL** (GORM) — the sole data store for all domain entities
- Factory in `internal/database/repository.go` initializes the repository at startup

### Authentication
- JWT-based with `Authorization: Bearer <token>` header
- Role hierarchy: `admin` > `devops` > `user`
- Admin can register users; self-registration is configurable

### Stack Instance Permissions

| Operation | Owner | `admin` | `devops` | Other `user` |
|---|---|---|---|---|
| View the instance, status, pods, deploy log, access URLs, export values, compare, clone | yes | yes | yes | yes |
| Deploy, deploy preview, stop, clean, delete, rollback, run actions | yes | yes | yes | no (403) |
| Edit the instance, extend the TTL | yes | yes | yes | no (403) |
| Read or change value, branch and quota overrides | yes | yes | yes | no (403) |

- A refused request returns 403 before any side effect: no hook, no status change, no deploy-log entry, no Helm call. The backend logs the refused attempt.
- The web UI hides the lifecycle buttons and shows the page read-only for a user who cannot modify the instance.
- A clone always belongs to the user who creates it.
- The override restriction limits who can *edit* through the override endpoints. It is not a secrecy control: the merged values (export, compare, deploy-log values) and a clone still contain the override values. Do not put secrets in value overrides; use Kubernetes Secrets or an external secret store.

### Sessions

A login (local or SSO) starts a session. The session has one refresh token at a time. Each refresh replaces (rotates) the refresh token. All refresh tokens of one session form a family. The access token carries the session ID in the `sid` claim.

| Variable | Default | Effect |
|---|---|---|
| `ACCESS_TOKEN_EXPIRATION` | `15m` | Lifetime of one access token. The client then calls `POST /api/v1/auth/refresh`. |
| `REFRESH_TOKEN_EXPIRATION` | `168h` | Upper limit for one refresh token. `SESSION_MAX_LIFETIME` usually ends the session first. |
| `SESSION_IDLE_TIMEOUT` | `30m` | The session ends when no request arrives for this time. Must be at least `ACCESS_TOKEN_EXPIRATION`. |
| `SESSION_MAX_LIFETIME` | `12h` | The session ends this long after the login, also when the user is active. Must be at least `ACCESS_TOKEN_EXPIRATION`. |
| `REFRESH_REUSE_GRACE` | `30s` | Time in which a just-rotated refresh token still gets a new access token. `0` disables the grace. Maximum `5m`. |
| `SECURE_COOKIES` | `false` | Sets the `Secure` flag on the refresh-token cookie. Set it to `true` behind HTTPS. The Helm chart sets it when the ingress has TLS. |

How the limits work together:

- Idle time counts from the last authenticated request, not from the last refresh. Each request with a session access token updates the session activity (at most one database write per minute per session). API-key requests do not change sessions.
- A refresh never extends the session. A new refresh token expires at the earlier of `now + REFRESH_TOKEN_EXPIRATION` and `login time + SESSION_MAX_LIFETIME`.
- After `SESSION_MAX_LIFETIME` the user must log in again. An SSO login reads the role and the account state from the identity provider again. So a role change in the identity provider applies at the latest after `SESSION_MAX_LIFETIME` plus one `ACCESS_TOKEN_EXPIRATION`.
- Two browser tabs can refresh at the same time with the same cookie. The second request presents a token that the first request just rotated. Inside `REFRESH_REUSE_GRACE` it gets a new access token and no new cookie. Nothing is revoked.
- Any other reuse of a used refresh token revokes the session family (replay protection). Other sessions of the user stay active. A token revoked by logout never gets the grace.
- An access token stays valid until it expires, also when the session idles out. Revocation (see below) uses the token blocklist instead.

Note: The application has no API to change the role of a user. For SSO users the identity provider controls the role; the role syncs at each SSO login.

Exceptions to the session limits:

- **CLI token from the SSO CLI login** (`stackctl login` through the browser): a long-lived access token (`JWT_EXPIRATION`, default 24h) without a session. `SESSION_IDLE_TIMEOUT` and `SESSION_MAX_LIFETIME` do not apply. It keeps the role it had at the login until it expires. Revoking the user (table below) still rejects it.
- **API keys** have no session. They use the user's current role in the database and stay valid until they expire or are revoked.
- **WebSocket** traffic (`/ws`) does not count as activity for the idle limit. A tab that only receives live updates idles out after `SESSION_IDLE_TIMEOUT`.
- **Lost refresh response:** if the server rotates the refresh token but the browser never gets the response (network drop, client timeout), the browser keeps the used cookie. A retry within `REFRESH_REUSE_GRACE` still works, but sets no new cookie. The next refresh after the grace window counts as a replay and ends the session; the user must log in again. This fails safe by design.

### Revoking a User

| Action | Access tokens issued before | Refresh tokens | API keys |
|---|---|---|---|
| Delete the user | rejected (401) | revoked | deleted |
| Disable the user | rejected (401) | revoked | kept, but rejected while the user is disabled |
| Reset the password | rejected (401) | revoked | kept |

A new login after a password reset works at once. Only tokens issued before the action are rejected. Enabling a disabled user does not bring back the tokens issued before the disable.

Known gap: the WebSocket connection (`/ws`) does not check the blocklists yet (#466).

### Multi-Cluster
- Clusters are registered via the API with a kubeconfig path or kubeconfig data (encrypted at rest with AES-GCM)
- `ClusterRegistry` manages per-cluster Kubernetes and Helm clients
- A health poller periodically checks cluster connectivity and updates status
- Stack instances target a specific cluster (or the default cluster)
- The `deployer` package routes deploy/undeploy/status operations through the registry to the correct cluster
- Per-cluster container registry credentials enable automatic image pull secret provisioning; a `SecretRefresher` runs every 4 hours to keep tokens current

### Git Integration
- Auto-detects provider from repository URL (`dev.azure.com` → Azure DevOps, `gitlab.com` → GitLab)
- Branch listing with in-memory caching (5-minute TTL)
- Service-level tokens (PAT/token), not per-user

### Helm Values
- Deep merge: chart defaults ← instance overrides
- Template variable substitution: `{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`
- Export as YAML

## Development

### Prerequisites
- Docker and Docker Compose
- Go 1.26+ (backend)
- Node.js 22+ (frontend)

### Running
```bash
make dev              # Full stack via Docker Compose
make dev-local        # Backend only, local
```

### Testing
```bash
make test             # All unit tests
make test-backend-all # Backend unit + integration (starts MySQL)
make test-e2e         # End-to-end with Playwright
```

### Adding a New API Resource
See `.github/instructions/api-extension.instructions.md` for the step-by-step guide.

## Troubleshooting

- **JWT errors**: Ensure `JWT_SECRET` is set and at least 16 characters.
- **Database connection errors (MySQL)**: Ensure MySQL is running. Check `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD` environment variables.
- **Git provider errors**: Check `AZURE_DEVOPS_PAT` (or `AZURE_DEVOPS_AUTH=workload-identity` with the `AZURE_*` variables) or `GITLAB_TOKEN` are set correctly. Empty tokens are valid (provider just won't be available).
- **Cluster connection errors**: Verify the kubeconfig path or data is valid. Use the "Test Connection" button on the Clusters admin page. If `KUBECONFIG_ENCRYPTION_KEY` is set, all kubeconfig data is encrypted at rest — changing the key will make existing encrypted data unreadable.
