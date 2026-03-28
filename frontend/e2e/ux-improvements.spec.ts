import { test, expect, Page } from '@playwright/test';
import { loginAsDevops, loginAsUser, uniqueName, createTemplate, createAndPublishTemplate } from './helpers';

// ---------------------------------------------------------------------------
// Mock data & helpers
// ---------------------------------------------------------------------------

const mockInstances = [
  {
    id: 'ux-inst-001',
    name: 'webapp-alpha',
    branch: 'main',
    namespace: 'stack-webapp-alpha-user',
    status: 'running',
    owner: 'user',
    cluster_id: '',
    definition_id: 'def-001',
    created_at: '2025-06-01T00:00:00Z',
    updated_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(), // 2 hours ago
    last_deployed_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
  },
  {
    id: 'ux-inst-002',
    name: 'api-beta',
    branch: 'develop',
    namespace: 'stack-api-beta-user',
    status: 'stopped',
    owner: 'user',
    cluster_id: '',
    definition_id: 'def-002',
    created_at: '2025-06-02T00:00:00Z',
    updated_at: new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString(), // 3 days ago
    last_deployed_at: new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString(),
  },
  {
    id: 'ux-inst-003',
    name: 'worker-gamma',
    branch: 'main',
    namespace: 'stack-worker-gamma-user',
    status: 'draft',
    owner: 'user',
    cluster_id: '',
    definition_id: 'def-001',
    created_at: '2025-06-03T00:00:00Z',
    updated_at: new Date(Date.now() - 15 * 60 * 1000).toISOString(), // 15 minutes ago
  },
];

const mockDefinitions = [
  {
    id: 'def-001',
    name: 'My App',
    description: 'A test definition',
    default_branch: 'main',
    charts: [{ id: 'c1', name: 'nginx' }],
    source_template_id: '',
    source_template_version: '',
    created_at: '2025-06-01T00:00:00Z',
    updated_at: '2025-06-01T00:00:00Z',
  },
  {
    id: 'def-002',
    name: 'Backend Service',
    description: 'Another definition',
    default_branch: 'develop',
    charts: [{ id: 'c2', name: 'my-chart' }, { id: 'c3', name: 'redis' }],
    source_template_id: '',
    source_template_version: '',
    created_at: '2025-06-02T00:00:00Z',
    updated_at: '2025-06-02T00:00:00Z',
  },
];

/**
 * Mock dashboard API endpoints to avoid needing real data.
 */
async function mockDashboardAPIs(page: Page) {
  await page.route('**/api/v1/stack-instances', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockInstances),
      });
    }
    return route.continue();
  });

  await page.route('**/api/v1/stack-instances/recent', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockInstances.slice(0, 2)),
    }),
  );

  await page.route('**/api/v1/favorites', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    }),
  );

  await page.route(/\/api\/v1\/clusters$/, (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }
    return route.continue();
  });
}

/**
 * Mock the definitions list API.
 */
async function mockDefinitionsAPI(page: Page) {
  await page.route('**/api/v1/stack-definitions', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockDefinitions),
      });
    }
    return route.continue();
  });

  // Template list for the upgrade-check logic in the List
  await page.route('**/api/v1/templates', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }
    return route.continue();
  });

  await page.route('**/api/v1/favorites', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    }),
  );
}

