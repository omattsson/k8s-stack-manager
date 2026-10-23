# Backend and Frontend Structure

Reference for Claude Code and other agents. The root `CLAUDE.md` links here. Read this file before you work in this area.

## Backend Structure

```
backend/
  api/main.go                    # Entry point: bootstrap order, graceful shutdown, default cluster
  api/bootstrap.go               # Bootstrap helpers: repositories, services, handlers, router, leader workers, HTTP servers, Gin mode
  docs/                          # Generated swagger (swagger.json, swagger.yaml), hooks.md (webhook contract)
  internal/
    api/routes/routes.go         # All route registration and middleware order; returns *RateLimiters
    api/handlers/
      items.go                   # CRUD handler pattern (reference implementation)
      handlers.go                # Health handlers (closure injection, no Handler struct)
      rate_limiter.go            # Per-IP sliding window (API limiter and optional login limiter)
      errors.go                  # mapError() maps repository errors to HTTP status for domain handlers
      authz.go                   # Role names, canModifyInstance / requireInstanceModify (owner, devops, admin)
      admin.go                   # AdminHandler: orphaned namespace detection and cleanup
      analytics.go               # AnalyticsHandler: overview, template and user stats
      api_keys.go                # APIKeyHandler: API key management
      audit_logs.go              # AuditLogHandler: filterable audit log viewer and CSV export
      auth.go                    # AuthHandler: login, register, current user, refresh, logout, logout-all
      auth_recheck.go            # recheckUserActive: re-reads the user before a token is returned
      oidc.go                    # OIDCHandler: OpenID Connect authentication flow
      branch_overrides.go        # BranchOverrideHandler: per-chart branch overrides
      bulk_operations.go         # BulkHandler: bulk deploy, stop, clean, delete (up to 50 instances)
      bulk_template_operations.go # TemplateHandler: bulk delete, publish, unpublish (up to 50 templates)
      instance_quota_overrides.go # InstanceQuotaOverrideHandler: per-instance resource quota overrides, effective quota (GET /:id/quota)
      quota_apply.go             # Writes the effective quota (cluster quota merged with the override) to stack namespaces after a quota change
      chart_configs.go           # Chart config management (nested under definitions), chartSourceChanged
      cleanup_policies.go        # CleanupPolicyHandler: CRUD and manual run
      clusters.go                # ClusterHandler: CRUD, test-connection, health, quotas, utilization
      dashboard.go               # DashboardHandler: aggregated overview for the landing page
      definition_names.go        # Definition name uniqueness per owner, generated names
      display_names.go           # List filters (owner, cluster, definition), pagination parsing, owner/cluster/definition names
      favorites.go               # FavoriteHandler: user bookmarks
      git.go                     # GitHandler: branch listing, validation
      notifications.go           # NotificationHandler: list, read/unread, preferences
      notification_channels.go   # NotificationChannelHandler: outbound webhook channels, event subscriptions, test send, delivery logs
      quick_deploy.go            # QuickDeployHandler: template quick deploy (writes its own AuditLog)
      shared_values.go           # SharedValuesHandler: per-cluster shared values
      shared_values_loader.go    # Loads the shared values of the cluster that the deploy uses
      values_builder.go          # valuesBuilder: the one Helm values pipeline (deploy, bulk, quick deploy, preview, export, compare)
      stack_definitions.go       # DefinitionHandler: CRUD, chart management, import and export, requireDefinitionModify
      definition_template_link.go # DefinitionHandler: link a definition to a template version (PUT template-link), import link lookup
      stack_instances.go         # InstanceHandler: CRUD, clone, deploy, stop, clean, deploy-preview, compare, extend TTL
      instance_rollback.go       # Rollback response type and values-drift message
      deploy_log_history.go      # Deploy log list parameters (view, action, status), summary rows, started_by lookup
      instance_actions.go        # InstanceHandler: custom action list (UI metadata, can_invoke), async job log proxy (stores the final job status), action run recording and list (GET action-runs)
      instance_followers.go      # InstanceHandler: follow and unfollow
      stack_templates.go         # TemplateHandler: CRUD, publish, instantiate, clone
      template_releases.go       # Template draft and release rules (latest published snapshot)
      template_charts.go         # Template chart config management
      template_versions.go       # TemplateVersionHandler: version history listing and diff
      users.go                   # UserHandler: user management, role change (local users only)
      ui_config.go               # Public GET /ui-config: branding (APP_TITLE, APP_LOGO_URL, APP_FAVICON_URL), default_instance_ttl_minutes and max_instance_ttl_days
      instance_ttl.go            # TTL and stop-time rules: MAX_INSTANCE_TTL_DAYS limit, expires_at, stop time passed (409 on deploy)
      value_overrides.go         # Per-chart value overrides
      websocket.go               # WebSocket upgrade handler
      content_disposition.go     # Safe Content-Disposition header for downloads
      mock_repository.go         # In-memory mock for Item tests (same package)
      mock_broadcast_sender.go   # Test double for websocket.BroadcastSender
      mock_domain_repositories_test.go # Mocks for domain repositories (instances, clusters, users, ...)
      mock_session_store_test.go # Mock sessionstore
      mock_tx_runner_test.go     # Mock transaction runner
    api/middleware/
      middleware.go              # RequestID, Logger, Recovery, SecurityHeaders, CORS, MaxBodySize
      auth.go                    # JWT authentication and token generation
      combined_auth.go           # Combined JWT and API key auth; checks the sessionstore blocklist and User.Disabled
      audit.go                   # Audit logging middleware (applied to route groups) and the auditRoutes table
      role.go                    # RequireAdmin, RequireDevOps role-based access
      metrics.go                 # HTTPMetrics: OpenTelemetry HTTP request metrics
      auth_metrics.go            # Auth outcome counters (login, refresh, OIDC, API key)
      otel.go                    # SpanEnrichUser: adds user attributes to the active span
      ws_redact.go               # RedactWSToken: strips ?token= from /ws URLs before logging and tracing
      git_repo_redact.go         # RedactGitRepoQuery: redacts the ?repo= value of /git URLs before logging and tracing
    config/                      # config.go (env vars with godotenv .env fallback, typed config structs); branding.go, chart_repos.go, namespace_labels.go, stack_access.go (parsers for the matching env vars)
    database/database.go         # Database struct that wraps gorm.DB, NewDatabase(), Transaction, Ping
    database/config.go           # Database connection Config struct, loaded from env vars
    database/schema.go           # SchemaManager interface for table creation, dropping and inspection
    database/factory.go          # MySQL connection with retry (5 attempts, 2s delay)
    database/repository.go       # NewRepository() factory; repository_factory.go wires all domain repositories
    database/*_repository.go     # One GORM repository per domain model
    database/instance_delete.go  # DeleteInstanceRecord (overrides, quota override, followers, action runs), DeleteInstanceWithOwnedDefinition
    database/lock_retry.go       # retryOnDeadlock: runs a transaction or one statement again on MySQL 1213 (deadlock), 3 attempts
    database/migrations.go       # Versioned migrations through schema.Migrator, run on startup
    database/errors.go           # Re-exports from pkg/dberrors (single source of truth)
    database/schema/             # Migrator and versioned migration structs
    models/                      # One file per domain model plus the repository interface; models.go has Base, Item, Validator, Versionable, the generic Repository interface, GenericRepository, Filter, Pagination; validation.go; chart_source.go (chart repository allowlist)
    health/health.go             # Dependency health checks (liveness, readiness)
    auth/                        # OIDC provider (PKCE): discovery, authorize, token exchange (sessionstore keeps OIDC and CLI state)
    cache/                       # Generic concurrent in-memory TTL cache
    cluster/                     # ClusterRegistry (multi-cluster coordination), health poller, quota monitor, secret monitor and refresher, AccessReconciler
    deployer/                    # Helm CLI wrapper for deploy, undeploy, rollback, status (multi-cluster through the registry), cleanup executor, expiry stopper, interrupted operation recovery
    gitprovider/                 # Azure DevOps, GitHub and GitLab branch listing, URL detection, 5-minute cache
    helm/                        # Values deep-merge, template variable substitution
    hooks/                       # Outbound lifecycle webhooks: dispatcher, HMAC-signed client, actions, config file (see docs/hooks.md and EXTENDING.md)
    k8s/                         # Kubernetes cluster client, status watcher, resource quotas, pod exec, scaling, AccessBinding
    leader/                      # Leader election (coordination.k8s.io Lease through client-go) and leader worker group (Run(ctx) for each term)
    replica/                     # Process identity (POD_NAME or host name plus random suffix) and heartbeat writer (replica_heartbeats, every replica)
    notifier/                    # Notification dispatch (in-app notifications and outbound notification channels)
    sessionstore/                # Token blocklist and OIDC state persistence (mysql default, memory)
    telemetry/                   # OpenTelemetry bootstrap, DB pool metrics, business metrics, leader gauge
    websocket/                   # WebSocket hub, client, message types, fan-out between replicas (ws_events writer and poller, cleanup worker)
    scheduler/                   # Cron-based cleanup policy execution
    ttl/                         # TTL reaper and expiry warner for stack instances
    retention/                   # Leader worker log-retention: deletes old deploy logs, audit logs, notifications and delivery logs (*_RETENTION_DAYS)
  pkg/dberrors/errors.go         # Canonical error types: ErrNotFound, ErrDuplicateKey, ErrValidation, ErrConnectionFailed, ErrNotImplemented
  pkg/crypto/                    # AES-GCM encryption and decryption of kubeconfig data at rest (key derived with SHA-256)
  pkg/utils/                     # Shared utilities (for example cryptographic random strings)
  internal/test/test_helpers.go  # Shared test utilities (test server setup)
```

