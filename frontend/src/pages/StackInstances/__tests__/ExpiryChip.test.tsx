import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import ExpiryChip from '../ExpiryChip';
import { formatStopTimeWhen } from '../../../utils/stopTime';
import type { StackInstance } from '../../../types';

vi.mock('../../../hooks/useCountdown', () => ({
  default: vi.fn().mockReturnValue(null),
}));

import useCountdown from '../../../hooks/useCountdown';

type MockFn = ReturnType<typeof vi.fn>;

const baseInstance: StackInstance = {
  id: '1',
  name: 'Test',
  namespace: 'stack-test',
  owner_id: '1',
  branch: 'main',
  status: 'running',
  stack_definition_id: 'def1',
  ttl_minutes: 60,
  created_at: '',
  updated_at: '',
};

describe('ExpiryChip', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows Expired chip for TTL-expired stopped instance', () => {
    (useCountdown as unknown as MockFn).mockReturnValue(null);

    render(
      <ExpiryChip instance={{ ...baseInstance, status: 'stopped', error_message: 'Expired (TTL)' }} />
    );

    expect(screen.getByText('Expired')).toBeInTheDocument();
  });

  it('shows countdown chip for running instance with time remaining', () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '2h 30m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });

    render(
      <ExpiryChip instance={{ ...baseInstance, status: 'running', expires_at: '2026-01-01T12:00:00Z' }} />
    );

    expect(screen.getByText('⏱ 2h 30m')).toBeInTheDocument();
  });

  it('uses warning color when isWarning is true', () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '45m',
      isWarning: true,
      isCritical: false,
      isExpired: false,
    });

    render(
      <ExpiryChip instance={{ ...baseInstance, status: 'running', expires_at: '2026-01-01T12:00:00Z' }} />
    );

    const chip = screen.getByText('⏱ 45m');
    expect(chip).toBeInTheDocument();
    expect(chip.closest('.MuiChip-root')).toHaveClass('MuiChip-colorWarning');
  });

  it('uses error color when isCritical is true', () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '10m',
      isWarning: true,
      isCritical: true,
      isExpired: false,
    });

    render(
      <ExpiryChip instance={{ ...baseInstance, status: 'running', expires_at: '2026-01-01T12:00:00Z' }} />
    );

    const chip = screen.getByText('⏱ 10m');
    expect(chip).toBeInTheDocument();
    expect(chip.closest('.MuiChip-root')).toHaveClass('MuiChip-colorError');
  });

  it('uses success color when no warning flags', () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '5h 0m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });

    render(
      <ExpiryChip instance={{ ...baseInstance, status: 'running', expires_at: '2026-01-01T12:00:00Z' }} />
    );

    const chip = screen.getByText('⏱ 5h 0m');
    expect(chip.closest('.MuiChip-root')).toHaveClass('MuiChip-colorSuccess');
  });

  it('returns null when countdown is null (no expiry)', () => {
    (useCountdown as unknown as MockFn).mockReturnValue(null);

    const { container } = render(
      <ExpiryChip instance={{ ...baseInstance, status: 'running' }} />
    );

    expect(container.firstChild).toBeNull();
  });

  it('returns null when instance is not running', () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '3h 0m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });

    const { container } = render(
      <ExpiryChip instance={{ ...baseInstance, status: 'draft' }} />
    );

    expect(container.firstChild).toBeNull();
  });

  it('returns null when countdown is expired', () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: 'Expired',
      isWarning: true,
      isCritical: true,
      isExpired: true,
    });

    const { container } = render(
      <ExpiryChip instance={{ ...baseInstance, status: 'running' }} />
    );

    expect(container.firstChild).toBeNull();
  });

  it('gives Expired chip precedence even when status is stopped without running countdown', () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: 'Expired',
      isWarning: true,
      isCritical: true,
      isExpired: true,
    });

    render(
      <ExpiryChip instance={{ ...baseInstance, status: 'stopped', error_message: 'Expired (TTL)' }} />
    );

    expect(screen.getByText('Expired')).toBeInTheDocument();
  });

  describe('fixed stop time (no TTL)', () => {
    const counting = { remaining: '1d 2h', isWarning: false, isCritical: false, isExpired: false };
    const stop = '2026-10-17T16:00:00Z';

    it.each(['running', 'draft'] as const)('shows "Stops at" for a %s instance', (status) => {
      (useCountdown as unknown as MockFn).mockReturnValue(counting);
      render(<ExpiryChip instance={{ ...baseInstance, status, ttl_minutes: 0, expires_at: stop }} />);
      const chip = screen.getByText(/^⏱ Stops at /);
      expect(chip.textContent).toContain(formatStopTimeWhen(new Date(stop)));
    });

    it('shows Expired for a stopped instance after the stop time', () => {
      (useCountdown as unknown as MockFn).mockReturnValue({ ...counting, isExpired: true });
      render(<ExpiryChip instance={{ ...baseInstance, status: 'stopped', ttl_minutes: 0, expires_at: '2020-01-01T00:00:00Z' }} />);
      expect(screen.getByText('Expired')).toBeInTheDocument();
    });

    it('shows nothing for a draft with a TTL', () => {
      (useCountdown as unknown as MockFn).mockReturnValue(counting);
      const { container } = render(<ExpiryChip instance={{ ...baseInstance, status: 'draft', ttl_minutes: 60, expires_at: stop }} />);
      expect(container).toBeEmptyDOMElement();
    });
  });
});
