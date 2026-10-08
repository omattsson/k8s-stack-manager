import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

interface FakeRequest {
  url: string;
  headers: Record<string, string>;
  _retry?: boolean;
}

interface FakeError {
  config: FakeRequest;
  response: { status: number };
}

type ErrorHandler = (error: FakeError) => Promise<unknown>;

// One fake axios instance serves both `api` (client.ts) and `refreshApi` (refresh.ts).
const mocks = vi.hoisted(() => {
  const handlers: { onError: ErrorHandler | null } = { onError: null };
  const instance = Object.assign(vi.fn(), {
    post: vi.fn(),
    get: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
    interceptors: {
      request: { use: vi.fn() },
      response: {
        use: vi.fn((_ok: unknown, onError: ErrorHandler) => {
          handlers.onError = onError;
        }),
      },
    },
  });
  return { handlers, instance };
});

vi.mock('axios', () => ({
  default: {
    create: vi.fn(() => mocks.instance),
    isAxiosError: vi.fn(() => false),
  },
}));

import '../client';

function fakeJwt(payload: Record<string, unknown>): string {
  const header = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const body = btoa(JSON.stringify(payload));
  return `${header}.${body}.fakesig`;
}

function tokenWithExp(offsetSeconds: number): string {
  return fakeJwt({ user_id: '42', username: 'alice', role: 'user', exp: Math.floor(Date.now() / 1000) + offsetSeconds });
}

function unauthorized(url: string, token: string): FakeError {
  return {
    config: { url, headers: { Authorization: `Bearer ${token}` } },
    response: { status: 401 },
  };
}

function onError(error: FakeError): Promise<unknown> {
  if (!mocks.handlers.onError) throw new Error('response interceptor not registered');
  return mocks.handlers.onError(error);
}

