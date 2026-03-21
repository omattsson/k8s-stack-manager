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

1. Browse a **template gallery** of curated application stacks created by DevOps
2. **Instantiate** a template into a personal **stack definition**, using it as-is or extending it
3. Define freeform **stack definitions** (collections of Helm charts) without a template
4. Spin up **stack instances** from those definitions, choosing branches per service
5. Configure **values overrides** per chart with a YAML editor (respecting template-locked values)
6. **Export** generated `values.yaml` files for use with Helm
7. See **all stacks** across the team with a full **audit log** of changes
8. (Future) **Deploy directly** to the shared AKS Arc cluster

DevOps teams can:

1. **Create and version** stack templates with curated charts and default values
2. **Lock values** that developers cannot override (enforcing guardrails)
3. **Mark charts as required** that developers cannot remove
4. **Publish/unpublish** templates to control availability

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Frontend (React)                     │
│  MUI · TypeScript · Vite · Monaco Editor · WebSocket    │
├─────────────────────────────────────────────────────────┤
│                   Backend (Go + Gin)                     │
│  REST API · JWT Auth · Swagger · Audit Middleware        │
├──────────┬──────────┬───────────┬──────────┬──────────────┤
│ Azure    │ Template │ Git       │ Helm     │ K8s          │
│ Tables   │ Engine   │ Provider  │ Values   │ Deployer     │
│ (CRUD)   │ (Create, │ (AzDO +   │ (Merge + │ (Phase 3)    │
│          │  Publish,│  GitLab)  │  Export) │              │
│          │  Lock)   │           │         │              │
└──────────┴──────────┴───────────┴──────────┴──────────────┘
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

### Step 0.2 — Custom Agents (9 agents)

All agents in `.github/agents/`:

| Agent | File | Purpose |
|-------|------|---------|
| Go API Developer | `go-api-developer.md` | Go backend: models, repositories (MySQL + Azure Table), handlers, routes, migrations, swagger |
| Frontend Developer | `frontend-developer.md` | React pages, MUI components, API services, routing, data fetching |
| Git Provider Specialist | `git-provider.agent.md` | Azure DevOps + GitLab API integration, URL parsing, caching |
| Helm Values Specialist | `helm-values.agent.md` | YAML deep-merge, template variables, values export |
| QA Engineer | `qa-engineer.md` | Test strategy, Go tests (testify), React tests (Vitest/RTL), E2E (Playwright) |
| Orchestrator | `orchestrator.md` | Coordinates multi-step features across specialist agents |
| DevOps Engineer | `devops-engineer.md` | Docker, nginx, Makefile, CI/CD, deployment |
| SCM Engineer | `scm-engineer.md` | Git branches, commits, pull requests |
| Code Reviewer | `code-reviewer.md` | PR review, security audit, pattern compliance |

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
| Role | string | "admin" / "devops" / "user" |
| CreatedAt | time.Time | |
| UpdatedAt | time.Time | |

**StackTemplate** *(new — DevOps-managed reusable stack recipes)*
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| Name | string | Unique |
| Description | string | |
| Category | string | e.g., "Full Stack", "Backend Only", "Minimal", "Custom" |
| Version | string | Semver, e.g., "1.0.0" |
| OwnerID | string | FK → User (DevOps/Admin only) |
| DefaultBranch | string | Default: "master" |
| IsPublished | bool | Only published templates visible to developers |
| CreatedAt | time.Time | |
| UpdatedAt | time.Time | |

**TemplateChartConfig** *(new — charts within a template)*
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| StackTemplateID | string | FK → StackTemplate |
| ChartName | string | e.g., "my-service" |
| RepositoryURL | string | Helm chart repo URL |
| SourceRepoURL | string | Git repo for branch listing |
| ChartPath | string | Path within repo to chart |
| ChartVersion | string | Version or "latest" |
| DefaultValues | string | YAML — base values |
| LockedValues | string | YAML — values devs **cannot** override |
| DeployOrder | int | Deployment sequence |
| Required | bool | If true, dev cannot remove this chart from derived definitions |
| CreatedAt | time.Time | |

**StackDefinition**
| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| Name | string | Unique |
| Description | string | |
| OwnerID | string | FK → User |
| SourceTemplateID | string | Nullable — FK → StackTemplate if created from template |
| SourceTemplateVersion | string | Template version when instantiated (for upgrade awareness) |
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
| Users | `"users"` | username | Fast lookup by username for auth || StackTemplates | `"global"` | template_id | All templates visible for browsing |
| TemplateChartConfigs | stack_template_id | chart_config_id | All charts for a template || StackDefinitions | `"global"` | definition_id | All definitions visible to all users |
| ChartConfigs | stack_definition_id | chart_config_id | All charts for a definition |
| StackInstances | `"global"` | instance_id | All instances visible to all users |
| ValueOverrides | stack_instance_id | chart_config_id | All overrides for an instance |
| AuditLogs | `YYYY-MM` | reverse_timestamp + uuid | Recent-first within monthly partitions |

