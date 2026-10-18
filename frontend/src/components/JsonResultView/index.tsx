import { useState } from 'react';
import type { ReactNode } from 'react';
import {
  Box,
  FormControlLabel,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';

type JsonPrimitive = string | number | boolean | null;
type JsonObject = Record<string, unknown>;

const isPrimitive = (v: unknown): v is JsonPrimitive =>
  v === null || ['string', 'number', 'boolean'].includes(typeof v);

const isPlainObject = (v: unknown): v is JsonObject =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

/**
 * Turn a JSON key into a label: "last_refresh_at" gives "Last refresh at".
 * @param key - The JSON key
 * @returns The label
 */
export const humanizeKey = (key: string): string => {
  const text = key.replace(/[_-]+/g, ' ').replace(/([a-z])([A-Z])/g, '$1 $2').trim().toLowerCase();
  return text ? text.charAt(0).toUpperCase() + text.slice(1) : key;
};

/**
 * Columns of an array that shows as a table: every item is an object whose
 * values are all primitives. The columns are the keys in first-seen order.
 * @param items - The array
 * @returns The column keys, or null when the array is not uniform
 */
export const tableColumns = (items: unknown[]): string[] | null => {
  if (items.length === 0) return null;
  const columns: string[] = [];
  for (const item of items) {
    if (!isPlainObject(item)) return null;
    for (const [k, v] of Object.entries(item)) {
      if (!isPrimitive(v)) return null;
      if (!columns.includes(k)) columns.push(k);
    }
  }
  return columns.length > 0 ? columns : null;
};

const formatPrimitive = (v: JsonPrimitive | undefined): string => {
  if (v === null || v === undefined) return '-';
  if (typeof v === 'boolean') return v ? 'Yes' : 'No';
  return String(v);
};

const PrimitiveText = ({ value }: { value: JsonPrimitive | undefined }) => (
  <Typography
    component="span"
    variant="body2"
    sx={{ wordBreak: 'break-word', color: value === null || value === undefined ? 'text.secondary' : 'text.primary' }}
  >
    {formatPrimitive(value)}
  </Typography>
);

/** Most rows or items shown per object or array. The rest is in the raw JSON. */
export const MAX_ITEMS = 100;

/** Deepest nesting level shown as a readable view. Deeper values show as raw JSON. */
export const MAX_DEPTH = 6;

/** Note for the rows or items that are not shown. */
const MoreNote = ({ count }: { count: number }) =>
  count > 0 ? (
    <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }} data-testid="json-more">
      {count} more. See Raw JSON.
    </Typography>
  ) : null;

/**
 * A titled block for a nested value. Only the top level is a labelled
 * section (region); deeper levels use a plain heading.
 */
const Section = ({ title, landmark, children }: { title: string; landmark: boolean; children: ReactNode }) => (
  <Box
    component={landmark ? 'section' : 'div'}
    aria-label={landmark ? title : undefined}
    sx={{ mt: 1 }}
  >
    <Typography variant="subtitle2" sx={{ mb: 0.5 }}>{title}</Typography>
    <Box sx={{ pl: 1.5, borderLeft: 2, borderColor: 'divider' }}>{children}</Box>
  </Box>
);

const RawSubtree = ({ value }: { value: unknown }) => (
  <Box component="pre" data-testid="json-subtree-raw" sx={{ m: 0, fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
    {JSON.stringify(value, null, 2)}
  </Box>
);

const ArrayView = ({ items, depth }: { items: unknown[]; depth: number }) => {
  if (items.length === 0) {
    return <Typography variant="body2" color="text.secondary">No items</Typography>;
  }
  const shown = items.slice(0, MAX_ITEMS);
  const more = items.length - shown.length;
  if (items.every(isPrimitive)) {
    return (
      <>
        <PrimitiveText value={shown.map((i) => formatPrimitive(i as JsonPrimitive)).join(', ')} />
        <MoreNote count={more} />
      </>
    );
  }
  const columns = tableColumns(items);
  if (columns) {
    return (
      <Box sx={{ overflowX: 'auto' }}>
        <Table size="small">
          <TableHead>
            <TableRow>
              {columns.map((c) => <TableCell key={c}>{humanizeKey(c)}</TableCell>)}
            </TableRow>
          </TableHead>
          <TableBody>
            {shown.map((item, i) => (
              <TableRow key={i}>
                {columns.map((c) => (
                  <TableCell key={c}>
                    <PrimitiveText value={(item as Record<string, JsonPrimitive | undefined>)[c]} />
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <MoreNote count={more} />
      </Box>
    );
  }
  return (
    <>
      {shown.map((item, i) => (
        <Section key={i} title={`Item ${i + 1}`} landmark={depth === 0}>
          <ValueView value={item} depth={depth + 1} />
        </Section>
      ))}
      <MoreNote count={more} />
    </>
  );
};

const ObjectView = ({ value, depth }: { value: JsonObject; depth: number }) => {
  const all = Object.entries(value);
  if (all.length === 0) {
    return <Typography variant="body2" color="text.secondary">No data</Typography>;
  }
  const entries = all.slice(0, MAX_ITEMS);
  const simple = entries.filter(([, v]) => isPrimitive(v));
  const nested = entries.filter(([, v]) => !isPrimitive(v));
  return (
    <>
      {simple.length > 0 && (
        <Table size="small">
          <TableBody>
            {simple.map(([k, v]) => (
              <TableRow key={k}>
                <TableCell component="th" scope="row" sx={{ fontWeight: 500, width: '40%', verticalAlign: 'top' }}>
                  {humanizeKey(k)}
                </TableCell>
                <TableCell><PrimitiveText value={v as JsonPrimitive} /></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {nested.map(([k, v]) => (
        <Section key={k} title={humanizeKey(k)} landmark={depth === 0}>
          <ValueView value={v} depth={depth + 1} />
        </Section>
      ))}
      <MoreNote count={all.length - entries.length} />
    </>
  );
};

const ValueView = ({ value, depth }: { value: unknown; depth: number }) => {
  const container = Array.isArray(value) || isPlainObject(value);
  if (container && depth > MAX_DEPTH) return <RawSubtree value={value} />;
  if (Array.isArray(value)) return <ArrayView items={value} depth={depth} />;
  if (isPlainObject(value)) return <ObjectView value={value} depth={depth} />;
  return <PrimitiveText value={isPrimitive(value) ? value : String(value)} />;
};

interface JsonResultViewProps {
  /** The parsed JSON value to show. */
  value: unknown;
  /** Test ID of the outer box. */
  'data-testid'?: string;
}

/**
 * Readable view of a JSON value: object keys as a key/value table, nested
 * objects as sections, uniform arrays of objects as tables. A "Raw JSON"
 * switch shows the formatted JSON text.
 */
const JsonResultView = ({ value, 'data-testid': testId }: JsonResultViewProps) => {
  const [raw, setRaw] = useState(false);
  return (
    <Stack spacing={1} data-testid={testId}>
      <Box sx={{ maxHeight: 320, overflow: 'auto', p: 1, bgcolor: 'action.hover', borderRadius: 1 }}>
        {raw ? (
          <Box component="pre" data-testid="json-raw" sx={{ m: 0, fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
            {JSON.stringify(value, null, 2)}
          </Box>
        ) : (
          <Box data-testid="json-readable"><ValueView value={value} depth={0} /></Box>
        )}
      </Box>
      <FormControlLabel
        control={<Switch size="small" checked={raw} onChange={(e) => setRaw(e.target.checked)} />}
        label="Raw JSON"
        sx={{ alignSelf: 'flex-start' }}
      />
    </Stack>
  );
};

export default JsonResultView;
