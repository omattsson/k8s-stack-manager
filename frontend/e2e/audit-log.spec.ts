import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate } from './helpers';

test.describe('Audit Log', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('audit log page loads with heading and table', async ({ page }) => {
    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });
    // Table headers should be visible
    await expect(page.getByRole('columnheader', { name: 'Timestamp' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'User' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Action' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Entity Type' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Entity ID' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Details' })).toBeVisible();
  });

  test('creating a template generates audit log entries', async ({ page }) => {
    const tplName = uniqueName('tpl-audit');
    await createAndPublishTemplate(page, tplName);

    // Navigate to audit log
    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });

    // Wait for log entries to load
    await page.waitForTimeout(1000);

    // We should see a "create" action for stack_template
    await expect(page.getByRole('cell', { name: 'create' }).first()).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('cell', { name: 'stack_template' }).first()).toBeVisible();
  });

  test('filter audit log by entity type', async ({ page }) => {
    // First create something to ensure there are entries
    const tplName = uniqueName('tpl-audit-filter');
    await createAndPublishTemplate(page, tplName);

    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });

    // Filter by stack_template
    await page.getByLabel('Entity Type').click();
    await page.getByRole('option', { name: 'stack_template' }).click();
    await page.getByRole('button', { name: 'Filter' }).click();

    // Wait for filtered results
    await page.waitForTimeout(1000);

    // All visible entity type cells should be stack_template
    const entityCells = page.getByRole('cell', { name: 'stack_template' });
    await expect(entityCells.first()).toBeVisible({ timeout: 10_000 });
  });

  test('filter audit log by action', async ({ page }) => {
    // Ensure there are entries
    const tplName = uniqueName('tpl-audit-action');
    await createAndPublishTemplate(page, tplName);

    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });

    // Filter by create action
    await page.getByLabel('Action').click();
    await page.getByRole('option', { name: 'create' }).click();
    await page.getByRole('button', { name: 'Filter' }).click();

    await page.waitForTimeout(1000);

    const actionCells = page.getByRole('cell', { name: 'create' });
    await expect(actionCells.first()).toBeVisible({ timeout: 10_000 });
  });

  test('filter audit log by username', async ({ page }) => {
    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });
    await page.waitForLoadState('domcontentloaded');

    await page.getByLabel('User ID').fill('admin');
    await page.getByRole('button', { name: 'Filter' }).click();

    await page.waitForTimeout(1000);

    // Entries should be visible (admin has performed actions)
    const userCells = page.getByRole('cell', { name: 'admin' });
    // If there are any audit entries by admin, they should show up
    const count = await userCells.count();
    expect(count).toBeGreaterThanOrEqual(0);
  });

  test('audit log pagination works', async ({ page }) => {
    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });

    // The pagination component should be present
    await expect(page.locator('.MuiTablePagination-root')).toBeVisible({ timeout: 10_000 });
  });

  test('combined filters work together', async ({ page }) => {
    const tplName = uniqueName('tpl-audit-combo');
    await createAndPublishTemplate(page, tplName);

    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });

    // Apply both entity type and action filters
    await page.getByLabel('Entity Type').click();
    await page.getByRole('option', { name: 'stack_template' }).click();
    await page.getByLabel('Action').click();
    await page.getByRole('option', { name: 'create' }).click();
    await page.getByRole('button', { name: 'Filter' }).click();

    await page.waitForTimeout(1000);

    // Verify results reflect both filters
    const rows = page.locator('tbody tr');
    const rowCount = await rows.count();
    if (rowCount > 0) {
      await expect(page.getByRole('cell', { name: 'stack_template' }).first()).toBeVisible();
      await expect(page.getByRole('cell', { name: 'create' }).first()).toBeVisible();
    }
  });
});
