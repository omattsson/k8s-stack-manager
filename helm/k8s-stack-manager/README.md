# K8s Stack Manager

**Self-service environments for multi-service Helm applications, on your own Kubernetes clusters.**

Developers start a full copy of a stack (every chart, on their own branch, with their own values) from a template, with one click or one command. Platform teams keep control with roles, quotas, chart allowlists, time limits and an audit log. Developers need no kubeconfig and no access to the cluster.

This chart installs the backend (Go API), the web UI and, by default, a MySQL database. The command-line client is [stackctl](https://github.com/omattsson/stackctl).

- Source and documentation: <https://github.com/omattsson/k8s-stack-manager>
- Getting Started guide: <https://github.com/omattsson/k8s-stack-manager/blob/main/docs/getting-started.md>
- Try it on your machine with kind: `make demo` in the repository

## Prerequisites

- Kubernetes 1.30 or later (for the default ValidatingAdmissionPolicy that keeps the backend inside stack namespaces)
- Helm 3
- An ingress: Traefik with its CRDs for the default `ingress.type=traefik`, any controller for `ingress.type=ingress`, or none (see [Without an ingress](#without-an-ingress))

## Install

Put the secrets in a values file that only you can read, not on the command line. The example generates random secrets and creates the file with mode 0600; it refuses to overwrite an existing file. Replace `stacks.example.com` with a DNS name that points to your ingress:

```bash
(
umask 077
set -o noclobber
cat > stack-manager-values.yaml <<EOF
backend:
  secrets:
    JWT_SECRET: "$(openssl rand -base64 32)"
    ADMIN_PASSWORD: "$(openssl rand -base64 18)"
    KUBECONFIG_ENCRYPTION_KEY: "$(openssl rand -hex 32)"
mysql:
  auth:
    rootPassword: "$(openssl rand -base64 24)"
ingress:
  host: stacks.example.com
EOF
)

helm repo add k8s-stack-manager https://omattsson.github.io/k8s-stack-manager
helm repo update
helm install stack-manager k8s-stack-manager/k8s-stack-manager \
  --namespace stack-manager --create-namespace \
  --values stack-manager-values.yaml
```

The admin password is `ADMIN_PASSWORD` in `stack-manager-values.yaml`.

Set `ingress.host`: with an empty host the routes match every host name, and with the high default Traefik priority they can take traffic from other applications on the same entry point. Configure TLS before you sign in through an ingress that others can reach (`ingress.traefik.tls.secretName`, or `ingress.tls` for `ingress.type=ingress`); without it, the password and the session tokens go over plain HTTP. With Traefik, also set `ingress.traefik.entryPoints` to your HTTPS entry point (for example `[websecure]`): the default is `web`, and a TLS router on `web` does not answer on port 443. Until then, use a local port-forward (see [Without an ingress](#without-an-ingress)).

Open the address and sign in as `admin`. Then register a cluster (Admin > Clusters; the simplest option is the backend's own service account) and deploy a first stack. The [Getting Started guide](https://github.com/omattsson/k8s-stack-manager/blob/main/docs/getting-started.md) shows every step, also with stackctl.

### Without an ingress

Set `ingress.type=none` and `frontend.apiProxy.enabled=true`. The frontend then proxies the API, and one port-forward gives the UI and the API:

```bash
kubectl -n stack-manager port-forward svc/stack-manager-k8s-stack-manager-frontend 8080:8080
```

## Main values

| Value | Default | Description |
|---|---|---|
| `backend.secrets.JWT_SECRET` | placeholder | Signing secret of the access tokens (16 characters or more). Required. |
| `backend.secrets.ADMIN_PASSWORD` | placeholder | Password of the first `admin` user. Change it. |
| `backend.secrets.KUBECONFIG_ENCRYPTION_KEY` | `""` | Encrypts kubeconfig data, registry passwords and notification channel secrets at rest. Without it, the backend refuses them. |
| `ingress.type` | `traefik` | `traefik` (IngressRoute), `ingress` (standard Ingress) or `none` |
| `ingress.host` | `""` | Host name of the ingress. Empty matches all hosts. |
| `frontend.enabled` | `true` | `false` for a headless (API-only) installation |
| `frontend.apiProxy.enabled` | `false` | Proxy `/api`, `/ws`, `/health` and `/swagger` through the frontend, for installations without an ingress |
| `mysql.enabled` | `true` | Bundled MySQL. For an external database set `false`, `backend.env.DB_HOST`, `DB_PORT`, `DB_USER` and `DB_NAME`, and the password in `backend.secrets.DB_PASSWORD` (or External Secrets): `backend.env` is a ConfigMap. |
| `mysql.persistence.enabled` | `true` | Persistent volume of the bundled MySQL (5Gi) |
| `backend.allowedChartRepos` | `[]` | Chart repository URL prefixes that stacks may use. Empty allows every source. Set it on clusters that untrusted users deploy to. |
| `backend.rbac.scope` | `namespace` | `namespace`: all rights, custom resources included, in each stack namespace (RoleBinding `k8s-stack-manager-access`); outside them the backend reads nodes and pods, and creates, labels and deletes stack namespaces. `cluster`: a fixed list of namespaced resources (pods, Secrets, Deployments, Roles and more) in all namespaces, without namespaced custom resources. Helm runs as the backend in both scopes, so a stack chart can change objects in other stack namespaces too (with `cluster`, in any namespace). Neither scope lets a chart create cluster-scoped objects such as CRDs, ClusterRoles or webhooks. |
| `backend.maxInstanceTtlDays` | `30` | Longest time to live and latest stop time of a stack |
| `backend.portForward.enabled` | `false` | `stackctl stack port-forward` through the backend. Each cluster also needs "Allow port-forward". |
| `backend.exec.enabled` | `false` | `stackctl stack exec` through the backend. Each cluster also needs "Allow exec", and only containers in `backend.exec.allowedContainers` can be used. |
| `backend.streams.allowApiKeys` | `false` | Let API keys open port-forward and exec streams (API keys have no scopes) |
| `externalSecrets.enabled` | `false` | Create the backend Secret with the External Secrets Operator (for example from Azure Key Vault) |
| `argoRollouts.enabled` | `false` | Argo Rollouts with a canary strategy instead of Deployments |
| `branding.title`, `branding.logoUrl` | `""` | Your product name and logo in the UI |

All values, with comments: [values.yaml](https://github.com/omattsson/k8s-stack-manager/blob/main/helm/k8s-stack-manager/values.yaml). Deployment details (security settings, External Secrets, what the chart deploys): [docs/deployment.md](https://github.com/omattsson/k8s-stack-manager/blob/main/docs/deployment.md).

## Security notes

- Change the placeholder secrets. The notes after `helm install` warn about each one that is left.
- Port-forward bypasses NetworkPolicies and the ingress login of tools in a stack, and exec shows the environment of a container, including its secrets. Both are off by default; turn them on only for clusters where stack owners may see that data.
- The chart keeps `CORS_ALLOWED_ORIGINS=same-origin` and `SELF_REGISTRATION=false` by default.

See the [security model](https://github.com/omattsson/k8s-stack-manager/blob/main/docs/security.md).

## Upgrade

Read the release notes before an upgrade: <https://github.com/omattsson/k8s-stack-manager/releases>. The backend runs the database migrations at startup.

```bash
helm repo update
helm upgrade stack-manager k8s-stack-manager/k8s-stack-manager \
  --namespace stack-manager --values stack-manager-values.yaml
```

## Uninstall

Before you uninstall:

1. Back up the database before you delete anything: a later backup no longer holds the deleted stacks (their records, overrides and deploy history). `helm uninstall` deletes the PersistentVolumeClaim of the bundled MySQL; with the usual `Delete` reclaim policy that removes the database for good: the users, the definitions and templates, and the stored cluster credentials. For the bundled MySQL:

   ```bash
   (umask 077; kubectl -n stack-manager exec deploy/stack-manager-k8s-stack-manager-mysql -- \
     sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot --single-transaction app' \
     > stack-manager-backup.sql)
   ```

   Keep `stack-manager-values.yaml` (or the backend Secret) with the backup: the stored cluster credentials are encrypted with `KUBECONFIG_ENCRYPTION_KEY`, and without that key the backup cannot restore them.
2. Delete the stacks (in the UI or with `stackctl stack delete`). A delete starts a clean in the background: the backend uninstalls the Helm releases, deletes the namespace and then removes the stack. Wait until the stacks are gone from `stackctl stack list`. An uninstall during a clean leaves stack workloads behind.
3. Check that the stack namespaces are gone. A stack leaves the list once the namespace delete is sent, but finalizers can keep a namespace in `Terminating`. This lists every namespace that the application created, also those of stacks that you did not delete (remove those yourself):

   ```bash
   kubectl get namespaces -l managed-by=k8s-stack-manager
   ```

```bash
helm uninstall stack-manager --namespace stack-manager
```

## License

MIT
