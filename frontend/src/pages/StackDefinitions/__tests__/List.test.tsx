import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import List from '../List';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../../api/client', () => ({
  definitionService: {
    list: vi.fn(),
    delete: vi.fn(),
    importDefinition: vi.fn(),
    exportDefinition: vi.fn(),
    checkUpgrade: vi.fn(),
  },
  templateService: {
    list: vi.fn().mockResolvedValue([]),
  },
  favoriteService: {
    list: vi.fn().mockResolvedValue([]),
  },
}));

const mockShowSuccess = vi.fn();
const mockShowError = vi.fn();
vi.mock('../../../context/NotificationContext', () => ({
  useNotification: () => ({
    showSuccess: mockShowSuccess,
    showError: mockShowError,
    showWarning: vi.fn(),
    showInfo: vi.fn(),
  }),
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

import { definitionService } from '../../../api/client';

const mockDefinitions = [
  {
    id: '1', name: 'My Stack', description: 'My description', default_branch: 'master',
    owner_id: '1', created_at: '2024-01-01T00:00:00Z', updated_at: '2024-01-01T00:00:00Z',
  },
  {
    id: '2', name: 'API Stack', description: 'Backend services', default_branch: 'main',
    owner_id: '1', source_template_id: 'tpl1', created_at: '2024-02-01T00:00:00Z', updated_at: '2024-02-01T00:00:00Z',
  },
];

describe('Stack Definitions List', () => {
  beforeEach(() => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner initially', () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays definitions in a table', async () => {
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    expect(screen.getByText('API Stack')).toBeInTheDocument();
  });

  it('shows Import button', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /import/i })).toBeInTheDocument();
    });
  });

  it('shows error on failure', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('error'));
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  it('shows Create Definition button', async () => {
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    const createButton = screen.getByRole('button', { name: /create definition/i });
    expect(createButton).toBeInTheDocument();
  });

  it('navigates to create form on Create Definition click', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /create definition/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions/new');
  });

  it('shows deploy button for each definition', async () => {
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /deploy my stack/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /deploy api stack/i })).toBeInTheDocument();
  });

  it('navigates to edit on row click', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    await user.click(screen.getByText('My Stack'));
    expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions/1/edit');
  });

  it('navigates to new instance from deploy button', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /deploy my stack/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/stack-instances/new?definition=1');
  });

  it('shows empty state when no definitions exist', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });
    expect(screen.getByText(/no stack definitions/i)).toBeInTheDocument();
  });

  it('displays definition descriptions', async () => {
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My description')).toBeInTheDocument();
      expect(screen.getByText('Backend services')).toBeInTheDocument();
    });
  });

  it('displays default branch for each definition', async () => {
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    expect(screen.getByText('master')).toBeInTheDocument();
    expect(screen.getByText('main')).toBeInTheDocument();
  });

  it('shows page heading', async () => {
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /stack definitions/i })).toBeInTheDocument();
    });
  });

  it('shows source template chip for template-based definition', async () => {
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('API Stack')).toBeInTheDocument();
    });
    expect(screen.getByText('Template')).toBeInTheDocument();
  });

  it('shows Import button and opens import dialog', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <List />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Stack')).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /import/i }));
    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeInTheDocument();
    });
  });
});
