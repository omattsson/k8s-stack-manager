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

Or use the install script:

```bash
curl -fsSL https://raw.githubusercontent.com/omattsson/stackctl/main/install.sh | sudo bash
```

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
  --set backend.secrets.ADMIN_PASSWORD="your-admin-password" \
  --set mysql.auth.rootPassword="your-db-password"
```

Wait for all pods to be ready:

```bash
kubectl -n stack-manager rollout status deployment/stack-manager-backend
kubectl -n stack-manager rollout status deployment/stack-manager-frontend
```

## 3. Access the UI

Port-forward the frontend:

```bash
kubectl -n stack-manager port-forward svc/stack-manager-frontend 3000:80
```

Open http://localhost:3000 and log in with `admin` / the password you set above.

## 4. Configure stackctl

Start port-forwarding the backend API:

```bash
kubectl -n stack-manager port-forward svc/stack-manager-backend 8081:8081 &
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

## 5. Grant deploy permissions

The backend needs cluster-wide permissions to install Helm charts (many charts create ClusterRoles, CRDs, etc.). Grant the backend service account cluster-admin:

```bash
kubectl create clusterrolebinding stack-manager-admin \
  --clusterrole=cluster-admin \
  --serviceaccount=stack-manager:stack-manager-k8s-stack-manager-backend
```

> For production, scope this down to the specific resources your charts need.

## 6. Register a cluster


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

## 7. Import starter templates

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

## 8. Deploy your first stack

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

## 9. Clean up

When you're done experimenting:

```bash
# Stop the stack (keeps namespace)
stackctl stack stop my-first-stack

# Or fully remove it (undeploys and deletes namespace)
stackctl stack clean my-first-stack
stackctl stack delete my-first-stack
```

## Next steps

- [Concepts & Terminology](../WIKI.md) — templates, definitions, instances, and how they relate
- [Architecture](../ARCHITECTURE.md) — system design, data model, and package structure
- [Extending](../EXTENDING.md) — event hooks, custom actions, and webhook integrations
- [stackctl CLI Reference](https://github.com/omattsson/stackctl) — full command reference
