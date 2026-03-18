import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Profile from '../index';

vi.mock('../../../api/client', () => ({
  apiKeyService: {
    list: vi.fn(),
    create: vi.fn(),
    delete: vi.fn(),
  },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: vi.fn(),
}));

import { apiKeyService } from '../../../api/client';
import { useAuth } from '../../../context/AuthContext';

const currentUser = {
  id: 'u1',
  username: 'alice',
  role: 'devops',
  display_name: 'Alice Smith',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const mockApiKeys = [
  {
    id: 'k1',
    user_id: 'u1',
    name: 'CI Key',
    prefix: 'a1b2c3d4',
    created_at: '2026-03-01T00:00:00Z',
    last_used_at: '2026-03-10T00:00:00Z',
    expires_at: undefined,
  },
  {
    id: 'k2',
    user_id: 'u1',
    name: 'Deploy Key',
    prefix: 'e5f6g7h8',
    created_at: '2026-03-05T00:00:00Z',
  },
];

describe('Profile Page', () => {
  beforeEach(() => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: currentUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows a loading spinner initially', () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays page heading and account details', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('My Profile');
      expect(screen.getByText('alice')).toBeInTheDocument();
      expect(screen.getByText('Alice Smith')).toBeInTheDocument();
    });
  });

  it('displays API keys when loaded', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockApiKeys);
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('CI Key')).toBeInTheDocument();
      expect(screen.getByText('Deploy Key')).toBeInTheDocument();
      expect(screen.getByText('a1b2c3d4...')).toBeInTheDocument();
    });
  });

  it('shows empty state when no API keys exist', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no api keys yet/i)).toBeInTheDocument();
    });
  });

  it('shows error alert when API key fetch fails', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/failed to load api keys/i)).toBeInTheDocument();
    });
  });

  it('renders Generate API Key button', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate api key/i })).toBeInTheDocument();
    });
  });
});
