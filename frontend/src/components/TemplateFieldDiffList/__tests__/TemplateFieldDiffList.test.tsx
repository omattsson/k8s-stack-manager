import { describe, it, expect } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import TemplateFieldDiffList, { diffSideTitle, templateFieldDiffs } from '../index';
import type { TemplateSnapshot, VersionDiffResponse } from '../../../types';

const snapshot = (template: Partial<TemplateSnapshot['template']>): TemplateSnapshot => ({
  template: {
    name: 'Web', description: '', category: '', default_branch: 'main',
    repository_url: '', is_published: true, version: '1.0.0', ...template,
  },
  charts: [],
});

describe('templateFieldDiffs', () => {
  it('uses template_diffs from the server', () => {
    const diff: VersionDiffResponse = {
      left: { version: '1', snapshot: snapshot({}) },
      right: { version: '1', snapshot: snapshot({}) },
      template_diffs: [{ field: 'name', left: 'a', right: 'b' }],
      chart_diffs: [],
    };
    expect(templateFieldDiffs(diff)).toEqual([{ field: 'name', left: 'a', right: 'b' }]);
  });

  it('compares the snapshots when the server sends no template_diffs', () => {
    const diff: VersionDiffResponse = {
      left: { version: '1', snapshot: snapshot({ description: 'old', is_published: true }) },
      right: { version: '1', snapshot: snapshot({ description: 'new', default_branch: 'dev', is_published: false }) },
      chart_diffs: [],
    };
    expect(templateFieldDiffs(diff)).toEqual([
      { field: 'description', left: 'old', right: 'new' },
      { field: 'default_branch', left: 'main', right: 'dev' },
    ]);
  });
});

describe('TemplateFieldDiffList', () => {
  it('renders nothing without changes', () => {
    const { container } = render(<TemplateFieldDiffList diffs={[]} leftTitle="v1" rightTitle="Working copy" />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders one row per changed field with labels and both values', () => {
    render(
      <TemplateFieldDiffList
        diffs={[{ field: 'default_branch', left: 'main', right: '' }, { field: 'custom', left: 'x', right: 'y' }]}
        leftTitle="v1.0.0"
        rightTitle="Working copy"
      />,
    );
    const table = screen.getByRole('table', { name: 'Template details' });
    expect(within(table).getByRole('columnheader', { name: 'v1.0.0' })).toBeInTheDocument();
    expect(within(table).getByRole('columnheader', { name: 'Working copy' })).toBeInTheDocument();
    const branch = within(table).getByRole('row', { name: /default branch/i });
    expect(within(branch).getByText('main')).toBeInTheDocument();
    expect(within(branch).getByText('(empty)')).toBeInTheDocument();
    expect(within(table).getByRole('rowheader', { name: 'custom' })).toBeInTheDocument();
  });
});

describe('diffSideTitle', () => {
  it('names the working copy and versions', () => {
    expect(diffSideTitle({ version: '1.2.0', snapshot: snapshot({}), is_working_copy: true })).toBe('Working copy');
    expect(diffSideTitle({ version: '1.2.0', snapshot: snapshot({}) })).toBe('v1.2.0');
  });
});
