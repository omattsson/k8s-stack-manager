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
  action: 'deploy' | 'stop' | 'clean';
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

export interface PodInfo {
  name: string;
  phase: string;
  ready: boolean;
  restart_count: number;
  image: string;
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
  status: 'healthy' | 'degraded' | 'error' | 'not_found';
  charts: ChartStatus[];
  ingresses?: IngressInfo[];
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
}

export interface CreateAPIKeyResponse {
  id: string;
  user_id: string;
  name: string;
  prefix: string;
  raw_key: string;
  created_at: string;
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
