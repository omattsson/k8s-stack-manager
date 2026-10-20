import { useCallback, useEffect, useRef, useState } from 'react';
import { instanceService } from '../../api/client';
import { describeApiError, getApiErrorInfo } from '../../utils/apiError';

/** Time between two job log polls, in milliseconds. */
export const JOB_LOG_POLL_INTERVAL_MS = 3000;

/** Longest time a job log is followed, in milliseconds (60 minutes). */
export const JOB_LOG_MAX_POLL_MS = 60 * 60 * 1000;

/** Most log characters that are kept (about 1 MiB). Older output is cut. */
export const JOB_LOG_MAX_CHARS = 1024 * 1024;

/** Marker at the top of the log when older output was cut. */
export const JOB_LOG_CUT_MARKER = '[earlier output cut]\n';

/** Longest back-off delay without a Retry-After header, in milliseconds. */
export const JOB_LOG_MAX_BACKOFF_MS = 30000;

/** Longest delay accepted from a Retry-After header, in milliseconds. */
const MAX_RETRY_AFTER_MS = 5 * 60 * 1000;

/** HTTP statuses after which polling continues with a back-off. */
const BACKOFF_STATUSES = new Set([429, 502, 503, 504]);

const MAX_TIME_NOTICE = 'Stopped following the job log after 60 minutes. The job can still run on the server.';
const STOPPED_NOTICE = 'Stopped following the job log. The job can still run on the server.';

/**
 * Keep the last max characters of text. When a newline follows the cut
 * (and is not the last character), start after it, so the kept text has no
 * partial first line. Else drop a lone low surrogate at the start, so a
 * surrogate pair is never split.
 */
export const cutLogTail = (text: string, max: number): string => {
  if (text.length <= max) return text;
  let tail = text.slice(text.length - max);
  const nl = tail.indexOf('\n');
  if (nl >= 0 && nl < tail.length - 1) {
    tail = tail.slice(nl + 1);
  } else {
    const first = tail.charCodeAt(0);
    if (first >= 0xdc00 && first <= 0xdfff) tail = tail.slice(1);
  }
  return tail;
};

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

/** Options of {@link useJobLog}. */
export interface UseJobLogOptions {
  instanceId: string;
  actionName: string;
  /** Job to follow. Without an ID the hook does not poll. */
  jobId?: string;
  /** Delay before the first poll, in milliseconds. */
  initialDelayMs: number;
  /** Called once with the final status when the job has ended and its log is read to the end. */
  onDone?: (status: string) => void;
  /** Status to show before the first poll answers. Default: `running`. */
  initialStatus?: string;
  /** Error text for HTTP 404. Default: the server message. */
  notFoundMessage?: string;
}

/** State of a followed job log. */
export interface JobLogState {
  /** Log text, at most {@link JOB_LOG_MAX_CHARS} characters after the cut marker. */
  log: string;
  status: string;
  /** True when the job has ended and the log is read to the end. */
  done: boolean;
  /** Error that stopped the polling. */
  error: string | null;
  /** True while the polling backs off after a busy or unreachable server. */
  waiting: boolean;
  /** Info text after Stop following or the 60-minute limit. */
  notice: string | null;
  /** True while the polling continues. */
  following: boolean;
  /** Stop the polling and abort the request in flight. The job continues on the server. */
  stopFollowing: () => void;
}

/**
 * Poll the job log of an asynchronous action with the next offset until the
 * job ends. On 429, 502, 503, 504 or a network error the polling backs off
 * (the Retry-After delay, else a doubled delay up to 30 s) and continues.
 * Other errors (for example 401, 403, 404) stop it with a message. It also
 * stops on {@link JobLogState.stopFollowing}, on unmount and after
 * {@link JOB_LOG_MAX_POLL_MS}. Remount the caller to follow another job.
 *
 * @param options - The job and the timing of the first poll
 * @returns The log text, the status and the polling state
 */
export const useJobLog = ({
  instanceId,
  actionName,
  jobId,
  initialDelayMs,
  onDone,
  initialStatus = 'running',
  notFoundMessage,
}: UseJobLogOptions): JobLogState => {
  const [log, setLog] = useState('');
  const [status, setStatus] = useState(initialStatus);
  const [done, setDone] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [waiting, setWaiting] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const stopPollingRef = useRef<(() => void) | null>(null);
  const onDoneRef = useRef(onDone);

  useEffect(() => {
    onDoneRef.current = onDone;
  }, [onDone]);

  useEffect(() => {
    if (!jobId) return undefined;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let offset = 0;
    let backoff = JOB_LOG_POLL_INTERVAL_MS;
    let text = '';
    let controller: AbortController | null = null;
    const startedAt = Date.now();

    // Cancel the timers and abort the request in flight.
    const halt = () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      clearTimeout(deadline);
      controller?.abort();
    };

    const stop = () => {
      halt();
      setWaiting(false);
    };
    stopPollingRef.current = stop;

    const stopAtLimit = () => {
      stop();
      setNotice(MAX_TIME_NOTICE);
    };
    // Hard limit: also ends a request that is still in flight.
    const deadline = setTimeout(stopAtLimit, JOB_LOG_MAX_POLL_MS);

    const schedule = (delay: number) => {
      if (Date.now() - startedAt + delay > JOB_LOG_MAX_POLL_MS) {
        stopAtLimit();
        return;
      }
      timer = setTimeout(() => { void poll(); }, delay);
    };

    const appendLog = (chunkText: string) => {
      text += chunkText;
      if (text.length > JOB_LOG_MAX_CHARS) {
        text = JOB_LOG_CUT_MARKER + cutLogTail(text, JOB_LOG_MAX_CHARS);
      }
      setLog(text);
    };

    const poll = async () => {
      try {
        controller = new AbortController();
        const chunk = await instanceService.getActionJobLog(instanceId, actionName, jobId, offset, controller.signal);
        controller = null;
        if (cancelled) return;
        backoff = JOB_LOG_POLL_INTERVAL_MS;
        setWaiting(false);
        offset = chunk.next_offset;
        if (chunk.log) appendLog(chunk.log);
        setStatus(chunk.status);
        if (chunk.done) {
          halt();
          setDone(true);
          onDoneRef.current?.(chunk.status);
          return;
        }
        schedule(chunk.truncated ? 0 : JOB_LOG_POLL_INTERVAL_MS);
      } catch (err) {
        controller = null;
        if (cancelled) return;
        const { status: httpStatus } = getApiErrorInfo(err);
        if (httpStatus === undefined || BACKOFF_STATUSES.has(httpStatus)) {
          backoff = Math.min(backoff * 2, JOB_LOG_MAX_BACKOFF_MS);
          setWaiting(true);
          schedule(retryAfterMs(err) ?? backoff);
          return;
        }
        setWaiting(false);
        setError(httpStatus === 404 && notFoundMessage
          ? notFoundMessage
          : await describeApiError(err, 'Failed to read the job log'));
      }
    };
    schedule(initialDelayMs);

    return () => {
      halt();
      stopPollingRef.current = null;
    };
  }, [instanceId, actionName, jobId, initialDelayMs, notFoundMessage]);

  const stopFollowing = useCallback(() => {
    stopPollingRef.current?.();
    setNotice(STOPPED_NOTICE);
  }, []);

  const following = !!jobId && !done && !error && !notice;

  return { log, status, done, error, waiting, notice, following, stopFollowing };
};
