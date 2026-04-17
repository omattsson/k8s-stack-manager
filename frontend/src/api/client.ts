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
  CompareInstancesResponse,
  DefinitionExportBundle,
  BulkOperationResponse,
  BulkTemplateResponse,
  NotificationListResponse,
  NotificationPreference,
  Notification,
  ResourceQuotaConfig,
  ClusterUtilization,
  TemplateVersion,
  VersionDiffResponse,
  UpgradeCheckResponse,
  InstanceQuotaOverride,
  DeployPreviewResponse,
} from '../types';

// Extend Axios config to include our retry flag (avoids `any` cast).
declare module 'axios' {
  interface InternalAxiosRequestConfig {
    _retry?: boolean;
  }
}

const api = axios.create(axiosConfig);

// Separate instance for token refresh — sends the httpOnly cookie and has no
// response interceptors, preventing refresh-retry loops.
const refreshApi = axios.create({ ...axiosConfig, withCredentials: true });

// Auth interceptor — attach JWT from localStorage
api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

// --- Silent token refresh on 401 ---
interface QueueItem {
  resolve: (token: string) => void;
  reject: (error: unknown) => void;
}

let isRefreshing = false;
let failedQueue: QueueItem[] = [];

function processQueue(error: unknown, token: string | null): void {
  for (const item of failedQueue) {
    if (token) {
      item.resolve(token);
    } else {
      item.reject(error);
    }
  }
  failedQueue = [];
}

// Response interceptor — attempt a silent refresh on 401, then retry the
// original request. Concurrent 401s are queued so only one refresh fires.
api.interceptors.response.use(
  (response) => response,
  async (error) => {
    const originalRequest = error.config;
    const authPaths = ['/auth/login', '/auth/refresh', '/auth/logout', '/auth/logout-all'];
    const isAuthEndpoint = authPaths.some(p => originalRequest?.url?.includes(p));

    if (
      error.response?.status === 401 &&
      originalRequest &&
      !originalRequest._retry &&
      !isAuthEndpoint
    ) {
      if (isRefreshing) {
        // Park this request until the in-flight refresh settles.
        return new Promise<unknown>((resolve, reject) => {
          failedQueue.push({
            resolve: (token: string) => {
              originalRequest._retry = true;
              originalRequest.headers.Authorization = `Bearer ${token}`;
              resolve(api(originalRequest));
            },
            reject,
          });
        });
      }

      originalRequest._retry = true;
      isRefreshing = true;

      try {
        const { data } = await refreshApi.post<{ token: string }>(
          '/api/v1/auth/refresh',
        );
        const newToken = data.token;
        localStorage.setItem('token', newToken);
        originalRequest.headers.Authorization = `Bearer ${newToken}`;
        processQueue(null, newToken);
        return api(originalRequest);
      } catch (refreshError) {
        processQueue(refreshError, null);
        localStorage.removeItem('token');
        if (globalThis.location.pathname !== '/login') {
          globalThis.location.href = '/login';
        }
        return Promise.reject(refreshError);
      } finally {
        isRefreshing = false;
      }
    }

    // Retried request still got 401 — session is invalid
    if (error.response?.status === 401 && originalRequest?._retry) {
      localStorage.removeItem('token');
      if (globalThis.location.pathname !== '/login') {
        globalThis.location.href = '/login';
      }
    }

    return Promise.reject(error);
  },
);

/** Authentication service for login, registration, and session management. Maps to `/api/v1/auth`. */
export const authService = {
  /**
   * Authenticate a user with credentials.
   * @param data - Login credentials (username + password)
   * @returns JWT token and user info
   * @see POST /api/v1/auth/login
   */
  login: async (data: LoginRequest): Promise<LoginResponse> => {
    try {
      const response = await api.post('/api/v1/auth/login', data, { withCredentials: true });
      return response.data;
    } catch (error) {
      console.error('Failed to login:', error);
      throw error;
    }
  },
  /**
   * Register a new user account.
   * @param data - Registration details (username, password, role)
   * @returns The created user
   * @see POST /api/v1/auth/register
   */
  register: async (data: RegisterRequest): Promise<User> => {
    try {
      const response = await api.post('/api/v1/auth/register', data);
      return response.data;
    } catch (error) {
      console.error('Failed to register:', error);
      throw error;
    }
  },
  /**
   * Fetch the currently authenticated user's profile.
   * @returns Current user details
   * @see GET /api/v1/auth/me
   */
  me: async (): Promise<User> => {
    try {
      const response = await api.get('/api/v1/auth/me');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch current user:', error);
      throw error;
    }
  },
  /**
   * Refresh the access token using the httpOnly refresh cookie.
   * @returns Object containing the new short-lived access token
   * @see POST /api/v1/auth/refresh
   */
  refresh: async (): Promise<{ token: string }> => {
    const response = await refreshApi.post<{ token: string }>('/api/v1/auth/refresh');
    return response.data;
  },
  /**
   * Logout the current session (revokes the refresh token server-side and clears the cookie).
   * @see POST /api/v1/auth/logout
   */
  logout: async (token?: string): Promise<void> => {
    try {
      await api.post('/api/v1/auth/logout', null, {
        withCredentials: true,
        ...(token ? { headers: { Authorization: `Bearer ${token}` } } : {}),
      });
    } catch (error) {
      console.error('Failed to logout:', error);
      throw error;
    }
  },
  /**
   * Logout all sessions for the current user (revokes every refresh token).
   * @see POST /api/v1/auth/logout-all
   */
  logoutAll: async (): Promise<void> => {
    try {
      await api.post('/api/v1/auth/logout-all', null, { withCredentials: true });
    } catch (error) {
      console.error('Failed to logout all sessions:', error);
      throw error;
    }
  },
};

/** OIDC authentication service. Maps to `/api/v1/auth/oidc`. */
export const oidcService = {
  /**
   * Get OIDC configuration (public endpoint).
   * @returns OIDC config with enabled status and provider name
   * @see GET /api/v1/auth/oidc/config
   */
  getConfig: async (): Promise<{ enabled: boolean; provider_name: string; local_auth_enabled: boolean }> => {
    try {
      const response = await api.get('/api/v1/auth/oidc/config');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch OIDC config:', error);
      throw error;
    }
  },
  /**
   * Get the OIDC authorization URL to redirect the user to.
   * @param redirect - Optional URL to redirect to after authentication
   * @returns Object with redirect_url to the identity provider
   * @see GET /api/v1/auth/oidc/authorize
   */
  getAuthorizeUrl: async (redirect?: string): Promise<{ redirect_url: string }> => {
    try {
      const response = await api.get('/api/v1/auth/oidc/authorize', { params: redirect ? { redirect } : undefined });
      return response.data;
    } catch (error) {
      console.error('Failed to get OIDC authorize URL:', error);
      throw error;
    }
  },
};

