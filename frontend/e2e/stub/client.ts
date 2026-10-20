import { createHmac } from 'node:crypto';
import type { APIRequestContext } from '@playwright/test';

/**
 * Ports of the e2e stub (e2e/stub/server.ts). They are fixed, because
 * e2e/fixtures/hooks-e2e.json has the action URLs with port 18090.
 */
export const STUB_HTTP_PORT = 18090;
export const STUB_HTTPS_PORT = 18443;
export const STUB_HTTP_URL = `http://127.0.0.1:${STUB_HTTP_PORT}`;
export const STUB_HTTPS_URL = `https://127.0.0.1:${STUB_HTTPS_PORT}`;

/**
 * Secret of the signed actions in hooks-e2e.json (secret_env E2E_ACTION_SECRET).
 * The Makefile gives the backend the same value.
 */
export const E2E_ACTION_SECRET = process.env.E2E_ACTION_SECRET || 'e2e-action-secret';

/**
 * The backend trusts the stub certificate. `make test-e2e` sets E2E_STUB_TLS=1
 * on Linux, where Go reads SSL_CERT_DIR. On macOS Go uses the system keychain,
 * so the specs that need a successful HTTPS delivery skip there.
 */
export const STUB_TLS_TRUSTED = process.env.E2E_STUB_TLS === '1';

/** One request that the stub received. */
export interface StubRecord {
  at: number;
  method: string;
  /** The request target as received: path and query (the signed request URI of a job log poll). */
  url: string;
  path: string;
  query: Record<string, string>;
  /** Header names in lower case. */
  headers: Record<string, string>;
  body: string;
}

/** Health of the stub: null when it does not run. */
export async function stubHealth(request: APIRequestContext): Promise<{ https: boolean } | null> {
  try {
    const res = await request.get(`${STUB_HTTP_URL}/__health`, { timeout: 3000 });
    return res.ok() ? await res.json() : null;
  } catch {
    return null;
  }
}

/** The requests whose path starts with the prefix, oldest first. */
export async function stubRecords(request: APIRequestContext, prefix: string): Promise<StubRecord[]> {
  const res = await request.get(`${STUB_HTTP_URL}/__records?prefix=${encodeURIComponent(prefix)}`);
  if (!res.ok()) throw new Error(`stub records: HTTP ${res.status()}`);
  return res.json();
}

/** The X-StackManager-Signature value for a body: sha256=<hex HMAC-SHA256>. */
export function expectedSignature(body: string, secret: string): string {
  return `sha256=${createHmac('sha256', secret).update(body).digest('hex')}`;
}
