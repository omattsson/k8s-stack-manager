import { useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  List,
  ListItem,
  ListItemButton,
  MenuItem,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import { instanceService } from '../../api/client';
import type { DeploymentLog, DeployLogFilters } from '../../types';
import type { DeployHistory } from '../../hooks/useDeployHistory';
import { liveLogLines } from '../../utils/liveLogLines';
import { timeAgo } from '../../utils/timeAgo';
import {
  describeDeployRun,
  fallbackHighlights,
  formatDuration,
  groupDeployRuns,
  pinnedRows,
} from '../../utils/deployHistory';
import type { PinnedLog } from '../../utils/deployHistory';

interface DeploymentHistoryProps {
  instanceId: string;
  /** State and actions of useDeployHistory. */
  history: DeployHistory;
  /** Lines that arrived over the WebSocket, by log ID. */
  streamingLines?: Record<string, string[]>;
  /** ID of the deploy whose values run now. It gets a "current" label and no rollback action. */
  currentDeployLogId?: string;
  /** When set, successful deploys (except the current one) offer "Roll back to this deploy". */
  onRollbackTo?: (log: DeploymentLog) => void;
}

const ACTION_OPTIONS: { value: '' | DeploymentLog['action']; label: string }[] = [
  { value: '', label: 'All' },
  { value: 'deploy', label: 'Deploy' },
  { value: 'rollback', label: 'Rollback' },
  { value: 'stop', label: 'Stop' },
  { value: 'clean', label: 'Clean' },
];

const STATUS_OPTIONS: { value: '' | DeploymentLog['status']; label: string }[] = [
  { value: '', label: 'All' },
  { value: 'running', label: 'Running' },
  { value: 'success', label: 'Success' },
  { value: 'partial', label: 'Partial' },
  { value: 'error', label: 'Failed' },
];

const statusColor = (status: string): 'success' | 'warning' | 'error' | 'info' | 'default' => {
  switch (status) {
    case 'success': return 'success';
    // Same color as the instance status "partial" (StatusBadge).
    case 'partial': return 'warning';
    case 'error': return 'error';
    case 'running': return 'info';
    default: return 'default';
  }
};

const actionColor = (action: string): 'primary' | 'warning' | 'error' | 'info' => {
  switch (action) {
    case 'deploy': return 'primary';
    case 'stop': return 'warning';
    case 'clean': return 'error';
    case 'rollback': return 'info';
    default: return 'primary';
  }
};

const pinColor = (kind: PinnedLog['kind']): 'info' | 'success' | 'error' => {
  if (kind === 'running') return 'info';
  if (kind === 'current') return 'success';
  return 'error';
};

const terminalSx = {
  p: 2,
  bgcolor: 'background.paper',
  color: 'text.primary',
  fontFamily: 'monospace',
  fontSize: '0.8rem',
  lineHeight: 1.6,
  maxHeight: 300,
  overflow: 'auto',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
} as const;

const liveChipSx = {
  animation: 'pulse 2s infinite',
  '@keyframes pulse': {
    '0%, 100%': { opacity: 1 },
    '50%': { opacity: 0.5 },
  },
} as const;

/** Logs with output that do not change any more, by instance and log ID. */
type EntryCache = Map<string, DeploymentLog>;

interface LogOutputProps {
  instanceId: string;
  log: DeploymentLog;
  cache: EntryCache;
  streamed?: string[];
  isCurrent: boolean;
  currentKnown: boolean;
  onRollbackTo?: (log: DeploymentLog) => void;
}

/**
 * Output of one log. Loads the log once with its output; a finished log is
 * cached, a running log is loaded again on each open.
 */
const LogOutput = ({ instanceId, log, cache, streamed, isCurrent, currentKnown, onRollbackTo }: LogOutputProps) => {
  const { id: logId, status } = log;
  const running = status === 'running';
  const cacheKey = `${instanceId}:${logId}`;
  // A list row of an older server has the output already.
  const preloaded = running ? undefined : cache.get(cacheKey) ?? (log.output !== undefined ? log : undefined);
  const needsFetch = !preloaded;
  const [entry, setEntry] = useState<DeploymentLog | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);

  // status: a finished running log is loaded again for its final output.
  useEffect(() => {
    if (!needsFetch) return;
    let stale = false;
    setLoading(true);
    setFailed(false);
    instanceService.getDeployLogEntry(instanceId, logId)
      .then((loaded) => {
        if (stale) return;
        if (loaded.status !== 'running') cache.set(cacheKey, loaded);
        setEntry(loaded);
      })
      .catch(() => { if (!stale) setFailed(true); })
      .finally(() => { if (!stale) setLoading(false); });
    return () => { stale = true; };
  }, [needsFetch, instanceId, logId, status, cache, cacheKey]);

  const shown = preloaded ?? entry;
  const isLoading = !preloaded && loading;

  // A running log shows its stored output (the server writes it every few
  // seconds, so it survives a page reload) and then the streamed lines.
  const lines = running ? liveLogLines(shown?.output ?? log.output, streamed) : [];

  // Keep the newest streamed line in view.
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines.length]);

  const errorMessage = shown?.error_message || log.error_message;
  const canRollbackHere = Boolean(onRollbackTo)
    && log.action === 'deploy' && log.status === 'success' && !isCurrent;

  let body: ReactNode;
  if (running) {
    body = lines.length > 0 ? (
      <Box ref={scrollRef} sx={terminalSx}>
        {lines.map((line, i) => (
          <div key={`line-${i}`}>{line || ' '}</div>
        ))}
      </Box>
    ) : (
      <Box sx={{ p: 2, display: 'flex', alignItems: 'center', gap: 1 }}>
        {isLoading && <CircularProgress size={16} aria-label="Loading the log" />}
        <Typography variant="body2" color="text.secondary">Waiting for output...</Typography>
      </Box>
    );
  } else if (isLoading) {
    body = (
      <Box sx={{ p: 2, display: 'flex', alignItems: 'center', gap: 1 }}>
        <CircularProgress size={16} aria-label="Loading the log" />
        <Typography variant="body2" color="text.secondary">Loading the log...</Typography>
      </Box>
    );
  } else if (shown?.output) {
    body = (
      <Box sx={terminalSx}>
        {shown.output}
        {errorMessage && (
          <Box sx={{ color: 'error.main', mt: 1 }}>
            Error: {errorMessage}
          </Box>
        )}
      </Box>
    );
  } else {
    body = (
      <Box sx={{ p: 2 }}>
        {failed && (
          <Alert severity="error" sx={{ mb: errorMessage ? 1 : 0 }}>Could not load the log output.</Alert>
        )}
        {!failed && (
          <Typography variant="body2" color="text.secondary">No output recorded</Typography>
        )}
        {errorMessage && (
          <Typography variant="body2" sx={{ color: 'error.main', fontFamily: 'monospace', mt: 1, wordBreak: 'break-word' }}>
            Error: {errorMessage}
          </Typography>
        )}
      </Box>
    );
  }

  return (
    <>
      {canRollbackHere && (
        <Box sx={{ px: 2, pt: 1, display: 'flex', justifyContent: 'flex-end' }}>
          <Tooltip
            describeChild
            title={currentKnown ? '' : 'The running values are unknown after a rollback without a target.'}
          >
            <Button size="small" variant="outlined" color="warning" onClick={() => onRollbackTo?.(log)}>
              Roll back to this deploy
            </Button>
          </Tooltip>
        </Box>
      )}
      {body}
    </>
  );
};

