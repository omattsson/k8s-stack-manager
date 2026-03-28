import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Compare from '../Compare';
import { NotificationProvider } from '../../../context/NotificationContext';
import type { StackInstance, CompareInstancesResponse } from '../../../types';

vi.mock('../../../api/client', () => ({
  instanceService: {
    list: vi.fn(),
    compareInstances: vi.fn(),
  },
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

vi.mock('react-diff-viewer-continued', () => ({
  default: ({ oldValue, newValue, leftTitle, rightTitle }: {
    oldValue: string;
    newValue: string;
    leftTitle?: string;
    rightTitle?: string;
  }) => (
    <div data-testid="diff-viewer">
      <span>{leftTitle}</span>
      <span>{rightTitle}</span>
      <pre>{oldValue}</pre>
      <pre>{newValue}</pre>
    </div>
  ),
}));

import { instanceService } from '../../../api/client';

const mockInstances: StackInstance[] = [
  {
    id: 'inst-1',
    name: 'Instance Alpha',
    status: 'running',
    branch: 'main',
    namespace: 'stack-alpha',
    owner_id: '1',
    stack_definition_id: 'def-1',
    created_at: '',
    updated_at: '',
  },
  {
    id: 'inst-2',
    name: 'Instance Beta',
    status: 'stopped',
    branch: 'develop',
    namespace: 'stack-beta',
    owner_id: '2',
    stack_definition_id: 'def-1',
    created_at: '',
    updated_at: '',
  },
];

const mockCompareResponse: CompareInstancesResponse = {
  left: { id: 'inst-1', name: 'Instance Alpha', definition_name: 'My Def', branch: 'main', owner: 'admin' },
  right: { id: 'inst-2', name: 'Instance Beta', definition_name: 'My Def', branch: 'develop', owner: 'dev' },
  charts: [
    {
      chart_name: 'frontend-chart',
      left_values: 'replicas: 2\nimage: nginx:latest',
      right_values: 'replicas: 3\nimage: nginx:1.25',
      has_differences: true,
    },
    {
      chart_name: 'backend-chart',
      left_values: 'port: 8080',
      right_values: 'port: 8080',
      has_differences: false,
    },
  ],
};

const renderCompare = (initialEntries: string[] = ['/stack-instances/compare']) => {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <NotificationProvider>
        <Compare />
      </NotificationProvider>
    </MemoryRouter>
  );
};

describe('Compare', () => {
  beforeEach(() => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockInstances);
    (instanceService.compareInstances as ReturnType<typeof vi.fn>).mockResolvedValue(mockCompareResponse);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner while fetching instances', () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    renderCompare();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('renders page title and selectors after loading', async () => {
    renderCompare();
    await waitFor(() => {
      expect(screen.getByText('Compare Stack Instances')).toBeInTheDocument();
    });
    expect(screen.getByLabelText('Left instance')).toBeInTheDocument();
    expect(screen.getByLabelText('Right instance')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /compare/i })).toBeInTheDocument();
  });

  it('shows empty state message when no comparison triggered', async () => {
    renderCompare();
    await waitFor(() => {
      expect(screen.getByText(/select two instances above/i)).toBeInTheDocument();
    });
  });

  it('shows comparison results after selecting instances and clicking compare', async () => {
    const user = userEvent.setup({ delay: null });
    renderCompare();

    await waitFor(() => {
      expect(screen.getByLabelText('Left instance')).toBeInTheDocument();
    });

    // Select left instance
    const leftInput = screen.getByLabelText('Left instance');
    await user.click(leftInput);
    await user.type(leftInput, 'Alpha');
    const alphaOption = await screen.findByText('Instance Alpha');
    await user.click(alphaOption);

    // Select right instance
    const rightInput = screen.getByLabelText('Right instance');
    await user.click(rightInput);
    await user.type(rightInput, 'Beta');
    const betaOption = await screen.findByText('Instance Beta');
    await user.click(betaOption);

    // Click compare
    const compareButton = screen.getByRole('button', { name: /compare/i });
    await waitFor(() => {
      expect(compareButton).toBeEnabled();
    });
    await user.click(compareButton);

    await waitFor(() => {
      expect(instanceService.compareInstances).toHaveBeenCalledWith('inst-1', 'inst-2');
    });

    // Check summary cards
    await waitFor(() => {
      expect(screen.getByText('Left Instance')).toBeInTheDocument();
      expect(screen.getByText('Right Instance')).toBeInTheDocument();
    });

    // Check chart tabs
    expect(screen.getByRole('tab', { name: /frontend-chart/i })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /backend-chart/i })).toBeInTheDocument();
  });

  it('shows error alert when comparison fails', async () => {
    (instanceService.compareInstances as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));
    const user = userEvent.setup({ delay: null });
    renderCompare();

    await waitFor(() => {
      expect(screen.getByLabelText('Left instance')).toBeInTheDocument();
    });

    // Select instances
    const leftInput = screen.getByLabelText('Left instance');
    await user.click(leftInput);
    await user.type(leftInput, 'Alpha');
    const alphaOption = await screen.findByText('Instance Alpha');
    await user.click(alphaOption);

    const rightInput = screen.getByLabelText('Right instance');
    await user.click(rightInput);
    await user.type(rightInput, 'Beta');
    const betaOption = await screen.findByText('Instance Beta');
    await user.click(betaOption);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /compare/i })).toBeEnabled();
    });
    await user.click(screen.getByRole('button', { name: /compare/i }));

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/failed to compare/i);
    });
  });

  it('shows loading spinner during comparison', async () => {
    (instanceService.compareInstances as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    const user = userEvent.setup({ delay: null });
    renderCompare();

    await waitFor(() => {
      expect(screen.getByLabelText('Left instance')).toBeInTheDocument();
    });

    const leftInput = screen.getByLabelText('Left instance');
    await user.click(leftInput);
    await user.type(leftInput, 'Alpha');
    const alphaOption = await screen.findByText('Instance Alpha');
    await user.click(alphaOption);

    const rightInput = screen.getByLabelText('Right instance');
    await user.click(rightInput);
    await user.type(rightInput, 'Beta');
    const betaOption = await screen.findByText('Instance Beta');
    await user.click(betaOption);

    // Auto-trigger fires once both IDs are set; compareInstances never resolves so
    // comparing stays true and the spinner remains visible — no button click needed.
    await waitFor(() => {
      expect(screen.getByRole('progressbar')).toBeInTheDocument();
    });
  });
});
