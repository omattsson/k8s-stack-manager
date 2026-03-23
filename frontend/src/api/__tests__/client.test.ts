import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { AxiosResponse, InternalAxiosRequestConfig } from 'axios';

// vi.hoisted runs before vi.mock hoisting, so mockApi is available in the factory
const mockApi = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
  interceptors: {
    request: { use: vi.fn() },
    response: { use: vi.fn() },
  },
}));

// Mock axios before importing the module under test
vi.mock('axios', () => ({
  default: {
    create: vi.fn(() => mockApi),
  },
}));

// Helper to build a mock AxiosResponse
function mockResponse<T>(data: T): AxiosResponse<T> {
  return {
    data,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
  };
}

// Import the services — they bind to the mocked axios instance created above
import {
  authService,
  templateService,
  definitionService,
  instanceService,
  clusterService,
  gitService,
  auditService,
  userService,
  apiKeyService,
  adminService,
  favoriteService,
  branchOverrideService,
  analyticsService,
  sharedValuesService,
  cleanupPolicyService,
} from '../client';

beforeEach(() => {
  mockApi.get.mockReset();
  mockApi.post.mockReset();
  mockApi.put.mockReset();
  mockApi.delete.mockReset();
});

// ---------------------------------------------------------------------------
// authService
// ---------------------------------------------------------------------------
describe('authService', () => {
  it('login sends POST to /api/v1/auth/login and returns data', async () => {
    const api = mockApi;
    const payload = { username: 'admin', password: 'secret' };
    const responseData = { token: 'jwt-token', user: { id: '1', username: 'admin' } };
    api.post.mockResolvedValueOnce(mockResponse(responseData));

    const result = await authService.login(payload);

    expect(api.post).toHaveBeenCalledWith('/api/v1/auth/login', payload);
    expect(result).toEqual(responseData);
  });

  it('login throws on API error', async () => {
    const api = mockApi;
    api.post.mockRejectedValueOnce(new Error('Unauthorized'));

    await expect(authService.login({ username: 'bad', password: 'bad' }))
      .rejects.toThrow('Unauthorized');
  });

  it('register sends POST to /api/v1/auth/register and returns user', async () => {
    const api = mockApi;
    const payload = { username: 'newuser', password: 'pass123', display_name: 'New User' };
    const user = { id: '2', username: 'newuser', display_name: 'New User' };
    api.post.mockResolvedValueOnce(mockResponse(user));

    const result = await authService.register(payload);

    expect(api.post).toHaveBeenCalledWith('/api/v1/auth/register', payload);
    expect(result).toEqual(user);
  });

  it('register throws on API error', async () => {
    const api = mockApi;
    api.post.mockRejectedValueOnce(new Error('Conflict'));

    await expect(authService.register({ username: 'dup', password: 'x', display_name: 'Dup' }))
      .rejects.toThrow('Conflict');
  });

  it('me sends GET to /api/v1/auth/me and returns user', async () => {
    const api = mockApi;
    const user = { id: '1', username: 'admin', role: 'admin' };
    api.get.mockResolvedValueOnce(mockResponse(user));

    const result = await authService.me();

    expect(api.get).toHaveBeenCalledWith('/api/v1/auth/me');
    expect(result).toEqual(user);
  });

  it('me throws on API error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Unauthorized'));

    await expect(authService.me()).rejects.toThrow('Unauthorized');
  });
});

