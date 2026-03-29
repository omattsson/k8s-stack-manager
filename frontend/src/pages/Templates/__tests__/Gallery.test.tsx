import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Gallery from '../Gallery';

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
    list: vi.fn(),
    publish: vi.fn(),
    unpublish: vi.fn(),
    quickDeploy: vi.fn(),
    bulkDelete: vi.fn(),
    bulkPublish: vi.fn(),
    bulkUnpublish: vi.fn(),
  },
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
  },
  favoriteService: {
    list: vi.fn().mockResolvedValue([]),
    add: vi.fn().mockResolvedValue({}),
    remove: vi.fn().mockResolvedValue({}),
    check: vi.fn().mockResolvedValue(false),
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

vi.mock('../../../utils/recentTemplates', () => ({
  getRecentTemplates: vi.fn().mockReturnValue([]),
}));

import { templateService, favoriteService } from '../../../api/client';

const publishedTemplate = {
  id: '1', name: 'My Template', description: 'A test template', category: 'Web',
  version: '1.0', is_published: true, owner_id: '1', default_branch: 'main',
  created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
};

const draftTemplate = {
  id: '2', name: 'Draft Template', description: 'Work in progress', category: 'API',
  version: '0.1', is_published: false, owner_id: '1', default_branch: 'develop',
  created_at: '2026-02-01T00:00:00Z', updated_at: '2026-02-01T00:00:00Z',
};

const apiTemplate = {
  id: '3', name: 'API Service', description: 'Backend API', category: 'API',
  version: '2.0', is_published: true, owner_id: '2', default_branch: 'main',
  created_at: '2026-01-15T00:00:00Z', updated_at: '2026-01-15T00:00:00Z',
};

describe('Template Gallery', () => {
  beforeEach(() => {
    (favoriteService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner initially', () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays published templates', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
      expect(screen.getByText('A test template')).toBeInTheDocument();
    });
  });

  it('shows error on fetch failure', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('error'));
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  it('shows empty state when no templates match', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no templates found/i)).toBeInTheDocument();
    });
  });

  it('filters templates by search text', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate, apiTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
    const searchInput = screen.getByPlaceholderText(/search templates/i);
    await user.type(searchInput, 'API');
    await waitFor(() => {
      expect(screen.queryByText('My Template')).not.toBeInTheDocument();
      expect(screen.getByText('API Service')).toBeInTheDocument();
    });
  });

  it('filters templates by category chip', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate, apiTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: 'API' }));
    await waitFor(() => {
      expect(screen.queryByText('My Template')).not.toBeInTheDocument();
      expect(screen.getByText('API Service')).toBeInTheDocument();
    });
  });

  it('switches between Published and My Templates tabs', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate, draftTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
    // Draft template should not show on Published tab
    expect(screen.queryByText('Draft Template')).not.toBeInTheDocument();

    // Switch to My Templates tab
    await user.click(screen.getByRole('tab', { name: /my templates/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
    });
  });

  it('shows Quick Deploy button on published templates', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /quick deploy/i })).toBeInTheDocument();
    });
  });

  it('opens Quick Deploy dialog when button is clicked', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /quick deploy/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /quick deploy/i }));
    await waitFor(() => {
      expect(screen.getByText(/quick deploy: my template/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/instance name/i)).toBeInTheDocument();
    });
  });

  it('shows Use Template button on published templates', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /use template/i })).toBeInTheDocument();
  });

  it('shows favorite button on templates', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /add to favorites/i })).toBeInTheDocument();
  });

  it('selects templates for bulk operations on All Drafts tab', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([draftTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /all drafts/i })).toBeInTheDocument();
    });
    // Switch to All Drafts tab where bulk selection is enabled
    await user.click(screen.getByRole('tab', { name: /all drafts/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
    });
    // Check the checkbox for the draft template
    const checkbox = screen.getByRole('checkbox', { name: /select draft template/i });
    await user.click(checkbox);
    // Should show bulk action toolbar
    await waitFor(() => {
      expect(screen.getByText(/1 selected/i)).toBeInTheDocument();
    });
  });

  it('executes bulk delete on selected templates', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([draftTemplate]);
    (templateService.bulkDelete as ReturnType<typeof vi.fn>).mockResolvedValue({
      total: 1, succeeded: 1, failed: 0, results: [{ template_id: '2', template_name: 'Draft Template', status: 'success' }],
    });
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    // Go to All Drafts tab
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /all drafts/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('tab', { name: /all drafts/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
    });
    // Select the template
    const checkbox = screen.getByRole('checkbox', { name: /select draft template/i });
    await user.click(checkbox);
    await waitFor(() => {
      expect(screen.getByText(/1 selected/i)).toBeInTheDocument();
    });
    // Click bulk delete
    await user.click(screen.getByRole('button', { name: /delete/i }));
    // Confirm dialog
    await waitFor(() => {
      expect(screen.getByText(/confirm bulk/i)).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /delete/i }));
    await waitFor(() => {
      expect(templateService.bulkDelete).toHaveBeenCalledWith(['2']);
    });
  });

  it('executes bulk publish on selected templates', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([draftTemplate]);
    (templateService.bulkPublish as ReturnType<typeof vi.fn>).mockResolvedValue({
      total: 1, succeeded: 1, failed: 0, results: [{ template_id: '2', template_name: 'Draft Template', status: 'success' }],
    });
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    // All Drafts tab has publish button
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /all drafts/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('tab', { name: /all drafts/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
    });
    const checkbox = screen.getByRole('checkbox', { name: /select draft template/i });
    await user.click(checkbox);
    await waitFor(() => {
      expect(screen.getByText(/1 selected/i)).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /publish/i }));
    await waitFor(() => {
      expect(screen.getByText(/confirm bulk/i)).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /publish/i }));
    await waitFor(() => {
      expect(templateService.bulkPublish).toHaveBeenCalledWith(['2']);
    });
  });

  it('shows bulk result dialog after operation completes', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([draftTemplate]);
    (templateService.bulkDelete as ReturnType<typeof vi.fn>).mockResolvedValue({
      total: 1, succeeded: 1, failed: 0,
      results: [{ template_id: '2', template_name: 'Draft Template', status: 'success' }],
    });
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /all drafts/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('tab', { name: /all drafts/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
    });
    const checkbox = screen.getByRole('checkbox', { name: /select draft template/i });
    await user.click(checkbox);
    await user.click(screen.getByRole('button', { name: /delete/i }));
    await waitFor(() => {
      expect(screen.getByText(/confirm bulk/i)).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /delete/i }));
    // Result dialog should appear
    await waitFor(() => {
      expect(screen.getByText(/bulk operation results/i)).toBeInTheDocument();
    });
    expect(screen.getAllByText('Draft Template').length).toBeGreaterThanOrEqual(1);
  });

  it('shows select all checkbox on bulk-enabled tabs', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([draftTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /all drafts/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('tab', { name: /all drafts/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
    });
    // Select All checkbox should be present
    const selectAllCheckbox = screen.getByRole('checkbox', { name: /select all/i });
    expect(selectAllCheckbox).toBeInTheDocument();
    await user.click(selectAllCheckbox);
    await waitFor(() => {
      expect(screen.getByText(/1 selected/i)).toBeInTheDocument();
    });
  });

  it('shows All Drafts tab for devops/admin users', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate, draftTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
    expect(screen.getByRole('tab', { name: /all drafts/i })).toBeInTheDocument();
  });

  it('displays template category chips', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
    // Category filter chips should be visible
    expect(screen.getByRole('button', { name: 'All' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Web' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'API' })).toBeInTheDocument();
  });

  it('shows template version badge', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('v1.0')).toBeInTheDocument();
    });
  });

  it('shows favorite templates in favorites tab when favorites exist', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([publishedTemplate]);
    (favoriteService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: 'f1', user_id: '1', entity_type: 'template', entity_id: '1' },
    ]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Template')).toBeInTheDocument();
    });
  });

  it('handles bulk operation error gracefully', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([draftTemplate]);
    (templateService.bulkDelete as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Bulk op failed'));
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /all drafts/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('tab', { name: /all drafts/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
    });
    const checkbox = screen.getByRole('checkbox', { name: /select draft template/i });
    await user.click(checkbox);
    await user.click(screen.getByRole('button', { name: /delete/i }));
    await waitFor(() => {
      expect(screen.getByText(/confirm bulk/i)).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /delete/i }));
    await waitFor(() => {
      expect(mockShowError).toHaveBeenCalled();
    });
  });

  it('displays templates with draft status on My Templates tab', async () => {
    const user = userEvent.setup();
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([draftTemplate]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /my templates/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole('tab', { name: /my templates/i }));
    await waitFor(() => {
      expect(screen.getByText('Draft Template')).toBeInTheDocument();
      expect(screen.getByText('Draft')).toBeInTheDocument();
    });
  });

  it('shows Create Template button', async () => {
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Gallery />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /create template/i })).toBeInTheDocument();
  });
});
