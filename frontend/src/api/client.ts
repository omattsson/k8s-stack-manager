import axios from 'axios';
import { axiosConfig } from './config';
import type {
  LoginRequest,
  LoginResponse,
  RegisterRequest,
  User,
  StackTemplate,
  TemplateChartConfig,
  CreateTemplateChartRequest,
  StackDefinition,
  ChartConfig,
  CreateChartConfigRequest,
  StackInstance,
  ValueOverride,
  AuditLog,
  AuditLogFilters,
  InstantiateTemplateRequest,
  CreateUserRequest,
  APIKey,
  CreateAPIKeyRequest,
  CreateAPIKeyResponse,
  DeploymentLog,
  NamespaceStatus,
  OrphanedNamespace,
  Cluster,
  CreateClusterRequest,
  UpdateClusterRequest,
  ClusterTestResult,
  ChartBranchOverride,
  UserFavorite,
  QuickDeployRequest,
  QuickDeployResponse,
  ClusterSummary,
  NodeStatusInfo,
  ClusterNamespaceInfo,
  OverviewStats,
  TemplateStats,
  UserStats,
  SharedValues,
  CleanupPolicy,
  CleanupResult,
} from '../types';

const api = axios.create(axiosConfig);

// Auth interceptor — attach JWT from localStorage
api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

// Response interceptor for error handling
api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('token');
      if (window.location.pathname !== '/login') {
        window.location.href = '/login';
      }
    }
    return Promise.reject(error);
  }
);

export const authService = {
  login: async (data: LoginRequest): Promise<LoginResponse> => {
    try {
      const response = await api.post('/api/v1/auth/login', data);
      return response.data;
    } catch (error) {
      console.error('Failed to login:', error);
      throw error;
    }
  },
  register: async (data: RegisterRequest): Promise<User> => {
    try {
      const response = await api.post('/api/v1/auth/register', data);
      return response.data;
    } catch (error) {
      console.error('Failed to register:', error);
      throw error;
    }
  },
  me: async (): Promise<User> => {
    try {
      const response = await api.get('/api/v1/auth/me');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch current user:', error);
      throw error;
    }
  },
};

export const templateService = {
  list: async (): Promise<StackTemplate[]> => {
    try {
      const response = await api.get('/api/v1/templates');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch templates:', error);
      throw error;
    }
  },
  get: async (id: string): Promise<StackTemplate> => {
    try {
      const response = await api.get(`/api/v1/templates/${id}`);
      // API returns { template: {...}, charts: [...] }
      const { template, charts } = response.data;
      return { ...template, charts: charts || [] };
    } catch (error) {
      console.error('Failed to fetch template:', error);
      throw error;
    }
  },
  create: async (data: Partial<StackTemplate>): Promise<StackTemplate> => {
    try {
      const response = await api.post('/api/v1/templates', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create template:', error);
      throw error;
    }
  },
  update: async (id: string, data: Partial<StackTemplate>): Promise<StackTemplate> => {
    try {
      const response = await api.put(`/api/v1/templates/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update template:', error);
      throw error;
    }
  },
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/templates/${id}`);
    } catch (error) {
      console.error('Failed to delete template:', error);
      throw error;
    }
  },
  publish: async (id: string): Promise<StackTemplate> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/publish`);
      return response.data;
    } catch (error) {
      console.error('Failed to publish template:', error);
      throw error;
    }
  },
  unpublish: async (id: string): Promise<StackTemplate> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/unpublish`);
      return response.data;
    } catch (error) {
      console.error('Failed to unpublish template:', error);
      throw error;
    }
  },
  instantiate: async (id: string, data: InstantiateTemplateRequest): Promise<StackDefinition> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/instantiate`, data);
      // API returns { definition: {...}, charts: [...] }
      return response.data.definition;
    } catch (error) {
      console.error('Failed to instantiate template:', error);
      throw error;
    }
  },
  clone: async (id: string): Promise<StackTemplate> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/clone`);
      return response.data;
    } catch (error) {
      console.error('Failed to clone template:', error);
      throw error;
    }
  },
  addChart: async (id: string, data: CreateTemplateChartRequest): Promise<TemplateChartConfig> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/charts`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to add template chart:', error);
      throw error;
    }
  },
  updateChart: async (id: string, chartId: string, data: Partial<TemplateChartConfig>): Promise<TemplateChartConfig> => {
    try {
      const response = await api.put(`/api/v1/templates/${id}/charts/${chartId}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update template chart:', error);
      throw error;
    }
  },
  deleteChart: async (id: string, chartId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/templates/${id}/charts/${chartId}`);
    } catch (error) {
      console.error('Failed to delete template chart:', error);
      throw error;
    }
  },
  quickDeploy: async (id: string, data: QuickDeployRequest): Promise<QuickDeployResponse> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/quick-deploy`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to quick deploy template:', error);
      throw error;
    }
  },
};