// ---------------------------------------------------------------------------
// templateService
// ---------------------------------------------------------------------------
describe('templateService', () => {
  it('list sends GET to /api/v1/templates', async () => {
    const api = mockApi;
    const templates = [{ id: '1', name: 'tmpl-1' }];
    api.get.mockResolvedValueOnce(mockResponse(templates));

    const result = await templateService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/templates');
    expect(result).toEqual(templates);
  });

  it('list throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Network Error'));

    await expect(templateService.list()).rejects.toThrow('Network Error');
  });

  it('get merges template with charts from response', async () => {
    const api = mockApi;
    const template = { id: '1', name: 'tmpl-1' };
    const charts = [{ id: 'c1', chart_name: 'nginx' }];
    api.get.mockResolvedValueOnce(mockResponse({ template, charts }));

    const result = await templateService.get('1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/templates/1');
    expect(result).toEqual({ ...template, charts });
  });

  it('get defaults charts to empty array when null', async () => {
    const api = mockApi;
    api.get.mockResolvedValueOnce(mockResponse({ template: { id: '1' }, charts: null }));

    const result = await templateService.get('1');

    expect(result.charts).toEqual([]);
  });

  it('create sends POST with template data', async () => {
    const api = mockApi;
    const data = { name: 'new-tmpl' };
    const created = { id: '2', name: 'new-tmpl' };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await templateService.create(data);

    expect(api.post).toHaveBeenCalledWith('/api/v1/templates', data);
    expect(result).toEqual(created);
  });

  it('update sends PUT with template data', async () => {
    const api = mockApi;
    const data = { name: 'updated-tmpl' };
    const updated = { id: '1', name: 'updated-tmpl' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await templateService.update('1', data);

    expect(api.put).toHaveBeenCalledWith('/api/v1/templates/1', data);
    expect(result).toEqual(updated);
  });

  it('delete sends DELETE to correct URL', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await templateService.delete('1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/templates/1');
  });

  it('publish sends POST to publish endpoint', async () => {
    const api = mockApi;
    const published = { id: '1', is_published: true };
    api.post.mockResolvedValueOnce(mockResponse(published));

    const result = await templateService.publish('1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/templates/1/publish');
    expect(result).toEqual(published);
  });

  it('unpublish sends POST to unpublish endpoint', async () => {
    const api = mockApi;
    const unpublished = { id: '1', is_published: false };
    api.post.mockResolvedValueOnce(mockResponse(unpublished));

    const result = await templateService.unpublish('1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/templates/1/unpublish');
    expect(result).toEqual(unpublished);
  });

  it('instantiate sends POST and returns definition from response', async () => {
    const api = mockApi;
    const definition = { id: 'def-1', name: 'instantiated' };
    api.post.mockResolvedValueOnce(mockResponse({ definition, charts: [] }));

    const data = { name: 'my-instance', owner: 'user1' };
    const result = await templateService.instantiate('1', data as never);

    expect(api.post).toHaveBeenCalledWith('/api/v1/templates/1/instantiate', data);
    expect(result).toEqual(definition);
  });

  it('clone sends POST to clone endpoint', async () => {
    const api = mockApi;
    const cloned = { id: '3', name: 'tmpl-1-copy' };
    api.post.mockResolvedValueOnce(mockResponse(cloned));

    const result = await templateService.clone('1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/templates/1/clone');
    expect(result).toEqual(cloned);
  });

  it('addChart sends POST to charts sub-resource', async () => {
    const api = mockApi;
    const chartData = { chart_name: 'nginx', repo_url: 'https://charts.example.com' };
    const created = { id: 'tc1', ...chartData };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await templateService.addChart('1', chartData as never);

    expect(api.post).toHaveBeenCalledWith('/api/v1/templates/1/charts', chartData);
    expect(result).toEqual(created);
  });

  it('updateChart sends PUT to chart sub-resource', async () => {
    const api = mockApi;
    const data = { chart_name: 'updated-nginx' };
    const updated = { id: 'tc1', chart_name: 'updated-nginx' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await templateService.updateChart('1', 'tc1', data as never);

    expect(api.put).toHaveBeenCalledWith('/api/v1/templates/1/charts/tc1', data);
    expect(result).toEqual(updated);
  });

  it('deleteChart sends DELETE to chart sub-resource', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await templateService.deleteChart('1', 'tc1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/templates/1/charts/tc1');
  });

  it('quickDeploy sends POST with deploy options', async () => {
    const api = mockApi;
    const data = { cluster_id: 'c1', ttl_minutes: 60 };
    const result_ = { instance: { id: 'i1' }, deployment_log_id: 'dl1' };
    api.post.mockResolvedValueOnce(mockResponse(result_));

    const result = await templateService.quickDeploy('1', data as never);

    expect(api.post).toHaveBeenCalledWith('/api/v1/templates/1/quick-deploy', data);
    expect(result).toEqual(result_);
  });
});

