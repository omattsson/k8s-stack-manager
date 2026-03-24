import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
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
    exportDefinition: vi.fn(),
  },
  templateService: {
    get: vi.fn(),
  },
}));

vi.mock('../../../components/YamlEditor', () => ({
  default: (props: { label?: string; value: string; readOnly?: boolean }) => (
    <div data-testid="yaml-editor">
      <span>{props.label}</span>
      <pre>{props.value}</pre>
      {props.readOnly && <span>read-only</span>}
    </div>
  ),
}));

import { definitionService } from '../../../api/client';

describe('StackDefinitions Form', () => {
  afterEach(() => {
    vi.clearAllMocks();
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
});
