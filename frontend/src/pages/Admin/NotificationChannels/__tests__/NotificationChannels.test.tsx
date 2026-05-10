import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import NotificationChannels from '../index';
import { NotificationProvider } from '../../../../context/NotificationContext';

vi.mock('../../../../api/client', () => ({
  notificationChannelService: {
    list: vi.fn(),
    create: vi.fn(),
    get: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    getSubscriptions: vi.fn(),
    updateSubscriptions: vi.fn(),
    test: vi.fn(),
    deliveryLogs: vi.fn(),
    eventTypes: vi.fn(),
  },
}));

import { notificationChannelService } from '../../../../api/client';

const mockChannels = [
  {
    id: 'ch1',
    name: 'slack-prod',
    webhook_url: 'https://hooks.slack.com/services/T00/B00/xxx',
    enabled: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    subscription_count: 3,
  },
  {
    id: 'ch2',
    name: 'teams-dev',
    webhook_url: 'https://outlook.office.com/webhook/abc',
    enabled: false,
    created_at: '2026-02-01T00:00:00Z',
    updated_at: '2026-02-01T00:00:00Z',
    subscription_count: 0,
  },
];

function renderPage() {
  return render(
    <MemoryRouter>
      <NotificationProvider>
        <NotificationChannels />
      </NotificationProvider>
    </MemoryRouter>
  );
}

describe('NotificationChannels Page', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('shows loading state initially', () => {
    (notificationChannelService.list as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise(() => {})
    );
    renderPage();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('renders channel table with data', async () => {
    (notificationChannelService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockChannels);
    renderPage();

    await waitFor(() => {
      expect(screen.getByText('slack-prod')).toBeInTheDocument();
      expect(screen.getByText('teams-dev')).toBeInTheDocument();
    });

    // Verify heading
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Notification Channels');
    // Verify Create button
    expect(screen.getByRole('button', { name: /create channel/i })).toBeInTheDocument();
  });

  it('shows empty state when no channels exist', async () => {
    (notificationChannelService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    renderPage();

    await waitFor(() => {
      expect(
        screen.getByText(/no notification channels configured/i)
      ).toBeInTheDocument();
    });
  });

  it('shows error state on API failure', async () => {
    (notificationChannelService.list as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error('Network error')
    );
    renderPage();

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/failed to load notification channels/i)).toBeInTheDocument();
    });
  });

  it('renders action buttons for each channel', async () => {
    (notificationChannelService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockChannels);
    renderPage();

    await waitFor(() => {
      expect(screen.getByText('slack-prod')).toBeInTheDocument();
    });

    // Edit, Test, and Delete buttons should exist for each channel.
    expect(screen.getByRole('button', { name: /edit slack-prod/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /test slack-prod/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /delete slack-prod/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /edit teams-dev/i })).toBeInTheDocument();
  });
});
