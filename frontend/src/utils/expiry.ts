/**
 * Formats a duration in minutes as a short text, for example "5h 12m" or "2d 3h".
 *
 * @param totalMinutes - Duration in minutes.
 * @returns The short duration text.
 */
export function formatDurationShort(totalMinutes: number): string {
  const minutes = Math.max(0, Math.round(totalMinutes));
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const mins = minutes % 60;
  if (days > 0) return `${days}d ${hours}h`;
  return `${hours}h ${mins}m`;
}

/**
 * Describes a new expiry time, for example "Expires 14:30 (in 5h 12m)".
 * Adds the date when the expiry is not today.
 *
 * @param expiresAt - The expiry as an ISO 8601 string.
 * @param now - The current time (for tests).
 * @returns The expiry text, or an empty string when the expiry is missing or invalid.
 */
export function formatExpiry(expiresAt: string | undefined | null, now: Date = new Date()): string {
  if (!expiresAt) return '';
  const expiry = new Date(expiresAt);
  if (Number.isNaN(expiry.getTime())) return '';
  const sameDay = expiry.toDateString() === now.toDateString();
  const when = sameDay
    ? expiry.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : expiry.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  const remaining = formatDurationShort((expiry.getTime() - now.getTime()) / 60000);
  return `Expires ${when} (in ${remaining})`;
}
