import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
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
  },
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
});