/** Stack template service for managing reusable deployment templates. Maps to `/api/v1/templates`. */
export const templateService = {
  /**
   * List all stack templates.
   * @returns Array of stack templates
   * @see GET /api/v1/templates
   */
  list: async (): Promise<StackTemplate[]> => {
    try {
      const response = await api.get('/api/v1/templates');
      const body = response.data;
      return Array.isArray(body) ? body : body.data ?? [];
    } catch (error) {
      console.error('Failed to fetch templates:', error);
      throw error;
    }
  },
  /**
   * Fetch a single template with its chart configurations.
   * @param id - Template ID
   * @returns Template with charts array merged in
   * @see GET /api/v1/templates/:id
   */
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
  /**
   * Create a new stack template.
   * @param data - Template fields
   * @returns The created template
   * @see POST /api/v1/templates
   */
  create: async (data: Partial<StackTemplate>): Promise<StackTemplate> => {
    try {
      const response = await api.post('/api/v1/templates', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create template:', error);
      throw error;
    }
  },
  /**
   * Update an existing template.
   * @param id - Template ID
   * @param data - Fields to update
   * @returns The updated template
   * @see PUT /api/v1/templates/:id
   */
  update: async (id: string, data: Partial<StackTemplate>): Promise<StackTemplate> => {
    try {
      const response = await api.put(`/api/v1/templates/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update template:', error);
      throw error;
    }
  },
  /**
   * Delete a template.
   * @param id - Template ID
   * @see DELETE /api/v1/templates/:id
   */
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/templates/${id}`);
    } catch (error) {
      console.error('Failed to delete template:', error);
      throw error;
    }
  },
  /**
   * Publish a template, making it available for instantiation.
   * @param id - Template ID
   * @returns The published template
   * @see POST /api/v1/templates/:id/publish
   */
  publish: async (id: string): Promise<StackTemplate> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/publish`);
      return response.data;
    } catch (error) {
      console.error('Failed to publish template:', error);
      throw error;
    }
  },
  /**
   * Unpublish a template, hiding it from the gallery.
   * @param id - Template ID
   * @returns The unpublished template
   * @see POST /api/v1/templates/:id/unpublish
   */
  unpublish: async (id: string): Promise<StackTemplate> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/unpublish`);
      return response.data;
    } catch (error) {
      console.error('Failed to unpublish template:', error);
      throw error;
    }
  },
  /**
   * Instantiate a template into a new stack definition.
   * @param id - Template ID
   * @param data - Instance name and configuration overrides
   * @returns The created stack definition
   * @see POST /api/v1/templates/:id/instantiate
   */
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
  /**
   * Clone a template, creating an independent copy.
   * @param id - Template ID to clone
   * @returns The cloned template
   * @see POST /api/v1/templates/:id/clone
   */
  clone: async (id: string): Promise<StackTemplate> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/clone`);
      return response.data;
    } catch (error) {
      console.error('Failed to clone template:', error);
      throw error;
    }
  },
  /**
   * Add a chart configuration to a template.
   * @param id - Template ID
   * @param data - Chart config to add
   * @returns The created template chart config
   * @see POST /api/v1/templates/:id/charts
   */
  addChart: async (id: string, data: CreateTemplateChartRequest): Promise<TemplateChartConfig> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/charts`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to add template chart:', error);
      throw error;
    }
  },
  /**
   * Update a chart configuration within a template.
   * @param id - Template ID
   * @param chartId - Chart config ID
   * @param data - Fields to update
   * @returns The updated template chart config
   * @see PUT /api/v1/templates/:id/charts/:chartId
   */
  updateChart: async (id: string, chartId: string, data: Partial<TemplateChartConfig>): Promise<TemplateChartConfig> => {
    try {
      const response = await api.put(`/api/v1/templates/${id}/charts/${chartId}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update template chart:', error);
      throw error;
    }
  },
  /**
   * Remove a chart configuration from a template.
   * @param id - Template ID
   * @param chartId - Chart config ID
   * @see DELETE /api/v1/templates/:id/charts/:chartId
   */
  deleteChart: async (id: string, chartId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/templates/${id}/charts/${chartId}`);
    } catch (error) {
      console.error('Failed to delete template chart:', error);
      throw error;
    }
  },
  /**
   * One-click deploy: create an instance from a template and deploy it immediately.
   * @param id - Template ID
   * @param data - Quick deploy options (cluster, TTL, etc.)
   * @returns The created instance and deployment result
   * @see POST /api/v1/templates/:id/quick-deploy
   */
  quickDeploy: async (id: string, data: QuickDeployRequest): Promise<QuickDeployResponse> => {
    try {
      const response = await api.post(`/api/v1/templates/${id}/quick-deploy`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to quick deploy template:', error);
      throw error;
    }
  },
  /**
   * Bulk delete unpublished templates.
   * @param templateIds - Array of template IDs to delete
   * @returns Bulk operation results
   * @see POST /api/v1/templates/bulk/delete
   */
  bulkDelete: async (templateIds: string[]): Promise<BulkTemplateResponse> => {
    const response = await api.post('/api/v1/templates/bulk/delete', { template_ids: templateIds });
    return response.data;
  },
  /**
   * Bulk publish templates.
   * @param templateIds - Array of template IDs to publish
   * @returns Bulk operation results
   * @see POST /api/v1/templates/bulk/publish
   */
  bulkPublish: async (templateIds: string[]): Promise<BulkTemplateResponse> => {
    const response = await api.post('/api/v1/templates/bulk/publish', { template_ids: templateIds });
    return response.data;
  },
  /**
   * Bulk unpublish templates.
   * @param templateIds - Array of template IDs to unpublish
   * @returns Bulk operation results
   * @see POST /api/v1/templates/bulk/unpublish
   */
  bulkUnpublish: async (templateIds: string[]): Promise<BulkTemplateResponse> => {
    const response = await api.post('/api/v1/templates/bulk/unpublish', { template_ids: templateIds });
    return response.data;
  },
  /**
   * List all versions of a template, newest first.
   * @param templateId - Template ID
   * @returns Array of template versions
   * @see GET /api/v1/templates/:id/versions
   */
  listVersions: async (templateId: string): Promise<TemplateVersion[]> => {
    try {
      const response = await api.get(`/api/v1/templates/${templateId}/versions`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch template versions:', error);
      throw error;
    }
  },
  /**
   * Fetch a single version with its snapshot data.
   * @param templateId - Template ID
   * @param versionId - Version ID
   * @returns Template version with snapshot
   * @see GET /api/v1/templates/:id/versions/:versionId
   */
  getVersion: async (templateId: string, versionId: string): Promise<TemplateVersion> => {
    try {
      const response = await api.get(`/api/v1/templates/${templateId}/versions/${versionId}`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch template version:', error);
      throw error;
    }
  },
  /**
   * Diff two versions of a template, showing per-chart YAML differences.
   * @param templateId - Template ID
   * @param leftId - Left version ID
   * @param rightId - Right version ID
   * @returns Diff response with chart-level differences
   * @see GET /api/v1/templates/:id/versions/diff?left=ID&right=ID
   */
  diffVersions: async (templateId: string, leftId: string, rightId: string): Promise<VersionDiffResponse> => {
    try {
      const response = await api.get(`/api/v1/templates/${templateId}/versions/diff`, {
        params: { left: leftId, right: rightId },
      });
      return response.data;
    } catch (error) {
      console.error('Failed to diff template versions:', error);
      throw error;
    }
  },
};

/** Stack definition service for managing Helm-based stack configurations. Maps to `/api/v1/stack-definitions`. */
export const definitionService = {
  /**
   * List stack definitions with optional server-side pagination.
   * Unwraps the pagination envelope and returns only the data array.
   * For full pagination metadata, use `listPaged()`.
   * @param params - Optional pagination parameters (page, pageSize)
   * @returns Array of stack definitions
   * @see GET /api/v1/stack-definitions
   */
  list: async (params?: { page?: number; pageSize?: number }): Promise<StackDefinition[]> => {
    try {
      const response = await api.get('/api/v1/stack-definitions', { params });
      // The API returns { data: [...], total, page, pageSize }.
      // Return the data array for backward compatibility with callers.
      const body = response.data;
      if (body && Array.isArray(body.data)) {
        return body.data;
      }
      // Fallback for unexpected shapes.
      return Array.isArray(body) ? body : [];
    } catch (error) {
      console.error('Failed to fetch definitions:', error);
      throw error;
    }
  },

  /**
   * List stack definitions with full pagination metadata.
   * @param params - Optional pagination parameters (page, pageSize)
   * @returns Paginated response envelope with data, total, page, pageSize
   * @see GET /api/v1/stack-definitions
   */
  listPaged: async (params?: { page?: number; pageSize?: number }): Promise<{
    data: StackDefinition[];
    total: number;
    page: number;
    pageSize: number;
  }> => {
    try {
      const response = await api.get('/api/v1/stack-definitions', { params });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch definitions:', error);
      throw error;
    }
  },
  /**
   * Fetch a single definition with its chart configurations.
   * @param id - Definition ID
   * @returns Definition with charts array merged in
   * @see GET /api/v1/stack-definitions/:id
   */
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
  /**
   * Create a new stack definition.
   * @param data - Definition fields
   * @returns The created definition
   * @see POST /api/v1/stack-definitions
   */
  create: async (data: Partial<StackDefinition>): Promise<StackDefinition> => {
    try {
      const response = await api.post('/api/v1/stack-definitions', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create definition:', error);
      throw error;
    }
  },
  /**
   * Update an existing stack definition.
   * @param id - Definition ID
   * @param data - Fields to update
   * @returns The updated definition
   * @see PUT /api/v1/stack-definitions/:id
   */
  update: async (id: string, data: Partial<StackDefinition>): Promise<StackDefinition> => {
    try {
      const response = await api.put(`/api/v1/stack-definitions/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update definition:', error);
      throw error;
    }
  },
  /**
   * Delete a stack definition.
   * @param id - Definition ID
   * @see DELETE /api/v1/stack-definitions/:id
   */
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-definitions/${id}`);
    } catch (error) {
      console.error('Failed to delete definition:', error);
      throw error;
    }
  },
  /**
   * Add a chart configuration to a definition.
   * @param id - Definition ID
   * @param data - Chart config to add
   * @returns The created chart config
   * @see POST /api/v1/stack-definitions/:id/charts
   */
  addChart: async (id: string, data: CreateChartConfigRequest): Promise<ChartConfig> => {
    try {
      const response = await api.post(`/api/v1/stack-definitions/${id}/charts`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to add chart:', error);
      throw error;
    }
  },
  /**
   * Update a chart configuration within a definition.
   * @param id - Definition ID
   * @param chartId - Chart config ID
   * @param data - Fields to update
   * @returns The updated chart config
   * @see PUT /api/v1/stack-definitions/:id/charts/:chartId
   */
  updateChart: async (id: string, chartId: string, data: Partial<ChartConfig>): Promise<ChartConfig> => {
    try {
      const response = await api.put(`/api/v1/stack-definitions/${id}/charts/${chartId}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update chart:', error);
      throw error;
    }
  },
  /**
   * Remove a chart configuration from a definition.
   * @param id - Definition ID
   * @param chartId - Chart config ID
   * @see DELETE /api/v1/stack-definitions/:id/charts/:chartId
   */
  deleteChart: async (id: string, chartId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-definitions/${id}/charts/${chartId}`);
    } catch (error) {
      console.error('Failed to delete chart:', error);
      throw error;
    }
  },
  /**
   * Export a stack definition as a portable JSON bundle.
   * @param id - Definition ID
   * @returns The export bundle containing definition and charts
   * @see GET /api/v1/stack-definitions/:id/export
   */
  exportDefinition: async (id: string): Promise<DefinitionExportBundle> => {
    try {
      const response = await api.get(`/api/v1/stack-definitions/${id}/export`);
      return response.data;
    } catch (error) {
      console.error('Failed to export definition:', error);
      throw error;
    }
  },
  /**
   * Import a stack definition from a JSON bundle.
   * @param bundle - The export bundle to import
   * @returns The created stack definition
   * @see POST /api/v1/stack-definitions/import
   */
  importDefinition: async (bundle: DefinitionExportBundle): Promise<StackDefinition> => {
    try {
      const response = await api.post('/api/v1/stack-definitions/import', bundle);
      return response.data;
    } catch (error) {
      console.error('Failed to import definition:', error);
      throw error;
    }
  },
  /**
   * Check if a template upgrade is available for a definition.
   * @param definitionId - Definition ID
   * @returns Upgrade check result with change details
   * @see GET /api/v1/stack-definitions/:id/check-upgrade
   */
  checkUpgrade: async (definitionId: string): Promise<UpgradeCheckResponse> => {
    try {
      const response = await api.get(`/api/v1/stack-definitions/${definitionId}/check-upgrade`);
      return response.data;
    } catch (error) {
      console.error('Failed to check for upgrade:', error);
      throw error;
    }
  },
  /**
   * Apply a template upgrade to a definition, updating its charts to match the latest template version.
   * @param definitionId - Definition ID
   * @returns The updated definition
   * @see POST /api/v1/stack-definitions/:id/upgrade
   */
  applyUpgrade: async (definitionId: string): Promise<StackDefinition> => {
    try {
      const response = await api.post(`/api/v1/stack-definitions/${definitionId}/upgrade`);
      return response.data;
    } catch (error) {
      console.error('Failed to apply upgrade:', error);
      throw error;
    }
  },
};

/** Stack instance service for managing deployed stack instances. Maps to `/api/v1/stack-instances`. */
export const instanceService = {
  /**
   * List stack instances with server-side pagination.
   * Unwraps the pagination envelope and returns only the data array.
   * For full pagination metadata, use `listPaged()`.
   * @param params - Optional pagination parameters (page, pageSize)
   * @returns Array of stack instances
   * @see GET /api/v1/stack-instances
   */
  list: async (params?: { page?: number; pageSize?: number }): Promise<StackInstance[]> => {
    try {
      const response = await api.get('/api/v1/stack-instances', { params });
      // The API returns { data: [...], total, page, pageSize }.
      // Return the data array for backward compatibility with callers.
      const body = response.data;
      if (body && Array.isArray(body.data)) {
        return body.data;
      }
      // Fallback for unexpected shapes.
      return Array.isArray(body) ? body : [];
    } catch (error) {
      console.error('Failed to fetch instances:', error);
      throw error;
    }
  },

  /**
   * List stack instances with full pagination metadata.
   * @param params - Optional pagination parameters (page, pageSize)
   * @returns Paginated response envelope with data, total, page, pageSize
   * @see GET /api/v1/stack-instances
   */
  listPaged: async (params?: { page?: number; pageSize?: number }): Promise<{
    data: StackInstance[];
    total: number;
    page: number;
    pageSize: number;
  }> => {
    try {
      const response = await api.get('/api/v1/stack-instances', { params });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch instances:', error);
      throw error;
    }
  },
  /**
   * Fetch a single stack instance.
   * @param id - Instance ID
   * @returns The stack instance
   * @see GET /api/v1/stack-instances/:id
   */
  get: async (id: string): Promise<StackInstance> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch instance:', error);
      throw error;
    }
  },
  /**
   * Create a new stack instance.
   * @param data - Instance fields
   * @returns The created instance
   * @see POST /api/v1/stack-instances
   */
  create: async (data: Partial<StackInstance>): Promise<StackInstance> => {
    try {
      const response = await api.post('/api/v1/stack-instances', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create instance:', error);
      throw error;
    }
  },
  /**
   * Update an existing stack instance.
   * @param id - Instance ID
   * @param data - Fields to update
   * @returns The updated instance
   * @see PUT /api/v1/stack-instances/:id
   */
  update: async (id: string, data: Partial<StackInstance>): Promise<StackInstance> => {
    try {
      const response = await api.put(`/api/v1/stack-instances/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update instance:', error);
      throw error;
    }
  },
  /**
   * Delete a stack instance.
   * @param id - Instance ID
   * @see DELETE /api/v1/stack-instances/:id
   */
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-instances/${id}`);
    } catch (error) {
      console.error('Failed to delete instance:', error);
      throw error;
    }
  },
  /**
   * Clone an instance, creating an independent copy.
   * @param id - Instance ID to clone
   * @returns The cloned instance
   * @see POST /api/v1/stack-instances/:id/clone
   */
  clone: async (id: string): Promise<StackInstance> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/clone`);
      return response.data;
    } catch (error) {
      console.error('Failed to clone instance:', error);
      throw error;
    }
  },
  /**
   * Fetch all value overrides for an instance.
   * @param id - Instance ID
   * @returns Array of per-chart value overrides
   * @see GET /api/v1/stack-instances/:id/overrides
   */
  getOverrides: async (id: string): Promise<ValueOverride[]> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/overrides`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch overrides:', error);
      throw error;
    }
  },
  /**
   * Set a value override for a specific chart in an instance.
   * @param id - Instance ID
   * @param chartConfigId - Chart config ID to override
   * @param data - YAML values string
   * @returns The created or updated override
   * @see PUT /api/v1/stack-instances/:id/overrides/:chartConfigId
   */
  setOverride: async (id: string, chartConfigId: string, data: { values: string }): Promise<ValueOverride> => {
    try {
      const response = await api.put(`/api/v1/stack-instances/${id}/overrides/${chartConfigId}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to set override:', error);
      throw error;
    }
  },
  /**
   * Export merged Helm values for an instance as YAML.
   * @param id - Instance ID
   * @returns YAML string of merged values
   * @see GET /api/v1/stack-instances/:id/export
   */
  exportValues: async (id: string): Promise<string> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/export`);
      return response.data;
    } catch (error) {
      console.error('Failed to export values:', error);
      throw error;
    }
  },
  /**
   * Deploy an instance to the target cluster.
   * @param id - Instance ID
   * @returns Deployment log ID and status message
   * @see POST /api/v1/stack-instances/:id/deploy
   */
  deploy: async (id: string): Promise<{ log_id: string; message: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/deploy`);
      return response.data;
    } catch (error) {
      console.error('Failed to deploy instance:', error);
      throw error;
    }
  },
  /**
   * Stop (undeploy) an instance from the cluster.
   * @param id - Instance ID
   * @returns Deployment log ID and status message
   * @see POST /api/v1/stack-instances/:id/stop
   */
  stop: async (id: string): Promise<{ log_id: string; message: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/stop`);
      return response.data;
    } catch (error) {
      console.error('Failed to stop instance:', error);
      throw error;
    }
  },
  /**
   * Clean the Kubernetes namespace associated with an instance.
   * @param id - Instance ID
   * @returns Deployment log ID and status message
   * @see POST /api/v1/stack-instances/:id/clean
   */
  clean: async (id: string): Promise<{ log_id: string; message: string }> => {
    try {
      const response = await api.post(`/api/v1/stack-instances/${id}/clean`);
      return response.data;
    } catch (error) {
      console.error('Failed to clean namespace:', error);
      throw error;
    }
  },
  /**
   * Fetch deployment logs for an instance.
   * @param id - Instance ID
   * @returns Array of deployment log entries
   * @see GET /api/v1/stack-instances/:id/deploy-log
   */
  getDeployLog: async (id: string): Promise<DeploymentLog[]> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/deploy-log`);
      const payload = response.data;
      return Array.isArray(payload) ? payload : payload.data ?? [];
    } catch (error) {
      console.error('Failed to fetch deployment logs:', error);
      throw error;
    }
  },
  /**
   * Get live Kubernetes namespace status for an instance.
   * @param id - Instance ID
   * @returns Namespace status with pod/service details
   * @see GET /api/v1/stack-instances/:id/status
   */
  getStatus: async (id: string): Promise<NamespaceStatus> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/status`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch instance status:', error);
      throw error;
    }
  },
  /**
   * Get detailed pod health status including container states and events.
   * @param id - Instance ID
   * @returns Namespace status with pod details
   * @see GET /api/v1/stack-instances/:id/pods
   */
  getPods: async (id: string): Promise<NamespaceStatus> => {
    try {
      const response = await api.get<NamespaceStatus>(`/api/v1/stack-instances/${id}/pods`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch pod status:', error);
      throw error;
    }
  },
  /**
   * Extend the TTL of a running instance.
   * @param id - Instance ID
   * @param ttlMinutes - New TTL in minutes (uses default if omitted)
   * @returns The updated instance with new expiration
   * @see POST /api/v1/stack-instances/:id/extend
   */
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
  /**
   * Fetch recently active stack instances.
   * @returns Array of recently updated instances
   * @see GET /api/v1/stack-instances/recent
   */
  recent: async (): Promise<StackInstance[]> => {
    try {
      const response = await api.get('/api/v1/stack-instances/recent');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch recent instances:', error);
      throw error;
    }
  },
  /**
   * Deploy multiple instances in bulk.
   * @param instanceIds - Array of instance IDs to deploy
   * @returns Bulk operation response with per-instance results
   * @see POST /api/v1/stack-instances/bulk/deploy
   */
  bulkDeploy: async (instanceIds: string[]): Promise<BulkOperationResponse> => {
    try {
      const response = await api.post('/api/v1/stack-instances/bulk/deploy', { instance_ids: instanceIds });
      return response.data;
    } catch (error) {
      console.error('Failed to bulk deploy instances:', error);
      throw error;
    }
  },
  /**
   * Stop multiple instances in bulk.
   * @param instanceIds - Array of instance IDs to stop
   * @returns Bulk operation response with per-instance results
   * @see POST /api/v1/stack-instances/bulk/stop
   */
  bulkStop: async (instanceIds: string[]): Promise<BulkOperationResponse> => {
    try {
      const response = await api.post('/api/v1/stack-instances/bulk/stop', { instance_ids: instanceIds });
      return response.data;
    } catch (error) {
      console.error('Failed to bulk stop instances:', error);
      throw error;
    }
  },
  /**
   * Clean namespaces for multiple instances in bulk.
   * @param instanceIds - Array of instance IDs to clean
   * @returns Bulk operation response with per-instance results
   * @see POST /api/v1/stack-instances/bulk/clean
   */
  bulkClean: async (instanceIds: string[]): Promise<BulkOperationResponse> => {
    try {
      const response = await api.post('/api/v1/stack-instances/bulk/clean', { instance_ids: instanceIds });
      return response.data;
    } catch (error) {
      console.error('Failed to bulk clean instances:', error);
      throw error;
    }
  },
  /**
   * Delete multiple instances in bulk.
   * @param instanceIds - Array of instance IDs to delete
   * @returns Bulk operation response with per-instance results
   * @see POST /api/v1/stack-instances/bulk/delete
   */
  bulkDelete: async (instanceIds: string[]): Promise<BulkOperationResponse> => {
    try {
      const response = await api.post('/api/v1/stack-instances/bulk/delete', { instance_ids: instanceIds });
      return response.data;
    } catch (error) {
      console.error('Failed to bulk delete instances:', error);
      throw error;
    }
  },
  /**
   * Compare two stack instances, returning per-chart value diffs.
   * @param leftId - ID of the left instance
   * @param rightId - ID of the right instance
   * @returns Comparison result with instance summaries and per-chart diffs
   * @see GET /api/v1/stack-instances/compare?left=ID&right=ID
   */
  compareInstances: async (leftId: string, rightId: string): Promise<CompareInstancesResponse> => {
    try {
      const response = await api.get('/api/v1/stack-instances/compare', {
        params: { left: leftId, right: rightId },
      });
      return response.data;
    } catch (error) {
      console.error('Failed to compare instances:', error);
      throw error;
    }
  },
  /**
   * Get quota override for a stack instance.
   * @param instanceId - Stack instance ID
   * @returns The quota override, or null if not set
   * @see GET /api/v1/stack-instances/:id/quota-overrides
   */
  getQuotaOverride: async (instanceId: string): Promise<InstanceQuotaOverride | null> => {
    try {
      const response = await api.get<InstanceQuotaOverride>(`/api/v1/stack-instances/${instanceId}/quota-overrides`);
      return response.data;
    } catch (error: unknown) {
      if (error && typeof error === 'object' && 'response' in error) {
        const axiosError = error as { response?: { status?: number } };
        if (axiosError.response?.status === 404) return null;
      }
      throw error;
    }
  },
  /**
   * Set or update quota override for a stack instance.
   * @param instanceId - Stack instance ID
   * @param override - Quota override values (empty strings fall back to cluster defaults)
   * @returns The saved quota override
   * @see PUT /api/v1/stack-instances/:id/quota-overrides
   */
  setQuotaOverride: async (instanceId: string, override: Omit<InstanceQuotaOverride, 'id' | 'stack_instance_id' | 'created_at' | 'updated_at'>): Promise<InstanceQuotaOverride> => {
    const response = await api.put<InstanceQuotaOverride>(`/api/v1/stack-instances/${instanceId}/quota-overrides`, override);
    return response.data;
  },
  /**
   * Remove quota override for a stack instance (reverts to cluster defaults).
   * @param instanceId - Stack instance ID
   * @see DELETE /api/v1/stack-instances/:id/quota-overrides
   */
  deleteQuotaOverride: async (instanceId: string): Promise<void> => {
    await api.delete(`/api/v1/stack-instances/${instanceId}/quota-overrides`);
  },
  /**
   * Preview deployment changes for an instance.
   * @param id - The instance ID
   * @returns Per-chart diff of previous vs pending values
   * @see GET /api/v1/stack-instances/:id/deploy-preview
   */
  deployPreview: async (id: number | string): Promise<DeployPreviewResponse> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${id}/deploy-preview`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch deploy preview:', error);
      throw error;
    }
  },
};

/** Git provider service for branch listing and validation. Maps to `/api/v1/git`. */
export const gitService = {
  /**
   * List branches for a git repository.
   * @param repoUrl - Repository URL (Azure DevOps or GitLab)
   * @returns Array of branch names
   * @see GET /api/v1/git/branches
   */
  branches: async (repoUrl: string): Promise<string[]> => {
    try {
      const response = await api.get('/api/v1/git/branches', { params: { repo: repoUrl } });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch branches:', error);
      throw error;
    }
  },
  /**
   * Check whether a branch exists in a repository.
   * @param repoUrl - Repository URL
   * @param branch - Branch name to validate
   * @returns `true` if the branch exists
   * @see GET /api/v1/git/validate-branch
   */
  validateBranch: async (repoUrl: string, branch: string): Promise<boolean> => {
    try {
      const response = await api.get('/api/v1/git/validate-branch', { params: { repo: repoUrl, branch } });
      return response.data.valid;
    } catch (error) {
      console.error('Failed to validate branch:', error);
      throw error;
    }
  },
  /**
   * List configured git providers.
   * @returns Array of provider names
   * @see GET /api/v1/git/providers
   */
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

/** Paginated response wrapper for audit log queries. */
export interface PaginatedAuditLogs {
  /** The audit log entries for the current page. */
  data: AuditLog[];
  /** Total number of matching entries across all pages, or -1 when the exact count is unknown. */
  total: number;
  /** Maximum entries per page. */
  limit: number;
  /** Number of entries skipped (for offset-based pagination). */
  offset: number;
}

/** Audit log service for viewing and exporting audit trail entries. Maps to `/api/v1/audit-logs`. */
export const auditService = {
  /**
   * Fetch a paginated, filterable list of audit log entries.
   * @param filters - Optional filters (user, entity type, action, date range, pagination)
   * @returns Paginated audit log response
   * @see GET /api/v1/audit-logs
   */
  list: async (filters?: AuditLogFilters): Promise<PaginatedAuditLogs> => {
    try {
      const response = await api.get('/api/v1/audit-logs', { params: filters });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch audit logs:', error);
      throw error;
    }
  },
  /**
   * Export audit logs as a downloadable file.
   * @param filters - Filters to apply before export
   * @param format - Export format (`'csv'` or `'json'`)
   * @see GET /api/v1/audit-logs/export
   */
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
      const match = /filename=([^;]+)/.exec(contentDisposition);
      if (match) {
        filename = match[1].trim().replaceAll(/(^")|("$)/g, '');
      }
    }

    const url = globalThis.URL.createObjectURL(new Blob([response.data]));
    const link = document.createElement('a');
    link.href = url;
    link.setAttribute('download', filename);
    document.body.appendChild(link);
    link.click();
    link.remove();
    globalThis.URL.revokeObjectURL(url);
  },
};

/** User management service (admin). Maps to `/api/v1/users`. */
export const userService = {
  /**
   * List all registered users.
   * @returns Array of users
   * @see GET /api/v1/users
   */
  list: async (): Promise<User[]> => {
    try {
      const response = await api.get('/api/v1/users');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch users:', error);
      throw error;
    }
  },
  /**
   * Create a new user (admin action via registration endpoint).
   * @param data - User credentials and role
   * @returns The created user
   * @see POST /api/v1/auth/register
   */
  create: async (data: CreateUserRequest): Promise<User> => {
    try {
      const response = await api.post('/api/v1/auth/register', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create user:', error);
      throw error;
    }
  },
  /**
   * Delete a user.
   * @param id - User ID
   * @see DELETE /api/v1/users/:id
   */
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/users/${id}`);
    } catch (error) {
      console.error('Failed to delete user:', error);
      throw error;
    }
  },
};