// ===========================================================================
// Feature 1: Template Gallery Tabs
// ===========================================================================
test.describe('Template Gallery Tabs', () => {
  test.describe('devops user sees three tabs', () => {
    test.beforeEach(async ({ page }) => {
      await loginAsDevops(page);
    });

    test('three tabs are visible: Published, My Templates, All Drafts', async ({ page }) => {
      await page.goto('/templates');
      await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
        timeout: 10_000,
      });

      await expect(page.getByRole('tab', { name: 'Published' })).toBeVisible();
      await expect(page.getByRole('tab', { name: 'My Templates' })).toBeVisible();
      await expect(page.getByRole('tab', { name: 'All Drafts' })).toBeVisible();
    });

    test('Published tab is selected by default and shows only published templates', async ({ page }) => {
      const pubName = uniqueName('tab-pub');
      const draftName = uniqueName('tab-draft');

      // Create a published and a draft template
      await createAndPublishTemplate(page, pubName);
      await createTemplate(page, draftName);

      await page.goto('/templates');
      await expect(page.getByRole('tab', { name: 'Published' })).toHaveAttribute('aria-selected', 'true', {
        timeout: 10_000,
      });

      // Published template visible
      await expect(page.getByRole('heading', { name: pubName })).toBeVisible({ timeout: 10_000 });
      // Draft template NOT visible on Published tab
      await expect(page.getByRole('heading', { name: draftName })).not.toBeVisible();
    });

    test('My Templates tab shows only own templates', async ({ page }) => {
      const ownName = uniqueName('tab-own');
      await createTemplate(page, ownName);

      await page.goto('/templates');
      await page.getByRole('tab', { name: 'My Templates' }).click();

      await expect(page.getByRole('heading', { name: ownName })).toBeVisible({ timeout: 10_000 });
    });

    test('All Drafts tab shows only unpublished templates', async ({ page }) => {
      const pubName = uniqueName('tab-apub');
      const draftName = uniqueName('tab-adraft');

      await createAndPublishTemplate(page, pubName);
      await createTemplate(page, draftName);

      await page.goto('/templates');
      await page.getByRole('tab', { name: 'All Drafts' }).click();

      // Draft visible
      await expect(page.getByRole('heading', { name: draftName })).toBeVisible({ timeout: 10_000 });
      // Published NOT visible on All Drafts tab
      await expect(page.getByRole('heading', { name: pubName })).not.toBeVisible();
    });

    test('tab switching clears bulk selection', async ({ page }) => {
      const name = uniqueName('tab-sel');
      await createTemplate(page, name);

      await page.goto('/templates');
      // Go to My Templates (has checkboxes)
      await page.getByRole('tab', { name: 'My Templates' }).click();

      // Select a template
      const checkbox = page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) });
      await expect(checkbox).toBeVisible({ timeout: 10_000 });
      await checkbox.click();

      // Toolbar visible
      await expect(page.getByRole('toolbar', { name: 'Bulk actions' })).toBeVisible({ timeout: 5_000 });

      // Switch tab
      await page.getByRole('tab', { name: 'Published' }).click();

      // Toolbar no longer visible
      await expect(page.getByRole('toolbar', { name: 'Bulk actions' })).not.toBeVisible({ timeout: 5_000 });
    });
  });

  test.describe('regular user does NOT see tabs', () => {
    test.beforeEach(async ({ page }) => {
      await loginAsUser(page);
    });

    test('no tabs visible for regular user', async ({ page }) => {
      await page.goto('/templates');
      await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
        timeout: 10_000,
      });

      await expect(page.getByRole('tab', { name: 'Published' })).not.toBeVisible();
      await expect(page.getByRole('tab', { name: 'My Templates' })).not.toBeVisible();
      await expect(page.getByRole('tab', { name: 'All Drafts' })).not.toBeVisible();
    });

    test('regular user only sees published templates', async ({ page }) => {
      // We can't easily create a published template as a regular user,
      // so we just verify that no draft chip appears
      await page.goto('/templates');
      await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
        timeout: 10_000,
      });

      // If any templates are visible, none should show "Draft" chip
      const draftChips = page.locator('.MuiCard-root .MuiChip-root').filter({ hasText: 'Draft' });
      await expect(draftChips).toHaveCount(0, { timeout: 5_000 });
    });
  });
});

