# Configuration

The environment variables of the backend, and the authentication of the Git providers. The Helm chart sets most of them through `backend.env` and `backend.secrets` (see [Deployment](deployment.md)). See the [README](../README.md) for an overview and the [documentation map](../README.md#documentation).

The table shows the key environment variables. The full list is in `backend/internal/config/config.go`, `docker-compose.yml`, and the Helm chart `values.yaml`.

| Variable | Required | Description |
|---|---|---|
| `JWT_SECRET` | Yes | JWT signing secret (minimum 16 characters) |
| `ADMIN_USERNAME` | No | Username of the initial admin user (default: `admin`) |
| `ADMIN_PASSWORD` | Yes | Password of the initial admin user. The backend creates the user at startup when it does not exist. |
| `SELF_REGISTRATION` | No | Let any signed-in user register new `user` accounts (default: `false`, also in the Helm chart; Docker Compose sets `true` for development). With `false`, only `admin` users register users. |
| `OIDC_ENABLED` and `OIDC_*` | No | OpenID Connect single sign-on. See [OIDC single sign-on](../WIKI.md#authentication). |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | No | MySQL connection. Set them for your database (defaults: `localhost`, `3306`, `root`, empty, `app`). The Helm chart sets them for the bundled MySQL. |
| `AZURE_DEVOPS_PAT` | No | Azure DevOps personal access token (PAT) |
| `AZURE_DEVOPS_AUTH` | No | `pat` (default) or `workload-identity`. With `workload-identity`, the backend lists Azure DevOps branches with an Entra ID token from `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and `AZURE_FEDERATED_TOKEN_FILE`, not with a PAT. |
| `GITHUB_TOKEN` | No | GitHub fine-grained personal access token. It requires `GITHUB_ALLOWED_REPOSITORIES`. See [GitHub authentication](#github-authentication). |
| `GITHUB_ALLOWED_REPOSITORIES` | With `GITHUB_TOKEN` | Comma-separated `owner/repository` entries that may use the token (exact match) |
| `GITLAB_TOKEN` | No | GitLab access token |
| `GITLAB_BASE_URL` | No | Origin of a self-hosted GitLab (default: `https://gitlab.com`). See [GitLab custom origins](#gitlab-custom-origins). |
| `DEFAULT_BRANCH` | No | Default Git branch (default: `master`) |
| `KUBECONFIG_ENCRYPTION_KEY` | No | Passphrase. The backend derives an AES-256 key from it (SHA-256) to encrypt kubeconfig data, registry passwords, notification channel secrets, and notification channel webhook URLs. Without it, the backend rejects a cluster that has kubeconfig data or a registry password, and a channel that has a secret (400). It stores channel webhook URLs in plaintext, so set the key when a URL holds a token. Use a kubeconfig path or in-cluster configuration for clusters then. |
| `ALLOWED_CHART_REPOS` | No | Comma-separated chart repository URL prefixes (`oci://`, `https://`, `http://`). Empty: every chart source is allowed. |
| `NAMESPACE_LABELS` | No | Extra labels for stack namespaces (`key=value,key=value`) |
| `UNIQUE_INSTANCE_NAMES` | No | `true`: an instance name must be unique on its cluster (409 on create, rename, clone and quick deploy). Use it when Ingress hosts use `{{.InstanceName}}`. Default: `false` |
| `DEFAULT_INSTANCE_TTL_MINUTES` | No | TTL in minutes of a new instance that does not set one. The create forms start with it. Default: `0` (no expiry); the Helm chart sets `480`. |
| `MAX_INSTANCE_TTL_DAYS` | No | Maximum TTL, and the latest fixed stop time from now, in days (1 to 365). Create, update, clone, quick deploy and extend refuse more (400). The web UI limits its TTL field and date picker with it. `DEFAULT_INSTANCE_TTL_MINUTES` must fit the limit, or the start fails. Default: `30`. Helm: `backend.maxInstanceTtlDays`. |
| `HELM_HISTORY_MAX` | No | Helm revisions kept for each release (`--history-max` on deploy and rollback). Each revision is a Secret in the stack namespace (Helm's default storage). A whole number of `2` or more; another value stops the start. Default: not set, so Helm's own default applies (its `HELM_MAX_HISTORY` environment variable, else `10`). Helm: `backend.helmHistoryMax`. |
| `CORS_ALLOWED_ORIGINS` | No | Allowed CORS origins: `*` (default; development only), `same-origin` (no cross-origin requests; Helm chart default) or a comma-separated list. The web UI on the same host always passes. |
| `RATE_LIMIT` / `LOGIN_RATE_LIMIT` | No | Requests per minute for each IP address (defaults: `100` / `10`) |
| `TRUSTED_PROXIES` | No | Comma-separated IPs or CIDR ranges of the proxies that may set the client IP with `X-Forwarded-For` or `X-Real-IP`. The per-IP rate limits count that client IP. Default: empty (no proxy is trusted; the client IP is the TCP peer). The Helm chart trusts the private IPv4 ranges and `fc00::/7`. An invalid entry, `0.0.0.0/0` or `::/0` stops the start. |
| `ENABLE_SWAGGER` | No | Serve the Swagger UI at `/swagger` (default: `false`) |
| `SESSION_STORE` | No | Session store backend: `mysql` (default) or `memory` |
| `ACCESS_TOKEN_EXPIRATION` | No | Lifetime of an access token (default: `15m`) |
| `SESSION_IDLE_TIMEOUT` | No | End a session after this time without requests (default: `30m`) |
| `SESSION_MAX_LIFETIME` | No | Maximum session time from login. A refresh does not extend it (default: `12h`) |
| `REFRESH_REUSE_GRACE` | No | A just-rotated refresh token still gets an access token for this time. This supports refreshes from several browser tabs. `0` disables it (default: `30s`). |
| `SECURE_COOKIES` | No | Set the `Secure` flag on the refresh-token cookie. Use `true` behind HTTPS (default: `false`). The Helm chart sets `true` when the ingress has TLS. |
| `APP_PUBLIC_URL` | No | Public https base URL of the web UI, for example `https://stacks.example.com`. Notification channels then get `instance_url` and Teams cards a "Stack Manager" button. An http URL, a same-origin path, a query or a fragment stops the start (default: empty). The Helm chart sets `https://<ingress.host>` when the ingress has TLS. |
| `LEADER_ELECTION_ENABLED` | No | Elect one replica (Kubernetes Lease) to run the background workers. Needs the in-cluster service account. Default: `false`, which means this process always runs the workers. The Helm chart sets `true`. |
| `LEADER_ELECTION_LEASE_NAME` | No | Name of the Lease (default: `k8s-stack-manager-workers`) |
| `LEADER_ELECTION_NAMESPACE` | No | Namespace of the Lease (default: `POD_NAMESPACE`, else the namespace of the service account) |
| `LEADER_ELECTION_LEASE_DURATION`, `LEADER_ELECTION_RENEW_DEADLINE`, `LEADER_ELECTION_RETRY_PERIOD` | No | Election timings (defaults: `15s`, `10s`, `2s`) |
| `WS_FANOUT_ENABLED` | No | Share WebSocket messages between replicas through the `ws_events` table (default: `false`). The Helm chart sets `true`. |
| `STACK_PORT_FORWARD_ENABLED` | No | Allow port-forward to stack services through the backend (default: `false`). The cluster also needs `allow_port_forward`. |
| `STACK_PORT_FORWARD_MAX_DURATION`, `STACK_PORT_FORWARD_IDLE_TIMEOUT` | No | Longest life and idle time of a tunnel (defaults: `1h`, `15m`; both above 0 and at most `24h`; the idle time at most the maximum duration) |
| `STACK_PORT_FORWARD_MAX_PER_USER` | No | Open tunnels per user and backend replica (default: `5`, 1 to 100) |
| `STACK_EXEC_ENABLED` | No | Allow exec in stack containers through the backend (default: `false`). The cluster also needs `allow_exec`. |
| `STACK_EXEC_ALLOWED_CONTAINERS` | No | Comma-separated container names (DNS-1123 labels, exact, no wildcard) that exec may use. Empty (default): every request is refused with `container_not_allowed`; the backend logs a warning when exec is on. |
| `STACK_EXEC_MAX_DURATION` | No | Longest life of an exec session (default: `10m`, above 0 and at most `24h`) |
| `STACK_EXEC_MAX_COMMAND_LENGTH` | No | Largest sum of the argument bytes of a command (default: `4096`, 1 to 32768) |
| `STACK_EXEC_MAX_PER_USER` | No | Open exec sessions per user and backend replica (default: `3`, 1 to 100) |
| `STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN` | No | Allow exec in a container that holds a service-account token (default: `false`; `true` logs a warning at start) |
| `STACK_STREAM_ALLOW_API_KEYS` | No | Let API keys open stream tunnels (default: `false`; API keys have no scopes) |
| `WS_FANOUT_POLL_INTERVAL`, `WS_FANOUT_RETENTION` | No | Poll interval and row retention of the fan-out (defaults: `500ms`, `5m`) |
| `DEPLOY_LOG_RETENTION_DAYS`, `AUDIT_LOG_RETENTION_DAYS`, `NOTIFICATION_RETENTION_DAYS`, `DELIVERY_LOG_RETENTION_DAYS`, `ACTION_RUN_RETENTION_DAYS` | No | Days that the log tables keep their rows (defaults: `180`, `365`, `90`, `30`, `180`). `ACTION_RUN_RETENTION_DAYS` applies to `action_runs`, the run history of custom actions. `0` keeps the rows forever. The leader deletes older rows every `LOG_RETENTION_INTERVAL` (default `1h`). The deploy counts of the Analytics page cover only the kept deploy logs. |
| `APP_TITLE` | No | Product name in the browser tab, sidebar, app bar, login page, and setup wizard (default: `K8s Stack Manager`, maximum 100 characters) |
| `APP_LOGO_URL` | No | Logo image: a same-origin path (`/branding/logo.svg`) or an `https://` URL (default: empty, which means the built-in logo) |
| `APP_FAVICON_URL` | No | Browser tab icon. The same URL rule applies (default: `/favicon.svg`). Other schemes (`http:`, `javascript:`, `data:`) stop the startup with an error. |

See [Sessions](../WIKI.md#sessions) for the way the session limits work together. See [Branding](../WIKI.md#branding) for the branding settings (Helm values `branding.*`).

## GitHub authentication

Public repositories work without a token. Leave `GITHUB_TOKEN` and `GITHUB_ALLOWED_REPOSITORIES` empty. GitHub then applies its lower rate limit for requests without authentication.

For private repositories or a higher rate limit, use a fine-grained personal access token:

- Give the token access only to the repositories in `GITHUB_ALLOWED_REPOSITORIES`, and only the permission **Contents: read**.
- Set an expiry date. Replace the token before it expires.
- `GITHUB_ALLOWED_REPOSITORIES` is a comma-separated list of `owner/repository` names. The match is exact and ignores case. Owners, wildcards, prefixes, and URLs are not accepted.
- A request for a repository that is not in the list gives HTTP 403.
- The backend does not start when `GITHUB_TOKEN` is set and the list is empty.
- GitHub App authentication is not available.

> **Upgrade: GitHub tokens.** If an installation sets `GITHUB_TOKEN`, set `GITHUB_ALLOWED_REPOSITORIES` before you update. Otherwise the new backend pods do not start. Installations without `GITHUB_TOKEN` are not affected.

The backend follows a GitHub redirect only when it keeps the repository path, or when it goes to the `/repositories/{id}` path of a renamed or moved repository. Any other redirect is an upstream failure (HTTP 503), so the token never goes to another repository path.

## GitLab custom origins

`GITLAB_BASE_URL` is optional. The default is `https://gitlab.com`.

- The value must be an absolute HTTP(S) origin: a scheme, a host name, and an optional port.
- Credentials, query strings, fragments, paths (for example `/api/v4`), and a host name that ends with a dot are not accepted. The backend removes spaces around the value and a trailing slash, and adds `/api/v4`.
- For Helm, set the value in `backend.env.GITLAB_BASE_URL`. It is not secret. Do not put it in `backend.secrets` or External Secrets.
- With a custom origin, only repository URLs on that origin use the GitLab provider. HTTP(S) URLs must also use the same port. `gitlab.com` URLs are then not supported.
- With an `http://` origin and a token, the backend logs a warning at startup, because the token goes over the network without encryption.

> **Upgrade: GitLab.** The backend checks `GITLAB_BASE_URL` at startup and does not start when the value is not valid. A GitLab that uses a relative URL root (for example `https://example.com/gitlab`) is not supported. Branch listing did not work for it before. Check the value before you update. Repository URLs with a query string or a fragment are not accepted for any provider. Host names that end with a dot (for example `gitlab.com.`) are not supported.
