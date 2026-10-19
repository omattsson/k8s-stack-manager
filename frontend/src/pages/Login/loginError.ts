import { getApiErrorInfo } from '../../utils/apiError';

export const INVALID_CREDENTIALS_MESSAGE = 'Invalid username or password';
export const RATE_LIMITED_MESSAGE = 'Too many sign-in attempts. Wait a minute and try again.';
export const SERVER_ERROR_MESSAGE = 'Sign-in failed because of a server error. Try again later.';
export const NETWORK_ERROR_MESSAGE = 'Cannot reach the server. Check your connection and try again.';

/** Longest Retry-After delay that the message names, in seconds (longer values use the generic text). */
const MAX_RETRY_AFTER_SECONDS = 60 * 60;

/**
 * Read the Retry-After header (seconds or an HTTP date) of an error response.
 * @param error - The thrown error (usually an AxiosError)
 * @param now - Current time in milliseconds (for tests)
 * @returns The delay in whole seconds, or undefined when the header is absent, invalid or too long
 */
export function retryAfterSeconds(error: unknown, now = Date.now()): number | undefined {
  const headers = (error as { response?: { headers?: unknown } } | null)?.response?.headers;
  if (!headers || typeof headers !== 'object') return undefined;
  const getter = (headers as { get?: unknown }).get;
  const record = headers as Record<string, unknown>;
  const raw: unknown = typeof getter === 'function'
    ? (getter as (name: string) => unknown).call(headers, 'retry-after')
    : record['retry-after'] ?? record['Retry-After'];
  if (typeof raw !== 'string' && typeof raw !== 'number') return undefined;
  const text = String(raw).trim();
  let seconds: number;
  if (/^\d+$/.test(text)) {
    seconds = Number(text);
  } else {
    const date = Date.parse(text);
    if (Number.isNaN(date)) return undefined;
    seconds = Math.ceil((date - now) / 1000);
  }
  if (seconds <= 0 || seconds > MAX_RETRY_AFTER_SECONDS) return undefined;
  return seconds;
}

/** Describe a wait in seconds as "N seconds" or "N minutes" (rounded up). */
function describeWait(seconds: number): string {
  if (seconds < 60) return seconds === 1 ? '1 second' : `${seconds} seconds`;
  const minutes = Math.ceil(seconds / 60);
  return minutes === 1 ? '1 minute' : `${minutes} minutes`;
}

/**
 * Map a failed sign-in request to a user-facing message. Only a 401 says that
 * the credentials are wrong; a server failure, the rate limiter or a network
 * error must not make the user retry the password.
 * @param error - The error that the login call threw (usually an AxiosError)
 * @returns The message for the error Alert
 */
export function describeLoginError(error: unknown): string {
  const { status, message } = getApiErrorInfo(error);
  if (status === undefined) return NETWORK_ERROR_MESSAGE;
  if (status === 401) return INVALID_CREDENTIALS_MESSAGE;
  if (status === 403 && message) return message;
  if (status === 429) {
    const wait = retryAfterSeconds(error);
    return wait === undefined
      ? RATE_LIMITED_MESSAGE
      : `Too many sign-in attempts. Wait ${describeWait(wait)} and try again.`;
  }
  if (status >= 500) return SERVER_ERROR_MESSAGE;
  return INVALID_CREDENTIALS_MESSAGE;
}

/**
 * Map a failed SSO start (GET authorize URL) to a user-facing message. A
 * rate limit, a server failure or a network error gets the matching text;
 * anything else keeps the generic SSO message.
 * @param error - The error that loginWithOIDC threw
 * @returns The message for the error Alert
 */
export function describeSsoError(error: unknown): string {
  const { status } = getApiErrorInfo(error);
  const hasResponse = (error as { response?: unknown } | null)?.response !== undefined;
  const isRequestError = (error as { isAxiosError?: unknown } | null)?.isAxiosError === true;
  if (!hasResponse && isRequestError) return NETWORK_ERROR_MESSAGE;
  if (status === 429) return RATE_LIMITED_MESSAGE;
  if (status !== undefined && status >= 500) return SERVER_ERROR_MESSAGE;
  return 'Failed to initiate SSO login. Please try again.';
}