export const definitionService = {
  list: async (): Promise<StackDefinition[]> => {
    try {
      const response = await api.get('/api/v1/stack-definitions');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch definitions:', error);
      throw error;
    }
  },
  get: async (id: string): Promise<StackDefinition> => {
    try {
      const response = await api.get(`/api/v1/stack-definitions/${id}`);
      // API returns { definition: {...}, charts: [...] }
      const { definition, charts } = response.data;
      return { ...definition, charts: charts || [] };
    } catch (error) {
      console.error('Failed to fetch definition:', error);
      throw error;
    }
  },
  create: async (data: Partial<StackDefinition>): Promise<StackDefinition> => {
    try {
      const response = await api.post('/api/v1/stack-definitions', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create definition:', error);
      throw error;
    }
  },
  update: async (id: string, data: Partial<StackDefinition>): Promise<StackDefinition> => {
    try {
      const response = await api.put(`/api/v1/stack-definitions/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update definition:', error);
      throw error;
    }
  },
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-definitions/${id}`);
    } catch (error) {
      console.error('Failed to delete definition:', error);
      throw error;
    }
  },
  addChart: async (id: string, data: CreateChartConfigRequest): Promise<ChartConfig> => {
    try {
      const response = await api.post(`/api/v1/stack-definitions/${id}/charts`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to add chart:', error);
      throw error;
    }
  },
  updateChart: async (id: string, chartId: string, data: Partial<ChartConfig>): Promise<ChartConfig> => {
    try {
      const response = await api.put(`/api/v1/stack-definitions/${id}/charts/${chartId}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update chart:', error);
      throw error;
    }
  },
  deleteChart: async (id: string, chartId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-definitions/${id}/charts/${chartId}`);
    } catch (error) {
      console.error('Failed to delete chart:', error);
      throw error;
    }
  },
};

export const instanceService = {
  list: async (): Promise<StackInstance[]> => {
    try {
      const response = await api.get('/api/v1/stack-instances');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch instances:', error);
      throw error;
    }
  },
  get: async (id: string): Promise<StackInstance> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch instance:', error);
      throw error;
    }
  },
  create: async (data: Partial<StackInstance>): Promise<StackInstance> => {
    try {
      const response = await api.post('/api/v1/stack-instances', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create instance:', error);
      throw error;
    }
  },
  update: async (id: string, data: Partial<StackInstance>): Promise<StackInstance> => {
    try {
      const response = await api.put(`/api/v1/stack-instances/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update instance:', error);
      throw error;
    }
  },
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-instances/${id}`);
    } catch (error) {
      console.error('Failed to delete instance:', error);
      throw error;
    }
  },
  clone: async (id: string): Promise<StackInstance> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/clone`);
      return response.data;
    } catch (error) {
      console.error('Failed to clone instance:', error);
      throw error;
    }
  },
  getOverrides: async (id: string): Promise<ValueOverride[]> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/overrides`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch overrides:', error);
      throw error;
    }
  },
  setOverride: async (id: string, chartConfigId: string, data: { values: string }): Promise<ValueOverride> => {
    try {
      const response = await api.put(`/api/v1/stack-instances/${id}/overrides/${chartConfigId}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to set override:', error);
      throw error;
    }
  },
  exportValues: async (id: string): Promise<string> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/export`);
      return response.data;
    } catch (error) {
      console.error('Failed to export values:', error);
      throw error;
    }
  },
  deploy: async (id: string): Promise<{ log_id: string; message: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/deploy`);
      return response.data;
    } catch (error) {
      console.error('Failed to deploy instance:', error);
      throw error;
    }
  },
  stop: async (id: string): Promise<{ log_id: string; message: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/stop`);
      return response.data;
    } catch (error) {
      console.error('Failed to stop instance:', error);
      throw error;
    }
  },
  clean: async (id: string): Promise<{ log_id: string; message: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/clean`);
      return response.data;
    } catch (error) {
      console.error('Failed to clean namespace:', error);
      throw error;
    }
  },
  getDeployLog: async (id: string): Promise<DeploymentLog[]> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/deploy-log`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch deployment logs:', error);
      throw error;
    }
  },
  getStatus: async (id: string): Promise<NamespaceStatus> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/status`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch instance status:', error);
      throw error;
    }
  },
  extend: async (id: string, ttlMinutes?: number): Promise<StackInstance> => {
    try {
      const response = await api.post(
        `/api/v1/stack-instances/${id}/extend`,
        ttlMinutes ? { ttl_minutes: ttlMinutes } : {},
      );
      return response.data;
    } catch (error) {
      console.error('Failed to extend TTL:', error);
      throw error;
    }
  },
  recent: async (): Promise<StackInstance[]> => {
    try {
      const response = await api.get('/api/v1/stack-instances/recent');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch recent instances:', error);
      throw error;
    }
  },
};

export const gitService = {
  branches: async (repoUrl: string): Promise<string[]> => {
    try {
      const response = await api.get('/api/v1/git/branches', { params: { repo: repoUrl } });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch branches:', error);
      throw error;
    }
  },
  validateBranch: async (repoUrl: string, branch: string): Promise<boolean> => {
    try {
      const response = await api.get('/api/v1/git/validate-branch', { params: { repo: repoUrl, branch } });
      return response.data.valid;
    } catch (error) {
      console.error('Failed to validate branch:', error);
      throw error;
    }
  },
  providers: async (): Promise<string[]> => {
    try {
      const response = await api.get('/api/v1/git/providers');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch providers:', error);
      throw error;
    }
  },
};

export interface PaginatedAuditLogs {
  data: AuditLog[];
  total: number;
  limit: number;
  offset: number;
}

export const auditService = {
  list: async (filters?: AuditLogFilters): Promise<PaginatedAuditLogs> => {
    try {
      const response = await api.get('/api/v1/audit-logs', { params: filters });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch audit logs:', error);
      throw error;
    }
  },
  export: async (filters: AuditLogFilters, format: 'csv' | 'json' = 'json'): Promise<void> => {
    const params = new URLSearchParams();
    params.set('format', format);
    if (filters.user_id) params.set('user_id', filters.user_id);
    if (filters.entity_type) params.set('entity_type', filters.entity_type);
    if (filters.action) params.set('action', filters.action);
    if (filters.start_date) params.set('start_date', filters.start_date);
    if (filters.end_date) params.set('end_date', filters.end_date);

    const response = await api.get('/api/v1/audit-logs/export', {
      params,
      responseType: 'blob',
    });

    const contentDisposition = response.headers['content-disposition'];
    let filename = `audit-logs.${format}`;
    if (contentDisposition) {
      const match = contentDisposition.match(/filename=([^;]+)/);
      if (match) filename = match[1].trim();
    }

    const url = window.URL.createObjectURL(new Blob([response.data]));
    const link = document.createElement('a');
    link.href = url;
    link.setAttribute('download', filename);
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.URL.revokeObjectURL(url);
  },
};

export const userService = {
  list: async (): Promise<User[]> => {
    try {
      const response = await api.get('/api/v1/users');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch users:', error);
      throw error;
    }
  },
  create: async (data: CreateUserRequest): Promise<User> => {
    try {
      const response = await api.post('/api/v1/auth/register', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create user:', error);
      throw error;
    }
  },
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/users/${id}`);
    } catch (error) {
      console.error('Failed to delete user:', error);
      throw error;
    }
  },
};

