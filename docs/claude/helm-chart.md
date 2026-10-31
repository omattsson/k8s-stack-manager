# Helm Chart

Reference for Claude Code and other agents. The root `CLAUDE.md` links here. Read this file before you work in this area.

## Helm Chart (Kubernetes Deployment)

The Helm chart is in `helm/k8s-stack-manager/`. `Chart.yaml` holds the chart version and the app version.
- The chart deploys the backend and the frontend.
- It can also deploy a bundled MySQL and an OpenTelemetry collector.
- `values.yaml` has toggles for the workload kind, the ingress type, the secrets source and observability.

### Prerequisites
- Kubernetes cluster with a configured `kubectl` context
- Helm 3 or later
- Argo Rollouts controller, only if `argoRollouts.enabled=true`: `kubectl create namespace argo-rollouts && kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml`
- Traefik with CRDs, only if `ingress.type=traefik`
- External Secrets Operator, only if `externalSecrets.enabled=true`
- Kubernetes 1.30 or later, only if `backend.rbac.admissionPolicy.enabled=true` (default)

### Chart Structure

```
helm/k8s-stack-manager/
  Chart.yaml                              # Chart metadata (version, appVersion; source of truth)
  values.yaml                             # All configurable values
  tests/                                  # Values files for `make helm-test`: backend-extra, branding, exec, external-secrets, namespace-label, port-forward
  templates/_helpers.tpl                  # Reusable named templates
  templates/NOTES.txt                     # Post-install instructions
  templates/analysis-template.yaml        # Argo AnalysisTemplate for canary steps
  templates/ingress.yaml                  # Standard Ingress (ingress.type=ingress)
  templates/backend/
    configmap.yaml                        # Non-secret environment variables
    secret.yaml                           # Secret environment variables (skipped when externalSecrets.enabled)
    serviceaccount.yaml, clusterrole.yaml # Identity and RBAC (see "Namespace-scoped RBAC"); clusterrole.yaml also holds the ValidatingAdmissionPolicy
    leader-election-rbac.yaml             # Role and RoleBinding: leases get, create, update (backend.leaderElection.enabled)
    deployment.yaml                       # Used when argoRollouts.enabled=false (default)
    rollout.yaml                          # Argo Rollout, canary 20%, 50%, 80% (argoRollouts.enabled=true)
    service.yaml, service-canary.yaml     # Stable and canary services
    hpa.yaml, pdb.yaml                    # Autoscaling (off by default), PodDisruptionBudget (on)
    hooks-configmap.yaml                  # Hook subscriptions file (hooks.enabled)
    servicemonitor.yaml                   # Prometheus Operator scrape (metrics.serviceMonitor.enabled)
  templates/grafana-dashboard.yaml        # ConfigMap with dashboards/k8s-stack-manager.json for a Grafana dashboard sidecar (metrics.grafanaDashboard.enabled)
  dashboards/k8s-stack-manager.json       # Grafana dashboard of the backend metrics (API, auth, deploys, hooks, clusters, WebSocket, DB pool, runtime)
  templates/frontend/
    configmap.yaml                        # nginx.conf (SPA only)
    branding-configmap.yaml               # Branding files served at /branding/ (branding.files, branding.binaryFiles)
    serviceaccount.yaml, deployment.yaml, rollout.yaml, service.yaml, service-canary.yaml, hpa.yaml, pdb.yaml
  templates/mysql/                        # Bundled MySQL (mysql.enabled, default true): deployment, service, pvc, configmap, secret, networkpolicy
  templates/otel/                         # OpenTelemetry collector (otel.enabled): collector-configmap, collector-deployment, collector-service
  templates/external-secrets/             # SecretStore and ExternalSecret (externalSecrets.enabled)
  templates/traefik/                      # IngressRoute, StripPrefix and secure-headers Middleware, Traefik Service (ingress.type=traefik)
```

### Key Design Decisions
- **Deployments by default, Argo Rollouts opt-in.** `argoRollouts.enabled=true` swaps the Deployments for Rollouts. A Rollout uses a canary strategy and an AnalysisTemplate. Weight-based traffic splitting needs `ingress.type=traefik`.
- **Three ingress modes** (`ingress.type`):
  - `traefik`: IngressRoute. It routes `/api`, `/ws`, `/health` and `/swagger` to the backend, and `/` to the frontend.
  - `ingress`: standard Ingress, any controller.
  - `none`: use port-forward.