/** API key management service for per-user key CRUD. Maps to `/api/v1/users/:id/api-keys`. */
export const apiKeyService = {
  /**
   * List API keys for a user.
   * @param userId - User ID
   * @returns Array of API keys (secrets redacted)
   * @see GET /api/v1/users/:userId/api-keys
   */
  list: async (userId: string): Promise<APIKey[]> => {
    try {
      const response = await api.get(`/api/v1/users/${userId}/api-keys`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch API keys:', error);
      throw error;
    }
  },
  /**
   * Create a new API key for a user.
   * @param userId - User ID
   * @param data - Key name and optional expiration
   * @returns The created key including the plaintext secret (shown once)
   * @see POST /api/v1/users/:userId/api-keys
   */
  create: async (userId: string, data: CreateAPIKeyRequest): Promise<CreateAPIKeyResponse> => {
    try {
      const response = await api.post(`/api/v1/users/${userId}/api-keys`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to create API key:', error);
      throw error;
    }
  },
  /**
   * Revoke an API key.
   * @param userId - User ID
   * @param keyId - API key ID to revoke
   * @see DELETE /api/v1/users/:userId/api-keys/:keyId
   */
  delete: async (userId: string, keyId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/users/${userId}/api-keys/${keyId}`);
    } catch (error) {
      console.error('Failed to delete API key:', error);
      throw error;
    }
  },
};

/** Admin service for orphaned namespace detection and cleanup. Maps to `/api/v1/admin`. */
export const adminService = {
  /**
   * List Kubernetes namespaces not associated with any stack instance.
   * @returns Array of orphaned namespaces
   * @see GET /api/v1/admin/orphaned-namespaces
   */
  listOrphanedNamespaces: async (): Promise<OrphanedNamespace[]> => {
    try {
      const response = await api.get('/api/v1/admin/orphaned-namespaces');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch orphaned namespaces:', error);
      throw error;
    }
  },
  /**
   * Delete an orphaned namespace from the cluster.
   * @param namespace - Namespace name to delete
   * @see DELETE /api/v1/admin/orphaned-namespaces/:namespace
   */
  deleteOrphanedNamespace: async (namespace: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/admin/orphaned-namespaces/${namespace}`);
    } catch (error) {
      console.error('Failed to delete orphaned namespace:', error);
      throw error;
    }
  },
};

