import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Gallery from '../Gallery';

vi.mock('../../../api/client', () => ({
  templateService: {
    list: vi.fn(),
    quickDeploy: vi.fn(),
    bulkDelete: vi.fn(),
    bulkPublish: vi.fn(),
    bulkUnpublish: vi.fn(),
  },
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
  },
  favoriteService: {
    list: vi.fn().mockResolvedValue([]),
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

vi.mock('../../../context/NotificationContext', () => ({
  useNotification: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
    showWarning: vi.fn(),
    showInfo: vi.fn(),
  }),
}));

import { templateService } from '../../../api/client';

describe('Template Gallery', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner initially', () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays published templates', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'My Template', description: 'A test template', category: 'Web', version: '1.0', is_published: true, owner_id: '1', default_branch: 'main', created_at: '', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
      expect(screen.getByText('A test template')).toBeInTheDocument();
    });
  });

  it('shows error on fetch failure', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('error'));
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  it('shows empty state when no templates match', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no templates found/i)).toBeInTheDocument();
    });
  });

  it('shows Quick Deploy button on published templates', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'Published Template', description: 'Desc', category: 'Web', version: '1.0', is_published: true, owner_id: '1', default_branch: 'main', created_at: '', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /quick deploy/i })).toBeInTheDocument();
    });
  });

  it('opens Quick Deploy dialog when button is clicked', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'Deploy Me', description: 'Desc', category: 'Web', version: '1.0', is_published: true, owner_id: '1', default_branch: 'main', created_at: '', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /quick deploy/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /quick deploy/i }));
    await waitFor(() => {
      expect(screen.getByText(/quick deploy: deploy me/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/instance name/i)).toBeInTheDocument();
    });
  });
});
