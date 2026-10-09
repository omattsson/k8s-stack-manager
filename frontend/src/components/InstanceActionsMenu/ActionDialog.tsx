import { useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Typography,
} from '@mui/material';
import { instanceService } from '../../api/client';
import { describeApiError, getApiErrorInfo } from '../../utils/apiError';
import type { ActionInvokeResult, ActionParameter, InstanceAction } from '../../types';

/**
 * Time between two job log polls, in milliseconds. The first poll also waits
 * this long, so the subscriber has time to create the log.
 */
export const JOB_LOG_POLL_INTERVAL_MS = 3000;

/** Longest time the dialog follows a job log, in milliseconds (60 minutes). */
export const JOB_LOG_MAX_POLL_MS = 60 * 60 * 1000;

/** Most log characters the dialog keeps (about 1 MiB). Older output is cut. */
export const JOB_LOG_MAX_CHARS = 1024 * 1024;

/** Marker at the top of the log when older output was cut. */
export const JOB_LOG_CUT_MARKER = '[earlier output cut]\n';

/** Longest back-off delay without a Retry-After header, in milliseconds. */
export const JOB_LOG_MAX_BACKOFF_MS = 30000;

/** Longest delay accepted from a Retry-After header, in milliseconds. */
const MAX_RETRY_AFTER_MS = 5 * 60 * 1000;

/** HTTP statuses after which polling continues with a back-off. */
const BACKOFF_STATUSES = new Set([429, 502, 503, 504]);

/**
 * Read the Retry-After header (seconds or an HTTP date) of an error response.
 * @returns The delay in milliseconds, or undefined when the header is absent or invalid
 */
export const retryAfterMs = (error: unknown, now = Date.now()): number | undefined => {
  const headers = (error as { response?: { headers?: Record<string, unknown> | { get?: (k: string) => unknown } } })
    ?.response?.headers;
  if (!headers) return undefined;
  const getter = (headers as { get?: (k: string) => unknown }).get;
  const raw = typeof getter === 'function'
    ? getter.call(headers, 'retry-after')
    : (headers as Record<string, unknown>)['retry-after'] ?? (headers as Record<string, unknown>)['Retry-After'];
  if (typeof raw !== 'string' && typeof raw !== 'number') return undefined;
  const text = String(raw).trim();
  if (/^\d+$/.test(text)) return Math.min(Number(text) * 1000, MAX_RETRY_AFTER_MS);
  const date = Date.parse(text);
  if (Number.isNaN(date)) return undefined;
  return Math.min(Math.max(date - now, 0), MAX_RETRY_AFTER_MS);
};

type FormValues = Record<string, string | boolean>;

/** Initial form values from the parameter defaults. */
const initialValues = (params: ActionParameter[]): FormValues => {
  const values: FormValues = {};
  for (const p of params) {
    if (p.type === 'bool') values[p.name] = typeof p.default === 'boolean' ? p.default : false;
    else values[p.name] = typeof p.default === 'string' ? p.default : '';
  }
  return values;
};

/** True when a required parameter has no value. */
const isMissing = (p: ActionParameter, values: FormValues): boolean =>
  p.required && p.type !== 'bool' && String(values[p.name] ?? '').trim() === '';

/** Parameters to send: booleans always, strings only when not empty. */
const toParameters = (params: ActionParameter[], values: FormValues): FormValues => {
  const out: FormValues = {};
  for (const p of params) {
    const v = values[p.name];
    if (typeof v === 'boolean') out[p.name] = v;
    else if (typeof v === 'string' && v !== '') out[p.name] = v;
  }
  return out;
};

const statusColor = (status: string): 'info' | 'success' | 'error' => {
  if (status === 'succeeded') return 'success';
  if (['running', 'pending', 'queued', 'started'].includes(status)) return 'info';
  return 'error';
};

const isSuccessCode = (code: number): boolean => code >= 200 && code < 300;

interface ActionDialogProps {
  /** ID of the instance to run the action on. */
  instanceId: string;
  /** The action to run. */
  action: InstanceAction;
  /** Called when the user closes the dialog. Stops the job log polling. */
  onClose: () => void;
}

