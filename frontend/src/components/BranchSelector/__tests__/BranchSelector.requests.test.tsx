import { StrictMode } from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen } from '@testing-library/react';

// The real gitService (shared requests, cancellation) on a mocked axios.
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

vi.mock('axios', () => ({
  default: {
    create: vi.fn(() => mockApi),
    isAxiosError: vi.fn(() => false),
  },
}));

import BranchSelector from '../index';
import { clearBranchCache } from '../../../api/client';

interface BranchRequestConfig {
  params: { repo: string };
  signal: AbortSignal;
}

const A = 'https://github.com/org/a';
const B = 'https://github.com/org/b';
const C = 'https://github.com/org/c';

/** Pending branch requests by repository URL (one per request sent). */
let requests: Array<{ repo: string; signal: AbortSignal; resolve: () => void }>;

const branchRequests = (repo: string) => requests.filter((r) => r.repo === repo);

/** Let the deferred abort of unused shared requests run. */
const flushDeferredAborts = () => act(async () => {
  await new Promise((r) => setTimeout(r, 0));
});

describe('BranchSelector with the shared branch requests', () => {
  beforeEach(() => {
    clearBranchCache();
    requests = [];
    mockApi.get.mockImplementation((url: string, config?: BranchRequestConfig) => {
      if (url === '/api/v1/git/providers') {
        return Promise.resolve({ data: [{ type: 'github', available: true }] });
      }
      const { repo } = config!.params;
      return new Promise((resolve, reject) => {
        requests.push({
          repo,
          signal: config!.signal,
          resolve: () => resolve({ data: [{ name: `${repo.slice(-1)}-main` }] }),
        });
        config!.signal.addEventListener('abort', () => reject(new Error('canceled')));
      });
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('keeps the request of an unchanged URL when the URL list changes (StrictMode)', async () => {
    const props = { value: 'main', onChange: vi.fn() };
    const { rerender, unmount } = render(
      <StrictMode>
        <BranchSelector {...props} repoUrl={[A, B]} />
      </StrictMode>,
    );
    await flushDeferredAborts();

    // The StrictMode remount joins the requests in flight instead of
    // restarting them.
    expect(branchRequests(A)).toHaveLength(1);
    expect(branchRequests(B)).toHaveLength(1);

    rerender(
      <StrictMode>
        <BranchSelector {...props} repoUrl={[A, C]} />
      </StrictMode>,
    );
    await flushDeferredAborts();

    // A goes on, B lost its last caller, C is new.
    expect(branchRequests(A)).toHaveLength(1);
    expect(branchRequests(A)[0].signal.aborted).toBe(false);
    expect(branchRequests(B)[0].signal.aborted).toBe(true);
    expect(branchRequests(C)).toHaveLength(1);

    await act(async () => {
      branchRequests(A)[0].resolve();
      branchRequests(C)[0].resolve();
    });
    expect(await screen.findByText('GitHub available.')).toBeInTheDocument();
    expect(screen.queryByText(/Could not load branches/)).not.toBeInTheDocument();
    unmount();
  });

  it('aborts the shared requests after unmount', async () => {
    const { unmount } = render(
      <BranchSelector value="main" onChange={vi.fn()} repoUrl={[A, B]} />,
    );
    await flushDeferredAborts();
    expect(requests).toHaveLength(2);

    unmount();
    // The abort waits for the deferral, so a remount could still join.
    expect(requests.every((r) => !r.signal.aborted)).toBe(true);
    await flushDeferredAborts();
    expect(requests.every((r) => r.signal.aborted)).toBe(true);
  });
});
