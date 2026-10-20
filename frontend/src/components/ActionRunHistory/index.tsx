import { useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  List,
  ListItem,
  Tooltip,
  Typography,
} from '@mui/material';
import { instanceService } from '../../api/client';
import type { ActionRun, InstanceAction } from '../../types';
import { describeApiError } from '../../utils/apiError';
import { timeAgo } from '../../utils/timeAgo';
import { actionRunStatus, applyFirstPage, applyNextPage, runParameterEntries } from '../../utils/actionRuns';
import type { RunListState } from '../../utils/actionRuns';
import JobLogDialog from '../InstanceActionsMenu/JobLogDialog';

/** Rows per page of the action run list. */
export const ACTION_RUNS_PAGE_SIZE = 10;

interface ActionRunHistoryProps {
  instanceId: string;
  /** A new value loads the first page again and merges it into the shown rows. */
  refreshKey: number;
  /** Hide the section while it has no rows (true when the server lists no actions). */
  hideWhenEmpty: boolean;
  /** Actions of the instance, for the declared parameter order. */
  actions?: InstanceAction[];
}

interface LoadError {
  /** Which load failed: the first page or "Show more". */
  kind: 'first' | 'more';
  message: string;
}

interface RunRowProps {
  run: ActionRun;
  action?: InstanceAction;
  canViewLogs: boolean;
  onShowLog: (run: ActionRun) => void;
}

/** One action run: time, label, status, user, parameters and message. */
const RunRow = ({ run, action, canViewLogs, onShowLog }: RunRowProps) => {
  const absolute = new Date(run.started_at).toLocaleString();
  const ago = timeAgo(run.started_at) || absolute;
  const chip = actionRunStatus(run);
  const params = runParameterEntries(run, action);
  const label = run.action_label || run.action;
  const user = run.started_by || 'Unknown user';

  return (
    <ListItem
      disablePadding
      sx={{ display: 'block', px: 1, py: 0.75, borderBottom: 1, borderColor: 'divider', '&:last-of-type': { borderBottom: 0 } }}
      data-testid={`action-run-row-${run.id}`}
    >
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', columnGap: 1, rowGap: 0.5, minWidth: 0 }}>
        <Tooltip title={absolute}>
          <Typography variant="body2" color="text.secondary" sx={{ minWidth: { sm: 64 } }}>{ago}</Typography>
        </Tooltip>
        <Typography variant="body2" sx={{ fontWeight: 500, minWidth: 0, overflowWrap: 'anywhere' }}>{label}</Typography>
        <Chip size="small" label={chip.label} color={chip.color} data-testid={`action-run-status-${run.id}`} />
        <Typography variant="caption" color="text.secondary" sx={{ minWidth: 0, overflowWrap: 'anywhere' }}>
          {user}
        </Typography>
        {canViewLogs && run.has_job_log && run.job_id && (
          <Button
            size="small"
            variant="outlined"
            sx={{ ml: 'auto' }}
            onClick={() => onShowLog(run)}
            aria-label={`Show log of ${label}, ${ago}`}
            data-testid={`action-run-show-log-${run.id}`}
          >
            Show log
          </Button>
        )}
      </Box>
      {params.length > 0 && (
        <List
          dense
          disablePadding
          aria-label={`Parameters of ${label}`}
          data-testid={`action-run-params-${run.id}`}
          sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5, mt: 0.5 }}
        >
          {params.map(([name, value]) => (
            <ListItem key={name} disablePadding sx={{ width: 'auto', maxWidth: '100%' }}>
              <Typography
                variant="caption"
                sx={{
                  fontFamily: 'monospace',
                  overflowWrap: 'anywhere',
                  bgcolor: 'action.hover',
                  borderRadius: 0.5,
                  px: 0.75,
                  py: 0.25,
                }}
              >
                {`${name}: ${value}`}
              </Typography>
            </ListItem>
          ))}
        </List>
      )}
      {run.message && (
        <Typography
          variant="body2"
          color="error"
          sx={{ mt: 0.5, overflowWrap: 'anywhere' }}
          data-testid={`action-run-message-${run.id}`}
        >
          {run.message}
        </Typography>
      )}
    </ListItem>
  );
};

/**
 * Stored runs of the custom actions of an instance, newest first, with
 * "Show more" and the job log of a past run. Only the owner, a devops user
 * and an admin see "Show log".
 */
