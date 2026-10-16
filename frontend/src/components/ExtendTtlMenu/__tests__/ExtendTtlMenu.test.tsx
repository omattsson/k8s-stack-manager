import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ExtendTtlMenu from '..';
import { NotificationProvider } from '../../../context/NotificationContext';
import { instanceService } from '../../../api/client';
import type { StackInstance } from '../../../types';
import { toLocalInputValue } from '../../../utils/stopTime';

vi.mock('../../../api/client', () => ({
  instanceService: { extend: vi.fn(), extendUntil: vi.fn() },
}));

type MockFn = ReturnType<typeof vi.fn>;

const renderMenu = (props: Partial<Parameters<typeof ExtendTtlMenu>[0]> = {}) => {
  const onExtended = vi.fn();
  render(
    <NotificationProvider>
      <ExtendTtlMenu instanceId="i1" instanceName="my-stack" onExtended={onExtended} {...props} />
    </NotificationProvider>,
  );
  return { onExtended };
};

describe('ExtendTtlMenu', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists +1 h, +4 h, +24 h and Custom...', async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole('button', { name: 'Extend' }));
    const items = screen.getAllByRole('menuitem').map((m) => m.textContent);
    expect(items).toEqual(['+1 h', '+4 h', '+24 h', 'Custom...']);
  });

  it.each([
    ['+1 h', 60],
    ['+4 h', 240],
    ['+24 h', 1440],
  ])('%s adds %i minutes and shows the new expiry', async (label, minutes) => {
    const user = userEvent.setup();
    const expiresAt = new Date(Date.now() + (minutes + 30) * 60000).toISOString();
    const updated = { id: 'i1', expires_at: expiresAt } as StackInstance;
    (instanceService.extend as MockFn).mockResolvedValue(updated);
    const { onExtended } = renderMenu();

    await user.click(screen.getByRole('button', { name: 'Extend' }));
    await user.click(screen.getByRole('menuitem', { name: label }));

    await waitFor(() => expect(instanceService.extend).toHaveBeenCalledWith('i1', minutes));
    expect(onExtended).toHaveBeenCalledWith(updated);
    expect(await screen.findByText(/^Extended by .+\. Expires .+ \(in .+\)$/)).toBeInTheDocument();
  });

  it('reports a failure through onError', async () => {
    const user = userEvent.setup();
    (instanceService.extend as MockFn).mockRejectedValue(new Error('boom'));
    const onError = vi.fn();
    renderMenu({ onError });

    await user.click(screen.getByRole('button', { name: 'Extend' }));
    await user.click(screen.getByRole('menuitem', { name: '+1 h' }));

    await waitFor(() => expect(onError).toHaveBeenCalledWith('Failed to extend TTL for my-stack'));
  });

  it('shows an error toast without onError', async () => {
    const user = userEvent.setup();
    (instanceService.extend as MockFn).mockRejectedValue(new Error('boom'));
    renderMenu();

    await user.click(screen.getByRole('button', { name: 'Extend' }));
    await user.click(screen.getByRole('menuitem', { name: '+4 h' }));

    expect(await screen.findByText('Failed to extend TTL for my-stack')).toBeInTheDocument();
  });

  describe('Custom...', () => {
    const openCustom = async (props: Partial<Parameters<typeof ExtendTtlMenu>[0]> = {}) => {
      const user = userEvent.setup();
      const result = renderMenu(props);
      await user.click(screen.getByRole('button', { name: 'Extend' }));
      await user.click(screen.getByRole('menuitem', { name: 'Custom...' }));
      const input = await screen.findByLabelText('New expiry');
      return { user, input: input as HTMLInputElement, ...result };
    };

    it('limits the picker to after the current expiry and to now plus the limit', async () => {
      const expiresAt = new Date(Date.now() + 3 * 3600_000);
      const { input } = await openCustom({ expiresAt: expiresAt.toISOString() });
      expect(screen.getByRole('dialog', { name: 'Extend until' })).toBeInTheDocument();
      expect(input.min).toBe(toLocalInputValue(new Date(expiresAt.getTime() + 60_000)));
      const max = new Date(input.max).getTime();
      expect(Math.abs(max - (Date.now() + 30 * 86_400_000))).toBeLessThan(2 * 60_000);
      // The start value is one day after the current expiry.
      expect(input.value).toBe(toLocalInputValue(new Date(expiresAt.getTime() + 86_400_000)));
    });

    it('sends until and shows the new expiry', async () => {
      const until = new Date(Date.now() + 5 * 86_400_000);
      until.setSeconds(0, 0);
      const updated = { id: 'i1', expires_at: until.toISOString() } as StackInstance;
      (instanceService.extendUntil as MockFn).mockResolvedValue(updated);
      const { user, input, onExtended } = await openCustom();

      fireEvent.change(input, { target: { value: toLocalInputValue(until) } });
      await user.click(screen.getByRole('button', { name: 'Extend' }));

      await waitFor(() => expect(instanceService.extendUntil).toHaveBeenCalledWith('i1', until.toISOString()));
      expect(onExtended).toHaveBeenCalledWith(updated);
      expect(await screen.findByText(/^Extended\. Expires .+ \(in .+\)$/)).toBeInTheDocument();
    });

    it('disables Extend for a time before the current expiry', async () => {
      const expiresAt = new Date(Date.now() + 2 * 86_400_000);
      const { input } = await openCustom({ expiresAt: expiresAt.toISOString() });
      fireEvent.change(input, { target: { value: toLocalInputValue(new Date(Date.now() + 86_400_000)) } });
      expect(screen.getByText(/^Choose a time after /)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Extend' })).toBeDisabled();
      expect(instanceService.extendUntil).not.toHaveBeenCalled();
    });

    it('disables Extend for a time after the limit', async () => {
      const { input } = await openCustom();
      fireEvent.change(input, { target: { value: toLocalInputValue(new Date(Date.now() + 31 * 86_400_000)) } });
      expect(screen.getByText(/^Choose a time before /)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Extend' })).toBeDisabled();
    });
  });
});
