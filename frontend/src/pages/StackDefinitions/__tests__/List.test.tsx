import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import List from '../List';

vi.mock('../../../api/client', () => ({
  definitionService: {
    list: vi.fn(),
  },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({
    user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin' },
    isAuthenticated: true,
    isLoading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

import { definitionService } from '../../../api/client';

describe('Stack Definitions List', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner initially', () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays definitions in a table', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'My Stack', description: 'My description', default_branch: 'master', owner_id: '1', created_at: '2024-01-01T00:00:00Z', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
  });

  it('shows error on failure', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('error'));
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });
});
