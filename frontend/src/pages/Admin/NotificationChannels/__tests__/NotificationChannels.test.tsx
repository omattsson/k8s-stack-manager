import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, within, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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
  userService: { list: vi.fn() },
  instanceService: { listAll: vi.fn() },
  definitionService: { listAll: vi.fn() },
  clusterService: { list: vi.fn() },
}));

const mockAuth = vi.hoisted(() => ({ role: 'devops' }));
vi.mock('../../../../context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'me', username: 'me', role: mockAuth.role } }),
}));

import {
  notificationChannelService,
  userService,
  instanceService,
  definitionService,
  clusterService,
} from '../../../../api/client';

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
    mockAuth.role = 'devops';
    vi.mocked(userService.list).mockResolvedValue([
      { id: 'u1', username: 'alice', display_name: 'Alice', role: 'user', auth_provider: 'local', disabled: false, service_account: false, created_at: '', updated_at: '' },
    ]);
    vi.mocked(instanceService.listAll).mockResolvedValue([
      { id: 'i1', owner_id: 'u2', owner_username: 'bob', name: 'a', stack_definition_id: 'd1', namespace: 'n', branch: 'master', status: 'running', created_at: '', updated_at: '' },
      { id: 'i2', owner_id: 'u2', owner_username: 'bob', name: 'b', stack_definition_id: 'd1', namespace: 'n2', branch: 'master', status: 'running', created_at: '', updated_at: '' },
    ]);
    vi.mocked(definitionService.listAll).mockResolvedValue([
      { id: 'd1', name: 'Full stack' } as never,
    ]);
    vi.mocked(clusterService.list).mockResolvedValue([
      { id: 'c1', name: 'dev-cluster' } as never,
    ]);
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

  it('shows a success toast when the Enabled switch changes', async () => {
    const user = userEvent.setup();
    (notificationChannelService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockChannels);
    vi.mocked(notificationChannelService.update).mockResolvedValue({ ...mockChannels[0], enabled: false });
    renderPage();

    const toggle = await screen.findByRole('switch', { name: 'Toggle slack-prod' });
    await user.click(toggle);

    await waitFor(() => {
      expect(notificationChannelService.update).toHaveBeenCalledWith('ch1', { enabled: false });
    });
    expect(await screen.findByText('Channel "slack-prod" is disabled')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Toggle slack-prod' })).not.toBeChecked();
  });

  it('shows an error toast and keeps the state when the Enabled switch fails', async () => {
    const user = userEvent.setup();
    (notificationChannelService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockChannels);
    vi.mocked(notificationChannelService.update).mockRejectedValue(new Error('Network error'));
    renderPage();

    const toggle = await screen.findByRole('switch', { name: 'Toggle teams-dev' });
    await user.click(toggle);

    expect(await screen.findByText(/failed to toggle channel/i)).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Toggle teams-dev' })).not.toBeChecked();
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

  it('shows "All instances" or a filter summary in the list', async () => {
    vi.mocked(notificationChannelService.list).mockResolvedValue([
      mockChannels[0],
      { ...mockChannels[1], filters: { instance_name_patterns: ['rdbtest-*'], cluster_ids: ['c1'] } },
    ]);
    renderPage();

    await waitFor(() => {
      expect(screen.getByTestId('channel-filters-ch1')).toHaveTextContent('All instances');
    });
    expect(screen.getByTestId('channel-filters-ch2')).toHaveTextContent('Names: rdbtest-*; Clusters: 1');
  });

  it('creates a channel with filters from the Filters section', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([]);
    vi.mocked(notificationChannelService.create).mockResolvedValue({ ...mockChannels[0], warnings: [] });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /create channel/i }));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByLabelText(/^name/i), 'team-a');
    await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/a');
    await user.type(within(dialog).getByLabelText(/instance name patterns/i), 'RdbTest-*{Enter}');

    // Devops users pick owners from the stack owners (GET /users is admin-only).
    await user.click(within(dialog).getByLabelText(/owners/i));
    await user.click(await screen.findByRole('option', { name: 'bob' }));
    await user.click(within(dialog).getByLabelText(/clusters/i));
    await user.click(await screen.findByRole('option', { name: 'dev-cluster' }));

    await user.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => {
      expect(notificationChannelService.create).toHaveBeenCalledWith({
        name: 'team-a',
        webhook_url: 'https://hooks.example.com/a',
        enabled: true,
        format: 'generic',
        filters: { instance_name_patterns: ['rdbtest-*'], owner_ids: ['u2'], cluster_ids: ['c1'] },
      });
    });
    expect(userService.list).not.toHaveBeenCalled();
    expect(instanceService.listAll).toHaveBeenCalledTimes(1);
  });

  it('admins pick owners from all users', async () => {
    const user = userEvent.setup();
    mockAuth.role = 'admin';
    vi.mocked(notificationChannelService.list).mockResolvedValue([]);
    renderPage();

    await user.click(await screen.findByRole('button', { name: /create channel/i }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByLabelText(/owners/i));
    expect(await screen.findByRole('option', { name: 'Alice (alice)' })).toBeInTheDocument();
    expect(instanceService.listAll).not.toHaveBeenCalled();
  });

  it('edits the filters of a channel and shows unknown IDs as the ID', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([
      { ...mockChannels[0], filters: { cluster_ids: ['c1', 'gone-cluster'] } },
    ]);
    vi.mocked(notificationChannelService.update).mockResolvedValue({ ...mockChannels[0], warnings: ['cluster_ids: cluster gone-cluster does not exist'] });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /edit slack-prod/i }));
    const dialog = await screen.findByRole('dialog');
    await waitFor(() => {
      expect(within(dialog).getByRole('button', { name: 'dev-cluster' })).toBeInTheDocument();
    });
    expect(within(dialog).getByRole('button', { name: 'gone-cluster' })).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Update' }));

    await waitFor(() => {
      expect(notificationChannelService.update).toHaveBeenCalledWith('ch1', expect.objectContaining({
        filters: { cluster_ids: ['c1', 'gone-cluster'] },
      }));
    });
    expect(await screen.findByText(/saved with filter warnings/i)).toBeInTheDocument();
  });

  it('sends an empty filters object when all filters are removed', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([
      { ...mockChannels[0], filters: { instance_name_patterns: [] } },
    ]);
    vi.mocked(notificationChannelService.update).mockResolvedValue({ ...mockChannels[0] });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /edit slack-prod/i }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Update' }));

    await waitFor(() => {
      expect(notificationChannelService.update).toHaveBeenCalledWith('ch1', expect.objectContaining({ filters: {} }));
    });
  });

  it('shows the API error for an invalid pattern', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([]);
    vi.mocked(notificationChannelService.create).mockRejectedValue({
      response: { data: { error: 'Invalid filters: instance_name_patterns: "[" is not a valid pattern' } },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /create channel/i }));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByLabelText(/^name/i), 'bad');
    await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/a');
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByText(/is not a valid pattern/i)).toBeInTheDocument();
  });

  it('shows the Format select with Generic JSON as the default', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([]);
    renderPage();

    await user.click(await screen.findByRole('button', { name: /create channel/i }));
    const dialog = await screen.findByRole('dialog');
    const select = within(dialog).getByRole('combobox', { name: /format/i });
    expect(select).toHaveTextContent('Generic JSON');
    expect(within(dialog).getByText('Sends the stack manager event JSON.')).toBeInTheDocument();
  });

  it('sends format teams on create when Microsoft Teams is chosen', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([]);
    vi.mocked(notificationChannelService.create).mockResolvedValue({ ...mockChannels[1], format: 'teams' });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /create channel/i }));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByLabelText(/^name/i), 'teams-alerts');
    await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/teams');
    await user.click(within(dialog).getByRole('combobox', { name: /format/i }));
    await user.click(await screen.findByRole('option', { name: 'Microsoft Teams (Adaptive Card)' }));
    expect(within(dialog).getByText(/post to a channel when a webhook request is received/i)).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => {
      expect(notificationChannelService.create).toHaveBeenCalledWith(expect.objectContaining({
        name: 'teams-alerts',
        format: 'teams',
      }));
    });
  });

  it('shows a Teams chip in the list and the stored format when editing', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([
      mockChannels[0],
      { ...mockChannels[1], format: 'teams' },
    ]);
    vi.mocked(notificationChannelService.update).mockResolvedValue({ ...mockChannels[1], format: 'generic' });
    renderPage();

    expect(await screen.findByText('Teams')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /edit teams-dev/i }));
    const dialog = await screen.findByRole('dialog');
    const select = within(dialog).getByRole('combobox', { name: /format/i });
    expect(select).toHaveTextContent('Microsoft Teams (Adaptive Card)');

    await user.click(select);
    await user.click(await screen.findByRole('option', { name: 'Generic JSON' }));
    await user.click(within(dialog).getByRole('button', { name: 'Update' }));

    await waitFor(() => {
      expect(notificationChannelService.update).toHaveBeenCalledWith('ch2', expect.objectContaining({ format: 'generic' }));
    });
  });

  it('treats a channel without format as generic when editing', async () => {
    const user = userEvent.setup();
    vi.mocked(notificationChannelService.list).mockResolvedValue([mockChannels[0]]);
    vi.mocked(notificationChannelService.update).mockResolvedValue({ ...mockChannels[0] });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /edit slack-prod/i }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('combobox', { name: /format/i })).toHaveTextContent('Generic JSON');
    expect(screen.queryByText('Teams')).not.toBeInTheDocument();
  });
  describe('secret and URL encryption states (#692)', () => {
    const unreadable = {
      ...mockChannels[0],
      id: 'ch3',
      name: 'broken',
      webhook_url: '',
      webhook_url_unreadable: true,
      secret_unreadable: true,
    };

    it('shows the API 400 message of a secret without encryption key verbatim', async () => {
      const user = userEvent.setup();
      const msg = 'A channel secret cannot be stored without KUBECONFIG_ENCRYPTION_KEY. Leave the secret empty, or set the key.';
      vi.mocked(notificationChannelService.list).mockResolvedValue([]);
      vi.mocked(notificationChannelService.create).mockRejectedValue({ response: { data: { error: msg } } });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /create channel/i }));
      const dialog = await screen.findByRole('dialog');
      await user.type(within(dialog).getByLabelText(/^name/i), 'x');
      await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/a');
      await user.type(within(dialog).getByLabelText(/^secret/i), 's3cret');
      await user.click(within(dialog).getByRole('button', { name: 'Create' }));

      expect(await within(dialog).findByText(msg)).toBeInTheDocument();
    });

    it('refuses a URL longer than 2048 characters without calling the API', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([]);
      renderPage();

      await user.click(await screen.findByRole('button', { name: /create channel/i }));
      const dialog = await screen.findByRole('dialog');
      await user.type(within(dialog).getByLabelText(/^name/i), 'x');
      fireEvent.change(within(dialog).getByLabelText(/webhook url/i), {
        target: { value: 'https://hooks.example.com/' + 'a'.repeat(2048) },
      });
      await user.click(within(dialog).getByRole('button', { name: 'Create' }));

      expect(await within(dialog).findByText('Webhook URL must be at most 2048 characters')).toBeInTheDocument();
      expect(notificationChannelService.create).not.toHaveBeenCalled();
    });

    it('shows the API message of a too long URL', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([]);
      vi.mocked(notificationChannelService.create).mockRejectedValue({
        response: { data: { error: 'Webhook URL must be at most 2048 characters' } },
      });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /create channel/i }));
      const dialog = await screen.findByRole('dialog');
      await user.type(within(dialog).getByLabelText(/^name/i), 'x');
      await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/a');
      await user.click(within(dialog).getByRole('button', { name: 'Create' }));

      expect(await within(dialog).findByText('Webhook URL must be at most 2048 characters')).toBeInTheDocument();
    });

    it('marks unreadable channels in the list', async () => {
      vi.mocked(notificationChannelService.list).mockResolvedValue([unreadable]);
      renderPage();

      expect(await screen.findByText('Secret unreadable')).toBeInTheDocument();
      expect(screen.getByText('URL unreadable')).toBeInTheDocument();
      expect(screen.getByText('Cannot be read')).toBeInTheDocument();
    });

    it('does not mark normal channels', async () => {
      vi.mocked(notificationChannelService.list).mockResolvedValue([mockChannels[0]]);
      renderPage();

      await screen.findByText('slack-prod');
      expect(screen.queryByText('Secret unreadable')).not.toBeInTheDocument();
      expect(screen.queryByText('URL unreadable')).not.toBeInTheDocument();
    });

    it('warns in the edit dialog and omits the empty URL from the PUT', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([unreadable]);
      vi.mocked(notificationChannelService.update).mockResolvedValue({ ...unreadable });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /edit broken/i }));
      const dialog = await screen.findByRole('dialog');
      expect(within(dialog).getByText('The stored webhook URL cannot be decrypted. Enter the URL again to repair the channel.')).toBeInTheDocument();
      expect(within(dialog).getByText('The stored secret cannot be decrypted. Deliveries fail until you set the secret again or remove it.')).toBeInTheDocument();
      expect(within(dialog).getByLabelText(/webhook url/i)).toHaveValue('');

      await user.click(within(dialog).getByLabelText('Enabled'));
      await user.click(within(dialog).getByRole('button', { name: 'Update' }));

      await waitFor(() => expect(notificationChannelService.update).toHaveBeenCalledTimes(1));
      const payload = vi.mocked(notificationChannelService.update).mock.calls[0][1];
      expect(payload).not.toHaveProperty('webhook_url');
      expect(payload).toMatchObject({ name: 'broken', enabled: false });
    });

    it('sends the new URL and secret to repair an unreadable channel', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([unreadable]);
      vi.mocked(notificationChannelService.update).mockResolvedValue({ ...unreadable });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /edit broken/i }));
      const dialog = await screen.findByRole('dialog');
      await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/new');
      await user.type(within(dialog).getByLabelText(/^secret/i), 'fresh');
      await user.click(within(dialog).getByRole('button', { name: 'Update' }));

      await waitFor(() => expect(notificationChannelService.update).toHaveBeenCalledTimes(1));
      expect(vi.mocked(notificationChannelService.update).mock.calls[0][1]).toMatchObject({
        webhook_url: 'https://hooks.example.com/new',
        secret: 'fresh',
      });
    });

    it('sends an empty secret when Remove secret is chosen', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([unreadable]);
      vi.mocked(notificationChannelService.update).mockResolvedValue({ ...unreadable });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /edit broken/i }));
      const dialog = await screen.findByRole('dialog');
      await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/new');
      await user.click(within(dialog).getByRole('checkbox', { name: /remove secret/i }));
      expect(within(dialog).getByLabelText(/^secret/i)).toBeDisabled();
      await user.click(within(dialog).getByRole('button', { name: 'Update' }));

      await waitFor(() => expect(notificationChannelService.update).toHaveBeenCalledTimes(1));
      const payload = vi.mocked(notificationChannelService.update).mock.calls[0][1];
      expect(payload).toHaveProperty('secret', '');
    });

    it('sends no secret key when nothing is typed and Remove is not chosen', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([unreadable]);
      vi.mocked(notificationChannelService.update).mockResolvedValue({ ...unreadable });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /edit broken/i }));
      const dialog = await screen.findByRole('dialog');
      await user.type(within(dialog).getByLabelText(/webhook url/i), 'https://hooks.example.com/new');
      await user.click(within(dialog).getByRole('button', { name: 'Update' }));

      await waitFor(() => expect(notificationChannelService.update).toHaveBeenCalledTimes(1));
      expect(vi.mocked(notificationChannelService.update).mock.calls[0][1]).not.toHaveProperty('secret');
    });

    it('offers Remove secret for a readable channel in edit mode, not in create mode', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([mockChannels[0]]);
      vi.mocked(notificationChannelService.update).mockResolvedValue({ ...mockChannels[0] });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /edit slack-prod/i }));
      let dialog = await screen.findByRole('dialog');
      await user.click(within(dialog).getByRole('checkbox', { name: /remove secret/i }));
      await user.click(within(dialog).getByRole('button', { name: 'Update' }));
      await waitFor(() => expect(notificationChannelService.update).toHaveBeenCalledTimes(1));
      expect(vi.mocked(notificationChannelService.update).mock.calls[0][1]).toHaveProperty('secret', '');
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

      await user.click(screen.getByRole('button', { name: /create channel/i }));
      dialog = await screen.findByRole('dialog');
      expect(within(dialog).queryByRole('checkbox', { name: /remove secret/i })).not.toBeInTheDocument();
    });

    it('counts code points, not UTF-16 units, for the URL length', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([]);
      vi.mocked(notificationChannelService.create).mockResolvedValue({ ...mockChannels[0] });
      renderPage();

      await user.click(await screen.findByRole('button', { name: /create channel/i }));
      const dialog = await screen.findByRole('dialog');
      await user.type(within(dialog).getByLabelText(/^name/i), 'x');
      fireEvent.change(within(dialog).getByLabelText(/webhook url/i), {
        target: { value: 'https://hooks.example.com/' + '\u{1F600}'.repeat(1100) },
      });
      await user.click(within(dialog).getByRole('button', { name: 'Create' }));

      await waitFor(() => expect(notificationChannelService.create).toHaveBeenCalledTimes(1));
    });

    it('still requires the URL when editing a normal channel', async () => {
      const user = userEvent.setup();
      vi.mocked(notificationChannelService.list).mockResolvedValue([mockChannels[0]]);
      renderPage();

      await user.click(await screen.findByRole('button', { name: /edit slack-prod/i }));
      const dialog = await screen.findByRole('dialog');
      expect(within(dialog).queryByText(/cannot be decrypted/i)).not.toBeInTheDocument();
      await user.clear(within(dialog).getByLabelText(/webhook url/i));
      await user.click(within(dialog).getByRole('button', { name: 'Update' }));

      expect((await within(dialog).findAllByText('Webhook URL is required')).length).toBeGreaterThan(0);
      expect(notificationChannelService.update).not.toHaveBeenCalled();
    });
  });
});