interface HistoryRowProps {
  instanceId: string;
  log: DeploymentLog;
  /** Where the row is: the pinned area or the list. Element IDs and test IDs use it. */
  scope: 'pinned' | 'list';
  pin?: PinnedLog;
  open: boolean;
  onToggle: () => void;
  cache: EntryCache;
  streamed?: string[];
  isCurrent: boolean;
  currentKnown: boolean;
  onRollbackTo?: (log: DeploymentLog) => void;
}

/** One dense row: time, action, status, branch, duration, user. It opens the output. */
const HistoryRow = ({
  instanceId, log, scope, pin, open, onToggle, cache, streamed, isCurrent, currentKnown, onRollbackTo,
}: HistoryRowProps) => {
  const absolute = new Date(log.started_at).toLocaleString();
  const ago = timeAgo(log.started_at) || absolute;
  const duration = log.completed_at ? formatDuration(log.started_at, log.completed_at) : '';
  // LIVE: lines arrive over the WebSocket now.
  const isLive = log.status === 'running' && (streamed?.length ?? 0) > 0;
  const showCurrent = isCurrent && pin?.kind !== 'current';
  const prefix = scope === 'pinned' ? 'pinned-log' : 'deploy-log';
  const panelId = `${prefix}-panel-${log.id}`;

  // The name starts with "<action> <status>", so a row is easy to find.
  const name = [
    `${log.action} ${log.status}`,
    pin?.label.toLowerCase(),
    showCurrent ? 'current' : undefined,
    isLive ? 'live' : undefined,
    log.branch ? `branch ${log.branch}` : undefined,
    `started ${ago}`,
    duration ? `took ${duration}` : undefined,
    log.started_by ? `by ${log.started_by}` : undefined,
  ].filter(Boolean).join(', ');

  return (
    <ListItem disablePadding sx={{ display: 'block' }} data-testid={`${prefix}-row-${log.id}`}>
      <ListItemButton
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={name}
        onClick={onToggle}
        sx={{ px: 1, py: 0.5, gap: 1, borderRadius: 1 }}
      >
        <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', columnGap: 1, rowGap: 0.5, flex: 1, minWidth: 0 }}>
          {pin && <Chip label={pin.label} size="small" color={pinColor(pin.kind)} />}
          <Tooltip title={absolute}>
            <Typography variant="body2" color="text.secondary" sx={{ minWidth: { sm: 64 } }}>{ago}</Typography>
          </Tooltip>
          <Chip label={log.action} size="small" color={actionColor(log.action)} variant="outlined" />
          <Chip label={log.status} size="small" color={statusColor(log.status)} />
          {showCurrent && <Chip label="current" size="small" color="success" variant="outlined" />}
          {isLive && <Chip label="LIVE" size="small" color="info" variant="filled" sx={liveChipSx} />}
          {log.branch && (
            <Typography
              variant="body2"
              noWrap
              title={log.branch}
              sx={{ fontFamily: 'monospace', minWidth: 0, maxWidth: { xs: '100%', sm: 260 } }}
            >
              {log.branch}
            </Typography>
          )}
          <Box sx={{ display: 'flex', gap: 1.5, ml: 'auto', minWidth: 0 }}>
            {duration && <Typography variant="caption" color="text.secondary">{duration}</Typography>}
            {log.started_by && (
              <Typography variant="caption" color="text.secondary" noWrap sx={{ minWidth: 0 }}>
                {log.started_by}
              </Typography>
            )}
          </Box>
        </Box>
        <ExpandMoreIcon
          fontSize="small"
          sx={{ color: 'text.secondary', transition: 'transform 150ms', transform: open ? 'rotate(180deg)' : 'none' }}
        />
      </ListItemButton>
      {open && (
        <Box
          id={panelId}
          role="region"
          aria-label={`Output of ${log.action} ${log.status}, ${absolute}`}
          data-testid={`${prefix}-output-${log.id}`}
          sx={{ borderLeft: 2, borderColor: 'divider', ml: 1, mb: 1 }}
        >
          <LogOutput
            instanceId={instanceId}
            log={log}
            cache={cache}
            streamed={streamed}
            isCurrent={isCurrent}
            currentKnown={currentKnown}
            onRollbackTo={onRollbackTo}
          />
        </Box>
      )}
    </ListItem>
  );
};

