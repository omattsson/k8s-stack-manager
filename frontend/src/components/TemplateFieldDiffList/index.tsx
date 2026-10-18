import { useId } from 'react';
import {
  Box,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import type { TemplateFieldDiff, VersionDiffResponse, VersionDiffSide } from '../../types';

/** Labels of the template fields of a version diff, in display order. */
export const TEMPLATE_FIELD_LABELS: Record<string, string> = {
  name: 'Name',
  description: 'Description',
  category: 'Category',
  default_branch: 'Default branch',
  version: 'Version',
};

/**
 * Changed template fields of a version diff. Uses template_diffs from the
 * server; for an older server without the field, compares the template data
 * of the two snapshots.
 * @param diff - The version diff response
 * @returns The changed fields
 */
export const templateFieldDiffs = (diff: VersionDiffResponse): TemplateFieldDiff[] => {
  if (Array.isArray(diff.template_diffs)) return diff.template_diffs;
  const left = (diff.left.snapshot?.template ?? {}) as unknown as Record<string, unknown>;
  const right = (diff.right.snapshot?.template ?? {}) as unknown as Record<string, unknown>;
  const text = (v: unknown): string => (typeof v === 'string' ? v : '');
  return Object.keys(TEMPLATE_FIELD_LABELS)
    .filter((f) => text(left[f]) !== text(right[f]))
    .map((f) => ({ field: f, left: text(left[f]), right: text(right[f]) }));
};

/**
 * Title of one side of a version diff: "Working copy" or "v<version>".
 * @param side - The diff side
 * @returns The title
 */
export const diffSideTitle = (side: VersionDiffSide): string =>
  side.is_working_copy ? 'Working copy' : `v${side.version}`;

const ValueCell = ({ value }: { value: string }) => (
  <TableCell sx={{ verticalAlign: 'top', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
    {value === '' ? (
      <Typography component="span" variant="body2" color="text.secondary">(empty)</Typography>
    ) : value}
  </TableCell>
);

interface TemplateFieldDiffListProps {
  /** The changed fields. */
  diffs: TemplateFieldDiff[];
  leftTitle: string;
  rightTitle: string;
}

/**
 * Table of changed template fields (name, description, category, default
 * branch, version) with the old and the new value. Renders nothing when no
 * field changed.
 */
const TemplateFieldDiffList = ({ diffs, leftTitle, rightTitle }: TemplateFieldDiffListProps) => {
  const titleId = useId();
  if (diffs.length === 0) return null;
  return (
    <Box component="section" aria-labelledby={titleId} sx={{ mb: 3 }}>
      <Typography id={titleId} variant="subtitle1" sx={{ mb: 1 }}>
        Template details
      </Typography>
      <Table size="small" aria-labelledby={titleId}>
        <TableHead>
          <TableRow>
            <TableCell sx={{ width: '20%' }}>Field</TableCell>
            <TableCell>{leftTitle}</TableCell>
            <TableCell>{rightTitle}</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {diffs.map((d) => (
            <TableRow key={d.field}>
              <TableCell component="th" scope="row" sx={{ verticalAlign: 'top', fontWeight: 500 }}>
                {TEMPLATE_FIELD_LABELS[d.field] ?? d.field}
              </TableCell>
              <ValueCell value={d.left} />
              <ValueCell value={d.right} />
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Box>
  );
};

export default TemplateFieldDiffList;