/**
 * Dialog that runs a custom action: it shows the description, the
 * confirmation text and a form for the parameters, then the result. For an
 * asynchronous action with a job log it polls the job log until the job ends
 * or the user closes the dialog.
 */
const ActionDialog = ({ instanceId, action, onClose }: ActionDialogProps) => {
  const [values, setValues] = useState<FormValues>(() => initialValues(action.parameters));
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<ActionInvokeResult | null>(null);
  const [jobLog, setJobLog] = useState('');
  const [jobStatus, setJobStatus] = useState('running');
  const [jobDone, setJobDone] = useState(false);
  const [jobError, setJobError] = useState<string | null>(null);
  const [jobWaiting, setJobWaiting] = useState(false);
  const [jobNotice, setJobNotice] = useState<string | null>(null);
  const logRef = useRef<HTMLElement | null>(null);
  const stopPollingRef = useRef<(() => void) | null>(null);

  const jobId = action.has_job_log && result?.job_id && isSuccessCode(result.status_code) ? result.job_id : undefined;
  const missing = action.parameters.some((p) => isMissing(p, values));

  // Poll the job log. The cleanup stops the polling when the dialog closes.
  // On 429, 502, 503, 504 or a network error the polling backs off (the
  // Retry-After delay, else a doubled delay up to 30 s) and continues. Other
  // errors (for example 401, 403, 404) stop the polling with a message.
  // The polling also stops on Stop and after JOB_LOG_MAX_POLL_MS.
  useEffect(() => {
    if (!jobId) return undefined;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let offset = 0;
    let backoff = JOB_LOG_POLL_INTERVAL_MS;
    let text = '';
    const startedAt = Date.now();

    const stop = () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      setJobWaiting(false);
    };
    stopPollingRef.current = stop;

    const schedule = (delay: number) => {
      if (Date.now() - startedAt + delay > JOB_LOG_MAX_POLL_MS) {
        stop();
        setJobNotice('Stopped following the job log after 60 minutes. The job can still run on the server.');
        return;
      }
      timer = setTimeout(() => { void poll(); }, delay);
    };

    const appendLog = (chunkText: string) => {
      text += chunkText;
      if (text.length > JOB_LOG_MAX_CHARS) {
        text = JOB_LOG_CUT_MARKER + text.slice(text.length - JOB_LOG_MAX_CHARS);
      }
      setJobLog(text);
    };

    const poll = async () => {
      try {
        const chunk = await instanceService.getActionJobLog(instanceId, action.name, jobId, offset);
        if (cancelled) return;
        backoff = JOB_LOG_POLL_INTERVAL_MS;
        setJobWaiting(false);
        offset = chunk.next_offset;
        if (chunk.log) appendLog(chunk.log);
        setJobStatus(chunk.status);
        if (chunk.done) {
          setJobDone(true);
          return;
        }
        schedule(chunk.truncated ? 0 : JOB_LOG_POLL_INTERVAL_MS);
      } catch (err) {
        if (cancelled) return;
        const { status } = getApiErrorInfo(err);
        if (status === undefined || BACKOFF_STATUSES.has(status)) {
          backoff = Math.min(backoff * 2, JOB_LOG_MAX_BACKOFF_MS);
          setJobWaiting(true);
          schedule(retryAfterMs(err) ?? backoff);
          return;
        }
        setJobWaiting(false);
        setJobError(await describeApiError(err, 'Failed to read the job log'));
      }
    };
    schedule(JOB_LOG_POLL_INTERVAL_MS);

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      stopPollingRef.current = null;
    };
  }, [instanceId, action.name, jobId]);

  const handleStopFollowing = () => {
    stopPollingRef.current?.();
    setJobNotice('Stopped following the job log. The job can still run on the server.');
  };

  const following = !!jobId && !jobDone && !jobError && !jobNotice;

  // Keep the newest log lines in view.
  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [jobLog]);

  const handleRun = async () => {
    setRunning(true);
    setError(null);
    try {
      const res = await instanceService.invokeAction(instanceId, action.name, toParameters(action.parameters, values));
      setResult(res);
    } catch (err) {
      setError(await describeApiError(err, `Failed to run ${action.label}`));
    } finally {
      setRunning(false);
    }
  };

  const renderField = (p: ActionParameter) => {
    if (p.type === 'bool') {
      return (
        <FormControlLabel
          key={p.name}
          control={
            <Switch
              checked={values[p.name] === true}
              onChange={(e) => setValues((v) => ({ ...v, [p.name]: e.target.checked }))}
            />
          }
          label={p.label}
        />
      );
    }
    const showMissing = isMissing(p, values);
    return (
      <TextField
        key={p.name}
        select={p.type === 'enum'}
        label={p.label}
        required={p.required}
        value={values[p.name] ?? ''}
        onChange={(e) => setValues((v) => ({ ...v, [p.name]: e.target.value }))}
        helperText={showMissing ? 'Required' : p.description}
        error={showMissing}
        size="small"
        fullWidth
      >
        {p.type === 'enum' && !p.required && <MenuItem value="">(none)</MenuItem>}
        {p.type === 'enum' && (p.options ?? []).map((o) => (
          <MenuItem key={o} value={o}>{o}</MenuItem>
        ))}
      </TextField>
    );
  };

  const renderResult = (res: ActionInvokeResult) => {
    const ok = isSuccessCode(res.status_code);
    return (
      <Stack spacing={2}>
        <Alert severity={ok ? 'success' : 'error'}>
          {ok
            ? `${action.label} returned status ${res.status_code}.`
            : `${action.label} failed: the action returned status ${res.status_code}.`}
        </Alert>
        {res.result !== null && res.result !== undefined && (
          <Box
            component="pre"
            data-testid="action-result"
            sx={{ m: 0, p: 1, bgcolor: 'action.hover', borderRadius: 1, overflow: 'auto', maxHeight: 200, fontSize: 12 }}
          >
            {JSON.stringify(res.result, null, 2)}
          </Box>
        )}
        {jobId && (
          <Box>
            <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1 }}>
              <Typography variant="subtitle2">Job {jobId}</Typography>
              <Chip size="small" label={jobStatus} color={statusColor(jobStatus)} data-testid="job-status" />
              {following && <CircularProgress size={14} aria-label="Job running" />}
              {following && jobWaiting && (
                <Typography variant="body2" color="text.secondary" data-testid="job-waiting">
                  Waiting…
                </Typography>
              )}
            </Stack>
            <Box
              component="pre"
              ref={logRef}
              data-testid="job-log"
              sx={{ m: 0, p: 1, bgcolor: 'grey.900', color: 'grey.100', borderRadius: 1, overflow: 'auto', height: 300, fontSize: 12 }}
            >
              {jobLog || 'Waiting for log output...'}
            </Box>
            {jobError && <Alert severity="error" sx={{ mt: 1 }}>{jobError}</Alert>}
            {jobNotice && <Alert severity="info" sx={{ mt: 1 }}>{jobNotice}</Alert>}
          </Box>
        )}
      </Stack>
    );
  };

  return (
    <Dialog open onClose={onClose} fullWidth maxWidth={jobId ? 'md' : 'sm'} aria-labelledby="action-dialog-title">
      <DialogTitle id="action-dialog-title">{action.label}</DialogTitle>
      <DialogContent>
        {result ? renderResult(result) : (
          <Stack spacing={2} sx={{ pt: 1 }}>
            {action.description && <DialogContentText>{action.description}</DialogContentText>}
            {action.confirm && <Alert severity="warning">{action.confirm}</Alert>}
            {action.parameters.map(renderField)}
            {error && <Alert severity="error">{error}</Alert>}
          </Stack>
        )}
      </DialogContent>
      <DialogActions>
        {result ? (
          <>
            {following && (
              <Button color="warning" onClick={handleStopFollowing}>Stop following</Button>
            )}
            <Button onClick={onClose}>Close</Button>
          </>
        ) : (
          <>
            <Button onClick={onClose} disabled={running}>Cancel</Button>
            <Button
              variant="contained"
              color={action.confirm ? 'warning' : 'primary'}
              onClick={handleRun}
              disabled={running || missing}
              startIcon={running ? <CircularProgress size={14} /> : undefined}
            >
              {running ? 'Running...' : 'Run'}
            </Button>
          </>
        )}
      </DialogActions>
    </Dialog>
  );
};

export default ActionDialog;
