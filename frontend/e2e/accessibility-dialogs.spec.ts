import { test, expect, type APIRequestContext, type Locator, type Page } from '@playwright/test';
import { API_BASE, ensureDefaultCluster } from './helpers';
import {
  MODES,
  checkPage,
  createSeed,
  deleteSeed,
  forceInstanceFields,
  hoverAndCheck,
  uniqueName,
  type PageSetup,
  type Seed,
} from './a11y';
import { mockDeployLog, type MockDeployLog } from './deployLogMock';

// Every dialog, every menu and the notification popover (#758), in both colour
// modes. Axe measures the surface, limited to it, and must have measured the title
// (or the named element). Mocks stand in where a state needs a cluster or a long
// setup: they change what the dialog shows, not its colours.

const seed = {} as Seed;

/** Rows that the admin pages need before a row action can open a dialog. */
interface AdminSeed {
  token: string;
  userName: string;
  userId: string;
  policyName: string;
  policyId: string;
  channelName: string;
  channelId: string;
  clusterId: string;
  clusterName: string;
  sharedName: string;
  sharedId: string;
}
const admin = {} as AdminSeed;

async function createAdminSeed(request: APIRequestContext): Promise<AdminSeed> {
  const headers = { Authorization: `Bearer ${seed.token}` };
  const post = async (path: string, data: unknown): Promise<{ id: string }> => {
    const res = await request.post(`${API_BASE}/api/v1${path}`, { headers, data });
    expect(res.ok(), `POST ${path}: ${await res.text()}`).toBe(true);
    return res.json();
  };
  await ensureDefaultCluster(request, seed.token);
  const clusters = await (await request.get(`${API_BASE}/api/v1/clusters`, { headers })).json();
  const cluster = clusters.find((c: { is_default: boolean }) => c.is_default) ?? clusters[0];
  const userName = uniqueName('a11y-user').slice(0, 30);
  const policyName = uniqueName('a11y-policy');
  const channelName = uniqueName('a11y-channel');
  const sharedName = uniqueName('a11y-shared');
  const user = await post('/auth/register', { username: userName, password: 'testpass123', role: 'user', display_name: userName });
  const policy = await post('/admin/cleanup-policies', {
    name: policyName, cluster_id: 'all', action: 'stop', condition: 'idle_days:7', schedule: '0 3 * * *', enabled: false, dry_run: true,
  });
  const channel = await post('/admin/notification-channels', { name: channelName, webhook_url: 'https://example.com/a11y', enabled: false, format: 'generic' });
  const shared = await post(`/clusters/${cluster.id}/shared-values`, { name: sharedName, description: 'Accessibility check', values: 'key: value\n', priority: 10 });
  return {
    token: seed.token,
    userName,
    userId: user.id,
    policyName,
    policyId: policy.id,
    channelName,
    channelId: channel.id,
    clusterId: cluster.id,
    clusterName: cluster.name,
    sharedName,
    sharedId: shared.id,
  };
}

async function deleteAdminSeed(request: APIRequestContext): Promise<void> {
  if (!admin.token) return;
  const headers = { Authorization: `Bearer ${admin.token}` };
  for (const url of [
    `/api/v1/users/${admin.userId}`,
    `/api/v1/admin/cleanup-policies/${admin.policyId}`,
    `/api/v1/admin/notification-channels/${admin.channelId}`,
    `/api/v1/clusters/${admin.clusterId}/shared-values/${admin.sharedId}`,
  ]) {
    await request.delete(`${API_BASE}${url}`, { headers }).catch(() => undefined);
  }
}

const instancePage = (status: string, fields: Record<string, unknown> = {}, extra?: (p: Page) => Promise<unknown>): PageSetup => ({
  path: `/stack-instances/${seed.instanceId}`,
  heading: seed.instanceName,
  // A status other than draft makes the page read the pods: 502 without cluster access.
  allowed5xx: [new RegExp(`^GET /api/v1/stack-instances/${seed.instanceId}/pods$`)],
  before: async (p) => {
    await forceInstanceFields(p, seed.instanceId, { status, ...fields });
    await extra?.(p);
  },
});

const deployHistory = (): MockDeployLog[] => [
  { id: 'a11y-d2', stack_instance_id: seed.instanceId, action: 'deploy', status: 'success', branch: 'main',
    started_at: '2026-10-19T10:00:00Z', completed_at: '2026-10-19T10:00:45Z' },
  { id: 'a11y-d1', stack_instance_id: seed.instanceId, action: 'deploy', status: 'success', branch: 'main',
    started_at: '2026-10-19T09:00:00Z', completed_at: '2026-10-19T09:00:45Z' },
];

