# Getting Started

Deploy K8s Stack Manager to your cluster and create your first stack in under 10 minutes.

## Prerequisites

- A running Kubernetes cluster with `kubectl` configured
- [Helm 3.x](https://helm.sh/docs/intro/install/)
- [stackctl](https://github.com/omattsson/stackctl) CLI

## 1. Install stackctl

```bash
brew install omattsson/tap/stackctl
```

Or download from [releases](https://github.com/omattsson/stackctl/releases) and install manually.

## 2. Deploy K8s Stack Manager

Add the Helm repository and install:

```bash
helm repo add k8s-stack-manager https://omattsson.github.io/k8s-stack-manager
helm repo update
```

Install with minimal configuration:

```bash
helm install stack-manager k8s-stack-manager/k8s-stack-manager \
  --namespace stack-manager --create-namespace \
  --set backend.secrets.JWT_SECRET="$(openssl rand -base64 32)" \
  --set backend.secrets.ADMIN_PASSWORD="CHANGE-ME-set-a-secure-password" \
  --set mysql.auth.rootPassword="CHANGE-ME-set-a-secure-password"
```

Wait for all pods to be ready:

```bash
kubectl -n stack-manager rollout status deployment/stack-manager-k8s-stack-manager-backend
kubectl -n stack-manager rollout status deployment/stack-manager-k8s-stack-manager-frontend
```

## 3. Access the UI

If your cluster has Traefik installed (the chart creates IngressRoutes automatically), access the UI via the Traefik load balancer IP. Otherwise, port-forward:

```bash
kubectl -n stack-manager port-forward svc/stack-manager-k8s-stack-manager-frontend 3000:80
```

Open http://localhost:3000 and log in with `admin` / the password you set above.

## 4. Configure stackctl

If using port-forward for the backend API:

```bash
kubectl -n stack-manager port-forward svc/stack-manager-k8s-stack-manager-backend 8081:8081 &
```

Point stackctl at your deployment and authenticate:

```bash
stackctl config use-context my-cluster
stackctl config set api-url http://localhost:8081
stackctl login
# Enter: admin / your-admin-password
```

Verify the connection:

```bash
stackctl whoami
stackctl version
```

## 5. Register a cluster

The backend needs a registered Kubernetes cluster to deploy stacks to. The simplest option is to register the cluster it's running on using in-cluster configuration.

Go to **Admin > Clusters** in the UI and click **Add Cluster**:

- **Name**: `default`
- **API Server URL**: `https://kubernetes.default.svc`
- **Set as Default**: checked

Or register an external cluster by providing a kubeconfig.

Verify with stackctl:

```bash
stackctl cluster list
```

> **Note:** The Helm chart creates a ClusterRole with permissions for namespaces, pods, deployments, and services. If your charts require additional cluster-scoped resources (ClusterRoles, CRDs, IngressClasses), you may need to extend the ClusterRole or grant broader permissions to the backend service account.

## 6. Import starter templates

The repo ships with 4 ready-to-use definition bundles. Import them with the bootstrap script:

```bash
./scripts/import-starter-templates.sh
```

Or import individually:

```bash
stackctl definition import --file examples/starter-templates/hello-world.json
stackctl definition import --file examples/starter-templates/web-app.json
stackctl definition import --file examples/starter-templates/api-backend.json
stackctl definition import --file examples/starter-templates/full-stack.json
```

Check what's available:

```bash
stackctl definition list
```

### Starter templates

| Name | Charts | Description |
|------|--------|-------------|
| Hello World | podinfo | Single chart — verify the platform works |
| Web App | backend + frontend | Two services with deploy ordering |
| API Backend | cache + api | API with cache tier and resource limits |
| Full Stack | cache + api + frontend | Production-like 3-chart stack |

## 7. Deploy your first stack

Pick the **Web App** definition (backend + frontend — two charts with deploy ordering) and create a stack:

```bash
# List definitions and pick the Web App ID
stackctl definition list

# Create an instance from the Web App definition
stackctl stack create --definition <web-app-id> --name my-first-stack

# Deploy it — both charts deploy in order (backend first, then frontend)
stackctl stack deploy my-first-stack
```

Monitor the deployment:

```bash
# Watch status
stackctl stack status my-first-stack

# View deployment logs — shows each chart installing in sequence
stackctl stack logs my-first-stack

# List your stacks
stackctl stack list --mine
```

## 8. Clean up

When you're done experimenting:

```bash
# Stop the stack (keeps namespace)
stackctl stack stop my-first-stack

# Remove Helm releases and delete the namespace
stackctl stack clean my-first-stack

# Delete the stack record from the platform
stackctl stack delete my-first-stack
```

## Next steps

- [Concepts & Terminology](../WIKI.md) — templates, definitions, instances, and how they relate
- [Architecture](../ARCHITECTURE.md) — system design, data model, and package structure
- [Extending](../EXTENDING.md) — event hooks, custom actions, and webhook integrations
- [stackctl CLI Reference](https://github.com/omattsson/stackctl) — full command reference