// ---------------------------------------------------------------------------
// definitionService
// ---------------------------------------------------------------------------
describe('definitionService', () => {
  it('list sends GET to /api/v1/stack-definitions', async () => {
    const api = mockApi;
    const defs = [{ id: '1', name: 'def-1' }];
    api.get.mockResolvedValueOnce(mockResponse(defs));

    const result = await definitionService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-definitions');
    expect(result).toEqual(defs);
  });

  it('list throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Server Error'));

    await expect(definitionService.list()).rejects.toThrow('Server Error');
  });

  it('get merges definition with charts', async () => {
    const api = mockApi;
    const definition = { id: '1', name: 'def-1' };
    const charts = [{ id: 'c1' }];
    api.get.mockResolvedValueOnce(mockResponse({ definition, charts }));

    const result = await definitionService.get('1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-definitions/1');
    expect(result).toEqual({ ...definition, charts });
  });

  it('get defaults charts to empty array when null', async () => {
    const api = mockApi;
    api.get.mockResolvedValueOnce(mockResponse({ definition: { id: '1' }, charts: null }));

    const result = await definitionService.get('1');

    expect(result.charts).toEqual([]);
  });

  it('create sends POST with definition data', async () => {
    const api = mockApi;
    const data = { name: 'new-def' };
    const created = { id: '2', name: 'new-def' };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await definitionService.create(data);

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-definitions', data);
    expect(result).toEqual(created);
  });

  it('update sends PUT with definition data', async () => {
    const api = mockApi;
    const data = { name: 'updated-def' };
    const updated = { id: '1', name: 'updated-def' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await definitionService.update('1', data);

    expect(api.put).toHaveBeenCalledWith('/api/v1/stack-definitions/1', data);
    expect(result).toEqual(updated);
  });

  it('delete sends DELETE to correct URL', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await definitionService.delete('1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/stack-definitions/1');
  });

  it('addChart sends POST to charts sub-resource', async () => {
    const api = mockApi;
    const data = { chart_name: 'redis' };
    const created = { id: 'cc1', chart_name: 'redis' };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await definitionService.addChart('1', data as never);

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-definitions/1/charts', data);
    expect(result).toEqual(created);
  });

  it('updateChart sends PUT to chart sub-resource', async () => {
    const api = mockApi;
    const data = { chart_name: 'redis-updated' };
    const updated = { id: 'cc1', chart_name: 'redis-updated' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await definitionService.updateChart('1', 'cc1', data as never);

    expect(api.put).toHaveBeenCalledWith('/api/v1/stack-definitions/1/charts/cc1', data);
    expect(result).toEqual(updated);
  });

  it('deleteChart sends DELETE to chart sub-resource', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await definitionService.deleteChart('1', 'cc1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/stack-definitions/1/charts/cc1');
  });
});

