import { describe, it, expect } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import JsonResultView, { MAX_DEPTH, MAX_ITEMS, humanizeKey, tableColumns } from '../index';

describe('humanizeKey', () => {
  it('turns snake case and camel case into a label', () => {
    expect(humanizeKey('last_refresh_at')).toBe('Last refresh at');
    expect(humanizeKey('snapshotName')).toBe('Snapshot name');
    expect(humanizeKey('ok')).toBe('Ok');
  });
});

describe('tableColumns', () => {
  it('returns the union of keys for an array of flat objects', () => {
    expect(tableColumns([{ a: 1, b: 2 }, { a: 3, c: null }])).toEqual(['a', 'b', 'c']);
  });

  it('returns null for mixed or nested arrays', () => {
    expect(tableColumns([])).toBeNull();
    expect(tableColumns([1, 2])).toBeNull();
    expect(tableColumns([{ a: { b: 1 } }])).toBeNull();
    expect(tableColumns([{ a: 1 }, 'x'])).toBeNull();
  });
});

describe('JsonResultView', () => {
  const golden = {
    status: 'ready',
    healthy: true,
    snapshot: { name: 'manual-5', size_gb: 12.5, created_at: null },
    markets: [
      { market: 'se', rows: 100 },
      { market: 'dk', rows: 50 },
    ],
    tags: ['a', 'b'],
    jobs: [{ id: 1, steps: ['x'] }],
  };

  it('renders keys, nested sections and uniform arrays as tables', () => {
    render(<JsonResultView value={golden} data-testid="view" />);
    const view = screen.getByTestId('view');

    expect(within(view).getByRole('rowheader', { name: 'Status' })).toBeInTheDocument();
    expect(within(view).getByText('ready')).toBeInTheDocument();
    expect(within(view).getByText('Yes')).toBeInTheDocument();

    const snapshot = within(view).getByRole('region', { name: 'Snapshot' });
    expect(within(snapshot).getByText('manual-5')).toBeInTheDocument();
    expect(within(snapshot).getByRole('rowheader', { name: 'Size gb' })).toBeInTheDocument();
    expect(within(snapshot).getByText('-')).toBeInTheDocument();

    const markets = within(view).getByRole('region', { name: 'Markets' });
    expect(within(markets).getByRole('columnheader', { name: 'Market' })).toBeInTheDocument();
    expect(within(markets).getAllByRole('row')).toHaveLength(3);

    expect(within(view).getByRole('region', { name: 'Tags' })).toHaveTextContent('a, b');
    expect(within(view).getByRole('region', { name: 'Jobs' })).toHaveTextContent('Item 1');
    expect(within(view).queryByTestId('json-raw')).not.toBeInTheDocument();
  });

  it('uses labelled sections only at the top level', () => {
    render(<JsonResultView value={{ outer: { inner: { x: 1 } } }} />);
    expect(screen.getByRole('region', { name: 'Outer' })).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Inner' })).not.toBeInTheDocument();
    expect(screen.getByText('Inner')).toBeInTheDocument();
  });

  it('shows at most MAX_ITEMS rows of a large array and a note for the rest', () => {
    const rows = Array.from({ length: MAX_ITEMS + 50 }, (_, i) => ({ id: i, name: `row-${i}` }));
    render(<JsonResultView value={{ rows }} />);
    const section = screen.getByRole('region', { name: 'Rows' });
    // One header row plus MAX_ITEMS body rows
    expect(within(section).getAllByRole('row')).toHaveLength(MAX_ITEMS + 1);
    expect(within(section).getByTestId('json-more')).toHaveTextContent('50 more. See Raw JSON.');
    expect(within(section).queryByText(`row-${MAX_ITEMS}`)).not.toBeInTheDocument();
  });

  it('limits primitive arrays and object keys', () => {
    const many = Object.fromEntries(Array.from({ length: MAX_ITEMS + 3 }, (_, i) => [`k${i}`, i]));
    const { rerender } = render(<JsonResultView value={many} />);
    expect(screen.getByTestId('json-more')).toHaveTextContent('3 more. See Raw JSON.');
    rerender(<JsonResultView value={Array.from({ length: MAX_ITEMS + 1 }, (_, i) => i)} />);
    expect(screen.getByTestId('json-more')).toHaveTextContent('1 more. See Raw JSON.');
  });

  it('shows values deeper than MAX_DEPTH as raw JSON', () => {
    let deep: Record<string, unknown> = { leaf: 'bottom' };
    for (let i = 0; i < MAX_DEPTH + 2; i += 1) deep = { [`level${i}`]: deep };
    render(<JsonResultView value={deep} />);
    expect(screen.getByTestId('json-subtree-raw')).toHaveTextContent('"leaf": "bottom"');
  });

  it('shows the raw JSON when the switch is on', async () => {
    const user = userEvent.setup();
    render(<JsonResultView value={{ ok: true }} />);

    await user.click(screen.getByRole('switch', { name: 'Raw JSON' }));

    expect(screen.getByTestId('json-raw')).toHaveTextContent('"ok": true');
    expect(screen.queryByTestId('json-readable')).not.toBeInTheDocument();
  });

  it('renders primitives, empty objects and empty arrays', () => {
    const { rerender } = render(<JsonResultView value="done" />);
    expect(screen.getByText('done')).toBeInTheDocument();
    rerender(<JsonResultView value={{}} />);
    expect(screen.getByText('No data')).toBeInTheDocument();
    rerender(<JsonResultView value={[]} />);
    expect(screen.getByText('No items')).toBeInTheDocument();
  });
});
