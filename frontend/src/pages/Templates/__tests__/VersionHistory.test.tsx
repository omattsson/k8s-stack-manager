import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import VersionHistory from '../VersionHistory';

vi.mock('../../../api/client', () => ({
  templateService: {
    listVersions: vi.fn(),
    getVersion: vi.fn(),
    diffVersions: vi.fn(),
  },
}));

// Mock react-diff-viewer-continued since it's a heavy component
vi.mock('react-diff-viewer-continued', () => ({
  default: ({ oldValue, newValue }: { oldValue: string; newValue: string }) => (
    <div data-testid="diff-viewer">
      <span>{oldValue}</span>
      <span>{newValue}</span>
    </div>
  ),
  DiffMethod: { LINES: 'diffLines' },
}));

import { templateService } from '../../../api/client';

const mockVersions = [
  {
    id: 'v2',
    template_id: 't1',
    version: '2.0',
    change_summary: 'Added monitoring chart',
    created_by: 'admin',
    created_at: '2025-06-15T10:00:00Z',
  },
  {
    id: 'v1',
    template_id: 't1',
    version: '1.0',
    change_summary: 'Initial version',
    created_by: 'admin',
    created_at: '2025-06-01T10:00:00Z',
  },
];

describe('VersionHistory', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner while fetching', () => {
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(<VersionHistory templateId="t1" />);
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('shows error alert when fetch fails', async () => {
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    render(<VersionHistory templateId="t1" />);
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load version history')).toBeInTheDocument();
    });
  });

  it('shows empty state when no versions exist', async () => {
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(<VersionHistory templateId="t1" />);
    await waitFor(() => {
      expect(screen.getByText(/no versions yet/i)).toBeInTheDocument();
    });
  });

  it('displays version list when loaded', async () => {
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockResolvedValue(mockVersions);
    render(<VersionHistory templateId="t1" />);
    await waitFor(() => {
      expect(screen.getByText('v2.0')).toBeInTheDocument();
      expect(screen.getByText('v1.0')).toBeInTheDocument();
    });
    expect(screen.getByText('Added monitoring chart')).toBeInTheDocument();
    expect(screen.getByText('Initial version')).toBeInTheDocument();
  });

  it('expands version to show snapshot details', async () => {
    const user = userEvent.setup();
    const singleVersion = [mockVersions[0]];
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockResolvedValue(singleVersion);
    (templateService.getVersion as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockVersions[0],
      snapshot: {
        template: {
          name: 'Web Stack',
          description: 'desc',
          category: 'Web',
          default_branch: 'main',
          repository_url: '',
          is_published: true,
          version: '2.0',
        },
        charts: [
          { chart_name: 'frontend', repo_url: 'https://example.com', default_values: '', locked_values: '', is_required: true, sort_order: 1 },
        ],
      },
    });

    render(<VersionHistory templateId="t1" />);

    await waitFor(() => {
      expect(screen.getByText('v2.0')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /expand version details/i }));

    await waitFor(() => {
      expect(screen.getByText('Template Snapshot')).toBeInTheDocument();
      expect(screen.getByText('frontend')).toBeInTheDocument();
    });
  });

  it('allows selecting two versions and comparing them', async () => {
    const user = userEvent.setup();
    (templateService.listVersions as ReturnType<typeof vi.fn>).mockResolvedValue(mockVersions);
    (templateService.diffVersions as ReturnType<typeof vi.fn>).mockResolvedValue({
      left: { version: mockVersions[1], snapshot: { template: {}, charts: [] } },
      right: { version: mockVersions[0], snapshot: { template: {}, charts: [] } },
      chart_diffs: [
        { chart_name: 'frontend', left_values: 'replicas: 1', right_values: 'replicas: 2', has_differences: true, change_type: 'modified' },
      ],
    });

    render(<VersionHistory templateId="t1" />);

    await waitFor(() => {
      expect(screen.getByText('v2.0')).toBeInTheDocument();
    });

    // Select both checkboxes for comparison
    const checkboxes = screen.getAllByRole('checkbox');
    await user.click(checkboxes[0]);

    await waitFor(() => {
      expect(screen.getByText(/select one more version to compare/i)).toBeInTheDocument();
    });

    await user.click(checkboxes[1]);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /compare selected versions/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /compare selected versions/i }));

    await waitFor(() => {
      expect(screen.getByText('Version Comparison')).toBeInTheDocument();
      expect(screen.getByText('frontend')).toBeInTheDocument();
    });
  });
});