export const apiKeyService = {
  list: async (userId: string): Promise<APIKey[]> => {
    try {
      const response = await api.get(`/api/v1/users/${userId}/api-keys`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch API keys:', error);
      throw error;
    }
  },
  create: async (userId: string, data: CreateAPIKeyRequest): Promise<CreateAPIKeyResponse> => {
    try {
      const response = await api.post(`/api/v1/users/${userId}/api-keys`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to create API key:', error);
      throw error;
    }
  },
  delete: async (userId: string, keyId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/users/${userId}/api-keys/${keyId}`);
    } catch (error) {
      console.error('Failed to delete API key:', error);
      throw error;
    }
  },
};

export const adminService = {
  listOrphanedNamespaces: async (): Promise<OrphanedNamespace[]> => {
    try {
      const response = await api.get('/api/v1/admin/orphaned-namespaces');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch orphaned namespaces:', error);
      throw error;
    }
  },
  deleteOrphanedNamespace: async (namespace: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/admin/orphaned-namespaces/${namespace}`);
    } catch (error) {
      console.error('Failed to delete orphaned namespace:', error);
      throw error;
    }
  },
};

export const clusterService = {
  list: async (): Promise<Cluster[]> => {
    try {
      const response = await api.get('/api/v1/clusters');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch clusters:', error);
      throw error;
    }
  },
  get: async (id: string): Promise<Cluster> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster:', error);
      throw error;
    }
  },
  create: async (data: CreateClusterRequest): Promise<Cluster> => {
    try {
      const response = await api.post('/api/v1/clusters', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create cluster:', error);
      throw error;
    }
  },
  update: async (id: string, data: UpdateClusterRequest): Promise<Cluster> => {
    try {
      const response = await api.put(`/api/v1/clusters/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update cluster:', error);
      throw error;
    }
  },
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/clusters/${id}`);
    } catch (error) {
      console.error('Failed to delete cluster:', error);
      throw error;
    }
  },
  testConnection: async (id: string): Promise<ClusterTestResult> => {
    try {
      const response = await api.post(`/api/v1/clusters/${id}/test`);
      return response.data;
    } catch (error) {
      console.error('Failed to test cluster connection:', error);
      throw error;
    }
  },
  setDefault: async (id: string): Promise<{ message: string }> => {
    try {
      const response = await api.post(`/api/v1/clusters/${id}/default`);
      return response.data;
    } catch (error) {
      console.error('Failed to set default cluster:', error);
      throw error;
    }
  },
  getHealthSummary: async (id: string): Promise<ClusterSummary> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}/health/summary`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster health summary:', error);
      throw error;
    }
  },
  getNodes: async (id: string): Promise<NodeStatusInfo[]> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}/health/nodes`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster nodes:', error);
      throw error;
    }
  },
  getNamespaces: async (id: string): Promise<ClusterNamespaceInfo[]> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}/namespaces`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster namespaces:', error);
      throw error;
    }
  },
};

export const favoriteService = {
  list: async (): Promise<UserFavorite[]> => {
    try {
      const response = await api.get('/api/v1/favorites');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch favorites:', error);
      throw error;
    }
  },
  add: async (entityType: string, entityId: string): Promise<UserFavorite> => {
    try {
      const response = await api.post('/api/v1/favorites', { entity_type: entityType, entity_id: entityId });
      return response.data;
    } catch (error) {
      console.error('Failed to add favorite:', error);
      throw error;
    }
  },
  remove: async (entityType: string, entityId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/favorites/${entityType}/${entityId}`);
    } catch (error) {
      console.error('Failed to remove favorite:', error);
      throw error;
    }
  },
  check: async (entityType: string, entityId: string): Promise<boolean> => {
    try {
      const response = await api.get('/api/v1/favorites/check', {
        params: { entity_type: entityType, entity_id: entityId },
      });
      return response.data.is_favorite;
    } catch (error) {
      console.error('Failed to check favorite:', error);
      throw error;
    }
  },
};