/**
 * Deployment history of an instance: pinned important operations, a compact
 * list with folded runs of successful deploys, filters and "Show more".
 * Hidden while the unfiltered history is empty.
 */
const DeploymentHistory = ({
  instanceId,
  history,
  streamingLines,
  currentDeployLogId,
  onRollbackTo,
}: DeploymentHistoryProps) => {
  const { rows, total, highlights, filters, filtersActive, hasMore, loading, loadingMore, error } = history;
  const cacheRef = useRef<EntryCache>(new Map());
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const autoOpenedRef = useRef<Set<string>>(new Set());

  const pinned = useMemo(
    () => pinnedRows(highlights ?? fallbackHighlights(rows, currentDeployLogId)),
    [highlights, rows, currentDeployLogId],
  );
  const items = useMemo(() => groupDeployRuns(rows, currentDeployLogId), [rows, currentDeployLogId]);

  // The running operation opens once, so its live log shows.
  const runningId = pinned.find((p) => p.kind === 'running')?.log.id;
  useEffect(() => {
    if (!runningId || autoOpenedRef.current.has(runningId)) return;
    autoOpenedRef.current.add(runningId);
    setOpen((prev) => new Set(prev).add(`pinned:${runningId}`));
  }, [runningId]);

  if (!history.hasHistory && !filtersActive && !error) return null;

  const toggle = (key: string) => setOpen((prev) => {
    const next = new Set(prev);
    if (next.has(key)) next.delete(key); else next.add(key);
    return next;
  });

  const changeFilter = (change: DeployLogFilters) => history.setFilters({ ...filters, ...change });

  const renderRow = (log: DeploymentLog, scope: 'pinned' | 'list', pin?: PinnedLog) => {
    const key = `${scope}:${log.id}`;
    return (
      <HistoryRow
        key={key}
        instanceId={instanceId}
        log={log}
        scope={scope}
        pin={pin}
        open={open.has(key)}
        onToggle={() => toggle(key)}
        cache={cacheRef.current}
        streamed={streamingLines?.[log.id]}
        isCurrent={log.id === currentDeployLogId}
        currentKnown={Boolean(currentDeployLogId)}
        onRollbackTo={onRollbackTo}
      />
    );
  };

  const countText = loading && rows.length === 0
    ? ''
    : filtersActive ? ` (${total} matching)` : ` (${total})`;

  return (
    <Box component="section" aria-labelledby="deployment-history-title" data-testid="deployment-history" sx={{ mb: 3 }}>
      <Box sx={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 1, mb: 1 }}>
        <Typography variant="h6" component="h2" id="deployment-history-title" sx={{ flexGrow: 1, minWidth: 0 }}>
          Deployment History{countText}
        </Typography>
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
          <TextField
            select
            size="small"
            label="Action"
            value={filters.action ?? ''}
            onChange={(e) => changeFilter({ action: (e.target.value || undefined) as DeployLogFilters['action'] })}
            sx={{ minWidth: 130 }}
            data-testid="deployment-history-action-filter"
          >
            {ACTION_OPTIONS.map((o) => <MenuItem key={o.label} value={o.value}>{o.label}</MenuItem>)}
          </TextField>
          <TextField
            select
            size="small"
            label="Status"
            value={filters.status ?? ''}
            onChange={(e) => changeFilter({ status: (e.target.value || undefined) as DeployLogFilters['status'] })}
            sx={{ minWidth: 130 }}
            data-testid="deployment-history-status-filter"
          >
            {STATUS_OPTIONS.map((o) => <MenuItem key={o.label} value={o.value}>{o.label}</MenuItem>)}
          </TextField>
        </Box>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 1 }}>{error}</Alert>}

      {pinned.length > 0 && (
        <List
          dense
          disablePadding
          aria-label="Pinned operations"
          data-testid="deployment-history-pinned"
          sx={{ mb: 1, p: 0.5, border: 1, borderColor: 'divider', borderRadius: 1 }}
        >
          {pinned.map((p) => renderRow(p.log, 'pinned', p))}
        </List>
      )}

      {loading && rows.length === 0 ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', p: 2 }}>
          <CircularProgress size={24} aria-label="Loading the deployment history" />
        </Box>
      ) : rows.length === 0 ? (
        !error && (
          <Typography variant="body2" color="text.secondary" sx={{ p: 1 }}>
            {filtersActive ? 'No operations match the filters.' : 'No deployment history'}
          </Typography>
        )
      ) : (
        <List dense disablePadding aria-label="Operations" data-testid="deployment-history-list">
          {items.map((item) => {
            if (item.kind === 'log') return renderRow(item.log, 'list');
            const key = `group:${item.key}`;
            const isOpen = open.has(key);
            const label = describeDeployRun(item.logs, item.branch);
            const groupId = `deploy-log-group-panel-${item.key}`;
            return (
              <ListItem key={key} disablePadding sx={{ display: 'block' }} data-testid={`deploy-log-group-${item.key}`}>
                <ListItemButton
                  aria-expanded={isOpen}
                  aria-controls={groupId}
                  onClick={() => toggle(key)}
                  sx={{ px: 1, py: 0.5, gap: 1, borderRadius: 1 }}
                >
                  <Typography variant="body2" sx={{ flex: 1, minWidth: 0 }}>{label}</Typography>
                  <ExpandMoreIcon
                    fontSize="small"
                    sx={{ color: 'text.secondary', transition: 'transform 150ms', transform: isOpen ? 'rotate(180deg)' : 'none' }}
                  />
                </ListItemButton>
                {isOpen && (
                  <List dense disablePadding id={groupId} aria-label={label} sx={{ pl: 2 }}>
                    {item.logs.map((log) => renderRow(log, 'list'))}
                  </List>
                )}
              </ListItem>
            );
          })}
        </List>
      )}

      {hasMore && (
        <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 2, mt: 1 }}>
          <Button
            size="small"
            variant="outlined"
            onClick={() => { history.loadMore(); }}
            disabled={loadingMore || loading}
            data-testid="deployment-history-show-more"
          >
            {loadingMore ? 'Loading...' : 'Show more'}
          </Button>
          <Typography variant="body2" color="text.secondary">
            Showing {rows.length} of {total}
          </Typography>
        </Box>
      )}
    </Box>
  );
};

export default DeploymentHistory;
