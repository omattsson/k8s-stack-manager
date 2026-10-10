export interface User {
  id: string;
  username: string;
  display_name: string;
  role: string;
  auth_provider: string;
  disabled: boolean;
  service_account: boolean;
  created_at: string;
  updated_at: string;
}

/** User role, lowest to highest permission. */
export type UserRole = 'user' | 'devops' | 'admin';

/** Response of `PUT /api/v1/users/:id/role`. */
export interface ChangeRoleResponse {
  id: string;
  old_role: string;
  new_role: UserRole;
  /** False when the user already had the role (no change, no sign-out). */
  changed: boolean;
  message: string;
}

export interface JwtPayload {
  user_id: string;
  username: string;
  display_name?: string;
  role: string;
  exp: number;
  auth_provider?: string;
  email?: string;
}

export interface LoginRequest {
  username: string;
  password: string;
}

export interface LoginResponse {
  token: string;
  user: User;
}

export interface RegisterRequest {
  username: string;
  password: string;
  display_name: string;
}

export interface StackTemplate {
  id: string;
  name: string;
  description: string;
  category: string;
  version: string;
  owner_id: string;
  default_branch: string;
  is_published: boolean;
  created_at: string;
  updated_at: string;
  charts?: TemplateChartConfig[];
  definition_count?: number;
  owner_username?: string;
  /** Version string of the latest release snapshot. Null when the template has no release. */
  published_version?: string | null;
  /** ID of the latest release snapshot. Null when the template has no release. */
  published_version_id?: string | null;
  /** True when the working copy differs from the latest release snapshot. */
  has_unpublished_changes?: boolean;
  /**
   * Charts of the latest release snapshot: what Use Template and Quick Deploy apply.
   * Their IDs are valid `chart_overrides` keys. Null when the template has no release.
   */
  published_charts?: TemplateChartConfig[] | null;
}

/** Optional body for `POST /api/v1/templates/:id/publish`. */
export interface PublishTemplateRequest {
  /** Release version. The API uses the working copy version when this is omitted. */
  version?: string;
  change_summary?: string;
}

/** Normalized result of a publish call. */
export interface PublishTemplateResult {
  template: StackTemplate;
  /** False when nothing changed since the latest release and the API created no snapshot. */
  snapshotCreated: boolean;
}

export interface TemplateChartConfig {
  id: string;
  stack_template_id: string;
  chart_name: string;
  repository_url: string;
  source_repo_url: string;
  chart_path: string;
  chart_version: string;
  default_values: string;
  locked_values: string;
  deploy_order: number;
  required: boolean;
  created_at: string;
}

export interface StackDefinition {
  id: string;
  name: string;
  description: string;
  owner_id: string;
  /** Username of the owner. Omitted when the owner does not exist. */
  owner_username?: string;
  source_template_id?: string;
  source_template_version?: string;
  default_branch: string;
  created_at: string;
  updated_at: string;
  charts?: ChartConfig[];
  /** Number of charts; set by the paged list, which does not load the charts. */
  chart_count?: number;
}

export interface ChartConfig {
  id: string;
  stack_definition_id: string;
  chart_name: string;
  repository_url: string;
  source_repo_url: string;
  chart_path: string;
  chart_version: string;
  default_values: string;
  deploy_order: number;
  created_at: string;
}

export interface StackInstance {
  id: string;
  stack_definition_id: string;
  name: string;
  namespace: string;
  owner_id: string;
  branch: string;
  cluster_id?: string;
  status: string;
  error_message?: string;
  last_deployed_at?: string;
  ttl_minutes?: number;
  expires_at?: string;
  /** True when the stored overrides differ from the values that run (for example after a rollback). */
  values_drift?: boolean;
  /** Username of the owner. Omitted when the owner does not exist. */
  owner_username?: string;
  /** Name of the stack definition. Omitted when the definition does not exist. */
  definition_name?: string;
  /** Name of the target cluster. Omitted when the cluster no longer exists or `cluster_id` is empty (older instances). */
  cluster_name?: string;
  /** True when the current user follows the instance. `GET`, `PUT` and `POST .../extend` on `/stack-instances/:id` set it; omitted when following is not available. */
  following?: boolean;
  /** Number of users who follow the instance. `GET`, `PUT` and `POST .../extend` on `/stack-instances/:id` set it. */
  follower_count?: number;
  created_at: string;
  updated_at: string;
  definition?: StackDefinition;
}