// ---------------------------------------------------------------------------
// instanceService
// ---------------------------------------------------------------------------
describe('instanceService', () => {
  it('list sends GET to /api/v1/stack-instances', async () => {
    const api = mockApi;
    const instances = [{ id: 'i1', name: 'inst-1' }];
    api.get.mockResolvedValueOnce(mockResponse(instances));

    const result = await instanceService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances');
    expect(result).toEqual(instances);
  });

  it('list throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Fetch failed'));

    await expect(instanceService.list()).rejects.toThrow('Fetch failed');
  });

  it('get sends GET with instance ID', async () => {
    const api = mockApi;
    const instance = { id: 'i1', name: 'inst-1' };
    api.get.mockResolvedValueOnce(mockResponse(instance));

    const result = await instanceService.get('i1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances/i1');
    expect(result).toEqual(instance);
  });

  it('create sends POST with instance data', async () => {
    const api = mockApi;
    const data = { name: 'new-inst' };
    const created = { id: 'i2', name: 'new-inst' };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await instanceService.create(data);

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-instances', data);
    expect(result).toEqual(created);
  });

  it('update sends PUT with instance data', async () => {
    const api = mockApi;
    const data = { name: 'updated-inst' };
    const updated = { id: 'i1', name: 'updated-inst' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await instanceService.update('i1', data);

    expect(api.put).toHaveBeenCalledWith('/api/v1/stack-instances/i1', data);
    expect(result).toEqual(updated);
  });

  it('delete sends DELETE to correct URL', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await instanceService.delete('i1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/stack-instances/i1');
  });

  it('clone sends POST to clone endpoint', async () => {
    const api = mockApi;
    const cloned = { id: 'i3', name: 'inst-1-copy' };
    api.post.mockResolvedValueOnce(mockResponse(cloned));

    const result = await instanceService.clone('i1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-instances/i1/clone');
    expect(result).toEqual(cloned);
  });

  it('getOverrides sends GET to overrides sub-resource', async () => {
    const api = mockApi;
    const overrides = [{ id: 'o1', chart_config_id: 'c1', values: 'key: val' }];
    api.get.mockResolvedValueOnce(mockResponse(overrides));

    const result = await instanceService.getOverrides('i1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances/i1/overrides');
    expect(result).toEqual(overrides);
  });

  it('setOverride sends PUT to specific chart override', async () => {
    const api = mockApi;
    const data = { values: 'replicas: 3' };
    const override = { id: 'o1', values: 'replicas: 3' };
    api.put.mockResolvedValueOnce(mockResponse(override));

    const result = await instanceService.setOverride('i1', 'c1', data);

    expect(api.put).toHaveBeenCalledWith('/api/v1/stack-instances/i1/overrides/c1', data);
    expect(result).toEqual(override);
  });

  it('exportValues sends GET to export endpoint', async () => {
    const api = mockApi;
    const yaml = 'key: value\nreplicas: 2';
    api.get.mockResolvedValueOnce(mockResponse(yaml));

    const result = await instanceService.exportValues('i1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances/i1/export');
    expect(result).toBe(yaml);
  });

  it('deploy sends POST to deploy endpoint', async () => {
    const api = mockApi;
    const deployResult = { log_id: 'log-1', message: 'Deployment started' };
    api.post.mockResolvedValueOnce(mockResponse(deployResult));

    const result = await instanceService.deploy('i1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-instances/i1/deploy');
    expect(result).toEqual(deployResult);
  });

  it('stop sends POST to stop endpoint', async () => {
    const api = mockApi;
    const stopResult = { log_id: 'log-2', message: 'Stop started' };
    api.post.mockResolvedValueOnce(mockResponse(stopResult));

    const result = await instanceService.stop('i1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-instances/i1/stop');
    expect(result).toEqual(stopResult);
  });

  it('clean sends POST to clean endpoint', async () => {
    const api = mockApi;
    const cleanResult = { log_id: 'log-3', message: 'Cleanup started' };
    api.post.mockResolvedValueOnce(mockResponse(cleanResult));

    const result = await instanceService.clean('i1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-instances/i1/clean');
    expect(result).toEqual(cleanResult);
  });

  it('getDeployLog sends GET to deploy-log endpoint', async () => {
    const api = mockApi;
    const logs = [{ id: 'dl1', action: 'deploy', status: 'success' }];
    api.get.mockResolvedValueOnce(mockResponse(logs));

    const result = await instanceService.getDeployLog('i1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances/i1/deploy-log');
    expect(result).toEqual(logs);
  });

  it('getStatus sends GET to status endpoint', async () => {
    const api = mockApi;
    const status = { namespace: 'stack-test', pods: [] };
    api.get.mockResolvedValueOnce(mockResponse(status));

    const result = await instanceService.getStatus('i1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances/i1/status');
    expect(result).toEqual(status);
  });

  it('extend sends POST with ttl_minutes when provided', async () => {
    const api = mockApi;
    const extended = { id: 'i1', ttl_minutes: 120 };
    api.post.mockResolvedValueOnce(mockResponse(extended));

    const result = await instanceService.extend('i1', 120);

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-instances/i1/extend', { ttl_minutes: 120 });
    expect(result).toEqual(extended);
  });

  it('extend sends POST with empty body when ttl not provided', async () => {
    const api = mockApi;
    const extended = { id: 'i1' };
    api.post.mockResolvedValueOnce(mockResponse(extended));

    await instanceService.extend('i1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/stack-instances/i1/extend', {});
  });

  it('recent sends GET to /api/v1/stack-instances/recent', async () => {
    const api = mockApi;
    const recent = [{ id: 'i1' }];
    api.get.mockResolvedValueOnce(mockResponse(recent));

    const result = await instanceService.recent();

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances/recent');
    expect(result).toEqual(recent);
  });
});

