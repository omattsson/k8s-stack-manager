import { describe, it, expect, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import TtlSelector from '../../../components/TtlSelector';
import { BRANDING_DEFAULTS, StaticBrandingProvider } from '../../../context/BrandingContext';
import { MAX_STOP_TIME_MARGIN_MINUTES, toLocalInputValue } from '../../../utils/stopTime';

describe('TtlSelector', () => {
  it('renders preset options', () => {
    render(<TtlSelector value={0} onChange={vi.fn()} />);
    expect(screen.getByLabelText(/ttl/i)).toBeInTheDocument();
  });

  it('shows "No expiry" when value is 0', () => {
    render(<TtlSelector value={0} onChange={vi.fn()} />);
    expect(screen.getByText('No expiry')).toBeInTheDocument();
  });

  it('shows "4 hours" when value is 240', () => {
    render(<TtlSelector value={240} onChange={vi.fn()} />);
    expect(screen.getByText('4 hours')).toBeInTheDocument();
  });

  it('calls onChange with 0 when No expiry is selected', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<TtlSelector value={240} onChange={onChange} />);

    // Open the select
    await user.click(screen.getByRole('combobox'));
    // Click "No expiry"
    await user.click(screen.getByRole('option', { name: 'No expiry' }));

    expect(onChange).toHaveBeenCalledWith(0);
  });

  it('calls onChange with 1440 when 24 hours is selected', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<TtlSelector value={0} onChange={onChange} />);

    await user.click(screen.getByRole('combobox'));
    await user.click(screen.getByRole('option', { name: '24 hours' }));

    expect(onChange).toHaveBeenCalledWith(1440);
  });

  it('offers "Custom stop time" only with onStopTimeChange', async () => {
    const user = userEvent.setup();
    const { unmount } = render(<TtlSelector value={0} onChange={vi.fn()} />);
    await user.click(screen.getByRole('combobox'));
    expect(screen.queryByRole('option', { name: 'Custom stop time' })).not.toBeInTheDocument();
    unmount();

    render(<TtlSelector value={0} onChange={vi.fn()} onStopTimeChange={vi.fn()} />);
    await user.click(screen.getByRole('combobox'));
    expect(screen.getByRole('option', { name: 'Custom stop time' })).toBeInTheDocument();
  });

  it('"Custom stop time" starts with tomorrow at the current time', async () => {
    const user = userEvent.setup();
    const onStopTimeChange = vi.fn();
    const onChange = vi.fn();
    render(<TtlSelector value={240} onChange={onChange} onStopTimeChange={onStopTimeChange} />);

    await user.click(screen.getByRole('combobox'));
    await user.click(screen.getByRole('option', { name: 'Custom stop time' }));

    expect(onChange).not.toHaveBeenCalled();
    expect(onStopTimeChange).toHaveBeenCalledTimes(1);
    const iso = onStopTimeChange.mock.calls[0][0] as string;
    expect(Math.abs(new Date(iso).getTime() - (Date.now() + 86_400_000))).toBeLessThan(2 * 60_000);
  });

  it('shows the picker for a stop time, limited by the configured maximum', () => {
    const stop = new Date(Date.now() + 2 * 86_400_000);
    stop.setSeconds(0, 0);
    render(
      <StaticBrandingProvider value={{ ...BRANDING_DEFAULTS, maxTtlDays: 7 }}>
        <TtlSelector value={0} onChange={vi.fn()} stopTime={stop.toISOString()} onStopTimeChange={vi.fn()} />
      </StaticBrandingProvider>,
    );
    expect(screen.getByRole('combobox')).toHaveTextContent('Custom stop time');
    const input = screen.getByLabelText('Stop at') as HTMLInputElement;
    expect(input.type).toBe('datetime-local');
    expect(input.value).toBe(toLocalInputValue(stop));
    expect(Math.abs(new Date(input.max).getTime() - (Date.now() + 7 * 86_400_000 - MAX_STOP_TIME_MARGIN_MINUTES * 60_000))).toBeLessThan(90_000);
    expect(Math.abs(new Date(input.min).getTime() - (Date.now() + 15 * 60_000))).toBeLessThan(2 * 60_000);
    // The chosen time and the time left
    expect(screen.getByText(/\(in 1d 23h\)|\(in 2d 0h\)/)).toBeInTheDocument();
  });

  it('accepts a saved stop time less than 15 minutes away', () => {
    const soon = new Date(Date.now() + 5 * 60_000).toISOString();
    render(<TtlSelector value={0} onChange={vi.fn()} stopTime={soon} onStopTimeChange={vi.fn()} acceptStopTime={soon} />);
    expect(screen.queryByText(/^Choose a time/)).not.toBeInTheDocument();
  });

  it('reports a time after the maximum', () => {
    render(
      <StaticBrandingProvider value={{ ...BRANDING_DEFAULTS, maxTtlDays: 7 }}>
        <TtlSelector value={0} onChange={vi.fn()} stopTime={new Date(Date.now() + 8 * 86_400_000).toISOString()} onStopTimeChange={vi.fn()} />
      </StaticBrandingProvider>,
    );
    expect(screen.getByText(/^Choose a time before /)).toBeInTheDocument();
  });

  it('passes picker changes on as ISO times', () => {
    const onStopTimeChange = vi.fn();
    render(<TtlSelector value={0} onChange={vi.fn()} stopTime={new Date(Date.now() + 86_400_000).toISOString()} onStopTimeChange={onStopTimeChange} />);
    const next = new Date(Date.now() + 3 * 86_400_000);
    next.setSeconds(0, 0);
    fireEvent.change(screen.getByLabelText('Stop at'), { target: { value: toLocalInputValue(next) } });
    expect(onStopTimeChange).toHaveBeenCalledWith(next.toISOString());
  });

  it('a preset leaves the stop time', async () => {
    const user = userEvent.setup();
    const onStopTimeChange = vi.fn();
    const onChange = vi.fn();
    render(<TtlSelector value={0} onChange={onChange} stopTime={new Date(Date.now() + 86_400_000).toISOString()} onStopTimeChange={onStopTimeChange} />);

    await user.click(screen.getByRole('combobox'));
    await user.click(screen.getByRole('option', { name: '8 hours' }));

    expect(onStopTimeChange).toHaveBeenCalledWith(undefined);
    expect(onChange).toHaveBeenCalledWith(480);
  });

  it('keeps a TTL that is not a preset as its own entry', () => {
    render(<TtlSelector value={90} onChange={vi.fn()} onStopTimeChange={vi.fn()} />);
    expect(screen.getByRole('combobox')).toHaveTextContent('1h 30m');
    expect(screen.queryByLabelText('Stop at')).not.toBeInTheDocument();
  });

  it('is disabled when disabled prop is true', () => {
    render(<TtlSelector value={0} onChange={vi.fn()} disabled />);
    expect(screen.getByRole('combobox')).toHaveAttribute('aria-disabled', 'true');
  });
});
