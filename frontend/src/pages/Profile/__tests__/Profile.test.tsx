import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Profile from '../index';

vi.mock('../../../api/client', () => ({
  apiKeyService: {
    list: vi.fn(),
    create: vi.fn(),
    delete: vi.fn(),
  },
  notificationService: {
    getPreferences: vi.fn(),
    updatePreferences: vi.fn(),
  },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: vi.fn(),
}));

vi.mock('../../../context/NotificationContext', () => ({
  useNotification: vi.fn().mockReturnValue({
    showSuccess: vi.fn(),
    showError: vi.fn(),
    showWarning: vi.fn(),
    showInfo: vi.fn(),
  }),
}));

import { apiKeyService, notificationService } from '../../../api/client';
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
    (notificationService.getPreferences as ReturnType<typeof vi.fn>).mockResolvedValue([
      { event_type: 'deployment.success', enabled: true },
      { event_type: 'deployment.error', enabled: true },
      { event_type: 'deployment.stopped', enabled: true },
      { event_type: 'instance.deleted', enabled: true },
    ]);
    (notificationService.updatePreferences as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
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
    expect(screen.getAllByRole('progressbar').length).toBeGreaterThanOrEqual(1);
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

  it('opens generate dialog, submits, and calls apiKeyService.create', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (apiKeyService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'k-new',
      name: 'Test Key',
      prefix: 'sk_test11',
      raw_key: 'sk_test11abcdef',
      created_at: '2026-03-18T00:00:00Z',
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate api key/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /generate api key/i }));
    expect(screen.getByRole('heading', { name: /generate api key/i })).toBeInTheDocument();

    await user.type(screen.getByLabelText(/key name/i), 'Test Key');
    await user.click(screen.getByRole('button', { name: /^generate$/i }));

    await waitFor(() => {
      expect(apiKeyService.create).toHaveBeenCalledWith('u1', { name: 'Test Key' });
    });
  });

  it('shows raw key modal with the generated key after successful creation', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (apiKeyService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'k-new',
      name: 'Test Key',
      prefix: 'sk_test11',
      raw_key: 'sk_test11abcdef',
      created_at: '2026-03-18T00:00:00Z',
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate api key/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /generate api key/i }));
    await user.type(screen.getByLabelText(/key name/i), 'Test Key');
    await user.click(screen.getByRole('button', { name: /^generate$/i }));

    await waitFor(() => {
      expect(screen.getByText('API Key Generated')).toBeInTheDocument();
      expect(screen.getByText('sk_test11abcdef')).toBeInTheDocument();
    });
  });

  it('shows revoke confirmation dialog and calls apiKeyService.delete on confirm', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockApiKeys);
    (apiKeyService.delete as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('CI Key')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /revoke key ci key/i }));
    expect(screen.getByText('Revoke API Key')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /^revoke$/i }));

    await waitFor(() => {
      expect(apiKeyService.delete).toHaveBeenCalledWith('u1', 'k1');
    });
  });

  it('shows validation error when submitting empty key name', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate api key/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /generate api key/i }));
    // Submit without filling name
    await user.click(screen.getByRole('button', { name: /^generate$/i }));

    await waitFor(() => {
      expect(screen.getByText('Key name is required')).toBeInTheDocument();
    });
  });

  it('shows error when generate key API fails', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (apiKeyService.create as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate api key/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /generate api key/i }));
    await user.type(screen.getByLabelText(/key name/i), 'Failing Key');
    await user.click(screen.getByRole('button', { name: /^generate$/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to generate API key')).toBeInTheDocument();
    });
  });

  it('shows error when revoking a key fails', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockApiKeys);
    (apiKeyService.delete as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('CI Key')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /revoke key ci key/i }));
    await user.click(screen.getByRole('button', { name: /^revoke$/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to revoke API key')).toBeInTheDocument();
    });
  });

  it('renders copy button in raw key modal', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (apiKeyService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'k-copy',
      name: 'Copy Key',
      prefix: 'sk_copy11',
      raw_key: 'sk_copy11xyz',
      created_at: '2026-03-18T00:00:00Z',
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate api key/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /generate api key/i }));
    await user.type(screen.getByLabelText(/key name/i), 'Copy Key');
    await user.click(screen.getByRole('button', { name: /^generate$/i }));

    await waitFor(() => {
      expect(screen.getByText('sk_copy11xyz')).toBeInTheDocument();
    });

    // Copy button is rendered in the raw key modal
    expect(screen.getByRole('button', { name: /copy api key/i })).toBeInTheDocument();

    // Close modal via Done button
    await user.click(screen.getByRole('button', { name: /done/i }));

    await waitFor(() => {
      expect(screen.queryByText('sk_copy11xyz')).not.toBeInTheDocument();
    });
  });

  it('shows OIDC auth chip when authProvider is set', async () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: currentUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
      authProvider: 'azure-ad',
      oidcConfig: { provider_name: 'Azure AD' },
      authEmail: 'alice@example.com',
    });
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/SSO via Azure AD/)).toBeInTheDocument();
      expect(screen.getByText('alice@example.com')).toBeInTheDocument();
    });
  });

  it('toggles notification preference and saves', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Notification Preferences')).toBeInTheDocument();
    });

    // Toggle the first preference
    const toggles = screen.getAllByRole('checkbox');
    expect(toggles.length).toBeGreaterThanOrEqual(1);
    await user.click(toggles[0]);

    // Save preferences
    await user.click(screen.getByRole('button', { name: /save/i }));
    await waitFor(() => {
      expect(notificationService.updatePreferences).toHaveBeenCalled();
    });
  });

  it('includes expires_at when generate form has an expiry date', async () => {
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (apiKeyService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'k-temp',
      name: 'Temp Key',
      prefix: 'sk_temp11',
      raw_key: 'sk_temp11abcdef',
      created_at: '2026-03-18T00:00:00Z',
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Profile />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /generate api key/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /generate api key/i }));
    await user.type(screen.getByLabelText(/key name/i), 'Temp Key');

    fireEvent.change(screen.getByLabelText(/expires at/i), {
      target: { value: '2026-12-31' },
    });

    await user.click(screen.getByRole('button', { name: /^generate$/i }));

    await waitFor(() => {
      expect(apiKeyService.create).toHaveBeenCalledWith(
        'u1',
        expect.objectContaining({ name: 'Temp Key', expires_at: '2026-12-31' }),
      );
    });
  });
});
