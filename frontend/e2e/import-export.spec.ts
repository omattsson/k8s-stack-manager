import { test, expect } from '@playwright/test';
import {
  loginAsDevops,
  uniqueName,
  createAndPublishTemplate,
  instantiateTemplate,
} from './helpers';

test.describe('Import / Export Stack Definitions', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('definitions list page shows Import button', async ({ page }) => {
    await page.goto('/stack-definitions');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('button', { name: 'Import' })).toBeVisible({ timeout: 10_000 });
  });

  test('clicking Import opens the import dialog', async ({ page }) => {
    await page.goto('/stack-definitions');
    await expect(page.getByRole('button', { name: 'Import' })).toBeVisible({ timeout: 10_000 });

    await page.getByRole('button', { name: 'Import' }).click();
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Import Stack Definition')).toBeVisible();
  });

  test('import dialog has file select button and action buttons', async ({ page }) => {
    await page.goto('/stack-definitions');
    await page.getByRole('button', { name: 'Import' }).click();

    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 });

    // File select button
    await expect(page.getByRole('button', { name: 'Select File' })).toBeVisible();

    // Action buttons
    await expect(page.getByRole('button', { name: 'Cancel' })).toBeVisible();
    // Import button should be disabled until a file is selected
    const importBtn = page.getByRole('button', { name: 'Import', exact: true }).last();
    await expect(importBtn).toBeVisible();
    await expect(importBtn).toBeDisabled();
  });

  test('cancel closes the import dialog', async ({ page }) => {
    await page.goto('/stack-definitions');
    await page.getByRole('button', { name: 'Import' }).click();
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 });

    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 5_000 });
  });

  test('definition edit page shows Export button', async ({ page }) => {
    const tplName = uniqueName('tpl-exp');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-exp');
    await instantiateTemplate(page, templateId, defName);

    // We should be on the edit page after instantiation
    await expect(page.getByRole('heading', { level: 1, name: /Edit Stack Definition/ })).toBeVisible({
      timeout: 10_000,
    });

    await expect(page.getByRole('button', { name: 'Export' })).toBeVisible({ timeout: 10_000 });
  });

  test('Export button triggers download', async ({ page }) => {
    const tplName = uniqueName('tpl-dl');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-dl');
    await instantiateTemplate(page, templateId, defName);

    await expect(page.getByRole('heading', { level: 1, name: /Edit Stack Definition/ })).toBeVisible({
      timeout: 10_000,
    });

    // Listen for the download event
    const downloadPromise = page.waitForEvent('download', { timeout: 15_000 });
    await page.getByRole('button', { name: 'Export' }).click();

    const download = await downloadPromise;
    expect(download.suggestedFilename()).toContain('-export.json');
  });

  test('import with valid JSON file creates a new definition', async ({ page }) => {
    const importedName = uniqueName('imported-def');
    const exportBundle = JSON.stringify({
      schema_version: '1.0',
      definition: {
        name: importedName,
        description: 'Imported via E2E test',
        default_branch: 'main',
      },
      charts: [
        {
          chart_name: 'test-chart',
          repository_url: 'https://charts.example.com',
        },
      ],
    });

    await page.goto('/stack-definitions');
    await expect(page.getByRole('button', { name: 'Import' })).toBeVisible({ timeout: 10_000 });
    await page.getByRole('button', { name: 'Import' }).click();
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 });

    // Use the hidden file input by setting its content programmatically
    const fileInput = page.locator('input[type="file"]');
    // Create a temporary file-like buffer from the JSON string
    await fileInput.setInputFiles({
      name: 'test-import.json',
      mimeType: 'application/json',
      buffer: Buffer.from(exportBundle),
    });

    // Preview should appear with the definition name
    await expect(page.getByText(importedName)).toBeVisible({ timeout: 10_000 });

    // Click Import in dialog
    const importBtn = page.getByRole('button', { name: 'Import', exact: true }).last();
    await expect(importBtn).toBeEnabled({ timeout: 5_000 });
    await importBtn.click();

    // Should navigate to the edit page after successful import
    await page.waitForURL(/\/stack-definitions\/[^/]+\/edit/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: /Edit Stack Definition/ })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('import with invalid JSON file shows error', async ({ page }) => {
    await page.goto('/stack-definitions');
    await page.getByRole('button', { name: 'Import' }).click();
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 10_000 });

    const fileInput = page.locator('input[type="file"]');
    await fileInput.setInputFiles({
      name: 'bad-import.json',
      mimeType: 'application/json',
      buffer: Buffer.from('this is not valid json {{{'),
    });

    // Should show a parse error
    await expect(page.getByText(/Invalid JSON/i)).toBeVisible({ timeout: 10_000 });
  });
});