/** Response of `POST` and `DELETE /stack-instances/:id/follow`. */
export interface FollowState {
  following: boolean;
  follower_count: number;
}

/** Response of `POST /stack-instances/:id/rollback`. */
export interface RollbackResponse {
  log_id: string;
  message: string;
  /** The deploy log the rollback goes back to, when a target was given. */
  target_log_id?: string;
  /** True when the stored overrides differ from the values that run after the rollback. */
  values_drift?: boolean;
  /** Warning for the user, for example that the next deploy applies the stored overrides again. */
  warning?: string;
}

/** Request body of `POST /stack-instances/:id/clone`. */
export interface CloneInstanceRequest {
  /** Name of the copy (RFC 1123 label). Omit it to let the server pick a free `<name>-copy[-N]`. */
  name?: string;
  branch?: string;
  ttl_minutes?: number;
}

export interface DeploymentLog {
  id: string;
  stack_instance_id: string;
  action: 'deploy' | 'stop' | 'clean' | 'rollback';
  status: 'running' | 'success' | 'error';
  output: string;
  error_message?: string;
  /** Branch of the deploy, when the API sends it. */
  branch?: string;
  /** For a rollback: the deploy log that was the rollback target. */
  target_log_id?: string;
  started_at: string;
  completed_at?: string;
}

export interface ChartStatus {
  release_name: string;
  chart_name: string;
  status: 'healthy' | 'progressing' | 'degraded' | 'error';
  deployments: DeploymentStatusInfo[];
  pods: PodInfo[];
  services: ServiceInfo[];
}

export interface DeploymentStatusInfo {
  name: string;
  ready_replicas: number;
  desired_replicas: number;
  updated_replicas: number;
  available: boolean;
}

export interface ContainerStateInfo {
  name: string;
  state: 'running' | 'waiting' | 'terminated' | 'unknown';
  reason?: string;
  message?: string;
  restart_count: number;
  ready: boolean;
  image: string;
  exit_code?: number;
}

export interface PodConditionInfo {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  reason?: string;
  message?: string;
}

export interface PodEvent {
  type: 'Normal' | 'Warning';
  reason: string;
  message: string;
  object: string;
  count: number;
  first_seen: string;
  last_seen: string;
}

export interface PodInfo {
  name: string;
  phase: string;
  ready: boolean;
  restart_count: number;
  image: string;
  container_states: ContainerStateInfo[];
  conditions?: PodConditionInfo[];
  start_time?: string;
  node_name?: string;
}

export interface ServiceInfo {
  name: string;
  type: string;
  cluster_ip: string;
  ports?: string[];
  external_ip?: string;
  node_ports?: number[];
  ingress_hosts?: string[];
}

export interface IngressInfo {
  name: string;
  host: string;
  path: string;
  tls: boolean;
  url: string;
}

export interface NamespaceStatus {
  namespace: string;
  status: 'healthy' | 'degraded' | 'error' | 'not_found' | 'progressing';
  charts: ChartStatus[];
  ingresses?: IngressInfo[];
  events?: PodEvent[];
  last_checked: string;
}

export interface ValueOverride {
  id: string;
  stack_instance_id: string;
  chart_config_id: string;
  values: string;
  updated_at: string;
}

export interface AuditLog {
  id: string;
  user_id: string;
  username: string;
  action: string;
  entity_type: string;
  entity_id: string;
  details: string;
  timestamp: string;
}

export interface AuditLogFilters {
  user_id?: string;
  entity_type?: string;
  entity_id?: string;
  action?: string;
  start_date?: string;
  end_date?: string;
  limit?: number;
  offset?: number;
}

