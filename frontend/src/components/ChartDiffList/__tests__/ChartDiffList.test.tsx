import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import ChartDiffList from '..';
import type { TemplateChartDiff } from '../../../types';

vi.mock('react-diff-viewer-continued', () => ({
  default: ({ oldValue, newValue, leftTitle, rightTitle }: {
    oldValue: string; newValue: string; leftTitle: string; rightTitle: string;
  }) => (
    <div data-testid="diff-viewer">
      <span>{leftTitle}</span>
      <span>{rightTitle}</span>
      <span>{oldValue}</span>
      <span>{newValue}</span>
    </div>
  ),
  DiffMethod: { LINES: 'diffLines' },
}));

const diffs: TemplateChartDiff[] = [
  { chart_name: 'api', left_values: 'replicas: 1', right_values: 'replicas: 2', has_differences: true, change_type: 'modified' },
  { chart_name: 'db', left_values: 'a: 1', right_values: 'a: 1', has_differences: false, change_type: 'unchanged' },
];

describe('ChartDiffList', () => {
  it('renders a diff with titles for changed charts and a note for unchanged charts', () => {
    render(<ChartDiffList chartDiffs={diffs} leftTitle="v1.0.0" rightTitle="Working copy" />);
    expect(screen.getByText('api')).toBeInTheDocument();
    expect(screen.getByText('v1.0.0')).toBeInTheDocument();
    expect(screen.getByText('Working copy')).toBeInTheDocument();
    expect(screen.getByText('replicas: 2')).toBeInTheDocument();
    expect(screen.getByText('db')).toBeInTheDocument();
    expect(screen.getByText('No value changes.')).toBeInTheDocument();
  });

  it('hides unchanged charts when hideUnchanged is set', () => {
    render(<ChartDiffList chartDiffs={diffs} leftTitle="a" rightTitle="b" hideUnchanged />);
    expect(screen.getByText('api')).toBeInTheDocument();
    expect(screen.queryByText('db')).not.toBeInTheDocument();
  });

  it('shows the empty message when there is nothing to show', () => {
    render(<ChartDiffList chartDiffs={[]} leftTitle="a" rightTitle="b" emptyMessage="Nothing changed." />);
    expect(screen.getByText('Nothing changed.')).toBeInTheDocument();
  });

  it('diffs locked values and lists attribute changes', () => {
    render(
      <ChartDiffList
        chartDiffs={[{
          chart_name: 'api',
          left_values: 'a: 1',
          right_values: 'a: 1',
          left_locked: 'image: old',
          right_locked: 'image: new',
          left_chart_version: '1.0.0',
          right_chart_version: '1.1.0',
          left_chart_path: 'charts/api',
          right_chart_path: 'charts/api',
          left_repo_url: 'https://a.example.com',
          right_repo_url: 'https://b.example.com',
          right_required: true,
          left_sort_order: 1,
          right_sort_order: 2,
          has_differences: true,
          change_type: 'modified',
        }]}
        leftTitle="v1"
        rightTitle="v2"
      />,
    );
    expect(screen.getByText('Locked values')).toBeInTheDocument();
    expect(screen.getByText('image: new')).toBeInTheDocument();
    // Default values are equal: no default values diff.
    expect(screen.queryByText('Default values')).not.toBeInTheDocument();
    expect(screen.getByText('Chart version: 1.0.0 to 1.1.0')).toBeInTheDocument();
    expect(screen.getByText('Repository: https://a.example.com to https://b.example.com')).toBeInTheDocument();
    expect(screen.getByText('Required: no to yes')).toBeInTheDocument();
    expect(screen.getByText('Deploy order: 1 to 2')).toBeInTheDocument();
    expect(screen.queryByText(/Chart path:/)).not.toBeInTheDocument();
  });

  it('shows removed charts as kept without a diff when removedIsKept is set', () => {
    render(
      <ChartDiffList
        chartDiffs={[{ chart_name: 'legacy', left_values: 'x: 1', right_values: '', has_differences: true, change_type: 'removed' }]}
        leftTitle="a"
        rightTitle="b"
        hideUnchanged
        removedIsKept
      />,
    );
    expect(screen.getByText('legacy')).toBeInTheDocument();
    expect(screen.getByText('Kept (not in the new template version).')).toBeInTheDocument();
    expect(screen.queryByTestId('diff-viewer')).not.toBeInTheDocument();
  });

  it('skips chart version and path lines when one side lacks them (legacy snapshot)', () => {
    render(
      <ChartDiffList
        chartDiffs={[{
          chart_name: 'api',
          left_values: 'a: 1',
          right_values: 'a: 2',
          right_chart_version: '1.1.0',
          right_chart_path: 'charts/api',
          has_differences: true,
          change_type: 'modified',
        }]}
        leftTitle="v1"
        rightTitle="v2"
      />,
    );
    expect(screen.getByText('Default values')).toBeInTheDocument();
    expect(screen.queryByText(/Chart version:/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Chart path:/)).not.toBeInTheDocument();
  });

  it('treats a leading-whitespace change as a change and ignores trailing newlines', () => {
    const { rerender } = render(
      <ChartDiffList
        chartDiffs={[{ chart_name: 'api', left_values: 'a: 1\n', right_values: 'a: 1', has_differences: true, change_type: 'modified' }]}
        leftTitle="v1"
        rightTitle="v2"
      />,
    );
    expect(screen.getByText('No value changes.')).toBeInTheDocument();

    rerender(
      <ChartDiffList
        chartDiffs={[{ chart_name: 'api', left_values: 'a: 1', right_values: '  a: 1', has_differences: true, change_type: 'modified' }]}
        leftTitle="v1"
        rightTitle="v2"
      />,
    );
    expect(screen.getByText('Default values')).toBeInTheDocument();
  });
});
