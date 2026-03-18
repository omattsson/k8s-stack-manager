# K8s Stack Manager — Project Plan

A web application that enables developers to configure, store, and deploy multi-service application stacks to a shared Kubernetes cluster. Built on Go (Gin) + React (TypeScript/Vite/MUI) with Azure Table Storage for persistence.

## Problem Statement

Today each developer runs a local docker-compose to spin up an application stack on their working branch. This approach:

- Doesn't scale when multiple services need to coordinate
- Has no visibility into what other developers are running
- Lacks audit trail of who changed what
- Requires manual translation of docker-compose config to Helm values as teams migrate to K8s
- Creates friction when maintaining multiple stacks with different branches/configurations

## Solution

A self-service web application where developers can:

1. Define reusable **stack definitions** (collections of Helm charts)
2. Spin up **stack instances** from those definitions, choosing branches per service
3. Configure **values overrides** per chart with a YAML editor
4. **Export** generated `values.yaml` files for use with Helm
5. See **all stacks** across the team with a full **audit log** of changes
6. (Future) **Deploy directly** to the shared AKS Arc cluster

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Frontend (React)                     │
│  MUI · TypeScript · Vite · Monaco Editor · WebSocket    │
├─────────────────────────────────────────────────────────┤
│                   Backend (Go + Gin)                     │
│  REST API · JWT Auth · Swagger · Audit Middleware        │
├──────────┬──────────┬───────────┬───────────────────────┤
│ Azure    │ Git      │ Helm      │ K8s                   │
│ Tables   │ Provider │ Values    │ Deployer              │
│ (CRUD)   │ (AzDO +  │ (Merge +  │ (Phase 3)             │
│          │  GitLab) │  Export)  │                       │
└──────────┴──────────┴───────────┴───────────────────────┘
```

## Tech Stack

- **Backend**: Go 1.24, Gin, Azure Table Storage SDK, JWT (golang-jwt)
- **Frontend**: React 18, TypeScript, Vite, MUI, Monaco Editor, Axios
- **Persistence**: Azure Tables (Azurite for local dev)
- **Auth**: JWT with username/password (Phase 1), upgradeable to OIDC
- **K8s target**: Single AKS Arc cluster on own hardware, shared by all devs
- **Git providers**: Azure DevOps + GitLab (shared service-level tokens)
- **Typical stack**: ~7 Helm charts per application stack

## Design Principles

- **Generic**: No hard-coded company references; all branding via env/config; usable by multiple organizations
- **Progressive**: Configuration management delivers value immediately, deployment integration added later
- **Auditable**: Every mutation produces an audit log entry with user, action, timestamp
- **Familiar**: YAML editor uses Monaco (VS Code's editor), branch selection pulls live data from Git providers

---

## Phase 0: Project Agents & Instructions

Create the custom agents, workspace instructions, and file-specific instructions that will accelerate all subsequent phases.

### Step 0.1 — Workspace Instructions

**File**: `.github/copilot-instructions.md`

Project-wide standards covering architecture, code style, build/test commands, and conventions (audit logging, branch defaults, namespace naming, Helm values merge, Git provider detection).

### Step 0.2 — Custom Agents (8 agents)

All agents in `.github/agents/`:

| Agent | File | Purpose |
|-------|------|---------|
| Backend API Developer | `backend-api.agent.md` | Go handlers, routes, middleware, Swagger annotations |
| Data Layer Specialist | `data-layer.agent.md` | Domain models, Azure Table repositories, factory wiring |
| Frontend UI Developer | `frontend-ui.agent.md` | React pages, MUI components, routing, state |
| Frontend API Integration | `frontend-api.agent.md` | Axios services, TypeScript types, data fetching hooks |
| Git Provider Specialist | `git-provider.agent.md` | Azure DevOps + GitLab API integration, URL parsing, caching |
| Helm Values Specialist | `helm-values.agent.md` | YAML deep-merge, template variables, values export |
| Test Specialist | `test-writer.agent.md` | Go tests (testify), React tests (Vitest/RTL), E2E (Playwright) |
| Build Orchestrator | `orchestrator.agent.md` | Coordinates multi-step features across specialist agents |

### Step 0.3 — File-Specific Instructions (3 files)

All in `.github/instructions/`:

| File | Applies To | Purpose |
|------|-----------|---------|
| `go-handlers.instructions.md` | `backend/internal/api/handlers/**/*.go` | Swagger annotations, request binding, audit logging |
| `azure-table-repos.instructions.md` | `backend/internal/database/azure/**/*.go` | CRUD patterns, partition key design, error mapping |
| `react-pages.instructions.md` | `frontend/src/pages/**/*.tsx` | Functional components, MUI, loading/error states, tests |

### Phase 0 Verification

- [ ] 8 `.agent.md` files in `.github/agents/`
- [ ] `copilot-instructions.md` in `.github/`
- [ ] 3 `.instructions.md` files in `.github/instructions/`
- [ ] Each agent selectable from VS Code agent picker
- [ ] Orchestrator can discover and invoke all specialist agents

---

## Phase 1: Core Data Model & API (Backend)

### Step 1.1 — Domain Models

New files in `backend/internal/models/`:

**User**
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| Username | string | Unique |
| PasswordHash | string | bcrypt |
| DisplayName | string | |
| Role | string | "admin" / "user" |
| CreatedAt | time.Time | |
| UpdatedAt | time.Time | |

**StackDefinition**
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| Name | string | Unique |
| Description | string | |
| OwnerID | string | FK → User |
| DefaultBranch | string | Default: "master" |
| CreatedAt | time.Time | |
| UpdatedAt | time.Time | |

**ChartConfig**
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| StackDefinitionID | string | FK → StackDefinition |
| ChartName | string | e.g., "my-service" |
| RepositoryURL | string | Helm chart repo URL |
| SourceRepoURL | string | Git repo for branch listing (AzDO/GitLab) |
| ChartPath | string | Path within repo to chart |
| ChartVersion | string | Version or "latest" |
| DefaultValues | string | JSON/YAML |
| DeployOrder | int | Deployment sequence |
| CreatedAt | time.Time | |

**StackInstance**
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| StackDefinitionID | string | FK → StackDefinition |
| Name | string | |
| Namespace | string | Auto: `stack-{name}-{owner}` |
| OwnerID | string | FK → User |
| Branch | string | Default: "master" |
| Status | string | draft/deploying/running/stopped/error |
| CreatedAt | time.Time | |
| UpdatedAt | time.Time | |

**ValueOverride**
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| StackInstanceID | string | FK → StackInstance |
| ChartConfigID | string | FK → ChartConfig |
| Values | string | YAML/JSON overrides |
| UpdatedAt | time.Time | |

**AuditLog**
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| UserID | string | FK → User |
| Username | string | Denormalized for display |
| Action | string | create/update/delete/deploy/stop |
| EntityType | string | stack_definition/stack_instance/chart_config/value_override |
| EntityID | string | |
| Details | string | JSON with before/after or context |
| Timestamp | time.Time | |

### Step 1.2 — Azure Table Repositories

Extend `backend/internal/database/azure/` following the existing `table.go` pattern.

**Partition Key Strategy**:

| Table | Partition Key | Row Key | Rationale |
|-------|--------------|---------|-----------|
| Users | `"users"` | username | Fast lookup by username for auth |
| StackDefinitions | `"global"` | definition_id | All definitions visible to all users |
| ChartConfigs | stack_definition_id | chart_config_id | All charts for a definition |
| StackInstances | `"global"` | instance_id | All instances visible to all users |
| ValueOverrides | stack_instance_id | chart_config_id | All overrides for an instance |
| AuditLogs | `YYYY-MM` | reverse_timestamp + uuid | Recent-first within monthly partitions |

**Repositories to create**:
- `UserRepository` — CRUD + `FindByUsername()`
- `StackDefinitionRepository` — CRUD + `List()`
- `ChartConfigRepository` — CRUD + `ListByDefinition(definitionID)`
- `StackInstanceRepository` — CRUD + `List()` + `ListByOwner(ownerID)`
- `ValueOverrideRepository` — CRUD + `ListByInstance(instanceID)`
- `AuditLogRepository` — `Create()` + `List(filters)` with date range, entity, user filters

### Step 1.3 — REST API Endpoints

New handlers in `backend/internal/api/handlers/` and routes in `backend/internal/api/routes/routes.go`.

**Auth** (`/api/v1/auth`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| POST | `/login` | Username/password → JWT token | None |
| POST | `/register` | Create user (admin-only or configurable) | Admin |
| GET | `/me` | Current user info from JWT | User |

**Stack Definitions** (`/api/v1/stack-definitions`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | List all definitions | User |
| POST | `/` | Create definition | User |
| GET | `/:id` | Get definition with charts | User |
| PUT | `/:id` | Update definition | User |
| DELETE | `/:id` | Delete (if no running instances) | User |

**Chart Configs** (nested under `/api/v1/stack-definitions/:id/charts`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| POST | `/` | Add chart to definition | User |
| PUT | `/:chartId` | Update chart config | User |
| DELETE | `/:chartId` | Remove chart from definition | User |

**Stack Instances** (`/api/v1/stack-instances`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | List all instances (query: `?owner=me`) | User |
| POST | `/` | Create from definition | User |
| GET | `/:id` | Get instance with overrides | User |
| PUT | `/:id` | Update (branch, name) | User |
| DELETE | `/:id` | Delete (stops if running) | User |
| POST | `/:id/clone` | Clone instance | User |
| GET | `/:id/values/:chartId` | Export generated values.yaml for a chart | User |
| GET | `/:id/values` | Export all values as zip | User |

**Value Overrides** (nested under `/api/v1/stack-instances/:id/overrides`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | Get all overrides for instance | User |
| PUT | `/:chartId` | Set/update overrides for a chart | User |

**Git** (`/api/v1/git`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/branches` | List branches (`?repo=<url>`) | User |
| GET | `/validate-branch` | Check branch exists (`?repo=<url>&branch=<name>`) | User |
| GET | `/providers` | List configured providers & status | User |