**Repositories to create**:
- `UserRepository` — CRUD + `FindByUsername()`
- `StackTemplateRepository` — CRUD + `List()` + `ListPublished()` + `ListByOwner(ownerID)`
- `TemplateChartConfigRepository` — CRUD + `ListByTemplate(templateID)`
- `StackDefinitionRepository` — CRUD + `List()` + `ListByTemplate(templateID)`
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

**Stack Templates** (`/api/v1/templates`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | List published templates (devs) / all templates (devops/admin) | User |
| POST | `/` | Create template | DevOps/Admin |
| GET | `/:id` | Get template with charts | User |
| PUT | `/:id` | Update template | DevOps/Admin |
| DELETE | `/:id` | Delete template (if no definitions link to it) | DevOps/Admin |
| POST | `/:id/publish` | Publish template (makes visible to devs) | DevOps/Admin |
| POST | `/:id/unpublish` | Unpublish template (hides from devs) | DevOps/Admin |
| POST | `/:id/instantiate` | Create StackDefinition from template (copies charts + values) | User |
| POST | `/:id/clone` | Clone as new draft template (for versioning) | DevOps/Admin |

**Template Charts** (nested under `/api/v1/templates/:id/charts`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| POST | `/` | Add chart to template | DevOps/Admin |
| PUT | `/:chartId` | Update chart (values, locked values, required flag) | DevOps/Admin |
| DELETE | `/:chartId` | Remove chart from template | DevOps/Admin |

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
- Support role hierarchy: admin > devops > user
- DevOps role can manage templates but not system-level admin tasks
- Return 403 for insufficient permissions

### Step 1.5 — Helm Values Generation

New package `backend/internal/helm/`:

**ValuesGenerator**:
- Deep-merge: template locked values (highest priority) + chart default values + instance value overrides → final `values.yaml`
- Locked values from templates always win and cannot be overridden
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

- [ ] All 8 model structs defined with proper types and tags
- [ ] All 8 Azure Table repositories implement CRUD + filters
- [ ] StackTemplate + TemplateChartConfig repos support publish/unpublish + ListPublished
- [ ] Instantiate endpoint copies template charts into a new StackDefinition
- [ ] Locked values enforced: override attempts on locked keys rejected by API
- [ ] Required charts enforced: delete attempts on required charts rejected by API
- [ ] DevOps role can manage templates but not admin-level operations
- [ ] All REST endpoints return correct status codes and response shapes
- [ ] JWT auth middleware rejects unauthenticated requests with 401
- [ ] Role middleware rejects unauthorized requests with 403 (admin > devops > user)
- [ ] Every POST/PUT/DELETE endpoint produces an AuditLog entry
- [ ] Helm values merge produces valid YAML with correct override precedence (locked > override > default)
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

### Step 2.2 — Stack Templates Pages (DevOps + Dev)

**Template Gallery** (`/templates`) — All authenticated users:
- Card/grid view of published templates with category filters
- Each card shows: Name, Description, Category, Version, Chart count, "Use Template" button
- Search bar for templates by name/description
- DevOps/Admin users see an additional "My Templates" tab with draft and published templates
- "Create Template" button visible only to DevOps/Admin

**Template Builder** (`/templates/new`, `/templates/:id/edit`) — DevOps/Admin only:
- Form fields: Name, Description, Category (dropdown), Version, Default Branch
- Dynamic chart list:
  - Add/remove/reorder charts via drag-and-drop
  - Per chart: Chart Name, Repo URL, Source Repo URL, Chart Path, Version, Deploy Order
  - Default Values: inline YAML editor (Monaco) per chart
  - Locked Values: separate YAML editor — values that devs **cannot** override
  - Required toggle: if on, devs cannot remove this chart from derived definitions
- Publish/Unpublish toggle
- Save → POST/PUT API call

**Template Preview** (`/templates/:id`) — All users:
- Read-only view of template: name, description, category, version, charts
- Per-chart: default values, locked values (highlighted), required badge
- "Use Template" button → navigates to instantiate flow
- "Clone as Template" button (DevOps/Admin only) → creates a new draft copy

**Instantiate from Template** (`/templates/:id/use`):
- Pre-filled form with template’s charts and values
- Dev enters: Definition name, optional description
- Per-chart value editor pre-populated with template defaults
- Locked values shown as read-only (visually distinct, grayed out)
- Required charts marked with badge; optional charts can be toggled off
- Save → POST `/api/v1/templates/:id/instantiate` → navigate to new StackDefinition

### Step 2.3 — Stack Definitions Pages

**List page** (`/stack-definitions`):
- Table with columns: Name, Description, Owner, Source Template, Charts count, Created, Actions
- Source Template column shows linked template name (with link) or "—" if freeform
- "Template upgrade available" badge when source template has newer version
- Create button → navigate to create form
- Row click → navigate to detail/edit

**Create/Edit page** (`/stack-definitions/new`, `/stack-definitions/:id/edit`):
- Form fields: Name, Description, Default Branch
- If derived from template: shows source template link + version
- Dynamic chart list:
  - Add/remove/reorder charts via drag-and-drop
  - Required charts (from template) cannot be removed — shown with lock icon
  - Per chart: Chart Name, Repo URL, Source Repo URL, Chart Path, Version, Deploy Order
  - Default Values: inline YAML editor (Monaco) per chart
  - Locked values (from template) shown as read-only within the editor
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

### Step 2.5 — Audit Log Page

**Audit Log** (`/audit-log`):
- Table with columns: Timestamp, User, Action, Entity Type, Entity, Details (expandable)
- Filters: User dropdown, Action type, Entity type, Date range picker
- Sortable by timestamp (default: newest first)
- Click entity → navigate to the entity detail page
- Accessible to all authenticated users (read-only)

### Step 2.6 — Shared Components

| Component | Location | Purpose |
|-----------|----------|---------|
| YamlEditor | `src/components/YamlEditor/` | Monaco editor configured for YAML with validation |
| StatusBadge | `src/components/StatusBadge/` | Color-coded status pill (draft=gray, deploying=blue, running=green, stopped=orange, error=red) |
| BranchSelector | `src/components/BranchSelector/` | Searchable dropdown calling `/api/v1/git/branches`, debounced, with provider icon, fallback to text input |
| ConfirmDialog | `src/components/ConfirmDialog/` | MUI dialog for destructive action confirmation |
| EntityLink | `src/components/EntityLink/` | Clickable link to entity detail pages (for audit log) |
| LockedValuesBanner | `src/components/LockedValuesBanner/` | Read-only YAML display for template-locked values |
| TemplateBadge | `src/components/TemplateBadge/` | Badge showing source template name + version, with upgrade indicator |

### Phase 2 Verification

- [ ] Login flow works: login → JWT stored → protected routes accessible → logout clears JWT
- [ ] Unauthenticated users redirected to login
- [ ] Template gallery: devs see only published templates; devops/admin see all
- [ ] Template builder: devops/admin can create, edit, publish/unpublish templates
- [ ] Template locked values and required charts are enforced in the UI
- [ ] Instantiate from template pre-fills definition with correct charts and values
- [ ] Derived definitions show source template link and upgrade indicator
- [ ] Stack definitions: create, list, edit, delete all functional
- [ ] Chart configs: add, remove, reorder within a definition (respecting required flag)
- [ ] YAML editor validates and highlights syntax errors
- [ ] Locked values shown as read-only in definition editor
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

## Phase 4: Multi-Cluster Support

Phase 4 adds support for multiple Kubernetes clusters — the foundation for multi-team usage and environment separation.

### Step 4.1 — Multi-Cluster Support

The current architecture is hardcoded to a single kubeconfig — `k8s.NewClient(kubeconfigPath)` and `HelmClient` both take one kubeconfig path. Multi-cluster requires a cluster registry and per-operation cluster routing.

**New model** — `Cluster`:

| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| Name | string | Unique display name (e.g., "dev-aks-westeu", "staging-arc") |
| Description | string | |
| APIServerURL | string | K8s API server endpoint (for display/health checks) |
| KubeconfigData | string | Encrypted kubeconfig content (stored encrypted at rest) |
| KubeconfigPath | string | Alternative: path on server filesystem (for mounted secrets) |
| IsDefault | bool | Default cluster for new instances (exactly one) |
| Region | string | Geographic/logical grouping |
| HealthStatus | string | healthy/degraded/unreachable (updated by health poller) |
| MaxNamespaces | int | Capacity limit (0 = unlimited) |
| CreatedAt | time.Time | |
| UpdatedAt | time.Time | |

**Architecture changes**:

- **ClusterRegistry** — New package `backend/internal/cluster/` with a `Registry` that manages multiple `*k8s.Client` and `*HelmClient` instances keyed by cluster ID. Lazy-initializes clients on first use, caches them, and handles reconnection.
- **StackInstance gets ClusterID** — Add `ClusterID string` field to `StackInstance`. When creating an instance, user picks a cluster (or uses the default). The deployer, k8s watcher, and admin handler all resolve the cluster ID to the correct k8s client/helm client.
- **HelmClient per cluster** — `NewHelmClient()` already takes a kubeconfig path. The registry creates one `HelmClient` per cluster. `deployer.Manager` gets a `ClusterRegistry` instead of a single `*k8s.Client` + `HelmExecutor`.
- **K8s Watcher per cluster** — The watcher polls all active clusters. `GetNamespaceStatus()` routes to the correct client by cluster ID.
- **Kubeconfig security** — Kubeconfig data is encrypted at rest using AES-256-GCM with a server-side key (`KUBECONFIG_ENCRYPTION_KEY` env var). Never exposed via API — only cluster name, URL, health, and region are returned to clients.
- **Health poller** — Background goroutine pings each cluster's API server periodically, updates `HealthStatus`, and broadcasts changes via WebSocket.

**API additions** (`/api/v1/clusters`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | List clusters (name, URL, region, health — no kubeconfig) | User |
| POST | `/` | Register cluster (with kubeconfig data or path) | Admin |
| GET | `/:id` | Cluster detail + resource summary | User |
| PUT | `/:id` | Update cluster metadata/kubeconfig | Admin |
| DELETE | `/:id` | Remove cluster (only if no running instances) | Admin |
| POST | `/:id/test` | Test connectivity to cluster | Admin |
| GET | `/:id/health` | Detailed cluster health (nodes, capacity) | DevOps/Admin |

**Frontend changes**:

- Cluster selector dropdown in instance create/edit form
- Cluster health indicators in dashboard sidebar
- Admin page: cluster management CRUD
- Instance detail shows which cluster it's deployed to

**Migration path**: Existing instances without `ClusterID` are assigned to a "default" cluster auto-created from the current `KUBECONFIG_PATH` env var on first startup.

**Implementation order**:
1. `Cluster` model + repository + migration
2. `ClusterRegistry` package (client pool with lazy init)
3. Refactor `deployer.Manager` and `k8s.Watcher` to accept registry
4. Add `ClusterID` to `StackInstance` + migration
5. Cluster CRUD handlers + routes
6. Health poller background goroutine
7. Frontend: cluster selector, admin page, health indicators
8. Tests: registry unit tests, handler tests, migration test

---

## Phase 5: Developer Experience

Features that make the daily developer workflow faster — deploy branches, see results, clean up automatically.

### Step 5.1 — Per-Chart Branch Override

Currently `StackInstance.Branch` is a single field applied to all charts. A developer working on one microservice wants `feature/my-branch` for that service and `master` for everything else.

**Model changes**:

- New model `ChartBranchOverride`:

| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| StackInstanceID | string | FK → StackInstance |
| ChartConfigID | string | FK → ChartConfig |
| Branch | string | Overrides instance-level branch for this chart |
| UpdatedAt | time.Time | |

**Values generation change**: `ValuesGenerator` already substitutes `{{.Branch}}`. Change to per-chart: if a `ChartBranchOverride` exists for the chart, use it; otherwise fall back to `StackInstance.Branch`.

**API additions**:

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/api/v1/stack-instances/:id/branches` | Get all branch overrides for instance | User |
| PUT | `/api/v1/stack-instances/:id/branches/:chartId` | Set branch for specific chart | User |
| DELETE | `/api/v1/stack-instances/:id/branches/:chartId` | Reset to instance default | User |

**Frontend changes**:

- Instance detail page: each chart row shows its effective branch
- Branch selector per chart (editable inline or in a popover)
- Visual indicator when a chart uses a non-default branch

**Implementation order**:
1. `ChartBranchOverride` model + repository + migration
2. Update `ValuesGenerator` to accept per-chart branches
3. Handler + routes for branch overrides
4. Update deploy handler to pass per-chart branches to values generation
5. Frontend: per-chart branch selector
6. Tests

### Step 5.2 — Instance TTL / Auto-Expiry

Developers spin up stacks for demos and forget about them. TTL prevents stale stacks from consuming cluster resources.

**Model changes**:

- `StackInstance` gets new fields: `ExpiresAt *time.Time`, `TTLMinutes int` (0 = no expiry)
- Default TTL configurable per-cluster or globally via `DEFAULT_INSTANCE_TTL_MINUTES` env var

**Background worker**: New `ttl.Reaper` goroutine that runs every minute:
1. Queries instances where `ExpiresAt < now` and `Status` is `running`
2. Calls `deployer.Manager.StopWithCharts()` for each expired instance
3. Updates status to `stopped` with message "Expired (TTL)"
4. Broadcasts WebSocket notification

**API changes**:

- Create/update instance accepts `ttl_minutes` field
- `GET /api/v1/stack-instances/:id` returns `expires_at` and `ttl_minutes`
- `POST /api/v1/stack-instances/:id/extend` — extend TTL by configured increment

**Frontend changes**:

- TTL selector when creating/deploying an instance (e.g., "4h", "8h", "24h", "No expiry")
- Countdown badge on running instances approaching expiry
- "Extend" button on instance detail and dashboard cards
- Toast notification 30 minutes before expiry (via WebSocket)

### Step 5.3 — Service URL / Ingress Display

After deploy, the developer needs to know how to reach their stack without running `kubectl`.

**Implementation**: Extend `k8s.GetNamespaceStatus()` to include:
- Ingress resources with host/path rules and URLs
- Services of type `LoadBalancer` with external IPs
- Services of type `NodePort` with node port numbers
- Constructed access URLs based on ingress rules

**Model changes**:

- `ServiceInfo` in `k8s/status.go` gets: `ExternalIP string`, `NodePort int32`, `IngressHosts []string`
- New `IngressInfo` struct: `Host`, `Path`, `TLS bool`, `URL string`
- `NamespaceStatus` gets `Ingresses []IngressInfo`

**API changes**: Existing `GET /api/v1/stack-instances/:id/status` response extended with ingress and URL data. No new endpoints needed.

**Frontend changes**:

- Instance detail "Access" section: clickable URLs for each exposed service
- Dashboard cards show primary URL as a quick-link
- Copy-to-clipboard for URLs and `kubectl port-forward` commands

### Step 5.4 — Quick Deploy (One-Click Flow)

Reduce the steps from "I want to test my branch" to a running stack.

**New API endpoint**:

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| POST | `/api/v1/quick-deploy` | Create instance from template + deploy in one step | User |

**Request body**:
```json
{
  "template_id": "...",
  "branch": "feature/my-branch",
  "cluster_id": "...",       // optional, uses default
  "ttl_minutes": 240,        // optional, uses default
  "name": "my-feature-test", // optional, auto-generated from branch
  "chart_branches": {        // optional, per-chart overrides
    "chart-config-id": "other-branch"
  }
}
```

**Backend flow**:
1. Instantiate template → create definition (transient, linked to template)
2. Create instance from definition with branch + optional per-chart overrides
3. Trigger deploy immediately
4. Return instance ID + log ID (HTTP 202)

**Frontend changes**:

- "Quick Deploy" button on template gallery cards
- Streamlined modal: pick template → enter branch → optional cluster/TTL → deploy
- Redirects to instance detail page showing live deploy log

### Step 5.5 — Favorites & Recent Stacks

Quick access to frequently-used definitions and instances.

**Model changes**:

- New model `UserFavorite`: `UserID`, `EntityType` (definition/instance/template), `EntityID`, `CreatedAt`
- Recent stacks: no model needed — query instances by `OwnerID` + `ORDER BY updated_at DESC LIMIT 5`

**API additions**:

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/api/v1/favorites` | List user's favorites | User |
| POST | `/api/v1/favorites` | Add favorite | User |
| DELETE | `/api/v1/favorites/:id` | Remove favorite | User |
| GET | `/api/v1/stack-instances/recent` | Recently used instances (top 5) | User |

**Frontend changes**:

- Star/heart toggle on cards (definitions, instances, templates)
- Dashboard: "My Favorites" and "Recent" sections above the full list
- Persisted via API, not localStorage (works across devices)

---

## Phase 6: Operations & Observability

Features for DevOps and admins to manage the platform at scale.

### Step 6.1 — Cluster Health Dashboard

DevOps needs a cluster-wide view beyond per-stack status.

**New K8s status methods** in `k8s/status.go`:
- `GetClusterSummary()` — node count, total/allocatable CPU/memory, namespace count
- `GetNodeStatuses()` — per-node: conditions, capacity, allocatable, pods running

**API additions** (`/api/v1/clusters/:id`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/health/summary` | Node count, total CPU/mem, namespace count | DevOps/Admin |
| GET | `/health/nodes` | Per-node status detail | DevOps/Admin |
| GET | `/namespaces` | All stack-* namespaces with resource usage | DevOps/Admin |

**Frontend changes**:

- New "Cluster Health" page (DevOps/Admin only)
- Per-cluster cards: node count, CPU/memory utilization bars, instance count
- Node list with status indicators (Ready, NotReady, MemoryPressure, DiskPressure)
- Namespace table with resource consumption per stack

### Step 6.2 — Namespace Cleanup Policies

Beyond manual orphan detection — automated scheduled cleanup.

**New model** — `CleanupPolicy`:

| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| Name | string | e.g., "Auto-stop idle stacks" |
| ClusterID | string | FK → Cluster (or "all") |
| Action | string | "stop" / "clean" / "delete" |
| Condition | string | "idle_days:7" / "status:stopped,age_days:14" / "ttl_expired" |
| Schedule | string | Cron expression (e.g., "0 2 * * *" = daily 2am) |
| Enabled | bool | |
| LastRunAt | *time.Time | |
| CreatedAt | time.Time | |

**Background scheduler**: Uses a cron-style runner (e.g., `robfig/cron/v3`) to evaluate policies on schedule. Each policy run:
1. Queries instances matching the condition on the target cluster(s)
2. Executes the action (stop/clean/delete)
3. Creates audit log entries for each affected instance
4. Updates `LastRunAt`

**API additions** (`/api/v1/admin/cleanup-policies`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | List cleanup policies | Admin |
| POST | `/` | Create policy | Admin |
| PUT | `/:id` | Update policy | Admin |
| DELETE | `/:id` | Delete policy | Admin |
| POST | `/:id/run` | Trigger policy manually (dry-run option) | Admin |

### Step 6.3 — Template Usage Analytics

Help DevOps understand which templates are effective and which charts cause problems.

**Implementation**: Aggregation queries over existing data — no new models needed.

- **Template popularity**: Count of definitions derived from each template
- **Deploy success rate**: Count of success/error deployment logs per template/chart
- **Most-used charts**: Frequency across all definitions
- **Failure hotspots**: Charts with highest error rate in deployments

**API additions** (`/api/v1/analytics`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/templates` | Template usage stats (instance count, deploy success rate) | DevOps/Admin |
| GET | `/charts` | Chart deploy stats (success/failure count, avg deploy time) | DevOps/Admin |
| GET | `/users` | Per-user instance count and resource usage | Admin |

**Frontend changes**:

- Analytics tab on template detail page (usage count, success rate, etc.)
- Admin dashboard: charts showing template adoption and deploy health over time

### Step 6.4 — Shared Values & Environment Variables

Common values (registry URLs, database endpoints, monitoring config) that apply across all stacks in a cluster, maintained in one place instead of duplicated per template.

**New model** — `SharedValues`:

| Field | Type | Notes |
|-------|------|-------|
| ID | string (UUID) | |
| ClusterID | string | FK → Cluster (or "global") |
| Name | string | e.g., "registry-config", "monitoring-defaults" |
| Description | string | |
| Values | string | YAML content |
| Priority | int | Merge order (lower = applied first, overridden by higher) |
| CreatedAt | time.Time | |
| UpdatedAt | time.Time | |

**Values merge order** (lowest to highest priority):
1. Shared values (by priority within cluster)
2. Template default values
3. Template locked values (cannot be overridden by anything below)
4. Definition default values
5. Instance value overrides

**API additions** (`/api/v1/clusters/:id/shared-values`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/` | List shared values for cluster | DevOps/Admin |
| POST | `/` | Create shared values set | Admin |
| PUT | `/:valueId` | Update shared values | Admin |
| DELETE | `/:valueId` | Delete shared values set | Admin |

### Step 6.5 — Audit Log Export & Alerting

Enterprise audit requirements — export to external systems and alert on anomalies.

**Export**: New endpoint `GET /api/v1/audit-logs/export?format=csv&from=...&to=...` returns audit logs as CSV or JSON download. Supports same filters as the list endpoint.

**Webhook alerting**: New model `AuditWebhook` — URL, events filter, secret for HMAC signature. On matching audit events, POST the event payload to the configured URL (async, best-effort with retry). Supports Slack-compatible webhook format.

**API additions**:

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/api/v1/audit-logs/export` | Download audit logs as CSV/JSON | Admin |
| GET | `/api/v1/admin/webhooks` | List configured webhooks | Admin |
| POST | `/api/v1/admin/webhooks` | Create webhook | Admin |
| DELETE | `/api/v1/admin/webhooks/:id` | Delete webhook | Admin |
| POST | `/api/v1/admin/webhooks/:id/test` | Send test event | Admin |

---

## Phase 7: Polish & Ecosystem

### Step 7.1 — Stack Comparison / Diff

- Compare two stack instances side-by-side
- Per-chart YAML diff with syntax highlighting
- Show which values differ from definition defaults
- Useful for debugging "why does my stack behave differently?"

### Step 7.2 — Resource Quotas

Important for shared clusters:

- Admin-configurable per-namespace resource limits (CPU, memory) via K8s `ResourceQuota` and `LimitRange`
- Enforce maximum stacks per user (configurable per cluster)
- Warning UI when approaching limits
- Dashboard widget showing cluster utilization vs. quotas

### Step 7.3 — Notifications

- WebSocket-based toasts for deployment status changes (already partially implemented via `deployment.status` messages)
- Notification center (bell icon) with unread count and notification history
- Per-user notification preferences (which events to notify)
- Optional webhook/Slack integration endpoint for external notifications (shares infra with Step 6.5 webhooks)

### Step 7.4 — Bulk Operations

- Multi-select stacks on dashboard
- Bulk start/stop/delete with confirmation dialog
- "Cleanup" button: delete all stopped stacks older than N days
- Integrates with cleanup policies from Step 6.2 for scheduled automation

### Step 7.5 — Import/Export

- Export stack definition + all chart configs as a portable JSON/YAML bundle
- Import bundle to recreate stack definition in another environment
- Useful for sharing configurations across organizations
- Include per-chart branch overrides and value overrides in export

### Step 7.6 — Template Versioning & Upgrades

- Template versioning with diff between versions
- Template upgrade workflow: detect newer template version, show diff, one-click upgrade for derived definitions
- Template marketplace: share templates across organizations (optional)

---

## Phase 8: External Authentication (OIDC / Entra ID)

The current auth is username/password with bcrypt hashing and JWT tokens (`middleware/auth.go`). External SSO extends this without replacing it — local accounts remain available as fallback. This is the last phase because the existing JWT auth works well for internal teams; OIDC is needed when onboarding external users or enforcing corporate SSO policies.

**Design**: OpenID Connect (OIDC) — works with Entra ID (Azure AD), Okta, Google Workspace, Keycloak, and any OIDC-compliant provider. The backend is the OIDC Relying Party; the frontend initiates the Authorization Code Flow with PKCE.

### Step 8.1 — OIDC Backend

**New config** — `OIDCConfig`:

```go
type OIDCConfig struct {
    Enabled      bool   // OIDC_ENABLED (default: false)
    ProviderURL  string // OIDC_PROVIDER_URL (e.g., https://login.microsoftonline.com/{tenant}/v2.0)
    ClientID     string // OIDC_CLIENT_ID
    ClientSecret string // OIDC_CLIENT_SECRET (empty for public clients with PKCE)
    RedirectURL  string // OIDC_REDIRECT_URL (e.g., https://app.example.com/auth/callback)
    Scopes       string // OIDC_SCOPES (default: "openid profile email")
    RoleClaim    string // OIDC_ROLE_CLAIM (default: "roles" — claim path for role mapping)
    AdminRoles   string // OIDC_ADMIN_ROLES (comma-sep, e.g., "k8s-stack-admin")
    DevOpsRoles  string // OIDC_DEVOPS_ROLES (comma-sep, e.g., "k8s-stack-devops")
    AutoProvision bool  // OIDC_AUTO_PROVISION (default: true — create user on first login)
    LocalAuth    bool   // OIDC_LOCAL_AUTH (default: true — keep local login available)
}
```

**Auth flow (Authorization Code with PKCE)**:

1. Frontend calls `GET /api/v1/auth/oidc/authorize` → backend returns redirect URL with PKCE `code_challenge` and `state` nonce (stored server-side with short TTL)
2. Browser redirects to IdP login page
3. IdP redirects back to `GET /api/v1/auth/oidc/callback?code=...&state=...`
4. Backend exchanges code for tokens at IdP's token endpoint
5. Backend validates ID token, extracts claims (sub, email, name, roles)
6. Backend provisions or updates the local `User` record (JIT provisioning)
7. Backend maps IdP roles → app roles (admin/devops/user) via configured claim + role mappings
8. Backend issues a local JWT (same format as today) and redirects to frontend with token

**Model changes**:

- `User` gets new fields: `AuthProvider string` ("local" or "oidc"), `ExternalID string` (IdP subject claim), `Email string`
- New model `AuthState` (short-lived, for CSRF/replay protection): `State string`, `CodeVerifier string`, `ExpiresAt time.Time`

**API additions** (`/api/v1/auth`):

| Method | Path | Description | Auth |
|--------|------|-------------|------|
| GET | `/oidc/authorize` | Start OIDC flow, return redirect URL | None |
| GET | `/oidc/callback` | Exchange code, provision user, return JWT | None |
| GET | `/oidc/config` | Public OIDC config (enabled, provider name) for frontend | None |

**Security considerations**:

- PKCE prevents authorization code interception (mandatory even with client secret)
- State parameter prevents CSRF on the callback
- ID token signature verified against IdP's JWKS endpoint
- Nonce in ID token prevents replay attacks
- Token exchange happens server-side — authorization code never processed in browser
- Auto-provisioned users get "user" role unless IdP roles match configured admin/devops mappings

### Step 8.2 — OIDC Frontend

**Frontend changes**:

- Login page shows "Sign in with [Provider Name]" button when OIDC is enabled
- Local login form shown when `LocalAuth` is true (side-by-side or tabbed)
- New route `/auth/callback` to receive the redirect and store the JWT
- Auth context updated to handle both flows

**Implementation order**:
1. `OIDCConfig` in config.go + env var loading
2. OIDC provider package (`backend/internal/auth/oidc.go`) — uses `coreos/go-oidc/v3` library
3. User model changes + migration (add `AuthProvider`, `ExternalID`, `Email`)
4. Auth state storage (in-memory with TTL or Azure Table with short partition expiry)
5. OIDC handlers (authorize, callback, config)
6. Update `AuthRequired` middleware to continue accepting local JWTs unchanged
7. Frontend: login page OIDC button, callback route, auth context
8. Tests: mock OIDC provider, token exchange, user provisioning, role mapping

---

## Implementation Order — Phases 4–8

### Dependency graph

```
Phase 4.1 (Multi-Cluster) ──────────────────────────────────────────────┐
  └─ Phase 5.2 (Instance TTL) requires cluster-aware reaper            │
  └─ Phase 6.1 (Cluster Health) requires cluster registry              │
  └─ Phase 6.2 (Cleanup Policies) requires cluster-aware scheduler     │
  └─ Phase 6.4 (Shared Values) requires cluster scoping                │
                                                                        │
Phase 5.1 (Per-Chart Branch) ── independent, no cluster dependency      │
Phase 5.3 (Service URLs) ── extends existing k8s status, minimal deps  │
Phase 5.4 (Quick Deploy) ── depends on 5.1 (per-chart branch)          │
Phase 5.5 (Favorites) ── independent, pure CRUD                        │
                                                                        │
Phase 6.3 (Analytics) ── independent, read-only aggregation             │
Phase 6.5 (Audit Export) ── independent                                 │
                                                                        │
Phase 7.* ── all depend on phases 4–6 being stable                     │
                                                                        │
Phase 8 (OIDC Auth) ── fully independent, no deps on other phases      │
```

### Recommended execution order

**Wave 1** (foundation — do first, cluster features depend on this):
1. **4.1 Multi-Cluster** — Cluster model, registry, refactor deployer/k8s client

**Wave 2** (developer experience — high daily impact):
2. **5.1 Per-Chart Branch Override** — Unblocks the core "test my branch" use case
3. **5.3 Service URL Display** — Eliminates `kubectl` after deploy
4. **5.2 Instance TTL** — Prevents stale stacks on shared clusters

**Wave 3** (developer convenience):
5. **5.4 Quick Deploy** — Depends on 5.1
6. **5.5 Favorites & Recent**

**Wave 4** (operations — DevOps/admin tooling):
7. **6.1 Cluster Health Dashboard**
8. **6.2 Cleanup Policies**
9. **6.4 Shared Values**
10. **6.3 Analytics**
11. **6.5 Audit Export & Webhooks**

**Wave 5** (polish):
12. **7.1–7.6** in any order based on demand

**Wave 6** (enterprise auth — when corporate SSO is required):
13. **8.1–8.2 OIDC Auth** — Entra ID / Okta / Keycloak integration

---

## Agent → Phase Mapping

| Agent | Primary Phase | Steps |
|-------|--------------|-------|
| Orchestrator | All | Coordinates multi-step features |
| Data Layer | Phase 1, 4 | 1.1 (models incl. templates), 1.2 (repositories), 4.1 (Cluster model/repo), 8.1 (User model changes) |
| Backend API | Phase 1, 4–6, 8 | 1.3 (endpoints), 1.4 (middleware), 4.1 (cluster API), 5.x (dev features), 6.x (ops APIs), 8.1 (OIDC handlers) |
| Git Provider | Phase 1 | 1.6 (AzDO + GitLab integration) |
| Helm Values | Phase 1, 3, 5 | 1.5 (generation), 3.1 (deployment), 5.1 (per-chart branches), 6.4 (shared values merge) |
| Frontend API | Phase 2, 5, 6, 8 | 2.1 (auth), 5.x (dev UX), 6.x (admin pages), 8.2 (OIDC login) |
| Frontend UI | Phase 2, 5–8 | 2.2–2.6 (core pages), 5.x (dev features), 6.1 (cluster health), 7.x (polish), 8.2 (OIDC login) |
| DevOps Engineer | Phase 4 | 4.1 (cluster registry, kubeconfig security) |
| Test Writer | All | Tests after each feature is built |

## Implementation Order

Within each phase, follow this dependency chain:

```
Models → Repositories → Handlers → Routes → API Client → UI Pages → Tests
```

Specifically:
1. go-api-developer agent: models + repositories + handlers + routes + middleware
2. git-provider agent: provider implementations (parallel with #1)
3. helm-values agent: values generator (parallel with #1)
4. frontend-developer agent: API client services + pages + components
5. qa-engineer agent: comprehensive tests for everything above

---

## Files Created/Modified Per Phase

### Phase 0 — New Files

```
.github/
├── copilot-instructions.md
├── agents/
│   ├── go-api-developer.md
│   ├── frontend-developer.md
│   ├── git-provider.agent.md
│   ├── helm-values.agent.md
│   ├── qa-engineer.md
│   ├── orchestrator.md
│   ├── devops-engineer.md
│   ├── scm-engineer.md
│   └── code-reviewer.md
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
│   ├── stack_template.go
│   ├── template_chart_config.go
│   ├── stack_definition.go
│   ├── chart_config.go
│   ├── stack_instance.go
│   ├── value_override.go
│   └── audit_log.go
├── database/azure/
│   ├── user_repository.go
│   ├── stack_template_repository.go
│   ├── template_chart_config_repository.go
│   ├── stack_definition_repository.go
│   ├── chart_config_repository.go
│   ├── stack_instance_repository.go
│   ├── value_override_repository.go
│   └── audit_log_repository.go
├── api/
│   ├── handlers/
│   │   ├── auth.go
│   │   ├── stack_templates.go
│   │   ├── template_charts.go
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
│   ├── Templates/
│   │   ├── Gallery.tsx
│   │   ├── Builder.tsx
│   │   ├── Preview.tsx
│   │   ├── Instantiate.tsx
│   │   └── __tests__/
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
│   ├── EntityLink/
│   │   └── index.tsx
│   ├── LockedValuesBanner/
│   │   ├── index.tsx
│   │   └── __tests__/
│   └── TemplateBadge/
│       ├── index.tsx
│       └── __tests__/
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
| 13 | Stack Templates as core feature | Templates are the primary workflow for devs; DevOps curates, devs consume |
| 14 | DevOps role (admin > devops > user) | Template management needs elevated perms without full admin access |
| 15 | Locked values + required charts | DevOps enforces guardrails; devs customize within bounds |
| 16 | Separate StackTemplate entity | Cleaner than overloading StackDefinition; separate permissions, publishing, versioning |

---

## Future Considerations

1. **Multi-cluster support**: Data model can extend with `cluster_id` field when needed
2. **Helm chart version pinning**: Support both explicit version and "latest" tracking
3. **OIDC/SSO upgrade**: Replace JWT login with OIDC provider (Azure AD, Okta) — AuthContext already abstracts this
4. **GitOps integration**: Export stack configurations as GitOps manifests (Flux/ArgoCD)
5. **Cost tracking**: Integrate with Azure Cost Management to show per-stack resource costs
6. **API rate limiting**: Add per-user rate limits for Git provider APIs
7. **Backup/restore**: Periodic Azure Table backup for disaster recovery
