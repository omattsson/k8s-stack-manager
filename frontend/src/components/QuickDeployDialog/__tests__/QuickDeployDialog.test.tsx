import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import QuickDeployDialog from '../index';
import type { StackTemplate } from '../../../types';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: () => mockNavigate };
});

vi.mock('../../../api/client', () => ({
  templateService: {
    quickDeploy: vi.fn(),
  },
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
  },
}));

import { templateService, clusterService } from '../../../api/client';

const template: StackTemplate = {
  id: 'tpl-1',
  name: 'Test Template',
  description: 'A template',
  category: 'Web',
  version: '1.0',
  owner_id: '1',
  default_branch: 'main',
  is_published: true,
  created_at: '',
  updated_at: '',
};

describe('QuickDeployDialog', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders form fields when open', async () => {
    render(
      <MemoryRouter>
        <QuickDeployDialog open={true} onClose={vi.fn()} template={template} />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByLabelText(/instance name/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/description/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/branch/i)).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /deploy/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /cancel/i })).toBeInTheDocument();
  });

  it('defaults branch to template default_branch', async () => {
    render(
      <MemoryRouter>
        <QuickDeployDialog open={true} onClose={vi.fn()} template={template} />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByLabelText(/branch/i)).toHaveValue('main');
    });
  });

  it('shows validation error when instance name is empty', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <QuickDeployDialog open={true} onClose={vi.fn()} template={template} />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /deploy/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /deploy/i }));
    expect(screen.getByText(/instance name is required/i)).toBeInTheDocument();
    expect(templateService.quickDeploy).not.toHaveBeenCalled();
  });

  it('calls quickDeploy and navigates on success', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    (templateService.quickDeploy as ReturnType<typeof vi.fn>).mockResolvedValue({
      instance: { id: 'inst-42', name: 'my-instance' },
      definition: { id: 'def-1' },
      log_id: 'log-1',
    });

    render(
      <MemoryRouter>
        <QuickDeployDialog open={true} onClose={onClose} template={template} />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByLabelText(/instance name/i)).toBeInTheDocument();
    });

    await user.type(screen.getByLabelText(/instance name/i), 'my-instance');
    await user.click(screen.getByRole('button', { name: /deploy/i }));

    await waitFor(() => {
      expect(templateService.quickDeploy).toHaveBeenCalledWith('tpl-1', expect.objectContaining({
        instance_name: 'my-instance',
        branch: 'main',
      }));
      expect(onClose).toHaveBeenCalled();
      expect(mockNavigate).toHaveBeenCalledWith('/stack-instances/inst-42');
    });
  }, 15_000);

  it('shows error alert on API failure', async () => {
    const user = userEvent.setup();
    (templateService.quickDeploy as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { data: { error: 'Deployment failed: no charts' } },
    });

    render(
      <MemoryRouter>
        <QuickDeployDialog open={true} onClose={vi.fn()} template={template} />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByLabelText(/instance name/i)).toBeInTheDocument();
    });

    await user.type(screen.getByLabelText(/instance name/i), 'fail-instance');
    await user.click(screen.getByRole('button', { name: /deploy/i }));

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('Deployment failed: no charts');
    });
  }, 15_000);

  it('shows cluster dropdown when multiple clusters exist', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: 'c1', name: 'Cluster A', is_default: true },
      { id: 'c2', name: 'Cluster B', is_default: false },
    ]);

    render(
      <MemoryRouter>
        <QuickDeployDialog open={true} onClose={vi.fn()} template={template} />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByLabelText(/cluster/i)).toBeInTheDocument();
    });
  });

  it('does not render when closed', () => {
    const { container } = render(
      <MemoryRouter>
        <QuickDeployDialog open={false} onClose={vi.fn()} template={null} />
      </MemoryRouter>
    );
    expect(container.querySelector('[role="dialog"]')).not.toBeInTheDocument();
  });
});
