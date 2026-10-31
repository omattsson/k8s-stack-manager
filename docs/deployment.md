# Deployment

How to install K8s Stack Manager on Kubernetes with the Helm chart, with or without the web UI. For a first installation, start with the [Getting Started guide](getting-started.md). See the [README](../README.md) for an overview and the [documentation map](../README.md#documentation).

## Helm Chart (Kubernetes Deployment)

The Helm chart in `helm/k8s-stack-manager/` deploys the backend, the frontend, and (optional) a bundled MySQL and an OpenTelemetry collector. The chart has these options:

- **Workload kind.** The chart creates Deployments by default. Set `argoRollouts.enabled=true` to create Argo Rollouts with a canary strategy (20%, 50%, 80%) instead.
- **Ingress type.** `ingress.type` is `traefik` (default), `ingress` (a standard Ingress for any controller), or `none` (use port-forward).
- **Secrets.** Secrets come from a chart Secret or from the External Secrets Operator.

### Prerequisites

- A Kubernetes cluster with a configured `kubectl` context. Kubernetes 1.30 or later is needed for the default ValidatingAdmissionPolicy (`backend.rbac.admissionPolicy.enabled`).
- Helm 3 or later.
- MySQL 8.0 or later, when you set `mysql.enabled=false` (external database).
- [Traefik](https://traefik.io/) with its CRDs, because the default `ingress.type` is `traefik`. If you do not use Traefik, set `ingress.type=ingress` or `ingress.type=none`.
- The [Argo Rollouts](https://argoproj.github.io/argo-rollouts/) controller, only when `argoRollouts.enabled=true`. Weight-based traffic splitting also needs `ingress.type=traefik`.
- The [External Secrets Operator](https://external-secrets.io/), only when `externalSecrets.enabled=true`.

### Install

Note: The chart needs a value for `backend.secrets.JWT_SECRET` when it renders. Set it with `--set`, with a values file, or with the `JWT_SECRET` environment variable for the `make` targets. The `make helm-install` and `make helm-upgrade` targets stop when `JWT_SECRET` is not set.

```bash
# Lint the chart
make helm-lint

# Install (the JWT_SECRET environment variable is required)
JWT_SECRET=my-secret-at-least-16-chars make helm-install

# Or install with custom values
helm install k8s-stack-manager helm/k8s-stack-manager \
  --namespace k8s-stack-manager --create-namespace \
  --set backend.secrets.JWT_SECRET=my-secret-at-least-16-chars \
  --set ingress.host=stacks.example.com

# Upgrade after a change
JWT_SECRET=my-secret-at-least-16-chars make helm-upgrade

# Uninstall
make helm-uninstall
```

Warning: Change the placeholder values `backend.secrets.ADMIN_PASSWORD` and `mysql.auth.rootPassword`. Keep `backend.env.CORS_ALLOWED_ORIGINS` at `same-origin` (default), or list the other origins that call the API from a browser. The notes after `helm install` warn you about each placeholder.

Note: The chart sets `SELF_REGISTRATION` to `"false"`: only `admin` users register users. Set `backend.env.SELF_REGISTRATION` to `"true"` to let any signed-in user register new `user` accounts. The notes after `helm install` then show a warning.

Upgrade note: Earlier chart versions set `CORS_ALLOWED_ORIGINS` to `"*"` and `SELF_REGISTRATION` to `"true"`. After an upgrade, a browser client on another origin gets 403, and only `admin` users register users. To keep the old behaviour, set these values explicitly: list the other origins in `backend.env.CORS_ALLOWED_ORIGINS` (or `"*"`), and set `backend.env.SELF_REGISTRATION: "true"`.

### Security settings of the chart

| Value | Default | Effect |
|---|---|---|
| `backend.allowedChartRepos` | `[]` | Chart repository URL prefixes that definitions and templates may use. Empty allows every chart source. |
| `backend.rbac.scope` | `namespace` | `namespace`: the backend has namespaced rights only in stack namespaces (RoleBinding `k8s-stack-manager-access`). `cluster`: the namespaced rights apply in all namespaces (the rules of chart 0.6 and earlier). Use `cluster` only when stack charts need cluster-scoped objects. |
| `backend.rbac.admissionPolicy.enabled` | `true` | Create a ValidatingAdmissionPolicy that limits the backend to stack namespaces (Kubernetes 1.30 or later). |
| `backend.exec.enabled` | `false` | Exec in stack containers through the backend. With `rbac.scope: cluster` the chart adds `pods/exec` `get` to the ClusterRole (`create` on `pods/exec` stays granted for stack chart Roles) and renders the stream-scope ValidatingAdmissionPolicy while port-forward or exec is on. With `rbac.scope: namespace` it adds no rule. |
| `backend.exec.allowedContainers` | `[]` | Container names that exec may use (DNS-1123 labels, exact; an invalid name fails the render). Empty: every exec request is refused. |
| `backend.exec.maxDuration`, `maxCommandLength`, `maxPerUser` | `10m`, `4096`, `3` | Longest life of a session (up to `24h`), largest sum of the argument bytes (1 to 32768) and sessions per user and replica (1 to 100) |
| `backend.exec.allowServiceAccountToken` | `false` | Allow exec in a container that holds a service-account token (the backend logs a warning at start) |
| `backend.portForward.enabled` | `false` | Port-forward to stack services through the backend. With `rbac.scope: cluster` the chart adds `pods/portforward` to the ClusterRole and a ValidatingAdmissionPolicy that limits it to stack namespaces. Also `maxDuration` (`1h`), `idleTimeout` (`15m`), `maxPerUser` (`5`, per replica). |
| `backend.streams.allowApiKeys` | `false` | Let API keys open stream tunnels. |
| `backend.namespaceLabels` | `{}` | Extra labels on stack namespaces, for example Pod Security Admission levels. |
| `ingress.traefik.priority` | `10000` | Traefik router priority of all IngressRoute routes. A high value stops another Ingress on the same host from taking a path of the stack manager. The frontend route gets this value minus 1. `0` uses the Traefik default. |

See [Backend rights on the cluster](../WIKI.md#backend-rights-on-the-cluster) and [Chart sources](../WIKI.md#chart-sources) for details.

### External Secrets Operator with Azure Key Vault

Set `externalSecrets.enabled=true`. The [External Secrets Operator (ESO)](https://external-secrets.io/) then creates the backend Secret from Azure Key Vault. In this mode the chart does not render its inline backend Secret. ESO creates the Secret `<fullname>-backend`, which the backend workload uses.

- When the bundled MySQL is on, ESO also creates the MySQL Secret with `MYSQL_ROOT_PASSWORD`. If `mysql.auth.user` is not empty, ESO also needs `MYSQL_PASSWORD`. The chart maps both keys only to the MySQL Secret.
- The backend `DB_PASSWORD` comes from the MySQL credential mapping: `MYSQL_PASSWORD` when `mysql.auth.user` is set, else `MYSQL_ROOT_PASSWORD`.

Before you install:

1. Install ESO with its `external-secrets.io/v1` CRDs.
2. Give the Azure identity read access to the Key Vault secrets.
3. Configure AKS workload identity.
4. For the `SecretStore` that the chart creates, create a federated identity credential for the managed identity. Use the subject `system:serviceaccount:<namespace>:<fullname>-backend`. `<fullname>` is the rendered fullname of the chart.

The fullname for a release called `stack-manager` is `stack-manager-k8s-stack-manager`. When the release name already contains `k8s-stack-manager`, the chart uses the release name. `fullnameOverride` replaces the fullname. The chart adds the annotation `azure.workload.identity/client-id` to the ServiceAccount.

```yaml
# external-secrets-values.yaml
backend:
  env:
    # The exact repositories that the token below may access.
    GITHUB_ALLOWED_REPOSITORIES: example-org/api

externalSecrets:
  enabled: true
  azureKeyVault:
    vaultUrl: https://my-vault.vault.azure.net
    tenantId: 00000000-0000-0000-0000-000000000000
    authType: WorkloadIdentity
    workloadIdentity:
      clientId: 00000000-0000-0000-0000-000000000000
  data:
    - secretKey: JWT_SECRET
      remoteRef:
        key: k8s-stack-manager-jwt-secret
    - secretKey: ADMIN_PASSWORD
      remoteRef:
        key: k8s-stack-manager-admin-password
    - secretKey: GITHUB_TOKEN
      remoteRef:
        key: k8s-stack-manager-github-token
    - secretKey: MYSQL_ROOT_PASSWORD
      remoteRef:
        key: k8s-stack-manager-mysql-root-password
    - secretKey: MYSQL_PASSWORD
      remoteRef:
        key: k8s-stack-manager-mysql-password
```

```bash
helm upgrade --install k8s-stack-manager helm/k8s-stack-manager \
  --namespace k8s-stack-manager --create-namespace \
  --values external-secrets-values.yaml
```

Rules for `externalSecrets.data`:

- Each entry maps a Key Vault secret to an environment key. `JWT_SECRET` is required.
- With `mysql.enabled=true`, the chart needs `MYSQL_ROOT_PASSWORD`. It also needs `MYSQL_PASSWORD` when `mysql.auth.user` is not empty.
- With `mysql.enabled=true`, do not add `DB_PASSWORD`. The chart derives it from the MySQL mapping.
- With `mysql.enabled=false`, add a `DB_PASSWORD` entry with a `remoteRef` to the password of the external database:

```yaml
externalSecrets:
  data:
    - secretKey: DB_PASSWORD
      remoteRef:
        key: k8s-stack-manager-external-db-password
```

- Do not put Key Vault values or Azure credentials in the values file.

To use a store that another team manages, set `externalSecrets.secretStore.create=false`, `externalSecrets.secretStore.kind=ClusterSecretStore` (or `SecretStore`), and `externalSecrets.secretStore.name`. The chart then creates only the `ExternalSecret`. It does not configure the Azure authentication, and it does not annotate the ServiceAccount.

When the remote `GITHUB_TOKEN` changes, ESO updates the Kubernetes Secret. A running backend process does not read the new value. After a rotation, restart the backend Deployment or start an Argo Rollout, unless a restart controller watches the Secret. A change of the remote value does not change the checksum of the chart, because Helm cannot see the remote value.

To use a service principal for a store that the chart creates, set `authType: ServicePrincipal`. Set `externalSecrets.azureKeyVault.servicePrincipal.secretName` to an existing Kubernetes Secret. The Secret must contain the keys `client-id` and `client-secret`. Set `clientIdKey` and `clientSecretKey` in the same block when your keys have other names.

### What the chart deploys

- **Backend.** A Deployment and a Service. With `argoRollouts.enabled=true`, the chart creates an Argo Rollout (canary steps 20%, 50%, 80%), a stable Service, and a canary Service instead. The chart also creates a ServiceAccount with RBAC, a PodDisruptionBudget (on by default), and an optional HPA and ServiceMonitor.
- **Frontend.** A Deployment (or an Argo Rollout) with nginx. nginx serves only the React application. The ingress sends the API requests to the backend. Without an ingress, set `frontend.apiProxy.enabled=true`: nginx then proxies `/api`, `/ws`, `/health` and `/swagger` (WebSocket upgrades included) to the backend Service, so a `kubectl port-forward` to the frontend Service gives the UI and the API on one address. You can switch the frontend off with `frontend.enabled=false`.
- **MySQL.** A bundled MySQL (`mysql.enabled`, default `true`). For production, point `backend.env.DB_*` at an external database.
- **Ingress.** With `ingress.type=traefik`, the chart creates an IngressRoute and a Middleware with secure response headers. The IngressRoute sends `/api`, `/ws`, `/health`, and `/swagger` to the backend, and `/` to the frontend. A Middleware that removes the `/api` prefix is optional (`ingress.traefik.middlewares.stripApiPrefix`, default off). With `ingress.type=ingress`, the chart creates a standard Ingress with the same paths.

See `helm/k8s-stack-manager/values.yaml` for all values.

## Headless / API-only deployment

K8s Stack Manager works without the React UI. The REST API drives every workflow of the dashboard. [stackctl](https://github.com/omattsson/stackctl) is the supported headless client. The options below are independent.

### Why run headless?

- CI/CD pipelines drive deploys, stops, and rollbacks with `stackctl`.
- Air-gapped environments do not need the web UI.
- The installation uses fewer resources: one Deployment less, and no nginx.
- Preview environments start faster.

The frontend authenticates against the backend in the same way as the CLI. When you remove it, you lose no backend function.

### Docker Compose

```bash
# The Makefile target sets the profile for you
make compose-api-only

# Or set the profile with an environment variable (works with any compose subcommand)
COMPOSE_PROFILES=api-only docker compose up
```

`docker-compose.yml` puts the frontend service in the profile `full`. The service runs only when that profile is active. `.env.example` sets `COMPOSE_PROFILES=full`. Remove it or change it to run headless.

Note: If you have an old `.env` file without `COMPOSE_PROFILES=full`, add that line. Otherwise `docker compose up` starts the headless stack. `make dev` and `make prod` always use `--profile full`.

### Helm chart

Set `frontend.enabled=false`. The chart then renders no frontend resources (Deployment or Rollout, Service, ConfigMap, HPA, PDB, ServiceAccount) and no ingress rule for `/`. The backend routes (`/api`, `/ws`, `/health`, `/swagger`) stay.

```bash
# Install from the published Helm repo.
# Use the local chart path helm/k8s-stack-manager only when you have a
# repository checkout, for example for chart development.
helm install stack-manager k8s-stack-manager/k8s-stack-manager \
  --namespace stack-manager --create-namespace \
  --set backend.secrets.JWT_SECRET=my-secret-at-least-16-chars \
  --set frontend.enabled=false \
  --set ingress.host=stacks.example.com
```

### stackctl CLI

When the API is reachable, install stackctl and point it at the API:

```bash
# Install
brew install --cask omattsson/tap/stackctl

# Create a context and point it at the backend
stackctl config use-context my-cluster
stackctl config set api-url https://stacks.example.com
stackctl login            # asks for the username and password
# Or, when your installation has OIDC configured:
stackctl login --sso      # opens a browser (RFC 8252 loopback flow)
```

`stackctl login --sso` uses the OIDC loopback flow. These steps run:

1. The CLI calls the `cli-auth` endpoint of the backend.
2. The CLI opens the returned `login_url` in your default browser.
3. The CLI starts a local HTTP server on `127.0.0.1:<random-port>`.
4. You sign in at the identity provider.
5. The backend redirects the browser (HTTP 302) to the local server of the CLI, with the tokens in the query string.

You do not copy any token. The CLI does not poll, and the frontend is not involved.

After you sign in, each API operation has a CLI command:

```bash
stackctl template list
stackctl stack deploy my-app
stackctl stack watch --id <instance-id>    # real-time WebSocket events
stackctl audit log export --format csv --output-file audit.csv
```

See the [stackctl README](https://github.com/omattsson/stackctl) for all commands.
