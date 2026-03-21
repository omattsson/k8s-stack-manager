import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import TtlSelector from '../../../components/TtlSelector';

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

  it('shows custom input when Custom is selected', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<TtlSelector value={0} onChange={onChange} />);

    await user.click(screen.getByRole('combobox'));
    await user.click(screen.getByRole('option', { name: 'Custom' }));

    expect(screen.getByLabelText(/minutes/i)).toBeInTheDocument();
  });

  it('shows custom input when value is non-preset', () => {
    render(<TtlSelector value={120} onChange={vi.fn()} />);
    expect(screen.getByLabelText(/minutes/i)).toBeInTheDocument();
  });

  it('is disabled when disabled prop is true', () => {
    render(<TtlSelector value={0} onChange={vi.fn()} disabled />);
    expect(screen.getByRole('combobox')).toHaveAttribute('aria-disabled', 'true');
  });
});