/** Cluster management service for multi-cluster registration and health. Maps to `/api/v1/clusters`. */
export const clusterService = {
  /**
   * List all registered clusters.
   * @returns Array of clusters
   * @see GET /api/v1/clusters
   */
  list: async (): Promise<Cluster[]> => {
    try {
      const response = await api.get('/api/v1/clusters');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch clusters:', error);
      throw error;
    }
  },
  /**
   * Fetch a single cluster.
   * @param id - Cluster ID
   * @returns The cluster
   * @see GET /api/v1/clusters/:id
   */
  get: async (id: string): Promise<Cluster> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster:', error);
      throw error;
    }
  },
  /**
   * Register a new cluster.
   * @param data - Cluster name, kubeconfig, and connection details
   * @returns The created cluster
   * @see POST /api/v1/clusters
   */
  create: async (data: CreateClusterRequest): Promise<Cluster> => {
    try {
      const response = await api.post('/api/v1/clusters', data);
      return response.data;
    } catch (error) {
      console.error('Failed to create cluster:', error);
      throw error;
    }
  },
  /**
   * Update a registered cluster.
   * @param id - Cluster ID
   * @param data - Fields to update
   * @returns The updated cluster
   * @see PUT /api/v1/clusters/:id
   */
  update: async (id: string, data: UpdateClusterRequest): Promise<Cluster> => {
    try {
      const response = await api.put(`/api/v1/clusters/${id}`, data);
      return response.data;
    } catch (error) {
      console.error('Failed to update cluster:', error);
      throw error;
    }
  },
  /**
   * Remove a registered cluster.
   * @param id - Cluster ID
   * @see DELETE /api/v1/clusters/:id
   */
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/clusters/${id}`);
    } catch (error) {
      console.error('Failed to delete cluster:', error);
      throw error;
    }
  },
  /**
   * Test connectivity to a cluster.
   * @param id - Cluster ID
   * @returns Connection test result with version info
   * @see POST /api/v1/clusters/:id/test
   */
  testConnection: async (id: string): Promise<ClusterTestResult> => {
    try {
      const response = await api.post(`/api/v1/clusters/${id}/test`);
      return response.data;
    } catch (error) {
      console.error('Failed to test cluster connection:', error);
      throw error;
    }
  },
  /**
   * Set a cluster as the default deployment target.
   * @param id - Cluster ID
   * @returns Confirmation message
   * @see POST /api/v1/clusters/:id/default
   */
  setDefault: async (id: string): Promise<{ message: string }> => {
    try {
      const response = await api.post(`/api/v1/clusters/${id}/default`);
      return response.data;
    } catch (error) {
      console.error('Failed to set default cluster:', error);
      throw error;
    }
  },
  /**
   * Fetch a health summary for a cluster.
   * @param id - Cluster ID
   * @returns Cluster health summary with resource utilization
   * @see GET /api/v1/clusters/:id/health/summary
   */
  getHealthSummary: async (id: string): Promise<ClusterSummary> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}/health/summary`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster health summary:', error);
      throw error;
    }
  },
  /**
   * List node status info for a cluster.
   * @param id - Cluster ID
   * @returns Array of node statuses
   * @see GET /api/v1/clusters/:id/health/nodes
   */
  getNodes: async (id: string): Promise<NodeStatusInfo[]> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}/health/nodes`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster nodes:', error);
      throw error;
    }
  },
  /**
   * List namespaces in a cluster.
   * @param id - Cluster ID
   * @returns Array of namespace info objects
   * @see GET /api/v1/clusters/:id/namespaces
   */
  getNamespaces: async (id: string): Promise<ClusterNamespaceInfo[]> => {
    try {
      const response = await api.get(`/api/v1/clusters/${id}/namespaces`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster namespaces:', error);
      throw error;
    }
  },
  /**
   * Fetch resource quota configuration for a cluster.
   * @param clusterId - Cluster ID
   * @returns The quota config, or null if none configured (404)
   * @see GET /api/v1/clusters/:id/quotas
   */
  getQuotas: async (clusterId: string): Promise<ResourceQuotaConfig | null> => {
    try {
      const response = await api.get(`/api/v1/clusters/${encodeURIComponent(clusterId)}/quotas`);
      return response.data;
    } catch (error) {
      if (axios.isAxiosError(error) && error.response?.status === 404) {
        return null;
      }
      console.error('Failed to fetch cluster quotas:', error);
      throw error;
    }
  },
  /**
   * Create or update resource quota configuration for a cluster.
   * @param clusterId - Cluster ID
   * @param config - Quota configuration fields
   * @returns The saved quota config
   * @see PUT /api/v1/clusters/:id/quotas
   */
  updateQuotas: async (clusterId: string, config: Partial<ResourceQuotaConfig>): Promise<ResourceQuotaConfig> => {
    try {
      const response = await api.put(`/api/v1/clusters/${encodeURIComponent(clusterId)}/quotas`, config);
      return response.data;
    } catch (error) {
      console.error('Failed to update cluster quotas:', error);
      throw error;
    }
  },
  /**
   * Remove resource quota configuration from a cluster.
   * @param clusterId - Cluster ID
   * @see DELETE /api/v1/clusters/:id/quotas
   */
  deleteQuotas: async (clusterId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/clusters/${encodeURIComponent(clusterId)}/quotas`);
    } catch (error) {
      console.error('Failed to delete cluster quotas:', error);
      throw error;
    }
  },
  /**
   * Fetch namespace-level resource utilization for a cluster.
   * @param clusterId - Cluster ID
   * @returns Utilization data with per-namespace CPU, memory, and pod usage
   * @see GET /api/v1/clusters/:id/utilization
   */
  getUtilization: async (clusterId: string): Promise<ClusterUtilization> => {
    try {
      const response = await api.get(`/api/v1/clusters/${encodeURIComponent(clusterId)}/utilization`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cluster utilization:', error);
      throw error;
    }
  },
};

