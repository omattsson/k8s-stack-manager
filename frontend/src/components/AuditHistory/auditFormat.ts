import type { AuditLog } from '../../types';

export interface DetailRow {
  label: string;
  value: string;
}

interface AuditChange {
  field: string;
  old?: unknown;
  new?: unknown;
  redacted?: boolean;
}

const CONTEXT_FIELDS: Array<[keyof AuditLog, string]> = [
  ['client_ip', 'Client IP'],
  ['request_id', 'Request ID'],
  ['trace_id', 'Trace ID'],
  ['auth_method', 'Auth method'],
  ['api_key_id', 'API key ID'],
];

const show = (v: unknown): string => (v === undefined || v === null ? '(none)' : String(v));

function changeValue(c: AuditChange): string {
  if (c.redacted) return 'changed (hidden)';
  if (c.old === undefined && c.new === undefined) return 'changed';
  return `${show(c.old)} to ${show(c.new)}`;
}

/**
 * Returns the label and value rows of an audit entry: the request context
 * that is set, then the details. A change set gives one row per field.
 * Details that are not a JSON object give one "Details" row.
 */
export function detailRows(log: AuditLog): DetailRow[] {
  const rows: DetailRow[] = [];
  for (const [key, label] of CONTEXT_FIELDS) {
    const v = log[key];
    if (v) rows.push({ label, value: String(v) });
  }
  if (!log.details) return rows;
  let parsed: unknown;
  try {
    parsed = JSON.parse(log.details);
  } catch {
    parsed = undefined;
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    rows.push({ label: 'Details', value: log.details });
    return rows;
  }
  for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
    if (key === 'changes' && Array.isArray(value)) {
      for (const c of value as AuditChange[]) rows.push({ label: c.field, value: changeValue(c) });
      continue;
    }
    rows.push({ label: key, value: typeof value === 'object' ? JSON.stringify(value) : String(value) });
  }
  return rows;
}