export interface CreateChartConfigRequest {
  chart_name: string;
  repository_url: string;
  source_repo_url: string;
  chart_path: string;
  chart_version: string;
  default_values: string;
  deploy_order: number;
}

export interface CreateTemplateChartRequest {
  chart_name: string;
  repository_url: string;
  source_repo_url: string;
  chart_path: string;
  chart_version: string;
  default_values: string;
  locked_values: string;
  deploy_order: number;
  required: boolean;
}

export interface InstantiateTemplateRequest {
  name: string;
  description: string;
  default_branch?: string;
  chart_overrides?: Record<string, string>;
}

export type StackStatus = 'draft' | 'deploying' | 'stabilizing' | 'running' | 'partial' | 'stopped' | 'error' | 'stopping' | 'cleaning';

export interface CreateUserRequest {
  username: string;
  password: string;
  display_name: string;
  role: string;
}

export interface APIKey {
  id: string;
  user_id: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
}

export interface CreateAPIKeyRequest {
  name: string;
  expires_at?: string;
  expires_in_days?: number;
}

export interface CreateAPIKeyResponse {
  id: string;
  name: string;
  prefix: string;
  raw_key: string;
  created_at: string;
  expires_at?: string;
}

export interface ResourceCounts {
  pods: number;
  deployments: number;
  services: number;
}

export interface OrphanedNamespace {
  name: string;
  created_at: string;
  phase: string;
  /** Null or absent when the server did not count the resources. */
  resource_counts?: ResourceCounts | null;
  /** Null when the namespace has no Helm releases (Go nil slice). */
  helm_releases: string[] | null;
  /** True when the namespace has the label managed-by=k8s-stack-manager. */
  managed: boolean;
}

