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

## Architecture

### Data Flow
```
Template → (instantiate) → Definition + ChartConfigs → (create instance) → Instance + ValueOverrides
```

### Storage
- **Azure Table Storage** (Azurite for local dev) for all domain entities
- **MySQL** (GORM) for legacy Items CRUD

### Authentication
- JWT-based with `Authorization: Bearer <token>` header
- Role hierarchy: `admin` > `devops` > `user`
- Admin can register users; self-registration is configurable

### Multi-Cluster
- Clusters are registered via the API with a kubeconfig path or kubeconfig data (encrypted at rest with AES-GCM)
- `ClusterRegistry` manages per-cluster Kubernetes and Helm clients
- A health poller periodically checks cluster connectivity and updates status
- Stack instances target a specific cluster (or the default cluster)
- The `deployer` package routes deploy/undeploy/status operations through the registry to the correct cluster

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
- Go 1.24+ (backend)
- Node.js 20+ (frontend)

### Running
```bash
make dev              # Full stack via Docker Compose
make dev-local        # Backend only, local against Azurite
```

### Testing
```bash
make test             # All unit tests
make test-backend-all # Backend unit + integration (starts MySQL + Azurite)
make test-e2e         # End-to-end with Playwright
```

### Adding a New API Resource
See `.github/instructions/api-extension.instructions.md` for the step-by-step guide.

## Troubleshooting

- **Azurite connection errors**: Ensure Azurite is running (`make azurite-start`). Inside Docker, endpoint is `azurite:10002`; locally it's `127.0.0.1:10002`.
- **JWT errors**: Ensure `JWT_SECRET` is set and at least 16 characters.
- **Database connection errors (MySQL)**: Only affects legacy Items. Ensure MySQL is running if `USE_AZURE_TABLE=false`.
- **Git provider errors**: Check `AZURE_DEVOPS_PAT` or `GITLAB_TOKEN` are set correctly. Empty tokens are valid (provider just won't be available).
- **Cluster connection errors**: Verify the kubeconfig path or data is valid. Use the "Test Connection" button on the Clusters admin page. If `KUBECONFIG_ENCRYPTION_KEY` is set, all kubeconfig data is encrypted at rest — changing the key will make existing encrypted data unreadable.