// ===========================================================================
// Feature 2: Bulk Template Operations
// ===========================================================================
test.describe('Bulk Template Operations', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('no checkboxes on Published tab', async ({ page }) => {
    const name = uniqueName('bulk-pub');
    await createAndPublishTemplate(page, name);

    await page.goto('/templates');
    await expect(page.getByRole('tab', { name: 'Published' })).toHaveAttribute('aria-selected', 'true', {
      timeout: 10_000,
    });
    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

    // No select-all checkbox
    await expect(page.getByRole('checkbox', { name: 'Select all templates' })).not.toBeVisible();
    // No per-template checkbox
    await expect(page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) })).not.toBeVisible();
  });

  test('checkboxes visible on My Templates tab', async ({ page }) => {
    const name = uniqueName('bulk-my');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await expect(page.getByRole('checkbox', { name: 'Select all templates' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) })).toBeVisible();
  });

  test('checkboxes visible on All Drafts tab', async ({ page }) => {
    const name = uniqueName('bulk-draft');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'All Drafts' }).click();

    await expect(page.getByRole('checkbox', { name: 'Select all templates' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) })).toBeVisible();
  });

  test('select all checkbox selects all templates', async ({ page }) => {
    const name1 = uniqueName('bulk-sa1');
    const name2 = uniqueName('bulk-sa2');
    await createTemplate(page, name1);
    await createTemplate(page, name2);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    // Wait for templates to render
    await expect(page.getByRole('checkbox', { name: new RegExp(`Select ${name1}`) })).toBeVisible({
      timeout: 10_000,
    });

    await page.getByRole('checkbox', { name: 'Select all templates' }).click();

    // Bulk toolbar should appear
    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });
    await expect(toolbar.getByText('selected')).toBeVisible();
  });

  test('selecting templates shows bulk action toolbar with count', async ({ page }) => {
    const name = uniqueName('bulk-count');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });
    await expect(toolbar.getByText('1 selected')).toBeVisible();
  });

  test('bulk delete shows confirmation dialog with warning', async ({ page }) => {
    const name = uniqueName('bulk-del');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await toolbar.getByRole('button', { name: /Delete/ }).click();

    // Confirmation dialog
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Confirm Bulk Delete')).toBeVisible({ timeout: 5_000 });
    await expect(dialog.getByText('This action cannot be undone.')).toBeVisible();
    await expect(dialog.getByText(name)).toBeVisible();
  });

  test('bulk publish shows confirmation on All Drafts tab', async ({ page }) => {
    const name = uniqueName('bulk-publ');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'All Drafts' }).click();

    await page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await toolbar.getByRole('button', { name: /Publish/ }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Confirm Bulk Publish')).toBeVisible({ timeout: 5_000 });
    await expect(dialog.getByText(/template/)).toBeVisible();
  });

  test('bulk unpublish shows confirmation on My Templates tab', async ({ page }) => {
    const name = uniqueName('bulk-unpub');
    // Create and publish so it shows on My Templates with published status
    await createAndPublishTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await expect(page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) })).toBeVisible({
      timeout: 10_000,
    });
    await page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await toolbar.getByRole('button', { name: /Unpublish/ }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Confirm Bulk Unpublish')).toBeVisible({ timeout: 5_000 });
    await expect(dialog.getByText(/template/)).toBeVisible();
  });

  test('cancel dismisses bulk confirmation dialog', async ({ page }) => {
    const name = uniqueName('bulk-cancel');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await toolbar.getByRole('button', { name: /Delete/ }).click();
    await expect(page.getByText('Confirm Bulk Delete')).toBeVisible({ timeout: 5_000 });

    // Cancel
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.getByText('Confirm Bulk Delete')).not.toBeVisible({ timeout: 5_000 });

    // Toolbar still visible (selection preserved)
    await expect(toolbar).toBeVisible();
  });

  test('confirming bulk delete executes operation and shows results dialog', async ({ page }) => {
    const name = uniqueName('bulk-exec');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await toolbar.getByRole('button', { name: /Delete/ }).click();
    await expect(page.getByText('Confirm Bulk Delete')).toBeVisible({ timeout: 5_000 });

    // Confirm
    await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();

    // Results dialog
    await expect(page.getByText('Bulk Operation Results')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(/succeeded/).first()).toBeVisible();
  });

  test('clear selection button removes all selections', async ({ page }) => {
    const name = uniqueName('bulk-clear');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await toolbar.getByRole('button', { name: 'Clear Selection' }).click();

    // Toolbar disappears
    await expect(toolbar).not.toBeVisible({ timeout: 5_000 });
    // Checkbox unchecked
    await expect(page.getByRole('checkbox', { name: new RegExp(`Select ${name}`) })).not.toBeChecked();
  });
});

// ===========================================================================
// Feature 3: Template Author Labels
// ===========================================================================
test.describe('Template Author Labels', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('template cards show "By {username}" when owner_username is available', async ({ page }) => {
    const name = uniqueName('author');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

    // The API enriches owner_username for devops-created templates
    // The template card should show "By {username}"
    const card = page.locator('.MuiCard-root').filter({ hasText: name });
    await expect(card.getByText(/^By /)).toBeVisible({ timeout: 5_000 });
  });
});

