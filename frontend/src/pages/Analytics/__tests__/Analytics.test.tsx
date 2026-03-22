import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Analytics from '../index';

vi.mock('../../../api/client', () => ({
  analyticsService: {
    getOverview: vi.fn(),
    getTemplateStats: vi.fn(),
    getUserStats: vi.fn(),
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

import { analyticsService } from '../../../api/client';

const mockOverview = {
  total_templates: 5,
  total_definitions: 12,
  total_instances: 8,
  running_instances: 3,
  total_deploys: 42,
  total_users: 7,
};

const mockTemplates = [
  {
    template_id: '1',
    template_name: 'Web App',
    category: 'web',
    is_published: true,
    definition_count: 3,
    instance_count: 5,
    deploy_count: 10,
    success_count: 9,
    error_count: 1,
    success_rate: 90,
  },
];

const mockUsers = [
  {
    user_id: '1',
    username: 'alice',
    instance_count: 4,
    deploy_count: 15,
    last_active: new Date().toISOString(),
  },
];

describe('Analytics Page', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner initially', () => {
    (analyticsService.getOverview as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    (analyticsService.getTemplateStats as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    (analyticsService.getUserStats as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <Analytics />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('renders overview cards and tables on success', async () => {
    (analyticsService.getOverview as ReturnType<typeof vi.fn>).mockResolvedValue(mockOverview);
    (analyticsService.getTemplateStats as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplates);
    (analyticsService.getUserStats as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    render(
      <MemoryRouter>
        <Analytics />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getAllByText('5').length).toBeGreaterThanOrEqual(1); // total_templates (also in table)
      expect(screen.getByText('12')).toBeInTheDocument(); // total_definitions
      expect(screen.getByText('42')).toBeInTheDocument(); // total_deploys
      expect(screen.getByText('3 running')).toBeInTheDocument();
    });
    // Template table
    expect(screen.getByText('Web App')).toBeInTheDocument();
    expect(screen.getAllByText('Published').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('90%')).toBeInTheDocument();
    // User table
    expect(screen.getByText('alice')).toBeInTheDocument();
  });

  it('shows error alert on API failure', async () => {
    (analyticsService.getOverview as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    (analyticsService.getTemplateStats as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    (analyticsService.getUserStats as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    render(
      <MemoryRouter>
        <Analytics />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load analytics data')).toBeInTheDocument();
    });
  });

  it('handles empty data gracefully', async () => {
    (analyticsService.getOverview as ReturnType<typeof vi.fn>).mockResolvedValue({
      total_templates: 0,
      total_definitions: 0,
      total_instances: 0,
      running_instances: 0,
      total_deploys: 0,
      total_users: 0,
    });
    (analyticsService.getTemplateStats as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (analyticsService.getUserStats as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Analytics />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('No template data available')).toBeInTheDocument();
      expect(screen.getByText('No user data available')).toBeInTheDocument();
    });
  });
});
