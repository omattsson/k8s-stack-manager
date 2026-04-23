export interface User {
  id: string;
  username: string;
  display_name: string;
  role: string;
  created_at: string;
  updated_at: string;
}

export interface JwtPayload {
  user_id: string;
  username: string;
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
  source_template_id?: string;
  source_template_version?: string;
  default_branch: string;
  created_at: string;
  updated_at: string;
  charts?: ChartConfig[];
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
  created_at: string;
  updated_at: string;
  definition?: StackDefinition;
}

export interface DeploymentLog {
  id: string;
  stack_instance_id: string;
  action: 'deploy' | 'stop' | 'clean' | 'rollback';
  status: 'running' | 'success' | 'error';
  output: string;
  error_message?: string;
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

export type StackStatus = 'draft' | 'deploying' | 'running' | 'stopped' | 'error' | 'stopping' | 'cleaning';

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
  resource_counts?: ResourceCounts;
  helm_releases: string[];
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

export interface VersionDiffResponse {
  left: { version: TemplateVersion; snapshot: TemplateSnapshot };
  right: { version: TemplateVersion; snapshot: TemplateSnapshot };
  chart_diffs: Array<{
    chart_name: string;
    left_values: string | null;
    right_values: string | null;
    has_differences: boolean;
    change_type: 'added' | 'removed' | 'modified' | 'unchanged';
  }>;
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