// ===========================================================================
// Feature 4: Draft Chip (amber color)
// ===========================================================================
test.describe('Draft Chip', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('draft templates show a "Draft" chip', async ({ page }) => {
    const name = uniqueName('draft-chip');
    await createTemplate(page, name);

    await page.goto('/templates');
    await page.getByRole('tab', { name: 'My Templates' }).click();

    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

    // Find the card containing our template name
    const card = page.locator('.MuiCard-root').filter({ hasText: name });
    await expect(card.locator('.MuiChip-root', { hasText: 'Draft' })).toBeVisible();
  });

  test('published templates do NOT show "Draft" chip', async ({ page }) => {
    const name = uniqueName('pub-nodraft');
    await createAndPublishTemplate(page, name);

    await page.goto('/templates');
    // Published tab (default)
    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

    const card = page.locator('.MuiCard-root').filter({ hasText: name });
    await expect(card.locator('.MuiChip-root', { hasText: 'Draft' })).not.toBeVisible();
  });
});

// ===========================================================================
// Feature 5: Usage Count
// ===========================================================================
test.describe('Usage Count', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('template cards show usage count when definition_count > 0', async ({ page }) => {
    // Create a template with a chart and publish it
    const tplName = uniqueName('usage');
    const tplId = await createAndPublishTemplate(page, tplName);

    // Instantiate it to create a definition (increases definition_count)
    await page.goto(`/templates/${tplId}/use`);
    await expect(page.getByRole('heading', { level: 1, name: /Use Template/ })).toBeVisible({ timeout: 10_000 });
    const defName = uniqueName('usage-def');
    const nameField = page.getByLabel('Definition Name');
    await nameField.clear();
    await nameField.fill(defName);
    await page.getByRole('button', { name: 'Create Stack Definition' }).click();
    await page.waitForURL(/\/stack-definitions\/[^/]+\/edit/, { timeout: 10_000 });

    // Go to gallery and check for usage count chip
    await page.goto('/templates');
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });

    const card = page.locator('.MuiCard-root').filter({ hasText: tplName });
    await expect(card.getByText(/Used by \d+ definition/)).toBeVisible({ timeout: 5_000 });
  });
});

// ===========================================================================
// Feature 6: Recently Used Section (localStorage driven)
// ===========================================================================
test.describe('Recently Used Section', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('shows "Recently Used" section when localStorage has recentTemplates data', async ({ page }) => {
    // First create a published template so we have a valid ID
    const name = uniqueName('recent');
    const templateId = await createAndPublishTemplate(page, name);

    // Inject recently used data into localStorage BEFORE navigating to gallery
    await page.addInitScript(
      ({ id, tplName }) => {
        localStorage.setItem(
          'recentTemplates',
          JSON.stringify([
            { id, name: tplName, usedAt: new Date().toISOString() },
          ]),
        );
      },
      { id: templateId, tplName: name },
    );

    await page.goto('/templates');

    // Published tab should show "Recently Used" heading
    await expect(page.getByRole('heading', { name: 'Recently Used' })).toBeVisible({ timeout: 10_000 });
    // The recently used template name should appear in that section
    await expect(page.getByText(name).first()).toBeVisible();
  });

  test('does NOT show "Recently Used" section when localStorage is empty', async ({ page }) => {
    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });

    await expect(page.getByRole('heading', { name: 'Recently Used' })).not.toBeVisible();
  });

  test('does NOT show "Recently Used" section on non-Published tabs', async ({ page }) => {
    const name = uniqueName('recent-tab');
    const templateId = await createAndPublishTemplate(page, name);

    // Inject recently used data
    await page.addInitScript(
      ({ id, tplName }) => {
        localStorage.setItem(
          'recentTemplates',
          JSON.stringify([
            { id, name: tplName, usedAt: new Date().toISOString() },
          ]),
        );
      },
      { id: templateId, tplName: name },
    );

    await page.goto('/templates');

    // Verify visible on Published tab
    await expect(page.getByRole('heading', { name: 'Recently Used' })).toBeVisible({ timeout: 10_000 });

    // Switch to My Templates — no Recently Used
    await page.getByRole('tab', { name: 'My Templates' }).click();
    await expect(page.getByRole('heading', { name: 'Recently Used' })).not.toBeVisible();

    // Switch to All Drafts — no Recently Used
    await page.getByRole('tab', { name: 'All Drafts' }).click();
    await expect(page.getByRole('heading', { name: 'Recently Used' })).not.toBeVisible();
  });
});

