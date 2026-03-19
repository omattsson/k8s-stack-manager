import { Page, expect } from '@playwright/test';

/**
 * Log in as admin and wait for the dashboard to load.
 */
export async function loginAsAdmin(page: Page) {
  await page.goto('/login');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill('admin');
  await page.getByRole('button', { name: 'Sign In' }).click();
  await page.waitForURL('/', { timeout: 10_000 });
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible({ timeout: 10_000 });
}

/**
 * Generate a unique name with a prefix to avoid cross-test collisions.
 */
export function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
}

/**
 * Create a template via the UI and return its name.
 * Navigates to /templates/new, fills the form, saves, and waits for the preview page.
 */
export async function createTemplate(page: Page, name: string): Promise<string> {
  await page.goto('/templates/new');
  await page.getByLabel('Name', { exact: true }).fill(name);
  await page.getByLabel('Description').fill(`E2e test template ${name}`);
  // Pick a category
  await page.getByLabel('Category').click();
  await page.getByRole('option', { name: 'Web' }).click();
  await page.getByLabel('Version').fill('1.0.0');
  await page.getByRole('button', { name: 'Save Template' }).click();
  // Wait for navigation to the preview page
  await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });
  await expect(page.getByRole('heading', { level: 1, name })).toBeVisible({ timeout: 10_000 });
  return name;
}

/**
 * Publish a template from its preview page. Assumes the page is already on /templates/:id.
 */
export async function publishTemplate(page: Page, templateId: string) {
  await page.goto(`/templates/${templateId}/edit`);
  // Toggle the publish switch
  const publishSwitch = page.getByRole('switch');
  await publishSwitch.check();
  // Wait briefly for the publish API call
  await page.waitForTimeout(500);
}

/**
 * Create a template with a chart, publish it, and return its ID.
 * Useful as a prerequisite for definition/instance tests.
 */
export async function createAndPublishTemplate(page: Page, name: string): Promise<string> {
  // Create template
  await page.goto('/templates/new');
  await page.getByLabel('Name', { exact: true }).fill(name);
  await page.getByLabel('Description').fill(`E2e test template ${name}`);
  await page.getByLabel('Category').click();
  await page.getByRole('option', { name: 'Web' }).click();
  await page.getByLabel('Version').fill('1.0.0');

  // Add a chart
  await page.getByRole('button', { name: 'Add Chart' }).click();
  await page.getByLabel('Chart Name').fill('my-chart');
  await page.getByLabel('Repository URL').fill('https://charts.example.com');

  await page.getByRole('button', { name: 'Save Template' }).click();
  await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible({ timeout: 10_000 });

  // Extract template ID from URL
  const url = page.url();
  const templateId = url.split('/templates/')[1];

  // Navigate to edit and publish
  await page.goto(`/templates/${templateId}/edit`);
  await expect(page.getByRole('heading', { level: 1, name: /Edit Template/ })).toBeVisible({ timeout: 10_000 });
  const publishSwitch = page.getByRole('switch').first();
  await publishSwitch.check();
  await page.waitForTimeout(1000);

  // Save to persist
  await page.getByRole('button', { name: 'Save Template' }).click();
  await page.waitForURL(/\/templates\//, { timeout: 10_000 });

  return templateId;
}

/**
 * Instantiate a published template into a stack definition via the UI.
 * Returns the definition name.
 */
export async function instantiateTemplate(
  page: Page,
  templateId: string,
  defName: string,
): Promise<string> {
  await page.goto(`/templates/${templateId}/use`);
  await expect(page.getByRole('heading', { level: 1, name: /Use Template/ })).toBeVisible({
    timeout: 10_000,
  });

  // Clear the pre-filled name and set our own
  const nameField = page.getByLabel('Definition Name');
  await nameField.clear();
  await nameField.fill(defName);

  await page.getByRole('button', { name: 'Create Stack Definition' }).click();
  await page.waitForURL(/\/stack-definitions\/[^/]+\/edit/, { timeout: 10_000 });

  return defName;
}
