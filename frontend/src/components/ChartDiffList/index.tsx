import { Box, Chip, Typography } from '@mui/material';
import ReactDiffViewer, { DiffMethod } from 'react-diff-viewer-continued';
import type { TemplateChartDiff } from '../../types';

interface ChartDiffListProps {
  chartDiffs: TemplateChartDiff[];
  leftTitle: string;
  rightTitle: string;
  /** Hide charts without differences. Default: false. */
  hideUnchanged?: boolean;
  /**
   * Show `removed` charts as kept (no value diff). Use this for definition
   * upgrades, where a chart that is only in the definition stays. Default: false.
   */
  removedIsKept?: boolean;
  emptyMessage?: string;
}

interface AttributeChange {
  label: string;
  from: string;
  to: string;
}

const changeTypeColor = (changeType: string): 'success' | 'error' | 'info' | 'default' => {
  switch (changeType) {
    case 'added': return 'success';
    case 'removed': return 'error';
    case 'modified': return 'info';
    default: return 'default';
  }
};

/** Drop trailing spaces, tabs and newlines only (same as the backend NormalizeValues). */
const norm = (v: string | null | undefined): string => (v ?? '').replace(/[ \t\r\n]+$/, '');
const shown = (v: string): string => v || '(none)';

/** Chart attribute changes of a modified chart (repository, version, path, required, order). */
const attributeChanges = (d: TemplateChartDiff): AttributeChange[] => {
  const changes: AttributeChange[] = [];
  const text = (label: string, left?: string, right?: string) => {
    if (norm(left) !== norm(right)) changes.push({ label, from: shown(norm(left)), to: shown(norm(right)) });
  };
  // Compare only when both sides have a value: legacy snapshots lack these fields.
  const bothSides = (label: string, left?: string, right?: string) => {
    if (norm(left) && norm(right)) text(label, left, right);
  };
  text('Repository', d.left_repo_url, d.right_repo_url);
  bothSides('Chart version', d.left_chart_version, d.right_chart_version);
  bothSides('Chart path', d.left_chart_path, d.right_chart_path);
  if (Boolean(d.left_required) !== Boolean(d.right_required)) {
    changes.push({ label: 'Required', from: d.left_required ? 'yes' : 'no', to: d.right_required ? 'yes' : 'no' });
  }
  if ((d.left_sort_order ?? 0) !== (d.right_sort_order ?? 0)) {
    changes.push({ label: 'Deploy order', from: String(d.left_sort_order ?? 0), to: String(d.right_sort_order ?? 0) });
  }
  return changes;
};

interface YamlDiffProps {
  heading: string;
  left: string;
  right: string;
  leftTitle: string;
  rightTitle: string;
}

const YamlDiff = ({ heading, left, right, leftTitle, rightTitle }: YamlDiffProps) => (
  <Box sx={{ mb: 1.5 }}>
    <Typography variant="subtitle2" component="h4" sx={{ mb: 0.5 }}>
      {heading}
    </Typography>
    <Box sx={{ border: 1, borderColor: 'divider', borderRadius: 1, overflow: 'hidden' }}>
      <ReactDiffViewer
        oldValue={left}
        newValue={right}
        splitView={true}
        compareMethod={DiffMethod.LINES}
        leftTitle={leftTitle}
        rightTitle={rightTitle}
      />
    </Box>
  </Box>
);

interface ChartDiffItemProps {
  diff: TemplateChartDiff;
  leftTitle: string;
  rightTitle: string;
  removedIsKept: boolean;
}

const ChartDiffItem = ({ diff, leftTitle, rightTitle, removedIsKept }: ChartDiffItemProps) => {
  const kept = removedIsKept && diff.change_type === 'removed';
  const attributes = diff.change_type === 'modified' ? attributeChanges(diff) : [];
  const valuesChanged = norm(diff.left_values) !== norm(diff.right_values);
  const lockedChanged = norm(diff.left_locked) !== norm(diff.right_locked);
  const hasDetail = attributes.length > 0 || valuesChanged || lockedChanged;

  return (
    <Box sx={{ mb: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
        <Typography variant="subtitle1" component="h3" sx={{ fontWeight: 'bold' }}>
          {diff.chart_name}
        </Typography>
        <Chip
          label={kept ? 'kept' : diff.change_type}
          size="small"
          color={kept ? 'default' : changeTypeColor(diff.change_type)}
        />
      </Box>
      {kept ? (
        <Typography variant="body2" color="text.secondary">
          Kept (not in the new template version).
        </Typography>
      ) : !diff.has_differences || !hasDetail ? (
        <Typography variant="body2" color="text.secondary">
          No value changes.
        </Typography>
      ) : (
        <>
          {attributes.length > 0 && (
            <Box component="ul" sx={{ m: 0, mb: 1.5, pl: 3 }}>
              {attributes.map((a) => (
                <Typography key={a.label} component="li" variant="body2">
                  {a.label}: {a.from} to {a.to}
                </Typography>
              ))}
            </Box>
          )}
          {valuesChanged && (
            <YamlDiff
              heading="Default values"
              left={diff.left_values ?? ''}
              right={diff.right_values ?? ''}
              leftTitle={leftTitle}
              rightTitle={rightTitle}
            />
          )}
          {lockedChanged && (
            <YamlDiff
              heading="Locked values"
              left={diff.left_locked ?? ''}
              right={diff.right_locked ?? ''}
              leftTitle={leftTitle}
              rightTitle={rightTitle}
            />
          )}
        </>
      )}
    </Box>
  );
};

/**
 * Per-chart diff: default values and locked values (split YAML view) plus
 * attribute changes (repository, chart version, chart path, required, order).
 * Shared by version history, template detail and upgrade dialog.
 */
const ChartDiffList = ({
  chartDiffs,
  leftTitle,
  rightTitle,
  hideUnchanged = false,
  removedIsKept = false,
  emptyMessage = 'No differences found.',
}: ChartDiffListProps) => {
  const visible = hideUnchanged
    ? chartDiffs.filter((d) => d.has_differences || (removedIsKept && d.change_type === 'removed'))
    : chartDiffs;

  if (visible.length === 0) {
    return <Typography color="text.secondary">{emptyMessage}</Typography>;
  }

  return (
    <Box>
      {visible.map((d) => (
        <ChartDiffItem
          key={d.chart_name}
          diff={d}
          leftTitle={leftTitle}
          rightTitle={rightTitle}
          removedIsKept={removedIsKept}
        />
      ))}
    </Box>
  );
};

export default ChartDiffList;