// ===========================================================================
// Feature 7: Dashboard Relative Timestamps
// ===========================================================================
test.describe('Dashboard Relative Timestamps', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
    await mockDashboardAPIs(page);
  });

  test('instance cards show relative time (e.g., "2h ago")', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });

    // The instance updated_at was set to 2h ago — should show "2h ago"
    await expect(page.getByText(/\d+[hm] ago/).first()).toBeVisible({ timeout: 10_000 });
  });

  test('recently deployed instances show "Deployed Xh ago"', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });

    // Mock data has last_deployed_at set — look for "Deployed" with relative time
    await expect(page.getByText(/Deployed \d+[hmd] ago/).first()).toBeVisible({ timeout: 10_000 });
  });

  test('relative time does not show absolute date format on cards', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });

    // Wait for cards to render
    await expect(page.getByRole('heading', { name: 'webapp-alpha' })).toBeVisible({ timeout: 10_000 });

    // The instance cards should NOT show full ISO or locale date strings
    // e.g., "2025-06-01" or "6/1/2025" should not appear in the main card text
    // (they may appear in tooltips, which is fine)
    const cardArea = page.locator('.MuiCard-root').filter({ hasText: 'webapp-alpha' }).first();
    const cardText = await cardArea.textContent();
    expect(cardText).not.toMatch(/\d{4}-\d{2}-\d{2}T/); // No raw ISO dates in visible text
  });
});

// ===========================================================================
// Feature 8: Dashboard Keyboard Shortcuts
// ===========================================================================
test.describe('Dashboard Keyboard Shortcuts', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
    await mockDashboardAPIs(page);
  });

  test('"/" key focuses the search input', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByPlaceholder('Search instances...')).toBeVisible({ timeout: 10_000 });

    // Press "/" while not focused on any input
    await page.keyboard.press('/');

    // Search input should be focused
    await expect(page.getByPlaceholder('Search instances...')).toBeFocused({ timeout: 5_000 });
  });

  test('Escape clears search text when search is active', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByPlaceholder('Search instances...')).toBeVisible({ timeout: 10_000 });

    // Type some search text
    await page.getByPlaceholder('Search instances...').fill('webapp');
    await expect(page.getByPlaceholder('Search instances...')).toHaveValue('webapp');

    // Click elsewhere to unfocus the search input
    await page.getByRole('heading', { level: 1, name: 'Stack Instances' }).click();

    // Press Escape
    await page.keyboard.press('Escape');

    // Search should be cleared
    await expect(page.getByPlaceholder('Search instances...')).toHaveValue('', { timeout: 5_000 });
  });

  test('"/" does NOT trigger when typing in the search input', async ({ page }) => {
    await page.goto('/');
    const searchInput = page.getByPlaceholder('Search instances...');
    await expect(searchInput).toBeVisible({ timeout: 10_000 });

    // Focus and type in search (including "/" character)
    await searchInput.click();
    await searchInput.fill('test/path');

    // The slash should have been typed into the input, not consumed by the shortcut
    await expect(searchInput).toHaveValue('test/path');
  });

  test('keyboard hint near search shows "/" shortcut', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByPlaceholder('Search instances...')).toBeVisible({ timeout: 10_000 });

    // There should be a visible hint about the "/" shortcut
    await expect(page.getByText('to search')).toBeVisible({ timeout: 5_000 });
  });
});

// ===========================================================================
// Feature 9: Definitions Inline Deploy Button
// ===========================================================================
test.describe('Definitions Inline Deploy Button', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
    await mockDefinitionsAPI(page);
  });

  test('definition table has "Actions" column header', async ({ page }) => {
    await page.goto('/stack-definitions');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({
      timeout: 10_000,
    });

    await expect(page.getByRole('columnheader', { name: 'Actions' })).toBeVisible({ timeout: 10_000 });
  });

  test('each definition row has a deploy (rocket) icon button', async ({ page }) => {
    await page.goto('/stack-definitions');
    await expect(page.getByText('My App')).toBeVisible({ timeout: 10_000 });

    // Both definitions should have deploy buttons
    await expect(page.getByRole('button', { name: /Deploy My App/ })).toBeVisible();
    await expect(page.getByRole('button', { name: /Deploy Backend Service/ })).toBeVisible();
  });

  test('clicking deploy button navigates to new instance page with definition param', async ({ page }) => {
    await page.goto('/stack-definitions');
    await expect(page.getByText('My App')).toBeVisible({ timeout: 10_000 });

    await page.getByRole('button', { name: /Deploy My App/ }).click();

    // Should navigate to new instance with definition query param
    await page.waitForURL(/\/stack-instances\/new\?definition=def-001/, { timeout: 10_000 });
  });
});

