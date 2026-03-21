import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import FavoriteButton from '../index';

vi.mock('../../../api/client', () => ({
  favoriteService: {
    check: vi.fn(),
    add: vi.fn(),
    remove: vi.fn(),
  },
}));

import { favoriteService } from '../../../api/client';

describe('FavoriteButton', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders outlined star when not favorited', async () => {
    (favoriteService.check as ReturnType<typeof vi.fn>).mockResolvedValue(false);
    render(<FavoriteButton entityType="instance" entityId="1" />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Add to favorites' })).toBeInTheDocument();
    });
  });

  it('renders filled star when favorited', async () => {
    (favoriteService.check as ReturnType<typeof vi.fn>).mockResolvedValue(true);
    render(<FavoriteButton entityType="instance" entityId="1" />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Remove from favorites' })).toBeInTheDocument();
    });
  });

  it('toggles from not-favorited to favorited on click', async () => {
    const user = userEvent.setup();
    (favoriteService.check as ReturnType<typeof vi.fn>).mockResolvedValue(false);
    (favoriteService.add as ReturnType<typeof vi.fn>).mockResolvedValue({ id: '1', entity_type: 'instance', entity_id: '1' });

    render(<FavoriteButton entityType="instance" entityId="1" />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Add to favorites' })).not.toBeDisabled();
    });

    await user.click(screen.getByRole('button', { name: 'Add to favorites' }));

    await waitFor(() => {
      expect(favoriteService.add).toHaveBeenCalledWith('instance', '1');
      expect(screen.getByRole('button', { name: 'Remove from favorites' })).toBeInTheDocument();
    });
  });

  it('toggles from favorited to not-favorited on click', async () => {
    const user = userEvent.setup();
    (favoriteService.check as ReturnType<typeof vi.fn>).mockResolvedValue(true);
    (favoriteService.remove as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    render(<FavoriteButton entityType="instance" entityId="1" />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Remove from favorites' })).not.toBeDisabled();
    });

    await user.click(screen.getByRole('button', { name: 'Remove from favorites' }));

    await waitFor(() => {
      expect(favoriteService.remove).toHaveBeenCalledWith('instance', '1');
      expect(screen.getByRole('button', { name: 'Add to favorites' })).toBeInTheDocument();
    });
  });

  it('disables button while loading', async () => {
    (favoriteService.check as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(<FavoriteButton entityType="instance" entityId="1" />);

    expect(screen.getByRole('button')).toBeDisabled();
  });
});