const ActionRunHistory = ({ instanceId, refreshKey, hideWhenEmpty, actions }: ActionRunHistoryProps) => {
  const [list, setList] = useState<RunListState>({ rows: [], pages: 0, canViewLogs: false });
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<LoadError | null>(null);
  const [reloadCount, setReloadCount] = useState(0);
  const [logRun, setLogRun] = useState<ActionRun | null>(null);
  const moreRef = useRef<AbortController | null>(null);

  // A new refreshKey keeps the loaded pages and the cursor of the last one.
  useEffect(() => {
    const controller = new AbortController();
    instanceService
      .listActionRuns(instanceId, { limit: ACTION_RUNS_PAGE_SIZE, signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted) return;
        setList((prev) => applyFirstPage(prev, page));
        setError(null);
      })
      .catch(async (err: unknown) => {
        if (controller.signal.aborted) return;
        const message = await describeApiError(err, 'Failed to load the action runs');
        if (!controller.signal.aborted) setError({ kind: 'first', message });
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      controller.abort();
      // A "Show more" page from before the reload would follow an old cursor.
      if (moreRef.current) {
        moreRef.current.abort();
        moreRef.current = null;
        setLoadingMore(false);
      }
    };
  }, [instanceId, refreshKey, reloadCount]);

  const loadMore = async () => {
    if (!list.cursor) return;
    moreRef.current?.abort();
    const controller = new AbortController();
    moreRef.current = controller;
    setLoadingMore(true);
    setError(null);
    try {
      const page = await instanceService.listActionRuns(instanceId, {
        limit: ACTION_RUNS_PAGE_SIZE,
        cursor: list.cursor,
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      setList((prev) => applyNextPage(prev, page));
    } catch (err) {
      if (controller.signal.aborted) return;
      const message = await describeApiError(err, 'Failed to load more action runs');
      if (!controller.signal.aborted) setError({ kind: 'more', message });
    } finally {
      if (moreRef.current === controller) {
        moreRef.current = null;
        setLoadingMore(false);
      }
    }
  };

  const retry = () => {
    if (error?.kind === 'more') {
      void loadMore();
      return;
    }
    setError(null);
    setLoading(true);
    setReloadCount((n) => n + 1);
  };

  const patchJobStatus = (runId: string, status: string) => {
    setList((prev) => ({
      ...prev,
      rows: prev.rows.map((r) => (r.id === runId ? { ...r, job_status: status } : r)),
    }));
  };

  const { rows, cursor, canViewLogs } = list;
  if (hideWhenEmpty && rows.length === 0) return null;

  const actionByName = new Map((actions ?? []).map((a) => [a.name, a]));

  return (
    <Box component="section" aria-labelledby="action-runs-title" data-testid="action-runs" sx={{ mb: 3 }}>
      <Typography variant="h6" component="h2" id="action-runs-title" sx={{ mb: 1 }}>
        Action runs
      </Typography>

      {error && (
        <Alert
          severity="error"
          sx={{ mb: 1 }}
          action={(
            <Button color="inherit" size="small" onClick={retry} disabled={loading || loadingMore}>
              Retry
            </Button>
          )}
        >
          {error.message}
        </Alert>
      )}

      {loading && rows.length === 0 ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', p: 2 }}>
          <CircularProgress size={24} aria-label="Loading the action runs" />
        </Box>
      ) : rows.length === 0 ? (
        !error && (
          <Typography variant="body2" color="text.secondary" sx={{ p: 1 }}>
            No action runs yet.
          </Typography>
        )
      ) : (
        <List
          dense
          disablePadding
          aria-label="Action runs"
          data-testid="action-runs-list"
          sx={{ border: 1, borderColor: 'divider', borderRadius: 1 }}
        >
          {rows.map((run) => (
            <RunRow
              key={run.id}
              run={run}
              action={actionByName.get(run.action)}
              canViewLogs={canViewLogs}
              onShowLog={setLogRun}
            />
          ))}
        </List>
      )}

      {cursor && (
        <Box sx={{ mt: 1 }}>
          <Button
            size="small"
            variant="outlined"
            onClick={() => { void loadMore(); }}
            disabled={loadingMore}
            data-testid="action-runs-show-more"
          >
            {loadingMore ? 'Loading...' : 'Show more'}
          </Button>
        </Box>
      )}

      {logRun?.job_id && (
        <JobLogDialog
          key={logRun.id}
          instanceId={instanceId}
          actionName={logRun.action}
          label={logRun.action_label || logRun.action}
          jobId={logRun.job_id}
          initialStatus={logRun.job_status}
          onDone={(status) => patchJobStatus(logRun.id, status)}
          onClose={() => setLogRun(null)}
        />
      )}
    </Box>
  );
};

export default ActionRunHistory;
