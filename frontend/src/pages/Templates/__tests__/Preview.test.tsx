import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import Preview from '../Preview';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../../api/client', () => ({
  templateService: {
    get: vi.fn(),
    clone: vi.fn(),
    publish: vi.fn(),
    unpublish: vi.fn(),
    listVersions: vi.fn(),
    getVersion: vi.fn(),
    diffVersions: vi.fn(),
  },
}));

vi.mock('react-diff-viewer-continued', () => ({
  default: ({ oldValue, newValue, leftTitle, rightTitle }: {
    oldValue: string; newValue: string; leftTitle: string; rightTitle: string;
  }) => (
    <div data-testid="diff-viewer">
      <span>{leftTitle}</span>
      <span>{rightTitle}</span>
      <span>{oldValue}</span>
      <span>{newValue}</span>
    </div>
  ),
  DiffMethod: { LINES: 'diffLines' },
}));

const authState = vi.hoisted(() => ({
  user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin' },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({
    user: authState.user,
    isAuthenticated: true,
    isLoading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

import { templateService } from '../../../api/client';

const mockTemplate = {
  id: 't1',
  name: 'Web Stack Template',
  description: 'A modern web stack',
  category: 'Web',
  version: '1.0',
  owner_id: '1',
  default_branch: 'main',
  is_published: true,
  created_at: '',
  updated_at: '',
  charts: [
    {
      id: 'tc1',
      stack_template_id: 't1',
      chart_name: 'frontend',
      repository_url: 'https://charts.example.com',
      source_repo_url: '',
      chart_path: 'charts/frontend',
      chart_version: '1.0.0',
      default_values: 'replicaCount: 2',
      locked_values: 'image: nginx',
      deploy_order: 1,
      required: true,
      created_at: '',
    },
  ],
};

describe('Templates Preview', () => {
  afterEach(() => {
    vi.clearAllMocks();
    authState.user = { id: '1', username: 'admin', role: 'admin', display_name: 'Admin' };
  });

  it('shows loading spinner while fetching', () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays template details when loaded', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });
    expect(screen.getByText('A modern web stack')).toBeInTheDocument();
    expect(screen.getByText('Published')).toBeInTheDocument();
    expect(screen.getByText('Web')).toBeInTheDocument();
    expect(screen.getByText('Working copy: v1.0')).toBeInTheDocument();
    expect(screen.getByText('Charts (1)')).toBeInTheDocument();
    expect(screen.getByText('frontend')).toBeInTheDocument();
  });

  it('shows error alert when fetch fails', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Not found'));
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load template')).toBeInTheDocument();
    });
  });

  it('shows Use Template button for published templates', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /use template/i })).toBeInTheDocument();
    });
  });

  it('shows Edit and Clone buttons for admin users who own the template', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /edit/i })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /clone as template/i })).toBeInTheDocument();
    });
  });

  it('displays chart default values and locked values', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Default Values')).toBeInTheDocument();
    });
    expect(screen.getByText('replicaCount: 2')).toBeInTheDocument();
    expect(screen.getByText('Locked Values')).toBeInTheDocument();
    expect(screen.getByText('image: nginx')).toBeInTheDocument();
  });

  it('clones template and navigates to edit', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    (templateService.clone as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 't-clone' });

    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clone as template/i }));

    // The dialog asks for the name first; nothing is created yet
    const dialog = await screen.findByRole('dialog', { name: /clone as template/i });
    const nameInput = within(dialog).getByRole('textbox', { name: /name/i });
    expect(nameInput).toHaveValue('Web Stack Template (Copy)');
    expect(templateService.clone).not.toHaveBeenCalled();

    await user.clear(nameInput);
    await user.type(nameInput, '  My Copy ');
    await user.click(within(dialog).getByRole('button', { name: /^clone$/i }));

    await waitFor(() => {
      expect(templateService.clone).toHaveBeenCalledWith('t1', { name: 'My Copy' });
    });
    expect(mockNavigate).toHaveBeenCalledWith('/templates/t-clone/edit');
  });

  it('creates nothing when the clone dialog is cancelled', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);

    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clone as template/i }));
    const dialog = await screen.findByRole('dialog', { name: /clone as template/i });
    const nameInput = within(dialog).getByRole('textbox', { name: /name/i });
    await user.clear(nameInput);
    expect(within(dialog).getByRole('button', { name: /^clone$/i })).toBeDisabled();
    await user.click(within(dialog).getByRole('button', { name: /cancel/i }));

    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: /clone as template/i })).not.toBeInTheDocument();
    });
    expect(templateService.clone).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalledWith(expect.stringMatching(/edit$/));
  });

  it('shows the API error in the clone dialog', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    (templateService.clone as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 400, data: { error: 'name is required' } },
    });

    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clone as template/i }));
    const dialog = await screen.findByRole('dialog', { name: /clone as template/i });
    await user.click(within(dialog).getByRole('button', { name: /^clone$/i }));

    expect(await within(dialog).findByText('Failed to clone template (HTTP 400: name is required)')).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalledWith(expect.stringMatching(/edit$/));
  });

  it('navigates back to gallery', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);

    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /back to gallery/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/templates');
  });

  const releasedTemplate = {
    ...mockTemplate,
    version: '1.1.0',
    published_version: '1.0.0',
    has_unpublished_changes: true,
  };

  const renderPreview = () =>
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );

  const mockGet = () => templateService.get as ReturnType<typeof vi.fn>;

  it('shows Publish and Unpublish for the owner', async () => {
    mockGet().mockResolvedValue(releasedTemplate);
    renderPreview();
    expect(await screen.findByRole('button', { name: /^publish$/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^unpublish$/i })).toBeInTheDocument();
  });

  it('hides Publish, Unpublish and Edit for a non-devops user', async () => {
    authState.user = { id: '2', username: 'dev', role: 'user', display_name: 'Dev' };
    mockGet().mockResolvedValue(releasedTemplate);
    renderPreview();
    await screen.findByText('Web Stack Template');
    expect(screen.queryByRole('button', { name: /^publish$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^unpublish$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^edit$/i })).not.toBeInTheDocument();
  });

  it('hides Publish for a devops user who does not own the template', async () => {
    authState.user = { id: '2', username: 'ops', role: 'devops', display_name: 'Ops' };
    mockGet().mockResolvedValue(releasedTemplate);
    renderPreview();
    await screen.findByText('Web Stack Template');
    expect(screen.queryByRole('button', { name: /^publish$/i })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /clone as template/i })).toBeInTheDocument();
  });

  it('shows the released version chip and the unpublished changes banner', async () => {
    mockGet().mockResolvedValue(releasedTemplate);
    renderPreview();
    expect(await screen.findByText('Released: v1.0.0')).toBeInTheDocument();
    expect(screen.getByText(/unpublished changes\. users get version 1\.0\.0/i)).toBeInTheDocument();
  });

  it('does not show the banner without unpublished changes', async () => {
    mockGet().mockResolvedValue({ ...releasedTemplate, has_unpublished_changes: false });
    renderPreview();
    await screen.findByText('Released: v1.0.0');
    expect(screen.queryByText(/unpublished changes/i)).not.toBeInTheDocument();
  });

  it('shows the diff between the release and the working copy', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: 'ver-1', template_id: 't1', version: '1.0.0', change_summary: '', created_by: '1', created_at: '' },
    ]);
    (templateService.diffVersions as ReturnType<typeof vi.fn>).mockResolvedValue({
      left: { version: '1.0.0', snapshot: { template: {}, charts: [] } },
      right: { version: '1.1.0', snapshot: { template: {}, charts: [] } },
      chart_diffs: [
        { chart_name: 'frontend', left_values: 'replicaCount: 2', right_values: 'replicaCount: 3', has_differences: true, change_type: 'modified' },
      ],
    });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /show changes/i }));

    const dialog = await screen.findByRole('dialog');
    await waitFor(() => {
      expect(templateService.diffVersions).toHaveBeenCalledWith('t1', 'ver-1');
      expect(within(dialog).getByText('replicaCount: 3')).toBeInTheDocument();
    });
    expect(within(dialog).getByText('v1.0.0')).toBeInTheDocument();
    expect(within(dialog).getByText('Working copy')).toBeInTheDocument();
  });

  it('uses published_version_id for the diff without listing versions', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue({ ...releasedTemplate, published_version_id: 'ver-9' });
    (templateService.diffVersions as ReturnType<typeof vi.fn>).mockResolvedValue({
      left: { id: 'ver-9', version: '1.0.0', snapshot: { template: {}, charts: [] } },
      right: { id: 'working', version: '1.1.0', snapshot: { template: {}, charts: [] }, is_working_copy: true },
      chart_diffs: [],
    });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /show changes/i }));

    expect(await screen.findByText(/no chart changes/i)).toBeInTheDocument();
    expect(templateService.diffVersions).toHaveBeenCalledWith('t1', 'ver-9');
    expect(templateService.listVersions).not.toHaveBeenCalled();
  });

  it('shows changed template fields in the changes dialog (#499)', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue({ ...releasedTemplate, published_version_id: 'ver-9' });
    (templateService.diffVersions as ReturnType<typeof vi.fn>).mockResolvedValue({
      left: { id: 'ver-9', version: '1.0.0', snapshot: { template: {}, charts: [] } },
      right: { id: 'working', version: '1.0.0', snapshot: { template: {}, charts: [] }, is_working_copy: true },
      template_diffs: [
        { field: 'description', left: 'Old text', right: 'New text' },
        { field: 'category', left: '', right: 'Web' },
      ],
      chart_diffs: [
        { chart_name: 'frontend', left_values: 'a: 1', right_values: 'a: 1', has_differences: false, change_type: 'unchanged' },
      ],
    });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /show changes/i }));

    const fields = await screen.findByRole('table', { name: 'Template details' });
    const description = within(fields).getByRole('row', { name: /description/i });
    expect(within(description).getByText('Old text')).toBeInTheDocument();
    expect(within(description).getByText('New text')).toBeInTheDocument();
    const category = within(fields).getByRole('row', { name: /category/i });
    expect(within(category).getByText('(empty)')).toBeInTheDocument();
    expect(within(category).getByText('Web')).toBeInTheDocument();
  });

  it('shows an error in the changes dialog when the diff fails', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('boom'));
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /show changes/i }));

    expect(await screen.findByText('Failed to load changes')).toBeInTheDocument();
  });

  it('prefills the publish dialog with the working copy version when it is newer', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^publish$/i }));

    await screen.findByRole('dialog');
    expect(screen.getByRole('textbox', { name: /^version/i })).toHaveValue('1.1.0');
  });

  it('prefills the next patch version when the working copy version is already released', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue({ ...releasedTemplate, version: '1.0.0' });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^publish$/i }));

    await screen.findByRole('dialog');
    expect(screen.getByRole('textbox', { name: /^version/i })).toHaveValue('1.0.1');
  });

  it('prefills the released version when there are no unpublished changes', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue({ ...releasedTemplate, version: '1.0.0', has_unpublished_changes: false });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^publish$/i }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('textbox', { name: /^version/i })).toHaveValue('1.0.0');
    expect(within(dialog).getByText(/no changes since version 1\.0\.0/i)).toBeInTheDocument();
  });

  it('publishes a version with a change summary and reloads the template', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    (templateService.publish as ReturnType<typeof vi.fn>).mockResolvedValue({
      template: { ...releasedTemplate, published_version: '1.1.0', has_unpublished_changes: false },
      snapshotCreated: true,
    });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^publish$/i }));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByRole('textbox', { name: /change summary/i }), 'More replicas');
    await user.click(within(dialog).getByRole('button', { name: /^publish$/i }));

    await waitFor(() => {
      expect(templateService.publish).toHaveBeenCalledWith('t1', { version: '1.1.0', change_summary: 'More replicas' });
      expect(screen.getByText('Published version 1.1.0.')).toBeInTheDocument();
    });
    expect(templateService.get).toHaveBeenCalledTimes(2);
  }, 15000);

  it('shows an inline error when the version already exists (409)', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    (templateService.publish as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 409, data: { error: 'Version 1.1.0 already exists' } },
    });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^publish$/i }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: /^publish$/i }));

    expect(await within(dialog).findByText(/version 1\.1\.0 already exists/i)).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('tells the user when nothing changed since the latest release', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue({ ...releasedTemplate, version: '1.0.0', has_unpublished_changes: false });
    (templateService.publish as ReturnType<typeof vi.fn>).mockResolvedValue({
      template: { ...releasedTemplate, published_version: '1.0.0' },
      snapshotCreated: false,
    });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^publish$/i }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: /^publish$/i }));

    expect(await screen.findByText(/no changes since version 1\.0\.0/i)).toBeInTheDocument();
  });

  it('unpublishes the template', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    (templateService.unpublish as ReturnType<typeof vi.fn>).mockResolvedValue({ ...releasedTemplate, is_published: false });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^unpublish$/i }));
    // Confirmation is required: nothing happens before confirm.
    const confirm = await screen.findByRole('dialog');
    expect(within(confirm).getByText(/use template and quick deploy/i)).toBeInTheDocument();
    expect(templateService.unpublish).not.toHaveBeenCalled();
    await user.click(within(confirm).getByRole('button', { name: /^unpublish$/i }));

    await waitFor(() => {
      expect(templateService.unpublish).toHaveBeenCalledWith('t1');
      expect(screen.getByText(/template unpublished/i)).toBeInTheDocument();
    });
  });

  it('shows an error when unpublish fails', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    (templateService.unpublish as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('boom'));
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^unpublish$/i }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: /^unpublish$/i }));

    expect(await screen.findByText('Failed to unpublish template')).toBeInTheDocument();
  });

  it('does not unpublish when the confirmation is cancelled', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^unpublish$/i }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: /cancel/i }));

    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
    expect(templateService.unpublish).not.toHaveBeenCalled();
  });

  it('shows the server message for any publish 409', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(releasedTemplate);
    (templateService.publish as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 409, data: { error: 'Template is being changed by another request' } },
    });
    renderPreview();

    await user.click(await screen.findByRole('button', { name: /^publish$/i }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: /^publish$/i }));

    expect(await within(dialog).findByText('Template is being changed by another request')).toBeInTheDocument();
  });

  const withReleasedCharts = {
    ...releasedTemplate,
    published_charts: [
      { ...mockTemplate.charts[0], id: 'pub-1', chart_name: 'released-frontend', default_values: 'replicaCount: 1' },
    ],
  };

  it('shows the released charts to users who cannot manage the template', async () => {
    authState.user = { id: '2', username: 'dev', role: 'user', display_name: 'Dev' };
    mockGet().mockResolvedValue(withReleasedCharts);
    renderPreview();

    expect(await screen.findByText('released-frontend')).toBeInTheDocument();
    expect(screen.getByText('Released (v1.0.0)')).toBeInTheDocument();
    expect(screen.queryByText('frontend')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /working copy/i })).not.toBeInTheDocument();
  });

  it('shows the working copy to managers and lets them switch to the release', async () => {
    const user = userEvent.setup();
    mockGet().mockResolvedValue(withReleasedCharts);
    renderPreview();

    expect(await screen.findByText('frontend')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Working copy' })).toHaveAttribute('aria-pressed', 'true');

    await user.click(screen.getByRole('button', { name: 'Released (v1.0.0)' }));

    expect(await screen.findByText('released-frontend')).toBeInTheDocument();
    expect(screen.queryByText('frontend')).not.toBeInTheDocument();
  });

  it('shows only the released version chip to users who cannot manage the template', async () => {
    authState.user = { id: '2', username: 'dev', role: 'user', display_name: 'Dev' };
    // The API gives non-managers charts = published_charts.
    mockGet().mockResolvedValue({ ...withReleasedCharts, charts: withReleasedCharts.published_charts });
    renderPreview();

    expect(await screen.findByText('Released: v1.0.0')).toBeInTheDocument();
    expect(screen.queryByText(/working copy/i)).not.toBeInTheDocument();
    expect(screen.queryByText('v1.1.0')).not.toBeInTheDocument();
    expect(screen.queryByText(/unpublished changes/i)).not.toBeInTheDocument();
    expect(screen.getByText('released-frontend')).toBeInTheDocument();
  });

  it('shows both labelled version chips to managers', async () => {
    mockGet().mockResolvedValue(releasedTemplate);
    renderPreview();

    expect(await screen.findByText('Working copy: v1.1.0')).toBeInTheDocument();
    expect(screen.getByText('Released: v1.0.0')).toBeInTheDocument();
  });
});
