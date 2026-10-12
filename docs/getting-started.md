# Getting Started

Deploy K8s Stack Manager to your cluster and create your first stack in about 10 minutes.

## Prerequisites

- A running Kubernetes cluster and a configured `kubectl` context.
- [Helm 3](https://helm.sh/docs/intro/install/) or later.
- [stackctl](https://github.com/omattsson/stackctl), the command-line interface (CLI) for K8s Stack Manager.
- An ingress controller for the web UI. The chart creates a Traefik IngressRoute by default, which needs [Traefik](https://traefik.io/) with its CRDs. For another controller, set `ingress.type=ingress`. See [Access the UI](#3-access-the-ui).
- Kubernetes 1.30 or later. The chart creates a ValidatingAdmissionPolicy by default, and this needs Kubernetes 1.30. For an older cluster, set `backend.rbac.admissionPolicy.enabled=false`. Without the policy, the cluster-wide RoleBinding right of the backend could give it access to any namespace.

## 1. Install stackctl

```bash
brew install --cask omattsson/tap/stackctl
```

You can also use the install script or download a binary from the [releases page](https://github.com/omattsson/stackctl/releases). See the [stackctl README](https://github.com/omattsson/stackctl#installation).

## 2. Deploy K8s Stack Manager

Add the Helm repository:

```bash
helm repo add k8s-stack-manager https://omattsson.github.io/k8s-stack-manager
helm repo update
```

Create a values file with your secrets. Do not pass secrets with `--set`, because they stay in your shell history.

```bash
cat > stack-manager-values.yaml <<EOF
backend:
  secrets:
    JWT_SECRET: "$(openssl rand -base64 32)"
    ADMIN_PASSWORD: "your-admin-password"
mysql:
  auth:
    rootPassword: "your-db-password"
EOF
```

Install the chart:

```bash
helm install stack-manager k8s-stack-manager/k8s-stack-manager \
  --namespace stack-manager --create-namespace \
  --values stack-manager-values.yaml
```

Wait for the pods to be ready:

```bash
kubectl -n stack-manager rollout status deployment/stack-manager-k8s-stack-manager-backend
kubectl -n stack-manager rollout status deployment/stack-manager-k8s-stack-manager-frontend
```

Note: If you set `argoRollouts.enabled: true`, use `rollout/` instead of `deployment/` in these commands.

Recommended: Restrict the chart sources. Set `backend.allowedChartRepos` to the chart repositories that your templates use. The backend then rejects all other chart sources. See [Chart sources](../WIKI.md#chart-sources). The starter definitions in step 6 use the repository `https://stefanprodan.github.io/podinfo`. Add this URL to the list if you want to import them.

Optional: Show your own product name and logo. Add a `branding` block to the values file. See [Branding](../WIKI.md#branding).

```yaml
branding:
  title: "Platform Portal"
  logoUrl: /branding/logo.svg      # or an https:// URL
  faviconUrl: /branding/logo.svg
  files:                           # the frontend serves these files at /branding/
    logo.svg: |
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="6" fill="#1565c0"/></svg>
```

## 3. Access the UI

The frontend pod serves only the web application. The ingress sends the paths `/api`, `/ws`, `/health`, and `/swagger` to the backend. So you must open the UI through the ingress.

- With the default `ingress.type=traefik` and an empty `ingress.host`, the IngressRoute matches all host names. Open the address of the Traefik load balancer. For example, with the default Traefik installation:

  ```bash
  kubectl get svc -n traefik traefik -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
  ```

- To use a host name, set `ingress.host` (for example `stacks.example.com`) and point the DNS name at the ingress.
- To use TLS, set `ingress.traefik.tls.secretName`. For `ingress.type=ingress`, set `ingress.tls`. The chart then sets `SECURE_COOKIES=true`.

Warning: A `kubectl port-forward` to the frontend Service alone does not give a working UI. The frontend pod has no proxy for the API, so the login request fails. If you have no external address, use `kubectl port-forward` to the Service of your ingress controller.

Open the address in a browser. Sign in as `admin` with the password that you set above.

## 4. Configure stackctl

stackctl needs the address of the API. If you use a port-forward for the backend, start it first:

```bash
kubectl -n stack-manager port-forward svc/stack-manager-k8s-stack-manager-backend 8081:8081 &
```

Create a context, set the API address, and sign in:

```bash
stackctl config use-context my-cluster
stackctl config set api-url http://localhost:8081
stackctl login
# Enter: admin / your-admin-password
```

Check the connection:

```bash
stackctl whoami
stackctl version
```

## 5. Register a cluster

The backend needs a registered Kubernetes cluster to deploy stacks to. The simplest option is to register the cluster that the backend runs on. The backend then uses its own service account.

1. In the UI, go to **Admin > Clusters** and click **Add Cluster**.
2. Enter the **Name** `default`.
3. Select **Use in-cluster configuration (the backend service account)**. The API Server URL is then optional.
4. Select **Set as default cluster**.
5. Save the cluster.

You can also register an external cluster. Enter a kubeconfig. For a kubeconfig that you paste as data, you must set `KUBECONFIG_ENCRYPTION_KEY` (the key is in `backend.secrets`). Without this key, the backend rejects kubeconfig data. A kubeconfig path does not need the key.

Check the cluster with stackctl:

```bash
stackctl cluster list
```

What rights does the backend have on the cluster?

- With `backend.rbac.scope: namespace` (default), the chart gives the backend rights on namespaced objects only inside stack namespaces. The backend creates the RoleBinding `k8s-stack-manager-access` in each stack namespace. A ValidatingAdmissionPolicy keeps the backend there. A stack chart cannot create cluster-scoped objects (ClusterRoles, CRDs, webhooks) or objects in other namespaces. Set `backend.rbac.scope: cluster` for the old rules, which apply in all namespaces. Use this only when your charts need cluster-scoped objects.
- For an external cluster (kubeconfig), the credentials need the same rights. See [Backend rights on the cluster](../WIKI.md#backend-rights-on-the-cluster).
- Each deploy and rollback also reads the pod problems of the stack namespace for the deploy log. This needs `list` on `events` and `get` and `list` on `pods`. Without these rights, the deploy works, but the deploy log has no `Pod event:` lines. The backend logs one warning for each operation.

## 6. Import starter definitions

The repository has four definition bundles in `examples/starter-templates/`. Each bundle is a stack definition, not a template. Importing a definition needs the `devops` or `admin` role. Import them with the script:

```bash
./scripts/import-starter-templates.sh
```

Or import them one by one:

```bash
stackctl definition import --file examples/starter-templates/hello-world.json
stackctl definition import --file examples/starter-templates/web-app.json
stackctl definition import --file examples/starter-templates/api-backend.json
stackctl definition import --file examples/starter-templates/full-stack.json
```

List the definitions:

```bash
stackctl definition list
```

### Starter definitions

| Name | Charts | Description |
|------|--------|-------------|
| Hello World | podinfo | One chart. Use it to check that the platform works. |
| Web App | backend and frontend | Two services with a deploy order. |
| API Backend | cache and api | An API with a cache tier and resource limits. |
| Full Stack | cache, api, and frontend | A stack with three charts and a deploy order. |

## 7. Deploy your first stack

Use the **Web App** definition. It has two charts (backend and frontend) with a deploy order. Create an instance of it:

```bash
# List the definitions and find the ID of Web App
stackctl definition list

# Create an instance from the Web App definition
stackctl stack create --definition <web-app-id> --name my-first-stack

# Deploy it. The charts deploy in order: backend first, then frontend.
stackctl stack deploy my-first-stack
```

An instance name must be a DNS label: lowercase letters, digits, and `-`, with at most 50 characters.

Monitor the deployment:

```bash
# Show the status
stackctl stack status my-first-stack

# Show the deploy log. It shows each chart as it installs.
stackctl stack logs my-first-stack

# List your stacks
stackctl stack list --mine
```

## 8. Clean up

When you finish your tests:

```bash
# Stop the stack (Helm releases are removed, the namespace stays)
stackctl stack stop my-first-stack

# Remove the Helm releases and delete the namespace
stackctl stack clean my-first-stack

# Delete the stack record from the platform
stackctl stack delete my-first-stack
```

## Next steps

- [Concepts and terminology](../WIKI.md): templates, definitions, instances, and how they relate.
- [Architecture](../ARCHITECTURE.md): system design, data model, and package structure.
- [Extending](../EXTENDING.md): event hooks, custom actions, and webhook integrations.
- [stackctl CLI reference](https://github.com/omattsson/stackctl): all commands.
