import { test, expect } from '@playwright/test';
import { ADMIN_PASSWORD } from './helpers';

/**
 * Creates a fake JWT token suitable for testing the frontend auth flow.
 * The frontend decodes JWTs with atob() (no signature verification),
 * so a fake signature is sufficient for unit-level E2E tests.
 */
function createFakeJwt(payload: Record<string, unknown>): string {
  const encode = (obj: unknown) => Buffer.from(JSON.stringify(obj)).toString('base64');
  return [encode({ alg: 'HS256', typ: 'JWT' }), encode(payload), 'fakesig'].join('.');
}

const OIDC_CONFIG_ENABLED = {
  enabled: true,
  provider_name: 'Microsoft',
  local_auth_enabled: false,
};

const OIDC_CONFIG_DISABLED = {
  enabled: false,
  provider_name: '',
  local_auth_enabled: true,
};

test.describe('OIDC Authentication', () => {
  test('shows only SSO button when OIDC is enabled', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    await page.goto('/login');

    await expect(
      page.getByRole('button', { name: /sign in with microsoft/i })
    ).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(/sign in with your organization account/i)).toBeVisible();
    await expect(page.getByLabel('Username')).not.toBeVisible();
    await expect(page.getByLabel('Password')).not.toBeVisible();
  });

  test('shows only local login when OIDC is disabled', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_DISABLED),
      });
    });

    await page.goto('/login');

    await expect(page.getByLabel('Username')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByLabel('Password')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible();
    await expect(
      page.getByRole('button', { name: /sign in with/i })
    ).not.toBeVisible();
  });

  test('SSO button triggers OIDC authorization flow', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    await page.route('**/api/v1/auth/oidc/authorize', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          redirect_url: 'https://idp.example.com/authorize?client_id=test&state=abc123',
        }),
      });
    });

    await page.route('https://idp.example.com/**', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: '<html><body>Mock IdP Login Page</body></html>',
      });
    });

    await page.goto('/login');
    await expect(
      page.getByRole('button', { name: /sign in with microsoft/i })
    ).toBeVisible({ timeout: 10_000 });

    const [authorizeRequest] = await Promise.all([
      page.waitForRequest('**/api/v1/auth/oidc/authorize'),
      page.getByRole('button', { name: /sign in with microsoft/i }).click(),
    ]);

    expect(authorizeRequest.url()).toContain('/api/v1/auth/oidc/authorize');
  });

  test('auth callback stores token and redirects to dashboard', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    const futureExp = Math.floor(Date.now() / 1000) + 3600;
    const fakeToken = createFakeJwt({
      user_id: '1',
      username: 'admin',
      role: 'admin',
      exp: futureExp,
    });

    await page.goto(`/auth/callback#token=${encodeURIComponent(fakeToken)}&redirect=%2F`);

    await expect(page).toHaveURL('/', { timeout: 10_000 });
  });

  test('auth callback shows error for invalid_state', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    await page.goto('/auth/callback?error=invalid_state');

    await expect(page.getByRole('alert')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(/session expired/i)).toBeVisible();
    await expect(page.getByRole('link', { name: /back to login/i })).toBeVisible();
  });

  test('auth callback shows error for no_account', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    await page.goto('/auth/callback?error=no_account');

    await expect(page.getByRole('alert')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(/no account found/i)).toBeVisible();
  });

  test('auth callback shows generic error for unknown error code', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    await page.goto('/auth/callback?error=unexpected_error_code');

    await expect(page.getByRole('alert')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(/something went wrong/i)).toBeVisible();
  });

  test('?local=true shows local login form even when OIDC is enabled', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    await page.goto('/login?local=true');

    await expect(page.getByLabel('Username')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByLabel('Password')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign In', exact: true })).toBeVisible();
    await expect(
      page.getByRole('button', { name: /sign in with/i })
    ).not.toBeVisible();
  });

  test('service account login works via ?local=true', async ({ page }) => {
    await page.route('**/api/v1/auth/oidc/config', (route) => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(OIDC_CONFIG_ENABLED),
      });
    });

    await page.goto('/login?local=true');
    await expect(page.getByLabel('Username')).toBeVisible({ timeout: 10_000 });

    await page.getByLabel('Username').fill('admin');
    await page.getByLabel('Password').fill(ADMIN_PASSWORD);
    await page.getByRole('button', { name: 'Sign In', exact: true }).click();

    await expect(page).toHaveURL('/', { timeout: 10_000 });
  });
});
