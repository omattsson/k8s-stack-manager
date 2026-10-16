import { formatDurationShort } from './expiry';

/** Earliest stop time: now plus this many minutes. */
export const MIN_STOP_TIME_MINUTES = 15;

/** Maximum TTL and stop-time distance when the server sends none (MAX_INSTANCE_TTL_DAYS). */
export const DEFAULT_MAX_TTL_DAYS = 30;

/**
 * The latest stop time is this many minutes before now plus the limit: the
 * browser clock can run ahead of the server, and the server refuses a time
 * after its own now plus the limit.
 */
export const MAX_STOP_TIME_MARGIN_MINUTES = 2;

const pad = (n: number) => String(n).padStart(2, '0');

/**
 * Formats a date as the value of an `<input type="datetime-local">`
 * (`YYYY-MM-DDTHH:mm` in the local time zone of the browser).
 *
 * @param date - The date to format.
 * @returns The local date and time without seconds.
 */
export function toLocalInputValue(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/**
 * Parses the value of an `<input type="datetime-local">` (local time zone)
 * to an ISO 8601 string in UTC, the format of `expires_at` and `until`.
 *
 * @param value - The input value, for example `2026-10-17T18:00`.
 * @returns The ISO string, or null when the value is empty or invalid.
 */
export function fromLocalInputValue(value: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(value)) return null;
  const date = new Date(value); // a date-time string without zone is local time
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

/** Allowed range of a stop time. */
export interface StopTimeBounds {
  min: Date;
  max: Date;
}

/**
 * The allowed range of a stop time: from now plus MIN_STOP_TIME_MINUTES (or
 * from `after`, when it is later) to now plus `maxDays`, minus
 * MAX_STOP_TIME_MARGIN_MINUTES.
 *
 * @param maxDays - MAX_INSTANCE_TTL_DAYS from the UI config.
 * @param now - The current time (for tests).
 * @param after - The stop time must be later than this time (for example the current expiry of an extend).
 * @returns The earliest and the latest allowed stop time.
 */
export function stopTimeBounds(maxDays: number, now: Date = new Date(), after?: Date | null): StopTimeBounds {
  let min = new Date(now.getTime() + MIN_STOP_TIME_MINUTES * 60_000);
  if (after && after.getTime() + 60_000 > min.getTime()) {
    min = new Date(after.getTime() + 60_000);
  }
  const days = maxDays > 0 ? maxDays : DEFAULT_MAX_TTL_DAYS;
  return { min, max: new Date(now.getTime() + days * 24 * 60 * 60_000 - MAX_STOP_TIME_MARGIN_MINUTES * 60_000) };
}

/**
 * Checks a stop time against its bounds.
 *
 * @param iso - The stop time as an ISO string, or null.
 * @param bounds - The allowed range.
 * @returns A message for the form, or null when the stop time is valid.
 */
export function stopTimeError(iso: string | null, bounds: StopTimeBounds): string | null {
  if (!iso) return 'Choose a stop date and time';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return 'Choose a stop date and time';
  // The picker has a one-minute resolution: compare whole minutes.
  const minute = 60_000;
  if (t < Math.floor(bounds.min.getTime() / minute) * minute) {
    return `Choose a time after ${formatStopTimeWhen(bounds.min)}`;
  }
  if (t > bounds.max.getTime()) {
    return `Choose a time before ${formatStopTimeWhen(bounds.max)}`;
  }
  return null;
}

/**
 * Formats the date and time of a stop time, for example "Fri, Oct 17, 18:00".
 *
 * @param date - The stop time.
 * @returns The local date and time.
 */
export function formatStopTimeWhen(date: Date): string {
  return date.toLocaleString([], { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

/**
 * Describes a stop time, for example "Fri, Oct 17, 18:00 (in 2d 6h)".
 *
 * @param iso - The stop time as an ISO string.
 * @param now - The current time (for tests).
 * @returns The text, or an empty string when the time is missing or invalid.
 */
export function formatStopTime(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return `${formatStopTimeWhen(date)} (in ${formatDurationShort((date.getTime() - now.getTime()) / 60000)})`;
}

/**
 * Whether an instance has a fixed stop time: no TTL, but an expiry time.
 *
 * @param instance - The instance (ttl_minutes and expires_at).
 * @returns true for a fixed stop time.
 */
export function hasFixedStopTime(instance: { ttl_minutes?: number; expires_at?: string | null } | null | undefined): boolean {
  return !!instance && !instance.ttl_minutes && !!instance.expires_at;
}