// ---------------------------------------------------------------------------
// clusterService
// ---------------------------------------------------------------------------
describe('clusterService', () => {
  it('list sends GET to /api/v1/clusters', async () => {
    const api = mockApi;
    const clusters = [{ id: 'cl1', name: 'prod' }];
    api.get.mockResolvedValueOnce(mockResponse(clusters));

    const result = await clusterService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/clusters');
    expect(result).toEqual(clusters);
  });

  it('list throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Server Error'));

    await expect(clusterService.list()).rejects.toThrow('Server Error');
  });

  it('get sends GET with cluster ID', async () => {
    const api = mockApi;
    const cluster = { id: 'cl1', name: 'prod' };
    api.get.mockResolvedValueOnce(mockResponse(cluster));

    const result = await clusterService.get('cl1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/clusters/cl1');
    expect(result).toEqual(cluster);
  });

  it('create sends POST with cluster data', async () => {
    const api = mockApi;
    const data = { name: 'staging', kubeconfig_data: 'base64...' };
    const created = { id: 'cl2', name: 'staging' };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await clusterService.create(data as never);

    expect(api.post).toHaveBeenCalledWith('/api/v1/clusters', data);
    expect(result).toEqual(created);
  });

  it('update sends PUT with cluster data', async () => {
    const api = mockApi;
    const data = { name: 'prod-updated' };
    const updated = { id: 'cl1', name: 'prod-updated' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await clusterService.update('cl1', data as never);

    expect(api.put).toHaveBeenCalledWith('/api/v1/clusters/cl1', data);
    expect(result).toEqual(updated);
  });

  it('delete sends DELETE to correct URL', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await clusterService.delete('cl1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/clusters/cl1');
  });

  it('testConnection sends POST to test endpoint', async () => {
    const api = mockApi;
    const testResult = { success: true, version: 'v1.28.0' };
    api.post.mockResolvedValueOnce(mockResponse(testResult));

    const result = await clusterService.testConnection('cl1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/clusters/cl1/test');
    expect(result).toEqual(testResult);
  });

  it('setDefault sends POST to default endpoint', async () => {
    const api = mockApi;
    const msg = { message: 'Default cluster set' };
    api.post.mockResolvedValueOnce(mockResponse(msg));

    const result = await clusterService.setDefault('cl1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/clusters/cl1/default');
    expect(result).toEqual(msg);
  });

  it('getHealthSummary sends GET to health summary endpoint', async () => {
    const api = mockApi;
    const summary = { status: 'healthy', node_count: 3 };
    api.get.mockResolvedValueOnce(mockResponse(summary));

    const result = await clusterService.getHealthSummary('cl1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/clusters/cl1/health/summary');
    expect(result).toEqual(summary);
  });

  it('getNodes sends GET to health nodes endpoint', async () => {
    const api = mockApi;
    const nodes = [{ name: 'node-1', status: 'Ready' }];
    api.get.mockResolvedValueOnce(mockResponse(nodes));

    const result = await clusterService.getNodes('cl1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/clusters/cl1/health/nodes');
    expect(result).toEqual(nodes);
  });

  it('getNamespaces sends GET to namespaces endpoint', async () => {
    const api = mockApi;
    const namespaces = [{ name: 'default', pod_count: 5 }];
    api.get.mockResolvedValueOnce(mockResponse(namespaces));

    const result = await clusterService.getNamespaces('cl1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/clusters/cl1/namespaces');
    expect(result).toEqual(namespaces);
  });
});

// ---------------------------------------------------------------------------
// gitService
// ---------------------------------------------------------------------------
describe('gitService', () => {
  it('branches sends GET with repo param', async () => {
    const api = mockApi;
    const branches = ['main', 'develop', 'feature/x'];
    api.get.mockResolvedValueOnce(mockResponse(branches));

    const result = await gitService.branches('https://dev.azure.com/org/proj/_git/repo');

    expect(api.get).toHaveBeenCalledWith('/api/v1/git/branches', {
      params: { repo: 'https://dev.azure.com/org/proj/_git/repo' },
    });
    expect(result).toEqual(branches);
  });

  it('branches throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Not Found'));

    await expect(gitService.branches('bad-url')).rejects.toThrow('Not Found');
  });

  it('validateBranch sends GET with repo and branch params', async () => {
    const api = mockApi;
    api.get.mockResolvedValueOnce(mockResponse({ valid: true }));

    const result = await gitService.validateBranch('https://repo.example.com', 'main');

    expect(api.get).toHaveBeenCalledWith('/api/v1/git/validate-branch', {
      params: { repo: 'https://repo.example.com', branch: 'main' },
    });
    expect(result).toBe(true);
  });

  it('validateBranch returns false when branch does not exist', async () => {
    const api = mockApi;
    api.get.mockResolvedValueOnce(mockResponse({ valid: false }));

    const result = await gitService.validateBranch('https://repo.example.com', 'nonexistent');

    expect(result).toBe(false);
  });

  it('providers sends GET to /api/v1/git/providers', async () => {
    const api = mockApi;
    const providers = ['azure-devops', 'gitlab'];
    api.get.mockResolvedValueOnce(mockResponse(providers));

    const result = await gitService.providers();

    expect(api.get).toHaveBeenCalledWith('/api/v1/git/providers');
    expect(result).toEqual(providers);
  });
});

