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
  setOverride: async (id: string, data: Partial<ValueOverride>): Promise<ValueOverride> => {
    try {
      const response = await api.put(`/api/v1/stack-instances/${id}/overrides`, data);
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
  deploy: async (id: string): Promise<{ log_id: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/deploy`);
      return response.data;
    } catch (error) {
      console.error('Failed to deploy instance:', error);
      throw error;
    }
  },
  stop: async (id: string): Promise<{ log_id: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/stop`);
      return response.data;
    } catch (error) {
      console.error('Failed to stop instance:', error);
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

export const auditService = {
  list: async (filters?: AuditLogFilters): Promise<AuditLog[]> => {
    try {
      const response = await api.get('/api/v1/audit-logs', { params: filters });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch audit logs:', error);
      throw error;
    }
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

export default api;
