import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Form from '../Form';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../../api/client', () => ({
  instanceService: {
    create: vi.fn(),
  },
  definitionService: {
    list: vi.fn(),
  },
}));

import { instanceService, definitionService } from '../../../api/client';

const mockDefinitions = [
  {
    id: 'def1',
    name: 'Web Stack',
    description: 'A web stack definition',
    default_branch: 'main',
    created_at: '',
    updated_at: '',
  },
  {
    id: 'def2',
    name: 'API Stack',
    description: '',
    default_branch: 'develop',
    created_at: '',
    updated_at: '',
  },
];

describe('StackInstances Form', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner while fetching definitions', () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays the form when definitions load', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('textbox', { name: /instance name/i })).toBeInTheDocument();
  });

  it('shows error alert when definitions fetch fails', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network'));
    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load definitions')).toBeInTheDocument();
    });
  });

  it('shows helper text about auto-generated namespace', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    expect(screen.getByText(/namespace will be auto-generated/i)).toBeInTheDocument();
  });

  it('creates an instance on form submit', async () => {
    const user = userEvent.setup();
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    (instanceService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'inst-new',
      name: 'My App',
    });

    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    // Select a definition
    const defSelect = screen.getByRole('combobox', { name: /stack definition/i });
    await user.click(defSelect);
    await user.click(screen.getByText(/Web Stack/));

    // Type instance name
    const nameInput = screen.getByRole('textbox', { name: /instance name/i });
    await user.type(nameInput, 'My App');

    // Click create
    await user.click(screen.getByRole('button', { name: /create instance/i }));

    await waitFor(() => {
      expect(instanceService.create).toHaveBeenCalled();
    });
    expect(mockNavigate).toHaveBeenCalledWith('/stack-instances/inst-new');
  });

  it('navigates home when Cancel is clicked', async () => {
    const user = userEvent.setup();
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /cancel/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/');
  });
});