/** User favorites (bookmarks) service. Maps to `/api/v1/favorites`. */
export const favoriteService = {
  /**
   * List the current user's favorites.
   * @returns Array of user favorites
   * @see GET /api/v1/favorites
   */
  list: async (): Promise<UserFavorite[]> => {
    try {
      const response = await api.get('/api/v1/favorites');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch favorites:', error);
      throw error;
    }
  },
  /**
   * Add an entity to the user's favorites.
   * @param entityType - Entity type (e.g., `'template'`, `'instance'`)
   * @param entityId - Entity ID
   * @returns The created favorite
   * @see POST /api/v1/favorites
   */
  add: async (entityType: string, entityId: string): Promise<UserFavorite> => {
    try {
      const response = await api.post('/api/v1/favorites', { entity_type: entityType, entity_id: entityId });
      return response.data;
    } catch (error) {
      console.error('Failed to add favorite:', error);
      throw error;
    }
  },
  /**
   * Remove an entity from the user's favorites.
   * @param entityType - Entity type
   * @param entityId - Entity ID
   * @see DELETE /api/v1/favorites/:entityType/:entityId
   */
  remove: async (entityType: string, entityId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/favorites/${entityType}/${entityId}`);
    } catch (error) {
      console.error('Failed to remove favorite:', error);
      throw error;
    }
  },
  /**
   * Check if an entity is in the user's favorites.
   * @param entityType - Entity type
   * @param entityId - Entity ID
   * @returns `true` if the entity is favorited
   * @see GET /api/v1/favorites/check
   */
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

/** Per-chart branch override service for stack instances. Maps to `/api/v1/stack-instances/:id/branches`. */
export const branchOverrideService = {
  /**
   * List branch overrides for an instance.
   * @param instanceId - Stack instance ID
   * @returns Array of per-chart branch overrides
   * @see GET /api/v1/stack-instances/:instanceId/branches
   */
  list: async (instanceId: string): Promise<ChartBranchOverride[]> => {
    try {
      const response = await api.get(`/api/v1/stack-instances/${instanceId}/branches`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch branch overrides:', error);
      throw error;
    }
  },
  /**
   * Set a branch override for a specific chart.
   * @param instanceId - Stack instance ID
   * @param chartId - Chart config ID
   * @param branch - Branch name to use for this chart
   * @returns The created or updated branch override
   * @see PUT /api/v1/stack-instances/:instanceId/branches/:chartId
   */
  set: async (instanceId: string, chartId: string, branch: string): Promise<ChartBranchOverride> => {
    try {
      const response = await api.put(`/api/v1/stack-instances/${instanceId}/branches/${chartId}`, { branch });
      return response.data;
    } catch (error) {
      console.error('Failed to set branch override:', error);
      throw error;
    }
  },
  /**
   * Remove a branch override, reverting to the default branch.
   * @param instanceId - Stack instance ID
   * @param chartId - Chart config ID
   * @see DELETE /api/v1/stack-instances/:instanceId/branches/:chartId
   */
  delete: async (instanceId: string, chartId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/stack-instances/${instanceId}/branches/${chartId}`);
    } catch (error) {
      console.error('Failed to delete branch override:', error);
      throw error;
    }
  },
};