// ---------------------------------------------------------------------------
// auditService
// ---------------------------------------------------------------------------
describe('auditService', () => {
  it('list sends GET with filter params', async () => {
    const api = mockApi;
    const filters = { user_id: '1', entity_type: 'instance', limit: 10, offset: 0 };
    const paginatedResult = { data: [{ id: 'a1' }], total: 1, limit: 10, offset: 0 };
    api.get.mockResolvedValueOnce(mockResponse(paginatedResult));

    const result = await auditService.list(filters);

    expect(api.get).toHaveBeenCalledWith('/api/v1/audit-logs', { params: filters });
    expect(result).toEqual(paginatedResult);
  });

  it('list sends GET without params when no filters', async () => {
    const api = mockApi;
    const paginatedResult = { data: [], total: 0, limit: 20, offset: 0 };
    api.get.mockResolvedValueOnce(mockResponse(paginatedResult));

    const result = await auditService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/audit-logs', { params: undefined });
    expect(result).toEqual(paginatedResult);
  });

  it('list throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Forbidden'));

    await expect(auditService.list()).rejects.toThrow('Forbidden');
  });
});

// ---------------------------------------------------------------------------
// userService
// ---------------------------------------------------------------------------
describe('userService', () => {
  it('list sends GET to /api/v1/users', async () => {
    const api = mockApi;
    const users = [{ id: '1', username: 'admin' }];
    api.get.mockResolvedValueOnce(mockResponse(users));

    const result = await userService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/users');
    expect(result).toEqual(users);
  });

  it('create sends POST to /api/v1/auth/register', async () => {
    const api = mockApi;
    const data = { username: 'newuser', password: 'pass', role: 'developer' };
    const user = { id: '2', username: 'newuser' };
    api.post.mockResolvedValueOnce(mockResponse(user));

    const result = await userService.create(data as never);

    expect(api.post).toHaveBeenCalledWith('/api/v1/auth/register', data);
    expect(result).toEqual(user);
  });

  it('delete sends DELETE to /api/v1/users/:id', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await userService.delete('2');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/users/2');
  });
});

// ---------------------------------------------------------------------------
// apiKeyService
// ---------------------------------------------------------------------------
describe('apiKeyService', () => {
  it('list sends GET to /api/v1/users/:userId/api-keys', async () => {
    const api = mockApi;
    const keys = [{ id: 'k1', name: 'ci-key' }];
    api.get.mockResolvedValueOnce(mockResponse(keys));

    const result = await apiKeyService.list('u1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/users/u1/api-keys');
    expect(result).toEqual(keys);
  });

  it('create sends POST to user api-keys endpoint', async () => {
    const api = mockApi;
    const data = { name: 'new-key' };
    const created = { id: 'k2', name: 'new-key', key: 'secret-key-value' };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await apiKeyService.create('u1', data as never);

    expect(api.post).toHaveBeenCalledWith('/api/v1/users/u1/api-keys', data);
    expect(result).toEqual(created);
  });

  it('delete sends DELETE to specific key endpoint', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await apiKeyService.delete('u1', 'k1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/users/u1/api-keys/k1');
  });
});

// ---------------------------------------------------------------------------
// adminService
// ---------------------------------------------------------------------------
describe('adminService', () => {
  it('listOrphanedNamespaces sends GET to admin endpoint', async () => {
    const api = mockApi;
    const orphans = [{ namespace: 'stack-old-user1', cluster_id: 'cl1' }];
    api.get.mockResolvedValueOnce(mockResponse(orphans));

    const result = await adminService.listOrphanedNamespaces();

    expect(api.get).toHaveBeenCalledWith('/api/v1/admin/orphaned-namespaces');
    expect(result).toEqual(orphans);
  });

  it('deleteOrphanedNamespace sends DELETE with namespace', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await adminService.deleteOrphanedNamespace('stack-old-user1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/admin/orphaned-namespaces/stack-old-user1');
  });

  it('listOrphanedNamespaces throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Forbidden'));

    await expect(adminService.listOrphanedNamespaces()).rejects.toThrow('Forbidden');
  });
});