**Audit Logs** (`/api/v1/audit-logs`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | List logs (filter: user, entity_type, entity_id, action, date range) | User |

### Step 1.4 — Auth & Audit Middleware

**JWT Auth Middleware** (`backend/internal/api/middleware/auth.go`):
- Parse `Authorization: Bearer <token>` header
- Validate JWT signature and expiration
- Inject user context (`UserID`, `Username`, `Role`) into Gin context
- Return 401 for missing/invalid tokens

**Audit Middleware** (`backend/internal/api/middleware/audit.go`):
- Wrap mutating endpoints (POST, PUT, DELETE)
- After handler completes successfully, write AuditLog entry
- Extract entity type/ID from route, user from context
- Capture request body summary in details field

**Role Middleware** (`backend/internal/api/middleware/role.go`):
- Check user role from context against required role
- Return 403 for insufficient permissions

### Step 1.5 — Helm Values Generation

New package `backend/internal/helm/`:

**ValuesGenerator**:
- Deep-merge chart default values + instance value overrides → final `values.yaml`
- Override-specific keys only (not full replacement)
- Template variable substitution:
  - `{{.Branch}}` → instance branch
  - `{{.Namespace}}` → instance namespace
  - `{{.InstanceName}}` → instance name
  - `{{.StackName}}` → definition name
  - `{{.Owner}}` → instance owner username
- Output as valid YAML (using `gopkg.in/yaml.v3`)
- Export single chart values or zip of all charts

### Step 1.6 — Git Provider Abstraction

New package `backend/internal/gitprovider/`:

**Interface** (`provider.go`):
```go
type GitProvider interface {
    ListBranches(ctx context.Context, repoURL string) ([]Branch, error)
    GetDefaultBranch(ctx context.Context, repoURL string) (string, error)
    ValidateBranch(ctx context.Context, repoURL string, branch string) (bool, error)
    ProviderType() string
}

type Branch struct {
    Name      string
    IsDefault bool
}
```

**Implementations**:
- `azuredevops.go` — Azure DevOps REST API (`dev.azure.com/{org}/{project}/_apis/git/repositories/{repo}/refs?filter=heads/&api-version=7.1`), auth via PAT in Basic header
- `gitlab.go` — GitLab REST API (`/api/v4/projects/:id/repository/branches`), auth via `PRIVATE-TOKEN` header
- `registry.go` — URL-based provider detection + routing + 5-minute in-memory cache

**URL Detection Logic**:
- Contains `dev.azure.com` or `visualstudio.com` → Azure DevOps
- Contains `gitlab.com` or configured custom GitLab domain → GitLab
- Extensible for future providers (GitHub, Bitbucket)

**Configuration** (added to `backend/internal/config/config.go`):
```go
GitProviders: GitProvidersConfig{
    AzureDevOps: AzureDevOpsConfig{
        PAT:         getEnv("AZURE_DEVOPS_PAT", ""),
        DefaultOrg:  getEnv("AZURE_DEVOPS_DEFAULT_ORG", ""),
    },
    GitLab: GitLabConfig{
        Token:   getEnv("GITLAB_TOKEN", ""),
        BaseURL: getEnv("GITLAB_BASE_URL", "https://gitlab.com"),
    },
}
```

### Phase 1 Verification

- [ ] All 6 model structs defined with proper types and tags
- [ ] All 6 Azure Table repositories implement CRUD + filters
- [ ] All REST endpoints return correct status codes and response shapes
- [ ] JWT auth middleware rejects unauthenticated requests with 401
- [ ] Role middleware rejects unauthorized requests with 403
- [ ] Every POST/PUT/DELETE endpoint produces an AuditLog entry
- [ ] Helm values merge produces valid YAML with correct override precedence
- [ ] Template variables are substituted in exported values
- [ ] Git provider detects Azure DevOps and GitLab URLs correctly
- [ ] Branch listing returns branches from both providers
- [ ] Branch cache returns cached results within TTL
- [ ] Swagger docs generated and accessible at `/swagger/index.html`
- [ ] All new Go code has unit tests with testify

---

## Phase 2: Frontend — Stack Management UI

### Step 2.1 — Auth Pages & Context

**New files**:
- `src/context/AuthContext.tsx` — JWT storage, login/logout, auto-refresh, user state
- `src/pages/Login/index.tsx` — username/password form
- `src/components/ProtectedRoute/index.tsx` — redirect to login if unauthenticated

**Changes**:
- `src/App.tsx` — wrap with `AuthProvider`
- `src/api/client.ts` — add auth header interceptor (`Authorization: Bearer <token>`)
- `src/components/Layout/index.tsx` — show username + logout button, update nav links

### Step 2.2 — Stack Definitions Pages

**List page** (`/stack-definitions`):
- Table with columns: Name, Description, Owner, Charts count, Created, Actions
- Create button → navigate to create form
- Row click → navigate to detail/edit

**Create/Edit page** (`/stack-definitions/new`, `/stack-definitions/:id/edit`):
- Form fields: Name, Description, Default Branch
- Dynamic chart list:
  - Add/remove/reorder charts via drag-and-drop
  - Per chart: Chart Name, Repo URL, Source Repo URL, Chart Path, Version, Deploy Order
  - Default Values: inline YAML editor (Monaco) per chart
- Save → POST/PUT API call + navigate to list

### Step 2.3 — Stack Instances Pages

**Dashboard** (`/` — replaces current Home page):
- Card/grid view of all stack instances
- Each card shows: Name, Status (color-coded badge), Owner, Branch, Definition name, Namespace
- Quick actions: Edit, Clone, Delete, Export Values
- Filter/search bar, filter by status, filter by owner ("My Stacks" toggle)

**Create Instance** (`/stack-instances/new`):
- Select stack definition (dropdown)
- Instance name (auto-suggest: `{definition}-{username}`)
- Branch selector:
  - If all charts use same provider: single BranchSelector with live branch listing
  - Toggle "Advanced" for per-chart branch overrides
  - Default: "master"
- Namespace: auto-generated preview (`stack-{name}-{owner}`), editable

**Instance Detail** (`/stack-instances/:id`):
- Header: Name, Status badge, Branch, Namespace, Owner, Definition link
- Per-chart tabs or accordion:
  - YAML editor (Monaco) for value overrides
  - Diff view: defaults vs. overrides (toggle)
  - Export single chart values button
- Actions toolbar: Save, Deploy (Phase 3), Stop (Phase 3), Clone, Delete, Export All Values
- Branch selector (changing branch updates all charts or per-chart in advanced mode)

### Step 2.4 — Audit Log Page

**Audit Log** (`/audit-log`):
- Table with columns: Timestamp, User, Action, Entity Type, Entity, Details (expandable)
- Filters: User dropdown, Action type, Entity type, Date range picker
- Sortable by timestamp (default: newest first)
- Click entity → navigate to the entity detail page
- Accessible to all authenticated users (read-only)

### Step 2.5 — Shared Components

| Component | Location | Purpose |
|-----------|----------|---------|
| YamlEditor | `src/components/YamlEditor/` | Monaco editor configured for YAML with validation |
| StatusBadge | `src/components/StatusBadge/` | Color-coded status pill (draft=gray, deploying=blue, running=green, stopped=orange, error=red) |
| BranchSelector | `src/components/BranchSelector/` | Searchable dropdown calling `/api/v1/git/branches`, debounced, with provider icon, fallback to text input |
| ConfirmDialog | `src/components/ConfirmDialog/` | MUI dialog for destructive action confirmation |
| EntityLink | `src/components/EntityLink/` | Clickable link to entity detail pages (for audit log) |

### Phase 2 Verification

- [ ] Login flow works: login → JWT stored → protected routes accessible → logout clears JWT
- [ ] Unauthenticated users redirected to login
- [ ] Stack definitions: create, list, edit, delete all functional
- [ ] Chart configs: add, remove, reorder within a definition
- [ ] YAML editor validates and highlights syntax errors
- [ ] Stack instances: create from definition, edit, clone, delete
- [ ] Branch selector fetches live branches from Azure DevOps and GitLab
- [ ] Branch selector falls back to text input when provider is unavailable
- [ ] Values export produces valid YAML files
- [ ] Dashboard shows all stacks with correct status colors
- [ ] Audit log displays all mutations with correct user attribution
- [ ] All new components have Vitest + RTL tests in `__tests__/`
- [ ] Responsive layout works on desktop (mobile-friendly is nice-to-have)

---

## Phase 3: Deployment Integration

### Step 3.1 — Helm CLI Integration

New package `backend/internal/deployer/`:

- Shell out to `helm upgrade --install` with generated values files
- Support `helm uninstall` for stopping stacks
- Namespace management: create K8s namespace if not exists
- Capture stdout/stderr and store in deployment log
- Timeout handling for long-running deployments
- Deployment status tracking: queued → deploying → success/error

**API additions**:

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/stack-instances/:id/deploy` | Trigger Helm deployment |
| POST | `/api/v1/stack-instances/:id/stop` | Trigger Helm uninstall |
| GET | `/api/v1/stack-instances/:id/deploy-log` | Get deployment output log |

### Step 3.2 — K8s Status Monitoring

New package `backend/internal/k8s/`:

- Connect to AKS Arc cluster via kubeconfig or in-cluster service account
- Watch/poll namespaces for deployment, pod, and service status
- Per-chart health: pods ready, restart count, image versions
- Map K8s status to stack instance status (running/error/stopped)
- Push status updates via existing WebSocket hub for real-time UI updates

**API additions**:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/stack-instances/:id/status` | Detailed K8s status per chart |
| WS | `/ws/stack-status` | WebSocket for real-time status updates |

**Frontend updates**:
- Instance detail page shows live pod status per chart
- Dashboard cards update status in real-time via WebSocket
- Deployment log viewer with auto-scroll

### Step 3.3 — Git Branch Integration Enhancement

Optional enhancement to branch selection:

- Cache invalidation endpoint: `POST /api/v1/git/cache/invalidate`
- Webhook receiver for branch push events (auto-invalidate cache)
- Branch metadata: last commit SHA, author, date (displayed in selector)

### Phase 3 Verification

- [ ] Helm deploy creates namespace and installs all charts in order
- [ ] Helm stop uninstalls all charts and updates status
- [ ] Deployment log captures full helm output
- [ ] K8s status shows correct pod/deployment health per chart
- [ ] WebSocket pushes status changes to connected clients
- [ ] Dashboard cards update in real-time when deployment status changes
- [ ] Failed deployments show error details and don't leave orphaned resources

---

## Phase 4: Value-Add Features

### Step 4.1 — Stack Comparison / Diff

- Compare two stack instances side-by-side
- Per-chart YAML diff with syntax highlighting
- Show which values differ from definition defaults
- Useful for debugging "why does my stack behave differently?"

### Step 4.2 — Stack Templates & Presets

- Mark stack definitions as "template" (official/shared)
- One-click "create stack from template" with sensible defaults
- Pre-ship common templates:
  - "Full Stack" (all 7 services)
  - "Backend Only" (API + database + message queue)
  - "Minimal" (single service + database)
- Template marketplace: browse, preview, clone

### Step 4.3 — Resource Quotas

Important for shared cluster:

- Admin-configurable per-namespace resource limits (CPU, memory)
- Enforce maximum stacks per user (configurable)
- Warning UI when approaching limits
- Dashboard widget showing cluster utilization

### Step 4.4 — Notifications

- WebSocket-based toasts for deployment status changes
- Notification center (bell icon) with unread count
- Per-user notification preferences (which events to notify)
- Optional webhook/Slack integration endpoint for external notifications

### Step 4.5 — Bulk Operations

- Multi-select stacks on dashboard
- Bulk start/stop/delete with confirmation
- "Cleanup" button: delete all stopped stacks older than N days
- Scheduled cleanup via cron-style background job

### Step 4.6 — Environment Promotion

- Promote a stack instance's configuration from dev → staging → production
- Diff values between environments before promotion
- Approval workflow for production promotions (admin-only)

### Step 4.7 — Import/Export

- Export stack definition + all chart configs as a portable JSON/YAML bundle
- Import bundle to recreate stack definition in another environment
- Useful for sharing configurations across organizations

---

## Agent → Phase Mapping

| Agent | Primary Phase | Steps |
|-------|--------------|-------|
| Orchestrator | All | Coordinates multi-step features |
| Data Layer | Phase 1 | 1.1 (models), 1.2 (repositories) |
| Backend API | Phase 1 | 1.3 (endpoints), 1.4 (middleware) |
| Git Provider | Phase 1 | 1.6 (AzDO + GitLab integration) |
| Helm Values | Phase 1, 3 | 1.5 (generation), 3.1 (deployment) |
| Frontend API | Phase 2 | 2.1 (auth), services for all endpoints |
| Frontend UI | Phase 2 | 2.2–2.5 (all pages and components) |
| Test Writer | All | Tests after each feature is built |

## Implementation Order

Within each phase, follow this dependency chain:

```
Models → Repositories → Handlers → Routes → API Client → UI Pages → Tests
```

Specifically:
1. data-layer agent: models + repos
2. backend-api agent: handlers + routes + middleware
3. git-provider agent: provider implementations (parallel with #2)
4. helm-values agent: values generator (parallel with #2)
5. frontend-api agent: API client services + auth context
6. frontend-ui agent: pages and components
7. test-writer agent: comprehensive tests for everything above

---

## Files Created/Modified Per Phase

### Phase 0 — New Files

```
.github/
├── copilot-instructions.md
├── agents/
│   ├── backend-api.agent.md
│   ├── data-layer.agent.md
│   ├── frontend-ui.agent.md
│   ├── frontend-api.agent.md
│   ├── git-provider.agent.md
│   ├── helm-values.agent.md
│   ├── test-writer.agent.md
│   └── orchestrator.agent.md
└── instructions/
    ├── go-handlers.instructions.md
    ├── azure-table-repos.instructions.md
    └── react-pages.instructions.md
```

### Phase 1 — New Files

```
backend/internal/
├── models/
│   ├── user.go
│   ├── stack_definition.go
│   ├── chart_config.go
│   ├── stack_instance.go
│   ├── value_override.go
│   └── audit_log.go
├── database/azure/
│   ├── user_repository.go
│   ├── stack_definition_repository.go
│   ├── chart_config_repository.go
│   ├── stack_instance_repository.go
│   ├── value_override_repository.go
│   └── audit_log_repository.go
├── api/
│   ├── handlers/
│   │   ├── auth.go
│   │   ├── stack_definitions.go
│   │   ├── chart_configs.go
│   │   ├── stack_instances.go
│   │   ├── value_overrides.go
│   │   ├── git.go
│   │   └── audit_logs.go
│   └── middleware/
│       ├── auth.go
│       ├── audit.go
│       └── role.go
├── helm/
│   ├── values_generator.go
│   └── values_generator_test.go
└── gitprovider/
    ├── provider.go
    ├── azuredevops.go
    ├── gitlab.go
    ├── registry.go
    └── provider_test.go
```

### Phase 2 — New Files

```
frontend/src/
├── context/
│   └── AuthContext.tsx
├── pages/
│   ├── Login/
│   │   ├── index.tsx
│   │   └── __tests__/Login.test.tsx
│   ├── StackDefinitions/
│   │   ├── List.tsx
│   │   ├── Form.tsx
│   │   └── __tests__/
│   ├── StackInstances/
│   │   ├── Dashboard.tsx
│   │   ├── Detail.tsx
│   │   ├── Form.tsx
│   │   └── __tests__/
│   └── AuditLog/
│       ├── index.tsx
│       └── __tests__/
├── components/
│   ├── YamlEditor/
│   │   ├── index.tsx
│   │   └── __tests__/
│   ├── StatusBadge/
│   │   ├── index.tsx
│   │   └── __tests__/
│   ├── BranchSelector/
│   │   ├── index.tsx
│   │   └── __tests__/
│   ├── ConfirmDialog/
│   │   ├── index.tsx
│   │   └── __tests__/
│   ├── ProtectedRoute/
│   │   └── index.tsx
│   └── EntityLink/
│       └── index.tsx
└── api/
    └── client.ts (modified: add auth + all new service methods)
```

### Phase 3 — New Files

```
backend/internal/
├── deployer/
│   ├── helm.go
│   └── helm_test.go
└── k8s/
    ├── client.go
    ├── watcher.go
    └── client_test.go
```

---

## Configuration Reference

### Environment Variables (all phases)

| Variable | Default | Phase | Description |
|----------|---------|-------|-------------|
| `USE_AZURE_TABLE` | `false` | 1 | Enable Azure Table storage |
| `AZURE_TABLE_ACCOUNT_NAME` | `""` | 1 | Azure Storage account name |
| `AZURE_TABLE_ACCOUNT_KEY` | `""` | 1 | Azure Storage account key |
| `USE_AZURITE` | `false` | 1 | Use Azurite emulator for local dev |
| `JWT_SECRET` | `""` | 1 | **Required.** Secret for JWT signing |
| `JWT_EXPIRATION` | `24h` | 1 | JWT token expiration duration |
| `AZURE_DEVOPS_PAT` | `""` | 1 | Azure DevOps personal access token |
| `AZURE_DEVOPS_DEFAULT_ORG` | `""` | 1 | Default Azure DevOps organization |
| `GITLAB_TOKEN` | `""` | 1 | GitLab private token |
| `GITLAB_BASE_URL` | `https://gitlab.com` | 1 | GitLab instance URL |
| `ADMIN_USERNAME` | `admin` | 1 | Default admin username (first-run) |
| `ADMIN_PASSWORD` | `""` | 1 | Default admin password (first-run) |
| `SELF_REGISTRATION` | `false` | 1 | Allow self-registration |
| `HELM_BINARY` | `helm` | 3 | Path to helm binary |
| `KUBECONFIG` | `""` | 3 | Path to kubeconfig file |
| `DEFAULT_BRANCH` | `master` | 1 | Global default branch name |

---

## Decisions Log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Phase 3 (Helm deployment) deferred | Config management + values export delivers immediate value without K8s access |
| 2 | Branch defaults to "master" | Configurable per definition; most common convention |
| 3 | Namespace auto-generated as `stack-{name}-{owner}` | Prevents collisions in shared cluster |
| 4 | No company-specific hardcoding | All branding via env/config for multi-company use |
| 5 | Monaco Editor for YAML | Familiar to developers (VS Code's editor) |
| 6 | Azure Tables for persistence | Already wired in template; matches existing infrastructure |
| 7 | Shared service-level tokens for Git providers | Simpler than per-user PATs; admin configures once |
| 8 | Azure DevOps + GitLab first | GitHub support easy to add later via same interface |
| 9 | JWT auth (Phase 1) | Simple to start; OIDC upgrade path clear for Phase 4+ |
| 10 | All stacks visible to all users | Team transparency; audit log tracks ownership |
| 11 | Branch cache 5-min TTL | Balances freshness vs. API rate limits |
| 12 | URL-based provider detection | No per-chart provider config needed; just look at the URL |

---

## Future Considerations

1. **Multi-cluster support**: Data model can extend with `cluster_id` field when needed
2. **Helm chart version pinning**: Support both explicit version and "latest" tracking
3. **OIDC/SSO upgrade**: Replace JWT login with OIDC provider (Azure AD, Okta) — AuthContext already abstracts this
4. **GitOps integration**: Export stack configurations as GitOps manifests (Flux/ArgoCD)
5. **Cost tracking**: Integrate with Azure Cost Management to show per-stack resource costs
6. **API rate limiting**: Add per-user rate limits for Git provider APIs
7. **Backup/restore**: Periodic Azure Table backup for disaster recovery
