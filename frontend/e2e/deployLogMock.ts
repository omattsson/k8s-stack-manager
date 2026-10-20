import type { Page, Request } from '@playwright/test';

/** One deployment log as the API returns it (full view). */
export interface MockDeployLog {
  id: string;
  stack_instance_id: string;
  action: 'deploy' | 'rollback' | 'stop' | 'clean';
  status: 'running' | 'success' | 'error' | 'partial';
  output?: string;
  error_message?: string;
  branch?: string;
  target_log_id?: string;
  user_id?: string;
  started_by?: string;
  started_at: string;
  completed_at?: string;
}

const newestFirst = (a: MockDeployLog, b: MockDeployLog) =>
  b.started_at.localeCompare(a.started_at) || b.id.localeCompare(a.id);

/** The row columns of view=summary: the list never sends the output. */
const summary = (log: MockDeployLog): MockDeployLog => {
  const row = { ...log };
  delete row.output;
  return row;
};

/**
 * The highlights of the backend: the newest running log, the deploy whose
 * values run now (also a partial deploy; for a newer successful rollback, its
 * target), and the newest failure when it is newer than that anchor.
 */
function highlights(logs: MockDeployLog[]) {
  const running = logs.find((l) => l.status === 'running');
  const lastSuccess = logs.find((l) =>
    (l.action === 'deploy' || l.action === 'rollback') && (l.status === 'success' || l.status === 'partial'));
  const current = lastSuccess?.action === 'rollback'
    ? logs.find((l) => l.id === lastSuccess.target_log_id)
    : lastSuccess;
  const failure = logs.find((l) => l.status === 'error');
  const latestFailure = failure && (!lastSuccess || failure.started_at > lastSuccess.started_at) ? failure : undefined;
  return {
    ...(running && { running: summary(running) }),
    ...(current && { current: summary(current) }),
    ...(latestFailure && { latest_failure: summary(latestFailure) }),
  };
}

const csv = (value: string | null) => (value ? value.split(',').map((v) => v.trim()).filter(Boolean) : []);

/**
 * Mocks GET /stack-instances/:id/deploy-log (view, action, status, limit,
 * cursor, highlights) and GET /stack-instances/:id/deploy-log/:logId as the
 * backend answers them. `logs` is read on each request, so a test can change
 * the history. Every list request goes into `requests` when it is given.
 */
export async function mockDeployLog(
  page: Page,
  instanceId: string,
  logs: () => MockDeployLog[],
  requests?: Request[],
) {
  await page.route(new RegExp(`/api/v1/stack-instances/${instanceId}/deploy-log(/[^/?]+)?(\\?.*)?$`), (route) => {
    const url = new URL(route.request().url());
    const all = [...logs()].sort(newestFirst);

    const entryId = /\/deploy-log\/([^/]+)$/.exec(url.pathname)?.[1];
    if (entryId) {
      const entry = all.find((l) => l.id === decodeURIComponent(entryId));
      return entry
        ? route.fulfill({ json: entry })
        : route.fulfill({ status: 404, json: { error: 'Deployment log not found for this instance' } });
    }

    requests?.push(route.request());
    const isSummary = url.searchParams.get('view') === 'summary';
    const actions = csv(url.searchParams.get('action'));
    const statuses = csv(url.searchParams.get('status'));
    const filtered = all.filter((l) =>
      (actions.length === 0 || actions.includes(l.action)) && (statuses.length === 0 || statuses.includes(l.status)));

    const limit = Number(url.searchParams.get('limit')) || 50;
    const cursor = url.searchParams.get('cursor');
    const offset = cursor ? Number(cursor) : Number(url.searchParams.get('offset')) || 0;
    const page_ = filtered.slice(offset, offset + limit);
    const more = offset + page_.length < filtered.length;

    return route.fulfill({
      json: {
        data: isSummary ? page_.map(summary) : page_,
        total: cursor ? -1 : filtered.length,
        ...(more && { next_cursor: String(offset + page_.length) }),
        ...(isSummary && !cursor && { highlights: highlights(all) }),
      },
    });
  });
}
