import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import Form from '../Form';
import { NotificationProvider } from '../../../context/NotificationContext';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

const mockAuth = { role: 'admin' };
vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({
    user: { id: '1', username: 'tester', role: mockAuth.role, display_name: 'Tester' },
    isAuthenticated: true,
    isLoading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

vi.mock('../../../hooks/useUnsavedChanges', () => ({
  useUnsavedChanges: vi.fn(),
}));

vi.mock('../../../api/client', () => ({
  definitionService: {
    get: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    addChart: vi.fn(),
    updateChart: vi.fn(),
    deleteChart: vi.fn(),
    exportDefinition: vi.fn(),
    setTemplateLink: vi.fn(),
  },
  templateService: {
    get: vi.fn(),
    listAll: vi.fn(),
    listVersions: vi.fn(),
  },
}));

vi.mock('../../../components/YamlEditor', async () => {
  const { useEffect } = await import('react');
  const { checkYamlValues } = await import('../../../utils/yamlValidation');
  const MockYamlEditor = (props: {
    label?: string;
    value: string;
    readOnly?: boolean;
    onChange?: (val: string) => void;
    onValidationChange?: (error: string | null) => void;
  }) => {
    // The real validator and the report of the real editor.
    const problem = props.readOnly ? null : checkYamlValues(props.value);
    const blocking = problem?.blocking ? problem.message : null;
    useEffect(() => {
      if (!props.readOnly) props.onValidationChange?.(blocking);
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [blocking]);
    return (
      <div data-testid="yaml-editor">
        <span>{props.label}</span>
        <pre>{props.value}</pre>
        {props.readOnly && <span>read-only</span>}
        {!props.readOnly && props.onChange && (
          <>
            <button type="button" onClick={() => props.onChange!('new: value')}>change-yaml</button>
            <button type="button" onClick={() => props.onChange!('tag: {{.Branch}}')}>break {props.label}</button>
            <button type="button" onClick={() => props.onChange!('tag: main')}>fix {props.label}</button>
            <button type="button" onClick={() => props.onChange!('tag: {{.Other}}')}>other {props.label}</button>
            <button type="button" onClick={() => props.onChange!('a: 1\na: 2')}>duplicate {props.label}</button>
          </>
        )}
      </div>
    );
  };
  return { default: MockYamlEditor };
});

import { definitionService, templateService } from '../../../api/client';
import { useUnsavedChanges } from '../../../hooks/useUnsavedChanges';

const mockDefinitionWithCharts = {
  id: '123',
  name: 'Test Def',
  description: 'A test definition',
  default_branch: 'develop',
  charts: [
    {
      id: 'chart1',
      stack_definition_id: '123',
      chart_name: 'frontend',
      repository_url: 'https://charts.example.com',
      source_repo_url: 'https://git.example.com/frontend',
      chart_path: './chart',
      chart_version: '1.0.0',
      default_values: 'replicaCount: 2',
      deploy_order: 1,
      created_at: '2026-01-01T00:00:00Z',
    },
  ],
};

describe('StackDefinitions Form', () => {
  afterEach(() => {
    vi.clearAllMocks();
    mockAuth.role = 'admin';
  });

  it('shows Create Stack Definition heading in create mode', () => {
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    expect(screen.getByText('Create Stack Definition')).toBeInTheDocument();
  });

  it('shows loading spinner in edit mode while fetching', () => {
    (definitionService.get as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('populates form fields in edit mode', async () => {
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: '123',
      name: 'Test Def',
      description: 'A test definition',
      default_branch: 'develop',
      charts: [],
    });
    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Edit Stack Definition')).toBeInTheDocument();
    });
    expect(screen.getByDisplayValue('Test Def')).toBeInTheDocument();
    expect(screen.getByDisplayValue('A test definition')).toBeInTheDocument();
    expect(screen.getByDisplayValue('develop')).toBeInTheDocument();
  });

  it('shows error alert when fetch fails in edit mode', async () => {
    (definitionService.get as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Not found'));
    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load definition')).toBeInTheDocument();
    });
  });

  it('creates a new definition on save', async () => {
    const user = userEvent.setup();
    (definitionService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'new-id',
      name: 'New Def',
    });
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

    const nameInput = screen.getByRole('textbox', { name: /^name$/i });
    await user.type(nameInput, 'New Def');

    const saveButton = screen.getByRole('button', { name: /save definition/i });
    await user.click(saveButton);

    await waitFor(() => {
      expect(definitionService.create).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'New Def' })
      );
    });
    expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions');
  }, 15000);

  it('navigates back when Cancel is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

    await user.click(screen.getByRole('button', { name: /cancel/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions');
  });

  it('shows Export button in edit mode', async () => {
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: '123',
      name: 'Test Def',
      description: 'A test definition',
      default_branch: 'develop',
      charts: [],
    });
    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Edit Stack Definition')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /export/i })).toBeInTheDocument();
  });

  it('does not show Export button in create mode', () => {
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    expect(screen.queryByRole('button', { name: /export/i })).not.toBeInTheDocument();
  });

  it('adds a chart when Add Chart is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

    await user.click(screen.getByRole('button', { name: /add chart/i }));

    await waitFor(() => {
      expect(screen.getByText('Chart #1')).toBeInTheDocument();
    });
  });

  it('adds multiple charts', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

    await user.click(screen.getByRole('button', { name: /add chart/i }));
    await waitFor(() => {
      expect(screen.getByText('Chart #1')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /add chart/i }));
    await waitFor(() => {
      expect(screen.getByText('Chart #2')).toBeInTheDocument();
    });
  });

  it('removes a chart when remove button is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

    await user.click(screen.getByRole('button', { name: /add chart/i }));
    await waitFor(() => {
      expect(screen.getByText('Chart #1')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /remove chart/i }));
    await waitFor(() => {
      expect(screen.queryByText('Chart #1')).not.toBeInTheDocument();
    });
  });

  it('displays existing charts in edit mode', async () => {
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitionWithCharts);
    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Edit Stack Definition')).toBeInTheDocument();
    });
    expect(screen.getByDisplayValue('frontend')).toBeInTheDocument();
    expect(screen.getByDisplayValue('https://charts.example.com')).toBeInTheDocument();
  });

  it('updates an existing definition on save', async () => {
    const user = userEvent.setup();
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: '123',
      name: 'Test Def',
      description: 'A test definition',
      default_branch: 'develop',
      charts: [],
    });
    (definitionService.update as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: '123',
      name: 'Updated Def',
    });
    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Edit Stack Definition')).toBeInTheDocument();
    });

    const nameInput = screen.getByDisplayValue('Test Def');
    await user.clear(nameInput);
    await user.type(nameInput, 'Updated Def');

    await user.click(screen.getByRole('button', { name: /save definition/i }));

    await waitFor(() => {
      expect(definitionService.update).toHaveBeenCalledWith(
        '123',
        expect.objectContaining({ name: 'Updated Def' })
      );
    });
    expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions');
  }, 15000);

  it('shows save error notification on create failure', async () => {
    const user = userEvent.setup();
    (definitionService.create as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

    const nameInput = screen.getByRole('textbox', { name: /^name$/i });
    await user.type(nameInput, 'Broken Def');

    await user.click(screen.getByRole('button', { name: /save definition/i }));

    await waitFor(() => {
      expect(definitionService.create).toHaveBeenCalled();
    });
    // Should not navigate on error
    expect(mockNavigate).not.toHaveBeenCalledWith('/stack-definitions');
  }, 15000);

  it('exports definition when Export button is clicked', async () => {
    const user = userEvent.setup();
    const mockExportData = {
      definition: { name: 'Test Def', description: 'A test definition', default_branch: 'develop' },
      charts: [],
    };
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: '123',
      name: 'Test Def',
      description: 'A test definition',
      default_branch: 'develop',
      charts: [],
    });
    (definitionService.exportDefinition as ReturnType<typeof vi.fn>).mockResolvedValue(mockExportData);

    // Mock URL.createObjectURL and link click
    const createObjectURLMock = vi.fn().mockReturnValue('blob:test');
    const revokeObjectURLMock = vi.fn();
    global.URL.createObjectURL = createObjectURLMock;
    global.URL.revokeObjectURL = revokeObjectURLMock;

    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Edit Stack Definition')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /export/i }));

    await waitFor(() => {
      expect(definitionService.exportDefinition).toHaveBeenCalledWith('123');
    });
  });

  it('shows description and default branch fields in create mode', () => {
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    expect(screen.getByRole('textbox', { name: /^name$/i })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /description/i })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /default branch/i })).toBeInTheDocument();
  });

  const lastDirtyFlag = () => {
    const calls = (useUnsavedChanges as ReturnType<typeof vi.fn>).mock.calls;
    return calls[calls.length - 1]?.[0];
  };

  const renderEdit = () =>
    render(
      <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/:id/edit" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

  it('is not dirty after loading a definition created from a template', async () => {
    // Shape after Use Template: source template set, some fields null.
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockDefinitionWithCharts,
      description: null,
      source_template_id: 't1',
      source_template_version: '1.0.0',
      charts: [{ ...mockDefinitionWithCharts.charts[0], source_repo_url: null, chart_path: null }],
    });
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 't1', charts: [] });

    renderEdit();

    await waitFor(() => {
      expect(screen.getByDisplayValue('Test Def')).toBeInTheDocument();
      expect(templateService.get).toHaveBeenCalledWith('t1');
    });
    await waitFor(() => {
      expect(lastDirtyFlag()).toBe(false);
    });
  });

  it('is dirty after a change to a loaded definition', async () => {
    const user = userEvent.setup();
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitionWithCharts);

    renderEdit();

    await waitFor(() => {
      expect(screen.getByDisplayValue('Test Def')).toBeInTheDocument();
    });
    expect(lastDirtyFlag()).toBe(false);

    await user.type(screen.getByDisplayValue('Test Def'), ' changed');

    await waitFor(() => {
      expect(lastDirtyFlag()).toBe(true);
    });
  });

  it('tells a user without devops role to start from a template in create mode', () => {
    mockAuth.role = 'user';
    render(
      <MemoryRouter initialEntries={['/stack-definitions/new']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-definitions/new" element={<Form />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    expect(screen.getByText(/Only devops and admin users can create a definition/)).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: /name/i })).not.toBeInTheDocument();
  });

  it('locks the chart source and hides add and remove for a user without devops role', async () => {
    mockAuth.role = 'user';
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitionWithCharts);
    renderEdit();

    await waitFor(() => {
      expect(screen.getByDisplayValue('Test Def')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: /add chart/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /remove chart/i })).not.toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /chart name/i })).toBeDisabled();
    expect(screen.getByRole('textbox', { name: /repository url/i })).toBeDisabled();
    expect(screen.getByText(/Only devops and admin users can add or remove charts/)).toBeInTheDocument();
  });

  it('allows chart management for a devops user', async () => {
    mockAuth.role = 'devops';
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitionWithCharts);
    renderEdit();

    await waitFor(() => {
      expect(screen.getByDisplayValue('Test Def')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /add chart/i })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /chart name/i })).not.toBeDisabled();
  });

  it('offers a template link to a devops user, not to a user', async () => {
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitionWithCharts);
    mockAuth.role = 'devops';
    const { unmount } = renderEdit();
    expect(await screen.findByRole('button', { name: /link to a template/i })).toBeInTheDocument();
    unmount();

    mockAuth.role = 'user';
    renderEdit();
    await waitFor(() => {
      expect(screen.getByDisplayValue('Test Def')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: /link to a template/i })).not.toBeInTheDocument();
  });

  it('links the definition and shows the template name', async () => {
    const user = userEvent.setup();
    mockAuth.role = 'devops';
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitionWithCharts);
    (templateService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([{ id: 't1', name: 'Web stack', is_published: true }]);
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockResolvedValue([{ id: 'v1', template_id: 't1', version: '1.0.0' }]);
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 't1', name: 'Web stack', charts: [] });
    (definitionService.setTemplateLink as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockDefinitionWithCharts,
      source_template_id: 't1',
      source_template_version: '1.0.0',
    });
    renderEdit();

    await user.click(await screen.findByRole('button', { name: /link to a template/i }));
    await user.click(await screen.findByRole('combobox', { name: 'Template' }));
    await user.click(await screen.findByRole('option', { name: 'Web stack' }));
    await waitFor(() => expect(templateService.listVersions).toHaveBeenCalledWith('t1'));
    await user.click(screen.getByRole('button', { name: 'Save link' }));

    await waitFor(() => expect(definitionService.setTemplateLink).toHaveBeenCalledWith('123', 't1', ''));
    expect(await screen.findByText(/Derived from template Web stack \(v1.0.0\)/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /link to a template/i })).not.toBeInTheDocument();
  });

  it('keeps an edit when a late, stale definition response arrives (StrictMode, #596)', async () => {
    // StrictMode runs the load effect twice. The first (stale) response comes last.
    let resolveFirst: (v: unknown) => void = () => {};
    let resolveSecond: (v: unknown) => void = () => {};
    const def = { id: '123', name: 'Test Def', description: 'A test definition', default_branch: 'develop', charts: [] };
    (definitionService.get as ReturnType<typeof vi.fn>)
      .mockReturnValueOnce(new Promise((r) => { resolveFirst = r; }))
      .mockReturnValueOnce(new Promise((r) => { resolveSecond = r; }));
    const user = userEvent.setup();
    render(
      <StrictMode>
        <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
          <NotificationProvider>
            <Routes>
              <Route path="/stack-definitions/:id/edit" element={<Form />} />
            </Routes>
          </NotificationProvider>
        </MemoryRouter>
      </StrictMode>
    );
    await waitFor(() => expect(definitionService.get).toHaveBeenCalledTimes(2));
    await act(async () => resolveSecond(def));
    const field = await screen.findByDisplayValue('Test Def');
    await user.clear(field);
    await user.type(field, 'Renamed Def');
    await act(async () => resolveFirst(def));
    expect(screen.getByDisplayValue('Renamed Def')).toBeInTheDocument();
  });

  describe('chart values validation (#756)', () => {
    const renderEdit = async () => {
      (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitionWithCharts);
      render(
        <MemoryRouter initialEntries={['/stack-definitions/123/edit']}>
          <NotificationProvider>
            <Routes>
              <Route path="/stack-definitions/:id/edit" element={<Form />} />
            </Routes>
          </NotificationProvider>
        </MemoryRouter>
      );
      await screen.findByText('Edit Stack Definition');
    };

    it('disables save while an editor reports an error and enables it after the fix', async () => {
      const user = userEvent.setup();
      await renderEdit();
      const save = screen.getByRole('button', { name: /save definition/i });
      expect(save).toBeEnabled();
      await user.click(screen.getByRole('button', { name: /break default values/i }));
      await waitFor(() => expect(save).toBeDisabled());
      expect(screen.getByTestId('yaml-error-hint')).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: /fix default values/i }));
      await waitFor(() => expect(save).toBeEnabled());
      expect(screen.queryByTestId('yaml-error-hint')).not.toBeInTheDocument();
    });

    it('does not block the save for a warning only', async () => {
      const user = userEvent.setup();
      await renderEdit();
      await user.click(screen.getByRole('button', { name: /duplicate default values/i }));
      expect(screen.getByRole('button', { name: /save definition/i })).toBeEnabled();
    });

    it('clears the error when the chart is removed', async () => {
      const user = userEvent.setup();
      await renderEdit();
      await user.click(screen.getByRole('button', { name: /break default values/i }));
      const save = screen.getByRole('button', { name: /save definition/i });
      await waitFor(() => expect(save).toBeDisabled());
      await user.click(screen.getByRole('button', { name: /remove chart frontend/i }));
      await waitFor(() => expect(save).toBeEnabled());
    });

    it('shows the API 400 message', async () => {
      const user = userEvent.setup();
      await renderEdit();
      (definitionService.update as ReturnType<typeof vi.fn>).mockRejectedValue({
        response: { status: 400, data: { error: 'Invalid YAML in chart "frontend" default_values: bad' } },
      });
      await user.click(screen.getByRole('button', { name: /save definition/i }));
      expect(await screen.findByText(/Invalid YAML in chart "frontend" default_values: bad/)).toBeInTheDocument();
    });
  });

  describe('stored values and partial saves (#756)', () => {
    const renderAt = (path: string) =>
      render(
        <MemoryRouter initialEntries={[path]}>
          <NotificationProvider>
            <Routes>
              <Route path="/stack-definitions/new" element={<Form />} />
              <Route path="/stack-definitions/:id/edit" element={<Form />} />
            </Routes>
          </NotificationProvider>
        </MemoryRouter>
      );

    it('does not block a rename while the stored value is unchanged, and blocks another invalid value', async () => {
      const user = userEvent.setup();
      const stored = { ...mockDefinitionWithCharts.charts[0], default_values: 'tag: {{.Branch}}' };
      (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue({ ...mockDefinitionWithCharts, charts: [stored] });
      (definitionService.update as ReturnType<typeof vi.fn>).mockResolvedValue({ id: '123' });
      (definitionService.updateChart as ReturnType<typeof vi.fn>).mockResolvedValue({});
      renderAt('/stack-definitions/123/edit');
      await screen.findByText('Edit Stack Definition');
      const save = screen.getByRole('button', { name: /save definition/i });
      await user.type(screen.getByLabelText(/^name/i), ' 2');
      expect(save).toBeEnabled();
      await user.click(screen.getByRole('button', { name: /other default values/i }));
      await waitFor(() => expect(save).toBeDisabled());
      await user.click(screen.getByRole('button', { name: /fix default values/i }));
      await waitFor(() => expect(save).toBeEnabled());
    });

    it('updates on a retry after a chart 400: no second definition, no duplicate chart', async () => {
      const user = userEvent.setup();
      (definitionService.create as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 'new-def' });
      (definitionService.update as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 'new-def' });
      (definitionService.updateChart as ReturnType<typeof vi.fn>).mockResolvedValue({});
      (definitionService.addChart as ReturnType<typeof vi.fn>)
        .mockResolvedValueOnce({ id: 'c1' })
        .mockRejectedValueOnce({ response: { status: 400, data: { error: 'Invalid YAML in chart "b" default_values: bad' } } })
        .mockResolvedValueOnce({ id: 'c2' });
      renderAt('/stack-definitions/new');
      await user.type(screen.getByLabelText(/^name/i), 'Def');
      await user.click(screen.getByRole('button', { name: /add chart/i }));
      await user.click(screen.getByRole('button', { name: /add chart/i }));
      const names = screen.getAllByLabelText(/chart name/i);
      await user.type(names[0], 'a');
      await user.type(names[1], 'b');
      await user.click(screen.getByRole('button', { name: /save definition/i }));
      expect(await screen.findByText(/Invalid YAML in chart "b"/)).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: /save definition/i }));
      await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions'));
      expect(definitionService.create).toHaveBeenCalledTimes(1);
      expect(definitionService.update).toHaveBeenCalledWith('new-def', expect.anything());
      expect(definitionService.updateChart).toHaveBeenCalledWith('new-def', 'c1', expect.anything());
      expect(definitionService.addChart).toHaveBeenCalledTimes(3);
    });

    it('deletes a saved chart that the user removes after a failed save', async () => {
      const user = userEvent.setup();
      (definitionService.create as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 'new-def' });
      (definitionService.update as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 'new-def' });
      (definitionService.deleteChart as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
      (definitionService.addChart as ReturnType<typeof vi.fn>)
        .mockResolvedValueOnce({ id: 'c1' })
        .mockRejectedValueOnce({ response: { status: 400, data: { error: 'Invalid YAML in chart "b" default_values: bad' } } })
        .mockResolvedValueOnce({ id: 'c2' });
      renderAt('/stack-definitions/new');
      await user.type(screen.getByLabelText(/^name/i), 'Def');
      await user.click(screen.getByRole('button', { name: /add chart/i }));
      await user.click(screen.getByRole('button', { name: /add chart/i }));
      const names = screen.getAllByLabelText(/chart name/i);
      await user.type(names[0], 'a');
      await user.type(names[1], 'b');
      await user.click(screen.getByRole('button', { name: /save definition/i }));
      expect(await screen.findByText(/Invalid YAML in chart "b"/)).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: /remove chart a/i }));
      await user.click(screen.getByRole('button', { name: /save definition/i }));
      await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions'));
      expect(definitionService.deleteChart).toHaveBeenCalledWith('new-def', 'c1');
      expect(definitionService.updateChart).not.toHaveBeenCalled();
    });

    it('keeps remove disabled during the save, and deletes a chart removed after a failed save', async () => {
      const user = userEvent.setup();
      (definitionService.create as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 'new-def' });
      (definitionService.update as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 'new-def' });
      (definitionService.updateChart as ReturnType<typeof vi.fn>).mockResolvedValue({});
      (definitionService.deleteChart as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
      let resolveA: (value: { id: string }) => void = () => {};
      (definitionService.addChart as ReturnType<typeof vi.fn>)
        .mockReturnValueOnce(new Promise((resolve) => { resolveA = resolve; }))
        .mockRejectedValueOnce({ response: { status: 400, data: { error: 'Invalid YAML in chart "b" default_values: bad' } } })
        .mockResolvedValueOnce({ id: 'c2' });
      renderAt('/stack-definitions/new');
      await user.type(screen.getByLabelText(/^name/i), 'Def');
      await user.click(screen.getByRole('button', { name: /add chart/i }));
      await user.click(screen.getByRole('button', { name: /add chart/i }));
      const names = screen.getAllByLabelText(/chart name/i);
      await user.type(names[0], 'a');
      await user.type(names[1], 'b');
      await user.click(screen.getByRole('button', { name: /save definition/i }));
      await waitFor(() => expect(definitionService.addChart).toHaveBeenCalledTimes(1));
      expect(screen.getByRole('button', { name: /remove chart a/i })).toBeDisabled();
      await act(async () => { resolveA({ id: 'c1' }); });
      expect(await screen.findByText(/Invalid YAML in chart "b"/)).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: /remove chart a/i }));
      await user.click(screen.getByRole('button', { name: /save definition/i }));
      await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions'));
      expect(definitionService.deleteChart).toHaveBeenCalledWith('new-def', 'c1');
      expect(definitionService.updateChart).not.toHaveBeenCalled();
      expect(definitionService.addChart).toHaveBeenLastCalledWith('new-def', expect.objectContaining({ chart_name: 'b' }));
    });
  });
});