// ---------------------------------------------------------------------------
// favoriteService
// ---------------------------------------------------------------------------
describe('favoriteService', () => {
  it('list sends GET to /api/v1/favorites', async () => {
    const api = mockApi;
    const favorites = [{ id: 'f1', entity_type: 'template', entity_id: 't1' }];
    api.get.mockResolvedValueOnce(mockResponse(favorites));

    const result = await favoriteService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/favorites');
    expect(result).toEqual(favorites);
  });

  it('add sends POST with entity type and ID', async () => {
    const api = mockApi;
    const favorite = { id: 'f2', entity_type: 'instance', entity_id: 'i1' };
    api.post.mockResolvedValueOnce(mockResponse(favorite));

    const result = await favoriteService.add('instance', 'i1');

    expect(api.post).toHaveBeenCalledWith('/api/v1/favorites', {
      entity_type: 'instance',
      entity_id: 'i1',
    });
    expect(result).toEqual(favorite);
  });

  it('remove sends DELETE to entity-specific URL', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await favoriteService.remove('template', 't1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/favorites/template/t1');
  });

  it('check sends GET with params and returns boolean', async () => {
    const api = mockApi;
    api.get.mockResolvedValueOnce(mockResponse({ is_favorite: true }));

    const result = await favoriteService.check('template', 't1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/favorites/check', {
      params: { entity_type: 'template', entity_id: 't1' },
    });
    expect(result).toBe(true);
  });

  it('check returns false when not favorited', async () => {
    const api = mockApi;
    api.get.mockResolvedValueOnce(mockResponse({ is_favorite: false }));

    const result = await favoriteService.check('instance', 'i99');

    expect(result).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// branchOverrideService
// ---------------------------------------------------------------------------
describe('branchOverrideService', () => {
  it('list sends GET to branches sub-resource', async () => {
    const api = mockApi;
    const overrides = [{ id: 'bo1', chart_config_id: 'c1', branch: 'feature/x' }];
    api.get.mockResolvedValueOnce(mockResponse(overrides));

    const result = await branchOverrideService.list('i1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/stack-instances/i1/branches');
    expect(result).toEqual(overrides);
  });

  it('set sends PUT with branch name', async () => {
    const api = mockApi;
    const override = { id: 'bo1', branch: 'develop' };
    api.put.mockResolvedValueOnce(mockResponse(override));

    const result = await branchOverrideService.set('i1', 'c1', 'develop');

    expect(api.put).toHaveBeenCalledWith('/api/v1/stack-instances/i1/branches/c1', { branch: 'develop' });
    expect(result).toEqual(override);
  });

  it('delete sends DELETE to specific chart branch override', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await branchOverrideService.delete('i1', 'c1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/stack-instances/i1/branches/c1');
  });
});

// ---------------------------------------------------------------------------
// analyticsService
// ---------------------------------------------------------------------------
describe('analyticsService', () => {
  it('getOverview sends GET to analytics overview', async () => {
    const api = mockApi;
    const overview = { total_instances: 42, active_deployments: 10 };
    api.get.mockResolvedValueOnce(mockResponse(overview));

    const result = await analyticsService.getOverview();

    expect(api.get).toHaveBeenCalledWith('/api/v1/analytics/overview');
    expect(result).toEqual(overview);
  });

  it('getTemplateStats sends GET to analytics templates', async () => {
    const api = mockApi;
    const stats = [{ template_id: 't1', instance_count: 5 }];
    api.get.mockResolvedValueOnce(mockResponse(stats));

    const result = await analyticsService.getTemplateStats();

    expect(api.get).toHaveBeenCalledWith('/api/v1/analytics/templates');
    expect(result).toEqual(stats);
  });

  it('getUserStats sends GET to analytics users', async () => {
    const api = mockApi;
    const stats = [{ user_id: 'u1', deployment_count: 12 }];
    api.get.mockResolvedValueOnce(mockResponse(stats));

    const result = await analyticsService.getUserStats();

    expect(api.get).toHaveBeenCalledWith('/api/v1/analytics/users');
    expect(result).toEqual(stats);
  });

  it('getOverview throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Internal Server Error'));

    await expect(analyticsService.getOverview()).rejects.toThrow('Internal Server Error');
  });
});