export const branchOverrideService = {
  list: async (instanceId: string): Promise<ChartBranchOverride[]> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${instanceId}/branches`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch branch overrides:', error);
      throw error;
    }
  },
  set: async (instanceId: string, chartId: string, branch: string): Promise<ChartBranchOverride> => {
    try {
      const response = await api.put(`/api/v1/stack-instances/${instanceId}/branches/${chartId}`, { branch });
      return response.data;
    } catch (error) {
      console.error('Failed to set branch override:', error);
      throw error;
    }
  },
  delete: async (instanceId: string, chartId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-instances/${instanceId}/branches/${chartId}`);
    } catch (error) {
      console.error('Failed to delete branch override:', error);
      throw error;
    }
  },
};

export const analyticsService = {
  getOverview: async (): Promise<OverviewStats> => {
    try {
      const response = await api.get('/api/v1/analytics/overview');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch analytics overview:', error);
      throw error;
    }
  },
  getTemplateStats: async (): Promise<TemplateStats[]> => {
    try {
      const response = await api.get('/api/v1/analytics/templates');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch template stats:', error);
      throw error;
    }
  },
  getUserStats: async (): Promise<UserStats[]> => {
    try {
      const response = await api.get('/api/v1/analytics/users');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch user stats:', error);
      throw error;
    }
  },
};