// ===========================================================================
// Feature 10: QuickDeploy Retry Button
// ===========================================================================
test.describe('QuickDeploy Retry Button', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('after deploy error, button label changes to "Retry"', async ({ page }) => {
    const name = uniqueName('retry');
    const templateId = await createAndPublishTemplate(page, name);

    // Mock the quick-deploy endpoint to return an error
    await page.route(`**/api/v1/templates/${templateId}/quick-deploy`, (route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Internal server error' }),
      }),
    );

    // Mock clusters for the dialog
    await page.route(/\/api\/v1\/clusters$/, (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        });
      }
      return route.continue();
    });

    await page.goto('/templates');
    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

    // Click Quick Deploy on the template card
    const card = page.locator('.MuiCard-root').filter({ hasText: name });
    await card.getByRole('button', { name: 'Quick Deploy' }).click();

    // Dialog opens
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText(`Quick Deploy: ${name}`)).toBeVisible({ timeout: 5_000 });

    // Fill in required name
    await dialog.getByLabel('Instance Name').fill('test-retry-instance');

    // Initial deploy button text
    const deployButton = dialog.getByRole('button', { name: 'Deploy' });
    await expect(deployButton).toBeVisible();

    // Click deploy — should fail
    await deployButton.click();

    // Error displayed
    await expect(dialog.getByText(/Internal server error|Failed to deploy/)).toBeVisible({
      timeout: 10_000,
    });

    // Button label changes to "Retry"
    await expect(dialog.getByRole('button', { name: 'Retry' })).toBeVisible({ timeout: 5_000 });
  });

  test('Retry button is clickable for a second attempt', async ({ page }) => {
    const name = uniqueName('retry2');
    const templateId = await createAndPublishTemplate(page, name);

    let callCount = 0;
    await page.route(`**/api/v1/templates/${templateId}/quick-deploy`, (route) => {
      callCount++;
      return route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Internal server error' }),
      });
    });

    await page.route(/\/api\/v1\/clusters$/, (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        });
      }
      return route.continue();
    });

    await page.goto('/templates');
    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

    const card = page.locator('.MuiCard-root').filter({ hasText: name });
    await card.getByRole('button', { name: 'Quick Deploy' }).click();

    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Instance Name').fill('test-retry-again');

    // First attempt
    await dialog.getByRole('button', { name: 'Deploy' }).click();
    await expect(dialog.getByRole('button', { name: 'Retry' })).toBeVisible({ timeout: 10_000 });

    // Second attempt (retry)
    await dialog.getByRole('button', { name: 'Retry' }).click();

    // Wait for the second request to complete
    await expect(dialog.getByText(/Internal server error|Failed to deploy/)).toBeVisible({
      timeout: 10_000,
    });

    // Verify two calls were made
    expect(callCount).toBe(2);
  });

  test('Deploy button shows "Deploy" initially (no error state)', async ({ page }) => {
    const name = uniqueName('deploy-init');
    const templateId = await createAndPublishTemplate(page, name);

    await page.route(/\/api\/v1\/clusters$/, (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        });
      }
      return route.continue();
    });

    await page.goto('/templates');
    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });

    const card = page.locator('.MuiCard-root').filter({ hasText: name });
    await card.getByRole('button', { name: 'Quick Deploy' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText(`Quick Deploy: ${name}`)).toBeVisible({ timeout: 5_000 });

    // Button label should be "Deploy" initially
    await expect(dialog.getByRole('button', { name: 'Deploy' })).toBeVisible();
    // "Retry" should NOT be visible initially
    await expect(dialog.getByRole('button', { name: 'Retry' })).not.toBeVisible();
  });
});