## Frontend Structure

```
frontend/src/
  api/config.ts                # API_BASE_URL: http://localhost:8081 (dev) or '' same-origin (prod); client paths already include /api/v1
  api/client.ts                # Axios instance and service objects
  api/refresh.ts               # Access token refresh (one refresh for all tabs, Web Locks)
  routes.tsx                   # Route definitions
  App.tsx                      # Root component with providers
  main.tsx                     # Entry point
  components/
    Layout/                    # AppBar, navigation, footer shell
    ActionRunHistory/          # Action runs of the instance page: who, when, parameters, result chip, "Show more", "Show log" (JobLogDialog)
    AccessUrls/                # Access URL display for stack instances (kubectl port-forward hints only for devops and admin)
    BrandLogo/                 # Logo from the branding config (falls back to the built-in /logo.svg)
    BranchSelector/            # Git branch picker with autocomplete
    ChartDiffList/             # Chart changes of a template version diff
    ConfirmDialog/             # Reusable confirmation modal
    DeployPreviewDialog/       # Merged values preview before deploy (GET /stack-instances/:id/deploy-preview)
    DeploymentHistory/         # Deployment History of the instance page: pinned rows (running, current, last failure), filters, compact rows, folded deploy runs, "Show more"; a row loads its output on open (a running log: stored output, then the streamed lines, utils/liveLogLines)
    EmptyState/                # Placeholder for empty lists
    EntityLink/                # Link to a related entity
    ErrorBoundary/             # React error boundary wrapper
    ExtendTtlMenu/             # Menu that adds time to the expiry of an instance (+1 h, +4 h, +24 h, Custom... with until)
    FavoriteButton/            # Bookmark toggle for templates and instances
    FollowButton/              # Follow toggle for an instance
    InstanceActionsMenu/       # Custom actions (`useInstanceActions`: first group of the instance "Actions" menu), parameter form, result, job log polling; `useJobLog`, `JobLogPanel`, `JobLogDialog`
    JsonResultView/            # Readable view of a JSON action result
    LoadingState/              # Centered CircularProgress wrapper
    InstanceQuota/             # Resource quota section of the instance page: effective values and their source, override edit and remove (InstanceQuotaDialog)
    PodStatusDisplay/          # Kubernetes pod status display
    ProtectedRoute/            # Auth-gated route wrapper (optional requiredRole)
    NotificationCenter/        # Notification bell dropdown in the app bar
    QuickDeployDialog/         # One-click template deploy modal
    QuotaConfigDialog/         # Resource quota configuration modal; quotaFields.ts has the field list and validation (shared with InstanceQuota)
    SetupWizard/               # First-run checklist (cluster, template, instance), can be dismissed
    StatusBadge/               # Colored status chip
    StopTimePicker/            # Stop time picker (datetime-local with min and max, time left); used by TtlSelector and ExtendTtlMenu
    TemplateFieldDiffList/     # Template field changes of a version diff
    TtlSelector/               # TTL presets and "Custom stop time" (fixed stop time, expires_at)
    YamlEditor/                # YAML text editor (Monaco)
  pages/                       # Simple pages export from index.tsx; domain pages use named files
    Login/                     # Authentication page
    AuthCallback/              # OIDC authentication callback handler
    StackInstances/            # Dashboard.tsx, Detail.tsx, Form.tsx, Compare.tsx, CloneDialog.tsx, RollbackDialog.tsx, ExpiryChip.tsx, widgets/: instance list, deploy, stop, clean, rollback
    StackDefinitions/          # List.tsx, Form.tsx, ImportDefinitionDialog.tsx, UpgradeDialog.tsx, TemplateLinkDialog.tsx
    Templates/                 # Gallery.tsx, Builder.tsx, Instantiate.tsx, Preview.tsx, VersionHistory.tsx, PublishDialog.tsx, CloneTemplateDialog.tsx
    AuditLog/                  # Filterable audit log viewer
    Admin/                     # Clusters/, Users/, OrphanedNamespaces/, NotificationChannels/
    Profile/                   # User profile and settings
    Analytics/                 # Usage overview, template stats, user stats
    CleanupPolicies/           # Cron-based cleanup policy management
    ClusterHealth/             # Multi-cluster health monitoring
    Notifications/             # Full notification list with filters and pagination
    SharedValues/              # Per-cluster shared Helm values
    NotFound/                  # 404 page
  context/
    AuthContext.tsx            # Authentication state and JWT management
    BrandingContext.tsx        # Renders the cached ui-config (localStorage) or the built-in defaults at once; loads GET /api/v1/ui-config once (5s timeout), swaps the values in and caches them; sets document.title and the favicon
    NotificationContext.tsx    # Toast and snackbar provider
    ThemeContext.tsx           # Light and dark theme toggle provider
  hooks/
    useCountdown.ts            # Countdown timer hook (for example TTL display)
    useUnsavedChanges.ts       # Unsaved changes warning hook
    useDeployHistory.ts        # Deployment History paging (10 rows, next_cursor), filters, highlights, refresh; useRollbackTargets (successful deploys)
    useWebSocket.ts            # WebSocket hook for real-time updates (one shared connection)
  theme/
    index.ts                   # MUI theme export (palette, typography, components)
    palette.ts                 # Color palette definitions
    typography.ts              # Typography variant overrides
    components.ts              # MUI component default prop and style overrides
  types/                       # Shared TypeScript type definitions
  utils/                       # roles, timeAgo, expiry, instanceName, instanceState, jwt, quantity, apiError, download, deployHistory, actionRuns, templateVersion, notificationEventTypes, notificationHelpers, recentTemplates, setupWizard, stopTime
```

**Stack:** React 19, MUI v9, react-router-dom v7, Vite 8, TypeScript 6, Vitest 5, Playwright (41 specs in `frontend/e2e/`; the project `isolated` runs `session-lifecycle.spec.ts`, then its teardown project `isolated-last-admin` runs `last-admin.spec.ts` (it disables `admin` for a moment, so it runs last; `global-setup.ts` re-enables a left-over disabled `admin` in the database), only with `E2E_ISOLATED=1`, serially, against a restarted short-limit backend; the project `mobile` runs `mobile.spec.ts` with the iPhone 13 viewport, the project `chromium` runs the rest).