/** Analytics service for usage statistics and deployment metrics. Maps to `/api/v1/analytics`. */
export const analyticsService = {
  /**
   * Fetch high-level platform usage statistics.
   * @returns Overview stats (instance counts, deployments, etc.)
   * @see GET /api/v1/analytics/overview
   */
  getOverview: async (): Promise<OverviewStats> => {
    try {
      const response = await api.get('/api/v1/analytics/overview');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch analytics overview:', error);
      throw error;
    }
  },
  /**
   * Fetch per-template usage statistics.
   * @returns Array of template usage stats
   * @see GET /api/v1/analytics/templates
   */
  getTemplateStats: async (): Promise<TemplateStats[]> => {
    try {
      const response = await api.get('/api/v1/analytics/templates');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch template stats:', error);
      throw error;
    }
  },
  /**
   * Fetch per-user activity statistics.
   * @returns Array of user activity stats
   * @see GET /api/v1/analytics/users
   */
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

/** Per-cluster shared Helm values service. Maps to `/api/v1/clusters/:id/shared-values`. */
export const sharedValuesService = {
  /**
   * List shared values for a cluster.
   * @param clusterId - Cluster ID
   * @returns Array of shared values entries
   * @see GET /api/v1/clusters/:clusterId/shared-values
   */
  list: async (clusterId: string): Promise<SharedValues[]> => {
    try {
      const response = await api.get(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values`);
      return response.data;
    } catch (error) {
      console.error('Failed to fetch shared values:', error);
      throw error;
    }
  },
  /**
   * Create a shared values entry for a cluster.
   * @param clusterId - Cluster ID
   * @param sv - Shared values fields (name, values YAML, priority)
   * @returns The created shared values entry
   * @see POST /api/v1/clusters/:clusterId/shared-values
   */
  create: async (clusterId: string, sv: Partial<SharedValues>): Promise<SharedValues> => {
    try {
      const response = await api.post(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values`, sv);
      return response.data;
    } catch (error) {
      console.error('Failed to create shared values:', error);
      throw error;
    }
  },
  /**
   * Update a shared values entry.
   * @param clusterId - Cluster ID
   * @param valueId - Shared values entry ID
   * @param sv - Fields to update
   * @returns The updated shared values entry
   * @see PUT /api/v1/clusters/:clusterId/shared-values/:valueId
   */
  update: async (clusterId: string, valueId: string, sv: Partial<SharedValues>): Promise<SharedValues> => {
    try {
      const response = await api.put(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values/${encodeURIComponent(valueId)}`, sv);
      return response.data;
    } catch (error) {
      console.error('Failed to update shared values:', error);
      throw error;
    }
  },
  /**
   * Delete a shared values entry.
   * @param clusterId - Cluster ID
   * @param valueId - Shared values entry ID
   * @see DELETE /api/v1/clusters/:clusterId/shared-values/:valueId
   */
  delete: async (clusterId: string, valueId: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/clusters/${encodeURIComponent(clusterId)}/shared-values/${encodeURIComponent(valueId)}`);
    } catch (error) {
      console.error('Failed to delete shared values:', error);
      throw error;
    }
  },
};

/** Cleanup policy service for cron-based instance lifecycle management. Maps to `/api/v1/admin/cleanup-policies`. */
export const cleanupPolicyService = {
  /**
   * List all cleanup policies.
   * @returns Array of cleanup policies
   * @see GET /api/v1/admin/cleanup-policies
   */
  list: async (): Promise<CleanupPolicy[]> => {
    try {
      const response = await api.get('/api/v1/admin/cleanup-policies');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch cleanup policies:', error);
      throw error;
    }
  },
  /**
   * Create a new cleanup policy.
   * @param policy - Policy fields (name, cron, action, condition)
   * @returns The created policy
   * @see POST /api/v1/admin/cleanup-policies
   */
  create: async (policy: Partial<CleanupPolicy>): Promise<CleanupPolicy> => {
    try {
      const response = await api.post('/api/v1/admin/cleanup-policies', policy);
      return response.data;
    } catch (error) {
      console.error('Failed to create cleanup policy:', error);
      throw error;
    }
  },
  /**
   * Update an existing cleanup policy.
   * @param id - Policy ID
   * @param policy - Fields to update
   * @returns The updated policy
   * @see PUT /api/v1/admin/cleanup-policies/:id
   */
  update: async (id: string, policy: Partial<CleanupPolicy>): Promise<CleanupPolicy> => {
    try {
      const response = await api.put(`/api/v1/admin/cleanup-policies/${encodeURIComponent(id)}`, policy);
      return response.data;
    } catch (error) {
      console.error('Failed to update cleanup policy:', error);
      throw error;
    }
  },
  /**
   * Delete a cleanup policy.
   * @param id - Policy ID
   * @see DELETE /api/v1/admin/cleanup-policies/:id
   */
  delete: async (id: string): Promise<void> => {
    try {
      await api.delete(`/api/v1/admin/cleanup-policies/${encodeURIComponent(id)}`);
    } catch (error) {
      console.error('Failed to delete cleanup policy:', error);
      throw error;
    }
  },
  /**
   * Manually execute a cleanup policy.
   * @param id - Policy ID
   * @param dryRun - If `true`, simulate without modifying instances
   * @returns Array of cleanup results (affected instances)
   * @see POST /api/v1/admin/cleanup-policies/:id/run
   */
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

/** Notification service for in-app notification management. Maps to `/api/v1/notifications`. */
export const notificationService = {
  /**
   * Fetch a paginated list of notifications for the current user.
   * @param unreadOnly - If `true`, only return unread notifications
   * @param limit - Maximum number of notifications to return
   * @param offset - Number of notifications to skip (for pagination)
   * @returns Paginated notification response with unread count
   * @see GET /api/v1/notifications
   */
  list: async (unreadOnly?: boolean, limit?: number, offset?: number): Promise<NotificationListResponse> => {
    try {
      const params: Record<string, string | number | boolean> = {};
      if (unreadOnly !== undefined) params.unread_only = unreadOnly;
      if (limit !== undefined) params.limit = limit;
      if (offset !== undefined) params.offset = offset;
      const response = await api.get('/api/v1/notifications', { params });
      return response.data;
    } catch (error) {
      console.error('Failed to fetch notifications:', error);
      throw error;
    }
  },
  /**
   * Fetch the count of unread notifications for the current user.
   * @returns Object containing the unread count
   * @see GET /api/v1/notifications/count
   */
  countUnread: async (): Promise<{ unread_count: number }> => {
    try {
      const response = await api.get('/api/v1/notifications/count');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch unread count:', error);
      throw error;
    }
  },
  /**
   * Mark a single notification as read.
   * @param id - Notification ID
   * @returns The updated notification
   * @see POST /api/v1/notifications/:id/read
   */
  markAsRead: async (id: string): Promise<Notification> => {
    try {
      const response = await api.post(`/api/v1/notifications/${id}/read`);
      return response.data;
    } catch (error) {
      console.error('Failed to mark notification as read:', error);
      throw error;
    }
  },
  /**
   * Mark all notifications as read for the current user.
   * @see POST /api/v1/notifications/read-all
   */
  markAllAsRead: async (): Promise<void> => {
    try {
      await api.post('/api/v1/notifications/read-all');
    } catch (error) {
      console.error('Failed to mark all notifications as read:', error);
      throw error;
    }
  },
  /**
   * Fetch notification preferences for the current user.
   * @returns Array of notification preference settings
   * @see GET /api/v1/notifications/preferences
   */
  getPreferences: async (): Promise<NotificationPreference[]> => {
    try {
      const response = await api.get('/api/v1/notifications/preferences');
      return response.data;
    } catch (error) {
      console.error('Failed to fetch notification preferences:', error);
      throw error;
    }
  },
  /**
   * Update notification preferences for the current user.
   * @param preferences - Array of event type / enabled pairs
   * @see PUT /api/v1/notifications/preferences
   */
  updatePreferences: async (preferences: { event_type: string; enabled: boolean }[]): Promise<void> => {
    try {
      await api.put('/api/v1/notifications/preferences', { preferences });
    } catch (error) {
      console.error('Failed to update notification preferences:', error);
      throw error;
    }
  },
};

export default api;
