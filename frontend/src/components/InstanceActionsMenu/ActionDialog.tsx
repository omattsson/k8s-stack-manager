import { useState } from 'react';
import {
  Alert,
  Button,
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
import JsonResultView from '../JsonResultView';
import { describeApiError } from '../../utils/apiError';
import type { ActionInvokeResult, ActionParameter, InstanceAction } from '../../types';
import JobLogPanel from './JobLogPanel';
import { JOB_LOG_POLL_INTERVAL_MS, useJobLog } from './useJobLog';

export {
  JOB_LOG_CUT_MARKER,
  JOB_LOG_MAX_BACKOFF_MS,
  JOB_LOG_MAX_CHARS,
  JOB_LOG_MAX_POLL_MS,
  JOB_LOG_POLL_INTERVAL_MS,
  cutLogTail,
  retryAfterMs,
} from './useJobLog';

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

const isSuccessCode = (code: number): boolean => code >= 200 && code < 300;

/**
 * Read the message of an action subscriber result: the result text, or the
 * first text field of error, message, detail or reason.
 * @param result - The JSON body of the action subscriber
 * @returns The message, or undefined when the result has none
 */
export const resultMessage = (result: unknown): string | undefined => {
  if (typeof result === 'string') return result.trim() || undefined;
  if (!result || typeof result !== 'object' || Array.isArray(result)) return undefined;
  const obj = result as Record<string, unknown>;
  for (const key of ['error', 'message', 'detail', 'reason']) {
    const v = obj[key];
    if (typeof v === 'string' && v.trim()) return v.trim();
  }
  return undefined;
};

interface ActionDialogProps {
  /** ID of the instance to run the action on. */
  instanceId: string;
  /** The action to run. */
  action: InstanceAction;
  /** Called when the user closes the dialog. Stops the job log polling. */
  onClose: () => void;
  /** Called after each invoke attempt, also after an error. */
  onInvoked?: () => void;
  /** Called when the job log of the run reaches its end. */
  onJobDone?: () => void;
}

/**
 * Dialog that runs a custom action: it shows the description, the
 * confirmation text and a form for the parameters, then the result. For an
 * asynchronous action with a job log it polls the job log until the job ends
 * or the user closes the dialog.
 */
const ActionDialog = ({ instanceId, action, onClose, onInvoked, onJobDone }: ActionDialogProps) => {
  const [values, setValues] = useState<FormValues>(() => initialValues(action.parameters));
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<ActionInvokeResult | null>(null);

  const jobId = action.has_job_log && result?.job_id && isSuccessCode(result.status_code) ? result.job_id : undefined;
  const missing = action.parameters.some((p) => isMissing(p, values));
  // The first poll waits one interval, so the subscriber has time to create the log.
  const job = useJobLog({
    instanceId,
    actionName: action.name,
    jobId,
    initialDelayMs: JOB_LOG_POLL_INTERVAL_MS,
    onDone: onJobDone,
  });

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
      onInvoked?.();
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
    const sensitive = p.type === 'string' && p.sensitive === true;
    return (
      <TextField
        key={p.name}
        select={p.type === 'enum'}
        label={p.label}
        required={p.required}
        type={sensitive ? 'password' : undefined}
        autoComplete={sensitive ? 'new-password' : undefined}
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
    // The server message comes first; older servers omit it, then read the result.
    const message = ok ? undefined : (res.message?.trim() || resultMessage(res.result));
    return (
      <Stack spacing={2}>
        <Alert severity={ok ? 'success' : 'error'} data-testid="action-status">
          {ok
            ? `${action.label} returned status ${res.status_code}.`
            : `${action.label} failed: the action returned status ${res.status_code}.`}
          {message && (
            <Typography variant="body2" sx={{ mt: 0.5, fontWeight: 500 }} data-testid="action-message">
              {message}
            </Typography>
          )}
        </Alert>
        {res.result !== null && res.result !== undefined && (
          <JsonResultView value={res.result} data-testid="action-result" />
        )}
        {jobId && <JobLogPanel jobId={jobId} job={job} />}
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
            {job.following && (
              <Button color="warning" onClick={job.stopFollowing}>Stop following</Button>
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
