import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import axios from 'axios';
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
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
  },
  gitService: {
    branches: vi.fn().mockResolvedValue([]),
  },
}));

import { instanceService, definitionService, clusterService } from '../../../api/client';

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
      expect(screen.getByText('Failed to load form data')).toBeInTheDocument();
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

  it('shows error on generic create failure', async () => {
    const user = userEvent.setup();
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    (instanceService.create as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));

    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    // Select a definition and type name
    const defSelect = screen.getByRole('combobox', { name: /stack definition/i });
    await user.click(defSelect);
    await user.click(screen.getByText(/Web Stack/));
    const nameInput = screen.getByRole('textbox', { name: /instance name/i });
    await user.type(nameInput, 'My App');

    await user.click(screen.getByRole('button', { name: /create instance/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to create instance')).toBeInTheDocument();
    });
  });

  it('shows name conflict error with suggestions on 409 response', async () => {
    const user = userEvent.setup();
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);

    const axiosError = new Error('Conflict') as Error & {
      isAxiosError: boolean;
      response: { status: number; data: { error: string; message: string; suggestions: string[] } };
    };
    axiosError.isAxiosError = true;
    axiosError.response = {
      status: 409,
      data: {
        error: 'Conflict',
        message: 'Name already taken',
        suggestions: ['my-app-2', 'my-app-3'],
      },
    };
    // Mock axios.isAxiosError to return true for our error
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true);
    (instanceService.create as ReturnType<typeof vi.fn>).mockRejectedValue(axiosError);

    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    const defSelect = screen.getByRole('combobox', { name: /stack definition/i });
    await user.click(defSelect);
    await user.click(screen.getByText(/Web Stack/));
    const nameInput = screen.getByRole('textbox', { name: /instance name/i });
    await user.type(nameInput, 'my-app');

    await user.click(screen.getByRole('button', { name: /create instance/i }));

    await waitFor(() => {
      expect(screen.getByText('Name already taken')).toBeInTheDocument();
    });
    expect(screen.getByText('my-app-2')).toBeInTheDocument();
    expect(screen.getByText('my-app-3')).toBeInTheDocument();
  });

  it('applies suggestion name when suggestion chip is clicked', async () => {
    const user = userEvent.setup();
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);

    const axiosError = new Error('Conflict') as Error & {
      isAxiosError: boolean;
      response: { status: number; data: { error: string; message: string; suggestions: string[] } };
    };
    axiosError.isAxiosError = true;
    axiosError.response = {
      status: 409,
      data: {
        error: 'Conflict',
        message: 'Name already taken',
        suggestions: ['my-app-2'],
      },
    };
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true);
    (instanceService.create as ReturnType<typeof vi.fn>).mockRejectedValue(axiosError);

    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    const defSelect = screen.getByRole('combobox', { name: /stack definition/i });
    await user.click(defSelect);
    await user.click(screen.getByText(/Web Stack/));
    const nameInput = screen.getByRole('textbox', { name: /instance name/i });
    await user.type(nameInput, 'my-app');

    await user.click(screen.getByRole('button', { name: /create instance/i }));

    await waitFor(() => {
      expect(screen.getByText('my-app-2')).toBeInTheDocument();
    });

    await user.click(screen.getByText('my-app-2'));

    // Error and suggestions should be cleared
    await waitFor(() => {
      expect(screen.queryByText('Name already taken')).not.toBeInTheDocument();
    });
    // Name field should have the suggestion
    expect(screen.getByRole('textbox', { name: /instance name/i })).toHaveValue('my-app-2');
  });

  it('shows 409 error without suggestions when no suggestions returned', async () => {
    const user = userEvent.setup();
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);

    const axiosError = new Error('Conflict') as Error & {
      isAxiosError: boolean;
      response: { status: number; data: { error: string; message: string; suggestions: string[] } };
    };
    axiosError.isAxiosError = true;
    axiosError.response = {
      status: 409,
      data: {
        error: 'Conflict',
        message: 'You have reached the instance limit for this cluster',
        suggestions: [],
      },
    };
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true);
    (instanceService.create as ReturnType<typeof vi.fn>).mockRejectedValue(axiosError);

    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    const defSelect = screen.getByRole('combobox', { name: /stack definition/i });
    await user.click(defSelect);
    await user.click(screen.getByText(/Web Stack/));
    const nameInput = screen.getByRole('textbox', { name: /instance name/i });
    await user.type(nameInput, 'my-app');

    await user.click(screen.getByRole('button', { name: /create instance/i }));

    await waitFor(() => {
      expect(screen.getByText('You have reached the instance limit for this cluster')).toBeInTheDocument();
    });
    expect(screen.queryByText('Try:')).not.toBeInTheDocument();
  });

  it('updates branch when definition changes', async () => {
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

    // Select API Stack which has default_branch: 'develop'
    const defSelect = screen.getByRole('combobox', { name: /stack definition/i });
    await user.click(defSelect);
    await user.click(screen.getByText(/API Stack/));

    // Branch field should update to the definition's default branch
    await waitFor(() => {
      const branchInput = screen.getByRole('textbox', { name: /branch/i });
      expect(branchInput).toHaveValue('develop');
    });
  });

  it('displays cluster selector when clusters are available', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      {
        id: 'c1',
        name: 'production',
        health_status: 'healthy',
        is_default: true,
        region: 'westeurope',
        max_instances_per_user: 10,
      },
      {
        id: 'c2',
        name: 'staging',
        health_status: 'degraded',
        is_default: false,
        region: '',
        max_instances_per_user: 0,
      },
    ]);

    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    // Cluster selector should be visible
    expect(screen.getByRole('combobox', { name: /cluster/i })).toBeInTheDocument();
  });

  it('shows instance limit info when cluster with limit is selected', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      {
        id: 'c1',
        name: 'production',
        health_status: 'healthy',
        is_default: true,
        region: 'westeurope',
        max_instances_per_user: 5,
      },
    ]);

    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    // The default cluster has max_instances_per_user: 5, so info should show
    await waitFor(() => {
      expect(screen.getByText(/maximum of 5 instances per user/)).toBeInTheDocument();
    });
  });

  it('disables Create button when name is empty', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    // Select a definition but don't type a name
    const defSelect = screen.getByRole('combobox', { name: /stack definition/i });
    await user.click(defSelect);
    await user.click(screen.getByText(/Web Stack/));

    expect(screen.getByRole('button', { name: /create instance/i })).toBeDisabled();
  });

  it('disables Create button when no definition is selected', async () => {
    (definitionService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinitions);
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Form />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Create Stack Instance')).toBeInTheDocument();
    });

    // Type name but don't select a definition
    const nameInput = screen.getByRole('textbox', { name: /instance name/i });
    await user.type(nameInput, 'My App');

    expect(screen.getByRole('button', { name: /create instance/i })).toBeDisabled();
  });
});