const openActions = async (page: Page, item: string | RegExp) => {
  await page.getByRole('button', { name: 'Actions', exact: true }).click();
  await page.getByRole('menu').getByRole('menuitem', { name: item }).click();
};

const clickButton = (name: string | RegExp) => (page: Page) => page.getByRole('button', { name, exact: typeof name === 'string' }).click();

/** Paths of the admin pages, for the dialog table. */
const adminPage = (path: string, heading: string, extra?: Partial<PageSetup>): PageSetup => ({ path, heading, ...extra });

const publishedPreview = (): PageSetup => ({ path: `/templates/${seed.publishedTemplateId}`, heading: seed.publishedTemplateName });

const apiKeyRows = () => [
  { id: 'k-1', user_id: 'u', name: 'a11y-key', prefix: 'aa', created_at: '2026-01-01T00:00:00Z' },
];

const mockGeneratedKey = (page: Page) => page.route(/\/api\/v1\/users\/[^/]+\/api-keys$/, (route) => (
  route.request().method() === 'POST'
    ? route.fulfill({ status: 201, json: { raw_key: 'sm_a11y_generated_key', api_key: apiKeyRows()[0] } })
    : route.fulfill({ json: apiKeyRows() })
));

/** One changed chart between two template versions: the diff viewer has content. */
const versionSide = (id: string, version: string, values: string) => ({
  id,
  version,
  created_at: '2026-10-19T09:00:00Z',
  snapshot: {
    template: {
      name: 'a11y', description: 'a11y', category: 'Web', default_branch: 'main', repository_url: '', is_published: true, version,
    },
    charts: [{ chart_name: 'web', repo_url: 'https://charts.example.com', default_values: values, locked_values: '', is_required: false, sort_order: 0 }],
  },
});