export interface Cluster {
  id: string;
  name: string;
  description: string;
  api_server_url: string;
  region: string;
  health_status: 'healthy' | 'degraded' | 'unreachable' | '';
  max_namespaces: number;
  max_instances_per_user: number;
  is_default: boolean;
  /** True when the backend uses its own service account (in-cluster config); api_server_url is then empty. */
  use_in_cluster?: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateClusterRequest {
  name: string;
  description: string;
  api_server_url: string;
  kubeconfig_data?: string;
  kubeconfig_path?: string;
  region: string;
  max_namespaces: number;
  max_instances_per_user?: number;
  is_default: boolean;
  use_in_cluster?: boolean;
}

export interface UpdateClusterRequest {
  name?: string;
  description?: string;
  api_server_url?: string;
  kubeconfig_data?: string;
  kubeconfig_path?: string;
  region?: string;
  max_namespaces?: number;
  max_instances_per_user?: number;
  is_default?: boolean;
  use_in_cluster?: boolean;
}

export interface ClusterTestResult {
  status: string;
  message: string;
  server_version?: string;
}

export interface ChartBranchOverride {
  id: string;
  stack_instance_id: string;
  chart_config_id: string;
  branch: string;
  updated_at: string;
}

export interface UserFavorite {
  id: string;
  user_id: string;
  entity_type: 'definition' | 'instance' | 'template';
  entity_id: string;
  created_at: string;
}

export interface QuickDeployRequest {
  instance_name: string;
  instance_description?: string;
  branch?: string;
  cluster_id?: string;
  ttl_minutes?: number;
  branch_overrides?: Record<string, string>;
}

export interface QuickDeployResponse {
  instance: StackInstance;
  definition: StackDefinition;
  log_id: string;
}

export interface ClusterSummary {
  node_count: number;
  ready_node_count: number;
  total_cpu: string;
  total_memory: string;
  allocatable_cpu: string;
  allocatable_memory: string;
  /** Sum of the CPU requests of scheduled, unfinished pods (not real use). */
  requested_cpu?: string;
  /** Sum of the memory requests of scheduled, unfinished pods (not real use). */
  requested_memory?: string;
  namespace_count: number;
}

export interface NodeStatusInfo {
  name: string;
  status: string;
  conditions: NodeCondition[];
  capacity: ResourceQuantityInfo;
  allocatable: ResourceQuantityInfo;
  pod_count: number;
}

export interface NodeCondition {
  type: string;
  status: string;
  message?: string;
}

export interface ResourceQuantityInfo {
  cpu: string;
  memory: string;
  pods?: string;
}

export interface ClusterNamespaceInfo {
  name: string;
  phase: string;
  created_at: string;
}

export interface SharedValues {
  id: string;
  cluster_id: string;
  name: string;
  description: string;
  values: string;
  priority: number;
  created_at: string;
  updated_at: string;
}

export interface OverviewStats {
  total_templates: number;
  total_definitions: number;
  total_instances: number;
  running_instances: number;
  total_deploys: number;
  total_users: number;
}

export interface TemplateStats {
  template_id: string;
  template_name: string;
  category: string;
  is_published: boolean;
  definition_count: number;
  instance_count: number;
  deploy_count: number;
  success_count: number;
  error_count: number;
  success_rate: number;
}

export interface UserStats {
  user_id: string;
  username: string;
  instance_count: number;
  deploy_count: number;
  last_active: string | null;
}

export interface CleanupPolicy {
  id: string;
  name: string;
  cluster_id: string;
  action: string;
  condition: string;
  schedule: string;
  enabled: boolean;
  dry_run: boolean;
  last_run_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface CleanupResult {
  instance_id: string;
  instance_name: string;
  namespace: string;
  action: string;
  status: string;
  error?: string;
}

export interface BulkOperationRequest {
  instance_ids: string[];
}

export interface BulkOperationResultItem {
  instance_id: string;
  instance_name: string;
  status: 'success' | 'error';
  error?: string;
}

export interface BulkOperationResponse {
  total: number;
  succeeded: number;
  failed: number;
  results: BulkOperationResultItem[];
}

export interface BulkTemplateResultItem {
  template_id: string;
  template_name: string;
  status: string;
  error?: string;
}

export interface BulkTemplateResponse {
  total: number;
  succeeded: number;
  failed: number;
  results: BulkTemplateResultItem[];
}

export interface CompareInstanceSummary {
  id: string;
  name: string;
  definition_name: string;
  branch: string;
  owner: string;
}

export interface CompareChartDiff {
  chart_name: string;
  left_values: string | null;
  right_values: string | null;
  has_differences: boolean;
}

export interface CompareInstancesResponse {
  left: CompareInstanceSummary;
  right: CompareInstanceSummary;
  charts: CompareChartDiff[];
}

export interface Notification {
  id: string;
  user_id: string;
  type: string;
  title: string;
  message: string;
  is_read: boolean;
  entity_type?: string;
  entity_id?: string;
  created_at: string;
}

export interface NotificationPreference {
  id?: string;
  user_id?: string;
  event_type: string;
  enabled: boolean;
  channel?: string;
}

export interface NotificationListResponse {
  notifications: Notification[];
  total: number;
  unread_count: number;
}

export interface DefinitionExportBundle {
  schema_version: string;
  exported_at: string;
  definition: {
    name: string;
    description: string;
    default_branch: string;
    repository_url: string;
    [key: string]: unknown;
  };
  charts: Array<{
    chart_name: string;
    repository_url: string;
    default_values: string;
    sort_order: number;
    [key: string]: unknown;
  }>;
}

export interface ResourceQuotaConfig {
  id?: string;
  cluster_id: string;
  cpu_request: string;
  cpu_limit: string;
  memory_request: string;
  memory_limit: string;
  storage_limit: string;
  pod_limit: number;
}

export interface InstanceQuotaOverride {
  id?: string;
  stack_instance_id: string;
  cpu_request: string;
  cpu_limit: string;
  memory_request: string;
  memory_limit: string;
  storage_limit: string;
  pod_limit: number | null;
  created_at?: string;
  updated_at?: string;
}

export interface NamespaceResourceUsage {
  namespace: string;
  cpu_used: string;
  cpu_limit: string;
  memory_used: string;
  memory_limit: string;
  pod_count: number;
  pod_limit: number;
}

export interface ClusterUtilization {
  namespaces: NamespaceResourceUsage[];
}

export interface TemplateVersion {
  id: string;
  template_id: string;
  version: string;
  change_summary: string;
  created_by: string;
  created_by_username?: string;
  created_at: string;
  snapshot?: TemplateSnapshot;
}

export interface TemplateSnapshot {
  template: {
    name: string;
    description: string;
    category: string;
    default_branch: string;
    repository_url: string;
    is_published: boolean;
    version: string;
  };
  charts: Array<{
    chart_name: string;
    repo_url: string;
    default_values: string;
    locked_values: string;
    is_required: boolean;
    sort_order: number;
  }>;
}

/** Per-chart difference between two template snapshots. */
export interface TemplateChartDiff {
  chart_name: string;
  left_values?: string | null;
  right_values?: string | null;
  left_locked?: string;
  right_locked?: string;
  left_repo_url?: string;
  right_repo_url?: string;
  /** Omitted by the API when false. */
  left_required?: boolean;
  right_required?: boolean;
  /** Omitted by the API when 0. */
  left_sort_order?: number;
  right_sort_order?: number;
  /** Compared by the API only when both sides store them (snapshot schema 1 or the working copy). */
  left_chart_version?: string;
  right_chart_version?: string;
  left_chart_path?: string;
  right_chart_path?: string;
  has_differences: boolean;
  change_type: 'added' | 'removed' | 'modified' | 'unchanged';
}

/** One side of a version diff. `version` is the version string (for example "1.0.0"). */
export interface VersionDiffSide {
  /** Version ID, or "working" for the working copy. */
  id?: string;
  version: string;
  snapshot: TemplateSnapshot;
  change_summary?: string;
  created_by?: string;
  created_by_username?: string;
  created_at?: string;
  is_working_copy?: boolean;
}

/** One template field that differs between the two sides of a version diff. */
export interface TemplateFieldDiff {
  /** Snapshot field name: name, description, category, default_branch or version. */
  field: string;
  left: string;
  right: string;
}

export interface VersionDiffResponse {
  left: VersionDiffSide;
  right: VersionDiffSide;
  /** Changed template fields. Absent in responses of older servers. */
  template_diffs?: TemplateFieldDiff[];
  chart_diffs: TemplateChartDiff[];
}

export interface UpgradeCheckResponse {
  upgrade_available: boolean;
  current_version?: string;
  latest_version?: string;
  changes?: {
    charts_added: string[];
    charts_removed: string[];
    charts_modified: string[];
    charts_unchanged: string[];
  };
  /** Optional per-chart value diff (current definition values vs the latest release). */
  chart_diffs?: TemplateChartDiff[];
}

export interface ChartDeployPreview {
  chart_name: string;
  previous_values: string;
  pending_values: string;
  has_changes: boolean;
}

export interface DeployPreviewResponse {
  instance_id: string;
  instance_name: string;
  charts: ChartDeployPreview[];
}

export interface DashboardCluster {
  id: string;
  name: string;
  health_status: string;
  node_count?: number;
  ready_node_count?: number;
  total_cpu?: string;
  total_memory?: string;
  allocatable_cpu?: string;
  allocatable_memory?: string;
  namespace_count?: number;
}

export interface DashboardDeployment {
  id: string;
  stack_instance_id: string;
  instance_name: string;
  action: string;
  status: string;
  started_at: string;
  completed_at?: string;
  owner_username?: string;
}

export interface DashboardExpiring {
  id: string;
  name: string;
  namespace: string;
  status: string;
  expires_at: string;
  ttl_minutes: number;
  cluster_id?: string;
  /** Owner user ID. Optional: older backends do not send it. */
  owner_id?: string;
}

export interface DashboardFailing {
  id: string;
  name: string;
  namespace: string;
  status: string;
  error_message: string;
  cluster_id?: string;
  updated_at: string;
}

export interface DashboardResponse {
  clusters: DashboardCluster[];
  recent_deployments: DashboardDeployment[];
  expiring_soon: DashboardExpiring[];
  failing_instances: DashboardFailing[];
}

/**
 * Instance filters of a notification channel. Each set filter must match
 * (AND); one value of a filter is enough (OR). Empty filters match all
 * instances. Events without an instance go only to channels without filters.
 */
export interface NotificationChannelFilters {
  /** Glob patterns for the instance name (`*`, `?`, `[a-z]`), case-insensitive. */
  instance_name_patterns?: string[];
  /** User IDs of instance owners. */
  owner_ids?: string[];
  /** Stack definition IDs. */
  definition_ids?: string[];
  /** Cluster IDs. */
  cluster_ids?: string[];
}

export interface NotificationChannel {
  id: string;
  name: string;
  webhook_url: string;
  enabled: boolean;
  /** Instance filters; an empty object means all instances. */
  filters?: NotificationChannelFilters;
  created_at: string;
  updated_at: string;
}

/** Create and update response of a channel: the channel and warnings for unknown filter IDs. */
export interface NotificationChannelSaveResult extends NotificationChannel {
  warnings?: string[];
}

export interface NotificationChannelWithCount extends NotificationChannel {
  subscription_count: number;
}

export interface NotificationChannelSubscription {
  id: string;
  channel_id: string;
  event_type: string;
}

export interface NotificationDeliveryLog {
  id: string;
  channel_id: string;
  channel_name: string;
  event_type: string;
  status: string;
  status_code: number;
  error_message?: string;
  created_at: string;
}

/** Type of an action parameter: a text field, a switch or a select. */
export type ActionParameterType = 'string' | 'bool' | 'enum';

/** One input of a custom action, from the action config. */
export interface ActionParameter {
  name: string;
  /** Form label. The server sets it to the name when the config has no label. */
  label: string;
  description?: string;
  type: ActionParameterType;
  required: boolean;
  /** A string for `string` and `enum`, a boolean for `bool`. */
  default?: string | boolean;
  /** The allowed values of an `enum` parameter. */
  options?: string[];
}

/** A custom action that can run on a stack instance (`GET /stack-instances/:id/actions`). */
export interface InstanceAction {
  name: string;
  /** Menu text. The server sets it to the name when the config has no label. */
  label: string;
  description?: string;
  /** Confirmation text to show before the action runs. */
  confirm?: string;
  parameters: ActionParameter[];
  /** True when the backend proxies the job log of the action. */
  has_job_log: boolean;
  /** True when the current user may run the action on this instance. */
  can_invoke: boolean;
}

/** Response of `GET /stack-instances/:id/actions`. */
export interface InstanceActionList {
  instance_id: string;
  can_invoke: boolean;
  actions: InstanceAction[];
}

/** Response of `POST /stack-instances/:id/actions/:name`. */
export interface ActionInvokeResult {
  action: string;
  instance_id: string;
  /** HTTP status of the action subscriber. A non-2xx value is an action failure. */
  status_code: number;
  /** JSON body of the action subscriber. */
  result: unknown;
  /** Job ID of an asynchronous action with a job log. */
  job_id?: string;
  /**
   * Reason of the subscriber for a status of 400 or higher (one line, at most
   * 500 characters, URLs replaced by [url]). Older servers omit it.
   */
  message?: string;
}

/** Response of `GET /stack-instances/:id/actions/:name/jobs/:job_id/log`. */
export interface ActionJobLog {
  action: string;
  instance_id: string;
  job_id: string;
  /** `running` while the job runs, else a final state such as `succeeded` or `failed`. */
  status: string;
  /** Log text from `offset` to `next_offset`. */
  log: string;
  offset: number;
  /** Offset for the next poll. */
  next_offset: number;
  /** True when the job has ended and the log is read to the end. */
  done: boolean;
  /** True when the chunk was cut at the size cap. Poll again at once. */
  truncated: boolean;
}

/**
 * Display values of the web UI from `GET /api/v1/ui-config` (env APP_TITLE,
 * APP_LOGO_URL, APP_FAVICON_URL).
 */
export interface UIConfig {
  /** Product name in the browser tab, sidebar, app bar, login page and setup wizard. */
  title: string;
  /** Logo image URL. Empty: the built-in logo. */
  logo_url: string;
  /** Browser tab icon URL. */
  favicon_url: string;
}
