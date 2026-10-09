import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ExtendTtlMenu from '..';
import { NotificationProvider } from '../../../context/NotificationContext';
import { instanceService } from '../../../api/client';
import type { StackInstance } from '../../../types';

vi.mock('../../../api/client', () => ({
  instanceService: { extend: vi.fn() },
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

  it('lists +1 h, +4 h and +24 h', async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole('button', { name: 'Extend' }));
    const items = screen.getAllByRole('menuitem').map((m) => m.textContent);
    expect(items).toEqual(['+1 h', '+4 h', '+24 h']);
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
});