// ---------------------------------------------------------------------------
// sharedValuesService
// ---------------------------------------------------------------------------
describe('sharedValuesService', () => {
  it('list sends GET with encoded cluster ID', async () => {
    const api = mockApi;
    const values = [{ id: 'sv1', name: 'global', values: 'env: prod' }];
    api.get.mockResolvedValueOnce(mockResponse(values));

    const result = await sharedValuesService.list('cl1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/clusters/cl1/shared-values');
    expect(result).toEqual(values);
  });

  it('create sends POST with shared values data', async () => {
    const api = mockApi;
    const data = { name: 'defaults', values: 'replicas: 2', priority: 10 };
    const created = { id: 'sv2', ...data };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await sharedValuesService.create('cl1', data);

    expect(api.post).toHaveBeenCalledWith('/api/v1/clusters/cl1/shared-values', data);
    expect(result).toEqual(created);
  });

  it('update sends PUT with shared values data', async () => {
    const api = mockApi;
    const data = { values: 'replicas: 3' };
    const updated = { id: 'sv1', values: 'replicas: 3' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await sharedValuesService.update('cl1', 'sv1', data);

    expect(api.put).toHaveBeenCalledWith('/api/v1/clusters/cl1/shared-values/sv1', data);
    expect(result).toEqual(updated);
  });

  it('delete sends DELETE with encoded IDs', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await sharedValuesService.delete('cl1', 'sv1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/clusters/cl1/shared-values/sv1');
  });
});

// ---------------------------------------------------------------------------
// cleanupPolicyService
// ---------------------------------------------------------------------------
describe('cleanupPolicyService', () => {
  it('list sends GET to /api/v1/admin/cleanup-policies', async () => {
    const api = mockApi;
    const policies = [{ id: 'cp1', name: 'nightly-cleanup' }];
    api.get.mockResolvedValueOnce(mockResponse(policies));

    const result = await cleanupPolicyService.list();

    expect(api.get).toHaveBeenCalledWith('/api/v1/admin/cleanup-policies');
    expect(result).toEqual(policies);
  });

  it('create sends POST with policy data', async () => {
    const api = mockApi;
    const data = { name: 'weekly', cron: '0 0 * * 0', action: 'stop' };
    const created = { id: 'cp2', ...data };
    api.post.mockResolvedValueOnce(mockResponse(created));

    const result = await cleanupPolicyService.create(data);

    expect(api.post).toHaveBeenCalledWith('/api/v1/admin/cleanup-policies', data);
    expect(result).toEqual(created);
  });

  it('update sends PUT with encoded policy ID', async () => {
    const api = mockApi;
    const data = { name: 'weekly-updated' };
    const updated = { id: 'cp1', name: 'weekly-updated' };
    api.put.mockResolvedValueOnce(mockResponse(updated));

    const result = await cleanupPolicyService.update('cp1', data);

    expect(api.put).toHaveBeenCalledWith('/api/v1/admin/cleanup-policies/cp1', data);
    expect(result).toEqual(updated);
  });

  it('delete sends DELETE with encoded policy ID', async () => {
    const api = mockApi;
    api.delete.mockResolvedValueOnce(mockResponse(undefined));

    await cleanupPolicyService.delete('cp1');

    expect(api.delete).toHaveBeenCalledWith('/api/v1/admin/cleanup-policies/cp1');
  });

  it('run sends POST with dry_run query param', async () => {
    const api = mockApi;
    const results = [{ instance_id: 'i1', action: 'stop', success: true }];
    api.post.mockResolvedValueOnce(mockResponse(results));

    const result = await cleanupPolicyService.run('cp1', true);

    expect(api.post).toHaveBeenCalledWith('/api/v1/admin/cleanup-policies/cp1/run?dry_run=true');
    expect(result).toEqual(results);
  });

  it('run sends POST with dry_run=false', async () => {
    const api = mockApi;
    const results = [{ instance_id: 'i1', action: 'stop', success: true }];
    api.post.mockResolvedValueOnce(mockResponse(results));

    await cleanupPolicyService.run('cp1', false);

    expect(api.post).toHaveBeenCalledWith('/api/v1/admin/cleanup-policies/cp1/run?dry_run=false');
  });

  it('list throws on error', async () => {
    const api = mockApi;
    api.get.mockRejectedValueOnce(new Error('Forbidden'));

    await expect(cleanupPolicyService.list()).rejects.toThrow('Forbidden');
  });
});
