import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import Builder from '../Builder';

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
    create: vi.fn(),
    update: vi.fn(),
    addChart: vi.fn(),
    updateChart: vi.fn(),
    deleteChart: vi.fn(),
    publish: vi.fn(),
  },
}));

const mockShowInfo = vi.fn();
const mockShowSuccess = vi.fn();
vi.mock('../../../context/NotificationContext', () => ({
  useNotification: () => ({
    showSuccess: mockShowSuccess,
    showError: vi.fn(),
    showWarning: vi.fn(),
    showInfo: mockShowInfo,
  }),
}));

vi.mock('../../../components/YamlEditor', () => ({
  default: (props: { label?: string; value: string }) => (
    <div data-testid="yaml-editor">
      <span>{props.label}</span>
      <pre>{props.value}</pre>
    </div>
  ),
}));

import { templateService } from '../../../api/client';

describe('Templates Builder', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows Create Template heading in create mode', () => {
    render(
      <MemoryRouter initialEntries={['/templates/new']}>
        <Routes>
          <Route path="/templates/new" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );
    expect(screen.getByText('Create Template')).toBeInTheDocument();
  });

  it('shows loading spinner in edit mode while fetching', () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter initialEntries={['/templates/t1/edit']}>
        <Routes>
          <Route path="/templates/:id/edit" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('populates form fields in edit mode', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 't1',
      name: 'My Template',
      description: 'A cool template',
      category: 'Web',
      version: '2.0',
      default_branch: 'develop',
      is_published: true,
      charts: [],
    });
    render(
      <MemoryRouter initialEntries={['/templates/t1/edit']}>
        <Routes>
          <Route path="/templates/:id/edit" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Edit Template')).toBeInTheDocument();
    });
    expect(screen.getByDisplayValue('My Template')).toBeInTheDocument();
    expect(screen.getByDisplayValue('A cool template')).toBeInTheDocument();
    expect(screen.getByDisplayValue('2.0')).toBeInTheDocument();
    expect(screen.getByDisplayValue('develop')).toBeInTheDocument();
  });

  it('shows error alert when fetch fails in edit mode', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Not found'));
    render(
      <MemoryRouter initialEntries={['/templates/t1/edit']}>
        <Routes>
          <Route path="/templates/:id/edit" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load template')).toBeInTheDocument();
    });
  });

  it('creates a new template on save', async () => {
    const user = userEvent.setup();
    (templateService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'new-t',
      name: 'New Template',
    });
    render(
      <MemoryRouter initialEntries={['/templates/new']}>
        <Routes>
          <Route path="/templates/new" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );

    const nameInput = screen.getByRole('textbox', { name: /^name$/i });
    await user.type(nameInput, 'New Template');

    await user.click(screen.getByRole('button', { name: /save template/i }));

    await waitFor(() => {
      expect(templateService.create).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'New Template' })
      );
    });
  }, 15000);

  it('adds a chart when Add Chart is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/templates/new']}>
        <Routes>
          <Route path="/templates/new" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );

    await user.click(screen.getByRole('button', { name: /add chart/i }));

    await waitFor(() => {
      expect(screen.getByText('Chart #1')).toBeInTheDocument();
    });
  });

  it('navigates back when Cancel is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/templates/new']}>
        <Routes>
          <Route path="/templates/new" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );

    await user.click(screen.getByRole('button', { name: /cancel/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/templates');
  });

  const publishedTemplate = {
    id: 't1',
    name: 'My Template',
    description: '',
    category: 'Web',
    version: '1.0.0',
    default_branch: 'main',
    is_published: true,
    published_version: '1.0.0',
    has_unpublished_changes: false,
    charts: [],
  };

  const renderEdit = () =>
    render(
      <MemoryRouter initialEntries={['/templates/t1/edit']}>
        <Routes>
          <Route path="/templates/:id/edit" element={<Builder />} />
        </Routes>
      </MemoryRouter>
    );

  it('replaces the publish switch with a draft note for a released template', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(publishedTemplate);
    renderEdit();
    await waitFor(() => {
      expect(screen.getByText(/users get version 1\.0\.0/i)).toBeInTheDocument();
    });
    expect(screen.queryByRole('switch', { name: /published|draft/i })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /save and publish/i })).toBeInTheDocument();
  });

  it('saves a released template as a draft and tells the user', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(publishedTemplate);
    (templateService.update as ReturnType<typeof vi.fn>).mockResolvedValue(publishedTemplate);
    renderEdit();
    await waitFor(() => {
      expect(screen.getByDisplayValue('My Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /save template/i }));

    await waitFor(() => {
      expect(mockShowInfo).toHaveBeenCalledWith('Saved as draft. Publish a new version to release the changes.');
    });
    const payload = (templateService.update as ReturnType<typeof vi.fn>).mock.calls[0][1];
    expect(payload).not.toHaveProperty('is_published');
    expect(mockNavigate).toHaveBeenCalledWith('/templates/t1');
  });

  it('does not show the draft message for a template without a release', async () => {
    const user = userEvent.setup();
    const draft = { ...publishedTemplate, is_published: false, published_version: null };
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(draft);
    (templateService.update as ReturnType<typeof vi.fn>).mockResolvedValue(draft);
    renderEdit();
    await waitFor(() => {
      expect(screen.getByDisplayValue('My Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /save template/i }));

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/templates/t1');
    });
    expect(mockShowInfo).not.toHaveBeenCalled();
  });

  it('saves and then publishes through the publish dialog', async () => {
    const user = userEvent.setup();
    // After the save the working copy differs from the release.
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({ ...publishedTemplate, has_unpublished_changes: true });
    (templateService.update as ReturnType<typeof vi.fn>).mockResolvedValue(publishedTemplate);
    (templateService.publish as ReturnType<typeof vi.fn>).mockResolvedValue({
      template: { ...publishedTemplate, published_version: '1.0.1' },
      snapshotCreated: true,
    });
    renderEdit();
    await waitFor(() => {
      expect(screen.getByDisplayValue('My Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /save and publish/i }));

    const dialog = await screen.findByRole('dialog');
    expect(screen.getByRole('textbox', { name: /^version/i })).toHaveValue('1.0.1');
    await user.click(within(dialog).getByRole('button', { name: /^publish$/i }));

    await waitFor(() => {
      expect(templateService.publish).toHaveBeenCalledWith('t1', { version: '1.0.1', change_summary: undefined });
      expect(mockShowSuccess).toHaveBeenCalledWith('Published version 1.0.1.');
      expect(mockNavigate).toHaveBeenCalledWith('/templates/t1');
    });
  }, 15000);

  const twoCharts = [
    {
      id: 'c1', stack_template_id: 't1', chart_name: 'api', repository_url: '', source_repo_url: '',
      chart_path: '', chart_version: '', default_values: '', locked_values: '', deploy_order: 1,
      required: false, created_at: '',
    },
    {
      id: 'c2', stack_template_id: 't1', chart_name: 'web', repository_url: '', source_repo_url: '',
      chart_path: '', chart_version: '', default_values: '', locked_values: '', deploy_order: 2,
      required: false, created_at: '',
    },
  ];

  it('deletes a removed chart on the server when saving', async () => {
    const user = userEvent.setup();
    const tmpl = { ...publishedTemplate, charts: twoCharts };
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(tmpl);
    (templateService.update as ReturnType<typeof vi.fn>).mockResolvedValue(tmpl);
    (templateService.deleteChart as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    renderEdit();
    await waitFor(() => {
      expect(screen.getByDisplayValue('api')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Remove chart 1' }));
    await user.click(screen.getByRole('button', { name: /save template/i }));

    await waitFor(() => {
      expect(templateService.deleteChart).toHaveBeenCalledWith('t1', 'c1');
    });
    expect(templateService.deleteChart).toHaveBeenCalledTimes(1);
    expect(templateService.updateChart).toHaveBeenCalledTimes(1);
    expect(templateService.updateChart).toHaveBeenCalledWith('t1', 'c2', expect.objectContaining({ chart_name: 'web' }));
  });

  it('deletes a removed chart before Save and Publish releases the template', async () => {
    const user = userEvent.setup();
    const tmpl = { ...publishedTemplate, charts: twoCharts, has_unpublished_changes: true };
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(tmpl);
    (templateService.update as ReturnType<typeof vi.fn>).mockResolvedValue(tmpl);
    (templateService.deleteChart as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (templateService.publish as ReturnType<typeof vi.fn>).mockResolvedValue({
      template: { ...tmpl, published_version: '1.0.1' },
      snapshotCreated: true,
    });
    renderEdit();
    await waitFor(() => {
      expect(screen.getByDisplayValue('api')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Remove chart 1' }));
    await user.click(screen.getByRole('button', { name: /save and publish/i }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: /^publish$/i }));

    await waitFor(() => {
      expect(templateService.publish).toHaveBeenCalled();
    });
    const deleteOrder = (templateService.deleteChart as ReturnType<typeof vi.fn>).mock.invocationCallOrder[0];
    const publishOrder = (templateService.publish as ReturnType<typeof vi.fn>).mock.invocationCallOrder[0];
    expect(deleteOrder).toBeLessThan(publishOrder);
  }, 15000);

  it('shows an error and does not navigate when deleting a chart fails', async () => {
    const user = userEvent.setup();
    const tmpl = { ...publishedTemplate, charts: twoCharts };
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(tmpl);
    (templateService.update as ReturnType<typeof vi.fn>).mockResolvedValue(tmpl);
    (templateService.deleteChart as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('boom'));
    renderEdit();
    await waitFor(() => {
      expect(screen.getByDisplayValue('api')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Remove chart 1' }));
    await user.click(screen.getByRole('button', { name: /save template/i }));

    expect(await screen.findByText('Failed to save template')).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