- **Traefik priority.** `ingress.traefik.priority` (default `10000`) is the router priority of every route. The frontend route `/` gets `priority - 1`, so the backend routes win over `/`. The value `0` uses the Traefik default (rule length).
- **nginx in the frontend pod** serves the SPA only by default: the chart ConfigMap (`templates/frontend/configmap.yaml`) has no API proxy, and the ingress does all routing. `frontend.apiProxy.enabled` (default false, for installations without an ingress such as `make demo`) adds a `location ~ ^/(api|ws|health|swagger)(/|$)` that proxies to the backend Service with the WebSocket upgrade headers (`map $http_upgrade $connection_upgrade`), `Host $http_host` (the same-origin CORS check) and `X-Forwarded-For` (the chart's `TRUSTED_PROXIES` trust the pod IPs), timeouts 3700s. `make helm-test` checks it off by default and on. Note: `frontend/nginx.conf` in the image (Docker Compose) proxies `/api/` and `/ws` to the backend; the chart replaces it.
- **Bundled MySQL** is for dev and small installs (`mysql.enabled`, optional PVC). For production, point `backend.env.DB_*` at an external database.
- **Secrets** come from a Secret or from External Secrets Operator. `externalSecrets.enabled` renders a SecretStore and an ExternalSecret and skips the chart Secret.
- **ConfigMap and Secret checksums** in pod annotations restart the pods on a config change.
- **Security contexts.** The backend runs as non-root (uid 65532) with a read-only root file system. The frontend drops all capabilities.
- **Observability.** `otel.enabled` deploys a collector. `metrics.enabled` exposes Prometheus metrics. `metrics.serviceMonitor.enabled` adds a ServiceMonitor. `metrics.grafanaDashboard.enabled` adds the Grafana dashboard as a ConfigMap (label `grafana_dashboard: "1"`). Histograms in seconds need explicit buckets (`metric.WithExplicitBucketBoundaries`): the OpenTelemetry defaults (0 to 10000) are for milliseconds.
- **Namespace-scoped RBAC.** `backend.rbac.scope: namespace` is the default.
  - The backend (and Helm, which runs with its service account) has rights on namespaced objects only in stack namespaces.
  - The RoleBinding `k8s-stack-manager-access` in each stack namespace binds the service account to the ClusterRole `<fullname>-stack-access`.
  - Cluster-wide, the backend keeps: nodes (read), namespaces (manage), pods (read), RoleBinding writes, and `bind` on the stack-access ClusterRole and on the ClusterRoles of `backend.namespaceRoleBindings`.
  - The chart sets `STACK_ACCESS_CLUSTER_ROLE` and `STACK_ACCESS_SERVICE_ACCOUNT` (`<namespace>/<name>`) for this mode. The backend applies the RoleBinding with `k8s.AccessBinding` (see "Namespace access binding" in `docs/claude/domain.md`).
  - A ValidatingAdmissionPolicy (`backend.rbac.admissionPolicy.enabled`, Kubernetes 1.30 or later) limits the backend. It may write Roles and RoleBindings only in namespaces with `managed-by=k8s-stack-manager`. It may create only namespaces with that label. It may change or delete only namespaces whose name starts with `stack-` or that have the label.
  - When `WILDCARD_TLS_SOURCE_NAMESPACE` and `WILDCARD_TLS_SOURCE_SECRET` are set, a Role gives `get` on that one Secret.
  - `backend.rbac.scope: cluster` keeps the old rules (namespaced objects in all namespaces).
  - Exec (`backend.exec.enabled`): with scope `namespace`, `stack-access` already grants `*/*/*` in stack namespaces (`pods/exec` and `serviceaccounts` `get`, which the service-account-token check reads), so the chart adds no rule. With scope `cluster`, the backend ClusterRole keeps `pods/exec` `create` unconditionally (as in chart 0.6 and earlier: Helm runs as the backend, which has no escalate or bind verb in this scope, so a stack chart Role that grants `pods/exec` is refused unless the backend holds it) and adds `get` (client-go WebSocket transport) only when the feature is on. `serviceaccounts` `get` is part of the cluster-scope namespaced rule. The `-backend-stream-scope` policy below renders when port-forward OR exec is on.
  - Port-forward (`backend.portForward.enabled`): with scope `namespace`, `stack-access` already grants `*/*/*` in stack namespaces, so the chart adds no rule. With scope `cluster`, the backend ClusterRole gets `pods/portforward` (`get`, `create`) only when the feature is on. In scope `cluster` the policy `<fullname>-backend-stream-scope` (only with the admission policy toggle and port-forward or exec on) limits CONNECT on `pods/exec` and `pods/portforward` by the backend service account to namespaces with `managed-by=k8s-stack-manager`. It reads the label from `namespaceObject` and fails closed. It only tests the label: the namespace of the backend is protected by the Go guard (`POD_NAMESPACE`) and because it has no `managed-by` label. When the feature is on and the cluster does not serve `admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy` (`.Capabilities.APIVersions`), the render fails with a message to upgrade Kubernetes or set `admissionPolicy.enabled=false` knowingly. With the feature off, no stream policy renders, so upgrades on Kubernetes below 1.30 keep working. The namespace-scope policy has no such check.
- **Leader election.** `backend.leaderElection.enabled` (default true) and `backend.leaderElection.leaseName` (Lease `<fullname>-workers`) set `LEADER_ELECTION_ENABLED` and `POD_NAME` / `POD_NAMESPACE` (downward API, Deployment and Rollout). It also creates the Lease Role. Only the leader replica runs the background workers. Every replica serves HTTP and WebSocket.
- **WebSocket fan-out.** `backend.wsFanout.enabled` (default true) sets `WS_FANOUT_ENABLED`. `backend.wsFanout.pollInterval` (500ms) and `retention` (5m) set `WS_FANOUT_POLL_INTERVAL` and `WS_FANOUT_RETENTION`. The replicas share WebSocket messages through the `ws_events` table. Clients on every replica get deploy logs and leader events.
- **Log retention.** `backend.logRetention` (`deployLogDays` 180, `auditLogDays` 365, `notificationDays` 90, `deliveryLogDays` 30, `actionRunDays` 180, `interval` 1h) sets `DEPLOY_LOG_RETENTION_DAYS`, `AUDIT_LOG_RETENTION_DAYS`, `NOTIFICATION_RETENTION_DAYS`, `DELIVERY_LOG_RETENTION_DAYS`, `ACTION_RUN_RETENTION_DAYS` and `LOG_RETENTION_INTERVAL`. The leader worker `log-retention` deletes older rows. `0` keeps the rows forever.

### Configuration
Key values in `values.yaml`:
- `argoRollouts.enabled`: Rollouts (canary) instead of Deployments
- `backend.env.*`: non-secret environment variables (ConfigMap)
- `backend.secrets.*`: secret environment variables, for example `JWT_SECRET` (required), `ADMIN_PASSWORD`, `KUBECONFIG_ENCRYPTION_KEY`, `DB_PASSWORD` (Secret or ExternalSecret)
- `backend.replicas`, `frontend.replicas`, `*.autoscaling`, `*.pdb`: independent scaling
- `backend.allowedChartRepos`: chart repository URL prefixes (`ALLOWED_CHART_REPOS`). Empty means all sources.
- `backend.uniqueInstanceNames`: unique instance names for each cluster (`UNIQUE_INSTANCE_NAMES`, default `false`)
- `backend.maxInstanceTtlDays`: maximum TTL and stop-time distance in days (`MAX_INSTANCE_TTL_DAYS`, default `30`, 1 to 365; another value fails the render)
- `backend.helmHistoryMax`: Helm revisions kept for each release (`HELM_HISTORY_MAX`, `--history-max`). Default `null`: not rendered, so Helm's own default applies (`HELM_MAX_HISTORY`, else 10). A value below 2 or not a whole number fails the render; a whole float (values file, `--set-json`) is converted to an integer first. An explicit `backend.env.HELM_HISTORY_MAX` wins.
- `backend.trustedProxies`: proxies that may set the client IP (`TRUSTED_PROXIES`, default the private IPv4 ranges and `fc00::/7`)
- `backend.portForward.*` (`enabled` false, `maxDuration` 1h, `idleTimeout` 15m, `maxPerUser` 5) and `backend.streams.allowApiKeys` (false): `STACK_PORT_FORWARD_*` and `STACK_STREAM_ALLOW_API_KEYS`. The per-cluster switch `allow_port_forward` must also be on. Limits are per replica. nginx-ingress needs `proxy-read-timeout` and `proxy-send-timeout` annotations of at least `maxDuration` (the frontend image proxy uses 3700s for the 1h default). Traefik passes the WebSocket upgrade.
  - The backend ConfigMap renders the five variables. An explicit `backend.env` key of the same name wins. `POD_NAMESPACE` (downward API) is the namespace that the namespace guard refuses.
  - `make helm-test` renders `tests/port-forward.yaml` (cluster scope, feature on) and checks the five variables, `pods/portforward` in the ClusterRole, the stream admission policy, that no `pods/portforward` rule and no stream policy render when the feature is off, and that the render fails without the ValidatingAdmissionPolicy API (`--api-versions` passes it where needed).
- `backend.exec.*` (`enabled` false, `allowedContainers` [], `maxDuration` 10m, `maxCommandLength` 4096, `maxPerUser` 3, `allowServiceAccountToken` false): `STACK_EXEC_ENABLED`, `STACK_EXEC_ALLOWED_CONTAINERS` (comma-joined), `STACK_EXEC_MAX_DURATION`, `STACK_EXEC_MAX_COMMAND_LENGTH`, `STACK_EXEC_MAX_PER_USER`, `STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN`. Exec shows the container environment including secrets: keep the allowed list short. The per-cluster switch `allow_exec` must also be on; API keys follow `backend.streams.allowApiKeys`. A name in `allowedContainers` that is not a DNS-1123 label fails the render. An explicit `backend.env` key of the same name wins.
  - `make helm-test` renders `tests/exec.yaml` (cluster scope, feature on) and checks the defaults, the six variables, the joined list, the failing invalid name, `pods/exec` `get` and `create` in the ClusterRole with exec on (only `create` with it off), the stream policy with exec alone, the render failure without the ValidatingAdmissionPolicy API, and that no stream policy renders with both features off and no `pods/exec` rule with scope `namespace`.
- `backend.rbac.scope` (`namespace` or `cluster`), `backend.rbac.admissionPolicy.enabled`: backend RBAC (see Key Design Decisions)
- `backend.priorityClassName`, `frontend.priorityClassName`, `mysql.priorityClassName`, `otel.priorityClassName`: PriorityClass of each pod; empty = cluster default.
- `backend.namespaceRoleBindings`: RoleBindings that the deployer applies to every stack namespace (`NAMESPACE_ROLE_BINDINGS_JSON`)
- `backend.namespaceLabels`: map of extra stack namespace labels, rendered as sorted `NAMESPACE_LABELS`. The chart renders it only when it is not empty. The keys `managed-by` and `k8s-stack-manager.io/instance-name` fail the render.
- `mysql.enabled`, `mysql.auth.*`, `mysql.persistence.enabled`: bundled database
- `ingress.type`, `ingress.host`, `ingress.traefik.*` (`priority`), `ingress.className`, `ingress.tls`: ingress settings
- `backend.env.APP_PUBLIC_URL`: public https URL of the web UI for the links in notification channel cards. Default: `https://<ingress.host>` when `ingress.host` is set and the ingress has TLS; a set value wins.
- `otel.enabled`, `metrics.enabled`, `metrics.serviceMonitor.enabled`, `metrics.grafanaDashboard.enabled`: observability
- `hooks.enabled`, `hooks.subscriptions`, `hooks.actions`: outbound webhook subscriptions and custom actions (see `EXTENDING.md`)
- `branding.title`, `branding.logoUrl`, `branding.faviconUrl`: set `APP_TITLE`, `APP_LOGO_URL`, `APP_FAVICON_URL` in the backend ConfigMap
- `branding.files`, `branding.binaryFiles`: ConfigMap that the frontend nginx serves at `/branding/` (nosniff header, sandbox CSP, names `^[-._a-zA-Z0-9]+$`, at most 900 KiB in total)
- `externalSecrets.enabled`, `externalSecrets.secretStore.*`: External Secrets Operator