const mockTemplateVersions = async (page: Page, fields: Record<string, unknown> = {}) => {
  const id = seed.publishedTemplateId;
  await page.route(new RegExp(`/api/v1/templates/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    return route.fulfill({ response: res, json: { ...(await res.json()), ...fields } });
  });
  await page.route(new RegExp(`/api/v1/templates/${id}/versions(\\?.*)?$`), (route) => route.fulfill({
    json: [
      { id: 'v2', template_id: id, version: '2.0.0', change_summary: 'Second', created_by: 'u', created_at: '2026-10-19T10:00:00Z' },
      { id: 'v1', template_id: id, version: '1.0.0', change_summary: 'First', created_by: 'u', created_at: '2026-10-19T09:00:00Z' },
    ],
  }));
  await page.route(new RegExp(`/api/v1/templates/${id}/versions/diff(\\?.*)?$`), (route) => route.fulfill({
    json: {
      left: versionSide('v1', '1.0.0', 'replicaCount: 1\n'),
      right: versionSide('v2', '2.0.0', 'replicaCount: 3\n'),
      chart_diffs: [{ chart_name: 'web', left_values: 'replicaCount: 1\n', right_values: 'replicaCount: 3\n' }],
    },
  }));
};

interface DialogState {
  name: string;
  setup: () => PageSetup;
  open: (page: Page, dialog: Locator) => Promise<unknown>;
  /** The element that axe must measure. Default: the title. */
  checked?: (dialog: Locator) => Locator;
  /** Hover this element and check again. */
  hover?: (dialog: Locator) => Locator;
  /** The dialog is a progress step and keeps a spinner. */
  mayKeepSpinner?: boolean;
}

const DIALOGS: DialogState[] = [
  // Instance page
  {
    name: 'instance: delete',
    setup: () => instancePage('running'),
    open: (page) => openActions(page, 'Delete'),
    hover: (d) => d.getByRole('button', { name: 'Delete' }),
  },
  {
    name: 'instance: stop',
    setup: () => instancePage('running'),
    open: clickButton('Stop'),
    hover: (d) => d.getByRole('button', { name: 'Stop' }),
  },
  {
    name: 'instance: clean namespace',
    setup: () => instancePage('running'),
    open: (page) => openActions(page, 'Clean namespace'),
  },
  {
    name: 'instance: clone, with the error text of a field',
    setup: () => instancePage('running'),
    open: async (page, dialog) => {
      await openActions(page, 'Clone');
      await dialog.getByRole('textbox', { name: 'Name' }).fill('NOT_VALID');
    },
    checked: (d) => d.locator('.MuiFormHelperText-root.Mui-error'),
  },
  {
    name: 'instance: rollback',
    setup: () => instancePage('running', {}, (p) => mockDeployLog(p, seed.instanceId, deployHistory)),
    open: clickButton('Rollback'),
    checked: (d) => d.locator('.MuiAlert-message').first(),
    hover: (d) => d.getByRole('button', { name: 'Roll Back' }),
  },
  {
    name: 'instance: extend until',
    setup: () => instancePage('running', { ttl_minutes: 600, expires_at: new Date(Date.now() + 3_600_000).toISOString() }),
    open: async (page) => {
      await page.getByRole('button', { name: /^Extend/ }).first().click();
      await page.getByRole('menuitem', { name: 'Custom...' }).click();
    },
  },
  {
    name: 'instance: deploy preview',
    setup: () => instancePage('draft'),
    open: clickButton('Deploy'),
  },
  {
    name: 'instance: unsaved changes',
    setup: () => instancePage('draft'),
    open: async (page) => {
      await page.getByRole('combobox', { name: /^Branch/ }).first().fill('a11y-changed');
      await page.keyboard.press('Enter');
      await page.getByRole('button', { name: 'Deploy', exact: true }).click();
    },
  },
  {
    name: 'instance: quota override',
    setup: () => instancePage('running'),
    open: clickButton('Edit override'),
  },
  {
    name: 'instance: remove the quota override',
    setup: () => instancePage('running', {}, (p) => p.route(
      new RegExp(`/api/v1/stack-instances/${seed.instanceId}/quota$`),
      async (route) => {
        const res = await route.fetch();
        const body = await res.json();
        return route.fulfill({
          response: res,
          json: { ...body, can_modify: true, override: { stack_instance_id: seed.instanceId, cpu_limit: '1' } },
        });
      },
    )),
    open: clickButton('Remove override'),
    hover: (d) => d.getByRole('button', { name: 'Remove' }),
  },
  {
    name: 'instance: custom action',
    setup: () => instancePage('running'),
    open: (page) => openActions(page, /^E2E echo/),
  },
  {
    name: 'instance: job log',
    setup: () => instancePage('running', {}, async (p) => {
      await p.route(new RegExp(`/api/v1/stack-instances/${seed.instanceId}/action-runs(\\?.*)?$`), (route) => route.fulfill({
        json: {
          instance_id: seed.instanceId,
          can_view_logs: true,
          data: [{
            id: 'a11y-run', instance_id: seed.instanceId, action: 'e2e-job', action_label: 'E2E job', user_id: 'u',
            started_by: 'admin', started_at: '2026-10-19T10:00:00Z', finished_at: '2026-10-19T10:00:05Z',
            parameters: {}, status_code: 200, job_id: 'a11y-job', job_status: 'succeeded', has_job_log: true,
          }],
        },
      }));
      await p.route(/\/api\/v1\/stack-instances\/[^/]+\/actions\/[^/]+\/jobs\/[^/]+\/log/, (route) => route.fulfill({
        json: { lines: ['Forced log line'], next_offset: 1, done: true, status: 'succeeded' },
      }));
    }),
    open: (page) => page.getByRole('button', { name: /^Show log of E2E job/ }).click(),
    mayKeepSpinner: true,
  },
  // Definitions
  {
    name: 'definition: template link',
    setup: () => ({
      path: `/stack-definitions/${seed.definitionId}/edit`,
      heading: 'Edit Stack Definition',
      // A real link needs a published template with charts; the dialog only needs the current link.
      before: (p) => p.route(new RegExp(`/api/v1/stack-definitions/${seed.definitionId}(\\?.*)?$`), async (route) => {
        if (route.request().method() !== 'GET') return route.continue();
        const res = await route.fetch();
        return route.fulfill({ response: res, json: { ...(await res.json()), source_template_id: seed.templateId, source_template_version: '1.0.0' } });
      }),
    }),
    open: clickButton('Change Link'),
    checked: (d) => d.getByRole('button', { name: 'Remove link' }),
    hover: (d) => d.getByRole('button', { name: 'Remove link' }),
  },
  {
    name: 'definition: template upgrade',
    setup: () => ({
      path: `/stack-definitions/${seed.definitionId}/edit`,
      heading: 'Edit Stack Definition',
      before: async (p) => {
        await p.route(new RegExp(`/api/v1/stack-definitions/${seed.definitionId}(\\?.*)?$`), async (route) => {
          if (route.request().method() !== 'GET') return route.continue();
          const res = await route.fetch();
          return route.fulfill({ response: res, json: { ...(await res.json()), source_template_id: seed.templateId, source_template_version: '1.0.0' } });
        });
        await p.route(new RegExp(`/api/v1/stack-definitions/${seed.definitionId}/check-upgrade$`), (route) => route.fulfill({
          json: {
            upgrade_available: true,
            current_version: '1.0.0',
            latest_version: '2.0.0',
            changes: { charts_added: ['added'], charts_removed: ['removed'], charts_modified: [], charts_unchanged: ['web'] },
          },
        }));
      },
    }),
    open: clickButton('Check for Upgrades'),
  },
  {
    name: 'definitions: import',
    setup: () => adminPage('/stack-definitions', 'Stack Definitions'),
    open: clickButton(/^Import/),
  },
  // Templates
  { name: 'template: publish', setup: publishedPreview, open: clickButton('Publish') },
  { name: 'template: clone', setup: publishedPreview, open: clickButton('Clone as Template') },
  { name: 'template: unpublish', setup: publishedPreview, open: clickButton('Unpublish') },
  {
    name: 'template: changes since the release',
    setup: () => ({
      ...publishedPreview(),
      before: (p) => mockTemplateVersions(p, { has_unpublished_changes: true, published_version: '1.0.0' }),
    }),
    open: clickButton('Show changes'),
  },
  {
    name: 'template: version comparison',
    setup: () => ({ ...publishedPreview(), before: (p) => mockTemplateVersions(p) }),
    open: async (page) => {
      await page.getByRole('tab', { name: 'Version History' }).click();
      await page.getByRole('checkbox', { name: /version 1\.0\.0/ }).check();
      await page.getByRole('checkbox', { name: /version 2\.0\.0/ }).check();
      await page.getByRole('button', { name: 'Compare Selected Versions' }).click();
    },
  },
  {
    name: 'gallery: quick deploy',
    setup: () => adminPage('/templates', 'Template Gallery'),
    open: async (page) => {
      await page.locator('.MuiCard-root').filter({ hasText: seed.publishedTemplateName }).getByRole('button', { name: 'Quick Deploy' }).click();
    },
  },
  {
    name: 'gallery: bulk delete',
    setup: () => adminPage('/templates', 'Template Gallery'),
    open: async (page) => {
      await page.getByRole('tab', { name: 'My Templates' }).click();
      await page.getByRole('checkbox', { name: `Select ${seed.templateName}` }).check();
      await page.getByRole('toolbar', { name: 'Bulk actions' }).getByRole('button', { name: /^Delete/ }).click();
    },
  },
  {
    name: 'gallery: bulk result',
    setup: () => adminPage('/templates', 'Template Gallery', {
      before: (p) => p.route(/\/api\/v1\/templates\/bulk\/delete$/, (route) => route.fulfill({
        json: { total: 2, succeeded: 1, failed: 1, results: [
          { template_id: seed.templateId, template_name: 'Worked', status: 'success' },
          { template_id: 'other', template_name: 'Refused', status: 'error', error: 'Forced error' },
        ] },
      })),
    }),
    open: async (page, dialog) => {
      await page.getByRole('tab', { name: 'My Templates' }).click();
      await page.getByRole('checkbox', { name: `Select ${seed.templateName}` }).check();
      await page.getByRole('toolbar', { name: 'Bulk actions' }).getByRole('button', { name: /^Delete/ }).click();
      await dialog.getByRole('button', { name: /^(Confirm|Delete)/ }).click();
      await expect(dialog.getByText('Forced error')).toBeVisible();
    },
    checked: (d) => d.getByText('Forced error'),
  },
  // Dashboard
  {
    name: 'dashboard: bulk confirm',
    setup: () => adminPage('/', 'Stack Instances', { allowed5xx: [/^GET \/api\/v1\/stack-instances\/[^/]+\/status$/] }),
    open: async (page) => {
      await page.getByRole('checkbox', { name: 'Select all instances' }).check();
      await page.getByRole('toolbar', { name: 'Bulk actions' }).getByRole('button', { name: 'Stop', exact: true }).click();
    },
  },
  {
    name: 'dashboard: bulk result',
    setup: () => adminPage('/', 'Stack Instances', {
      allowed5xx: [/^GET \/api\/v1\/stack-instances\/[^/]+\/status$/],
      before: (p) => p.route(/\/api\/v1\/stack-instances\/bulk\/stop$/, (route) => route.fulfill({
        json: { total: 2, succeeded: 1, failed: 1, results: [
          { instance_id: seed.instanceId, instance_name: 'Worked', status: 'success' },
          { instance_id: 'other', instance_name: 'Refused', status: 'error', error: 'Forced error' },
        ] },
      })),
    }),
    open: async (page, dialog) => {
      await page.getByRole('checkbox', { name: 'Select all instances' }).check();
      await page.getByRole('toolbar', { name: 'Bulk actions' }).getByRole('button', { name: 'Stop', exact: true }).click();
      await dialog.getByRole('button', { name: /Stop/ }).click();
      await expect(dialog.getByText('Forced error')).toBeVisible();
    },
    checked: (d) => d.getByText('Forced error'),
  },
  // Cleanup policies
  {
    name: 'policies: create',
    setup: () => adminPage('/admin/cleanup-policies', 'Cleanup Policies'),
    open: clickButton(/^(Add|Create|New)/),
  },
  {
    name: 'policies: edit',
    setup: () => adminPage('/admin/cleanup-policies', 'Cleanup Policies'),
    open: (page) => page.getByRole('button', { name: `Edit ${admin.policyName}` }).click(),
  },
  {
    name: 'policies: delete',
    setup: () => adminPage('/admin/cleanup-policies', 'Cleanup Policies'),
    open: (page) => page.getByRole('button', { name: `Delete ${admin.policyName}` }).click(),
  },
  {
    name: 'policies: run',
    setup: () => adminPage('/admin/cleanup-policies', 'Cleanup Policies'),
    open: (page) => page.getByRole('button', { name: `Run ${admin.policyName}` }).click(),
  },
  // Shared values
  {
    name: 'shared values: add',
    setup: () => adminPage('/admin/shared-values', 'Shared Values'),
    open: clickButton(/^Add/),
  },
  {
    name: 'shared values: delete',
    setup: () => adminPage('/admin/shared-values', 'Shared Values'),
    open: (page) => page.getByRole('button', { name: `Delete ${admin.sharedName}` }).click(),
  },
  // Clusters
  {
    name: 'clusters: add',
    setup: () => adminPage('/admin/clusters', 'Cluster Management'),
    open: clickButton(/^Add/),
  },
  {
    name: 'clusters: delete',
    setup: () => adminPage('/admin/clusters', 'Cluster Management'),
    open: (page) => page.getByRole('button', { name: `Delete ${admin.clusterName}` }).click(),
  },
  {
    name: 'clusters: resource quotas',
    setup: () => adminPage('/admin/clusters', 'Cluster Management'),
    open: (page) => page.getByRole('button', { name: `Resource quotas for ${admin.clusterName}` }).click(),
  },
  // Notification channels
  {
    name: 'channels: create',
    setup: () => adminPage('/admin/notification-channels', 'Notification Channels'),
    open: clickButton('Create Channel'),
  },
  {
    name: 'channels: delete',
    setup: () => adminPage('/admin/notification-channels', 'Notification Channels'),
    open: (page) => page.getByRole('button', { name: `Delete ${admin.channelName}` }).click(),
  },
  {
    name: 'channels: subscriptions',
    setup: () => adminPage('/admin/notification-channels', 'Notification Channels'),
    open: async (page) => {
      await page.getByRole('row', { name: new RegExp(admin.channelName) }).getByRole('button', { name: /^(Subscriptions|0)/ }).first().click();
    },
  },
  // Users
  {
    name: 'users: add',
    setup: () => adminPage('/admin/users', 'User Management'),
    open: clickButton('Add User'),
  },
  {
    name: 'users: disable',
    setup: () => adminPage('/admin/users', 'User Management'),
    open: (page) => page.getByRole('button', { name: `Disable user ${admin.userName}` }).click(),
  },
  {
    name: 'users: delete',
    setup: () => adminPage('/admin/users', 'User Management'),
    open: (page) => page.getByRole('button', { name: `Delete user ${admin.userName}` }).click(),
  },
  {
    name: 'users: reset password',
    setup: () => adminPage('/admin/users', 'User Management'),
    open: (page) => page.getByRole('button', { name: `Reset password for ${admin.userName}` }).click(),
  },
  {
    name: 'users: edit role',
    setup: () => adminPage('/admin/users', 'User Management'),
    open: (page) => page.getByRole('button', { name: `Edit role of ${admin.userName}` }).click(),
  },
  {
    name: 'users: generate API key',
    setup: () => adminPage('/admin/users', 'User Management'),
    open: async (page) => {
      await page.getByRole('row', { name: new RegExp(admin.userName) }).getByRole('button', { name: 'Expand API keys' }).click();
      await page.getByRole('button', { name: 'Generate API Key' }).click();
    },
  },
  {
    name: 'users: API key generated',
    setup: () => adminPage('/admin/users', 'User Management', { before: mockGeneratedKey }),
    open: async (page, dialog) => {
      await page.getByRole('row', { name: new RegExp(admin.userName) }).getByRole('button', { name: 'Expand API keys' }).click();
      await page.getByRole('button', { name: 'Generate API Key' }).click();
      await dialog.getByRole('textbox', { name: /Name/ }).fill('a11y-key');
      await dialog.getByRole('button', { name: /^(Generate|Create)/ }).click();
      await expect(page.getByRole('heading', { name: 'API Key Generated' })).toBeVisible();
    },
  },
  {
    name: 'users: revoke API key',
    setup: () => adminPage('/admin/users', 'User Management', { before: mockGeneratedKey }),
    open: async (page) => {
      await page.getByRole('row', { name: new RegExp(admin.userName) }).getByRole('button', { name: 'Expand API keys' }).click();
      await page.getByRole('button', { name: 'Revoke key a11y-key' }).click();
    },
  },
  // Orphaned namespaces
  {
    name: 'orphaned namespaces: delete',
    setup: () => adminPage('/admin/orphaned-namespaces', 'Orphaned Namespaces', {
      before: (p) => p.route(/\/api\/v1\/admin\/orphaned-namespaces(\?.*)?$/, (route) => route.fulfill({
        json: [{ name: 'stack-a11y', created_at: '2026-10-19T09:00:00Z', phase: 'Active', helm_releases: ['web'], managed: true, resource_counts: null }],
      })),
    }),
    open: (page) => page.getByRole('button', { name: 'Delete namespace stack-a11y' }).click(),
    hover: (d) => d.getByRole('button', { name: 'Cancel' }),
  },
  // Profile
  {
    name: 'profile: generate API key',
    setup: () => adminPage('/profile', 'My Profile'),
    open: clickButton(/^Generate/),
  },
  {
    name: 'profile: API key generated',
    setup: () => adminPage('/profile', 'My Profile', { before: mockGeneratedKey }),
    open: async (page, dialog) => {
      await page.getByRole('button', { name: /^Generate/ }).first().click();
      await dialog.getByRole('textbox', { name: /Name/ }).fill('a11y-key');
      await dialog.getByRole('button', { name: /^(Generate|Create)/ }).click();
      await expect(page.getByRole('heading', { name: 'API Key Generated' })).toBeVisible();
    },
  },
  {
    name: 'profile: revoke API key',
    setup: () => adminPage('/profile', 'My Profile', { before: mockGeneratedKey }),
    open: (page) => page.getByRole('button', { name: /^Revoke/ }).first().click(),
  },
];

test.describe('Accessibility: all dialogs, menus and popovers', () => {
  // Tall enough for the longest dialog: axe skips what is outside the screen.
  test.use({ viewport: { width: 1400, height: 2000 } });

  test.beforeAll(async ({ request }) => {
    Object.assign(seed, await createSeed(request));
    Object.assign(admin, await createAdminSeed(request));
  });

  test.afterAll(async ({ request }) => {
    await deleteAdminSeed(request);
    await deleteSeed(request, seed);
  });

  for (const mode of MODES) {
    for (const { name, setup, open, checked, hover, mayKeepSpinner } of DIALOGS) {
      test(`dialog ${name} (${mode})`, async ({ page }) => {
        await checkPage(page, mode, {
          ...setup(),
          ready: async (p, runAxe) => {
            const dialog = p.getByRole('dialog');
            await open(p, dialog);
            await expect(dialog, 'the dialog did not open').toBeVisible();
            if (!mayKeepSpinner) {
              await expect(dialog.locator('[role=progressbar]:not([aria-valuenow])'), 'the dialog is still loading').toHaveCount(0, { timeout: 15_000 });
            }
            const measured = checked ? checked(dialog) : dialog.locator('h2').first();
            await expect(measured, 'the element to measure is not visible').toBeVisible();
            await runAxe({ selector: '[role=dialog]', checked: measured });
            if (hover) await hoverAndCheck(p, hover(dialog), '[role=dialog]', runAxe);
          },
        });
      });
    }

    // -----------------------------------------------------------------------
    // Menus. A Select also opens a Menu, and an Autocomplete opens its own list.

    const instanceSetup = () => instancePage('running', { ttl_minutes: 600, expires_at: new Date(Date.now() + 3_600_000).toISOString() });

    test(`menu export values (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...instanceSetup(),
        ready: async (p, runAxe) => {
          await openActions(p, /^Export values/);
          const item = p.getByRole('menu', { name: 'Export values' }).getByRole('menuitem').first();
          await expect(item).toBeVisible();
          await hoverAndCheck(p, item, '[role=menu]', runAxe);
        },
      });
    });

    test(`menu extend the TTL (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...instanceSetup(),
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: /^Extend/ }).first().click();
          const item = p.getByRole('menuitem', { name: 'Custom...' });
          await expect(item).toBeVisible();
          await hoverAndCheck(p, item, '[role=menu]', runAxe);
        },
      });
    });

    test(`menu audit log export (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/audit-log',
        heading: 'Audit Log',
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: 'Export' }).click();
          const item = p.getByRole('menuitem').first();
          await expect(item).toBeVisible();
          await hoverAndCheck(p, item, '[role=menu]', runAxe);
        },
      });
    });

    test(`menu of a select field (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/stack-instances/new',
        heading: 'Create Stack Instance',
        ready: async (p, runAxe) => {
          await p.getByRole('combobox').first().click();
          const listbox = p.getByRole('listbox');
          await expect(listbox).toBeVisible();
          const option = listbox.getByRole('option').first();
          await runAxe({ selector: '[role=listbox]', checked: option });
        },
      });
    });

    test(`list of an autocomplete field (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/stack-instances/compare',
        heading: 'Compare Stack Instances',
        ready: async (p, runAxe) => {
          await p.getByRole('combobox').first().click();
          const option = p.getByRole('listbox').getByRole('option').first();
          await expect(option).toBeVisible();
          await runAxe({ selector: '[role=listbox]', checked: option });
        },
      });
    });

    // -----------------------------------------------------------------------
    // The notification popover keeps its elevation overlay in dark mode.

    test(`notification popover, rows on hover (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/',
        heading: 'Stack Instances',
        allowed5xx: [/^GET \/api\/v1\/stack-instances\/[^/]+\/status$/],
        before: async (p) => {
          const notifications = [
            { id: 'n1', user_id: 'u', type: 'deployment.error', title: 'A failed deploy', message: 'The deploy failed', is_read: false, created_at: '2026-10-19T10:00:00Z' },
            { id: 'n2', user_id: 'u', type: 'deployment.success', title: 'A good deploy', message: 'The deploy worked', is_read: true, created_at: '2026-10-19T09:00:00Z' },
          ];
          await p.route(/\/api\/v1\/notifications(\?.*)?$/, (route) => (
            route.request().method() !== 'GET'
              ? route.continue()
              : route.fulfill({ json: { notifications, total: 2, unread_count: 1 } })
          ));
          await p.route(/\/api\/v1\/notifications\/(count|unread-count)$/, (route) => route.fulfill({ json: { unread_count: 1, count: 1 } }));
        },
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: /^Open notifications/ }).first().click();
          const popover = p.locator('.MuiPopover-paper');
          await expect(popover).toBeVisible();
          const scope = '.MuiPopover-paper';
          const unread = popover.getByRole('button', { name: /A failed deploy/ });
          const read = popover.getByRole('button', { name: /A good deploy/ });
          await expect(unread).toBeVisible();
          // An unread row has the hover tint at rest, so this run already measures its hover colour.
          await runAxe({ selector: scope, checked: unread });
          await hoverAndCheck(p, read, scope, runAxe);
          await hoverAndCheck(p, popover.getByRole('button', { name: 'View all notifications' }), scope, runAxe);
        },
      });
    });
  }
});