describe('api 401 interceptor', () => {
  const originalLocation = globalThis.location;
  let fakeLocation: { pathname: string; href: string };

  beforeEach(() => {
    localStorage.clear();
    mocks.instance.mockReset();
    mocks.instance.post.mockReset();
    fakeLocation = { pathname: '/templates', href: 'http://localhost/templates' };
    Object.defineProperty(globalThis, 'location', { value: fakeLocation, configurable: true, writable: true });
  });

  afterEach(() => {
    Object.defineProperty(globalThis, 'location', { value: originalLocation, configurable: true, writable: true });
  });

  it('sends one refresh for two concurrent 401s and retries both requests', async () => {
    const expired = tokenWithExp(-60);
    const fresh = tokenWithExp(900);
    localStorage.setItem('token', expired);
    let resolveRefresh: (value: { data: { token: string } }) => void = () => {};
    mocks.instance.post.mockReturnValueOnce(new Promise((resolve) => { resolveRefresh = resolve; }));
    mocks.instance.mockResolvedValue({ data: 'ok' });

    const first = onError(unauthorized('/api/v1/templates', expired));
    const second = onError(unauthorized('/api/v1/stack-instances', expired));
    resolveRefresh({ data: { token: fresh } });
    const results = await Promise.all([first, second]);

    expect(mocks.instance.post).toHaveBeenCalledTimes(1);
    expect(mocks.instance.post).toHaveBeenCalledWith('/api/v1/auth/refresh');
    expect(results).toEqual([{ data: 'ok' }, { data: 'ok' }]);
    expect(mocks.instance).toHaveBeenCalledTimes(2);
    for (const [request] of mocks.instance.mock.calls) {
      expect(request.headers.Authorization).toBe(`Bearer ${fresh}`);
      expect(request._retry).toBe(true);
    }
    expect(localStorage.getItem('token')).toBe(fresh);
  });

  it('retries with a token that another tab already stored, without a refresh call', async () => {
    const old = tokenWithExp(-60);
    const otherTab = tokenWithExp(900);
    localStorage.setItem('token', otherTab);
    mocks.instance.mockResolvedValue({ data: 'ok' });

    await onError(unauthorized('/api/v1/templates', old));

    expect(mocks.instance.post).not.toHaveBeenCalled();
    expect(mocks.instance.mock.calls[0][0].headers.Authorization).toBe(`Bearer ${otherTab}`);
  });

  it('removes the token and redirects to /login when the refresh fails (session revoked)', async () => {
    const expired = tokenWithExp(-60);
    localStorage.setItem('token', expired);
    const refreshError = { response: { status: 401, data: { error: 'Session revoked' } } };
    mocks.instance.post.mockRejectedValueOnce(refreshError);

    await expect(onError(unauthorized('/api/v1/templates', expired))).rejects.toBe(refreshError);

    expect(localStorage.getItem('token')).toBeNull();
    expect(fakeLocation.href).toBe('/login');
    expect(mocks.instance).not.toHaveBeenCalled();
  });

  it('removes the token and redirects to /login when the refresh answers 403', async () => {
    const expired = tokenWithExp(-60);
    localStorage.setItem('token', expired);
    const refreshError = { response: { status: 403 } };
    mocks.instance.post.mockRejectedValueOnce(refreshError);

    await expect(onError(unauthorized('/api/v1/templates', expired))).rejects.toBe(refreshError);

    expect(localStorage.getItem('token')).toBeNull();
    expect(fakeLocation.href).toBe('/login');
  });

  it.each([
    ['a 500 answer', { response: { status: 500 } }],
    ['a network error', new Error('Network Error')],
  ])('keeps the token and does not redirect when the refresh fails with %s', async (_label, refreshError) => {
    const expired = tokenWithExp(-60);
    localStorage.setItem('token', expired);
    mocks.instance.post.mockRejectedValueOnce(refreshError);

    await expect(onError(unauthorized('/api/v1/templates', expired))).rejects.toBe(refreshError);

    expect(localStorage.getItem('token')).toBe(expired);
    expect(fakeLocation.href).toBe('http://localhost/templates');
    expect(mocks.instance).not.toHaveBeenCalled();
  });

  it('retries the refresh on the next 401 after a network error', async () => {
    const expired = tokenWithExp(-60);
    const fresh = tokenWithExp(900);
    localStorage.setItem('token', expired);
    mocks.instance.post
      .mockRejectedValueOnce(new Error('Network Error'))
      .mockResolvedValueOnce({ data: { token: fresh } });
    mocks.instance.mockResolvedValue({ data: 'ok' });

    await expect(onError(unauthorized('/api/v1/templates', expired))).rejects.toThrow('Network Error');
    await expect(onError(unauthorized('/api/v1/templates', expired))).resolves.toEqual({ data: 'ok' });

    expect(mocks.instance.post).toHaveBeenCalledTimes(2);
    expect(localStorage.getItem('token')).toBe(fresh);
  });

  it('does not redirect again when already on /login', async () => {
    fakeLocation.pathname = '/login';
    fakeLocation.href = 'http://localhost/login';
    const expired = tokenWithExp(-60);
    localStorage.setItem('token', expired);
    const refreshError = { response: { status: 401 } };
    mocks.instance.post.mockRejectedValueOnce(refreshError);

    await expect(onError(unauthorized('/api/v1/templates', expired))).rejects.toBe(refreshError);
    expect(localStorage.getItem('token')).toBeNull();

    expect(fakeLocation.href).toBe('http://localhost/login');
  });

  it('does not refresh again for a retried request that still gets 401', async () => {
    const token = tokenWithExp(900);
    localStorage.setItem('token', token);
    const error = unauthorized('/api/v1/templates', token);
    error.config._retry = true;

    await expect(onError(error)).rejects.toBe(error);

    expect(mocks.instance.post).not.toHaveBeenCalled();
    expect(localStorage.getItem('token')).toBeNull();
    expect(fakeLocation.href).toBe('/login');
  });

  it('does not refresh for 401s from auth endpoints', async () => {
    const error = unauthorized('/api/v1/auth/login', 'x');

    await expect(onError(error)).rejects.toBe(error);

    expect(mocks.instance.post).not.toHaveBeenCalled();
  });
});