export const sharedValuesService = {
  list: async (clusterId: string): Promise<SharedValues[]> => {
    try {
      const response = await api.get(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch shared values:', error);
      throw error;
    }
  },
  create: async (clusterId: string, sv: Partial<SharedValues>): Promise<SharedValues> => {
    try {
      const response = await api.post(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values`, sv);
      return response.data;
    } catch (error) {
      console.error('Failed to create shared values:', error);
      throw error;
    }
  },
  update: async (clusterId: string, valueId: string, sv: Partial<SharedValues>): Promise<SharedValues> => {
    try {
      const response = await api.put(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values/${encodeURIComponent(valueId)}`, sv);
      return response.data;
    } catch (error) {
      console.error('Failed to update shared values:', error);
      throw error;
    }
  },
  delete: async (clusterId: string, valueId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values/${encodeURIComponent(valueId)}`);
    } catch (error) {
      console.error('Failed to delete shared values:', error);
      throw error;
    }
  },
};

export const cleanupPolicyService = {
  list: async (): Promise<CleanupPolicy[]> => {
    try {
      const response = await api.get('/api/v1/admin/cleanup-policies');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cleanup policies:', error);
      throw error;
    }
  },
  create: async (policy: Partial<CleanupPolicy>): Promise<CleanupPolicy> => {
    try {
      const response = await api.post('/api/v1/admin/cleanup-policies', policy);
      return response.data;
    } catch (error) {
      console.error('Failed to create cleanup policy:', error);
      throw error;
    }
  },
  update: async (id: string, policy: Partial<CleanupPolicy>): Promise<CleanupPolicy> => {
    try {
      const response = await api.put(`/api/v1/admin/cleanup-policies/${encodeURIComponent(id)}`, policy);
      return response.data;
    } catch (error) {
      console.error('Failed to update cleanup policy:', error);
      throw error;
    }
  },
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/admin/cleanup-policies/${encodeURIComponent(id)}`);
    } catch (error) {
      console.error('Failed to delete cleanup policy:', error);
      throw error;
    }
  },
  run: async (id: string, dryRun: boolean): Promise<CleanupResult[]> => {
    try {
      const response = await api.post(`/api/v1/admin/cleanup-policies/${encodeURIComponent(id)}/run?dry_run=${dryRun}`);
      return response.data;
    } catch (error) {
      console.error('Failed to run cleanup policy:', error);
      throw error;
    }
  },
};

export default api;
