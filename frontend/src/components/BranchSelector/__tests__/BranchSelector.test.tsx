import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import BranchSelector from '../index';

vi.mock('../../../api/client', () => ({
  gitService: {
    branches: vi.fn(),
  },
}));

import { gitService } from '../../../api/client';

describe('BranchSelector', () => {
  const defaultProps = {
    repoUrl: 'https://dev.azure.com/org/project/_git/repo',
    value: 'main',
    onChange: vi.fn(),
  };

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders with the current branch value', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main', 'develop']);
    render(<BranchSelector {...defaultProps} />);
    // The Autocomplete input should have the current value
    const input = screen.getByRole('combobox');
    expect(input).toHaveValue('main');
  });

  it('renders with the default "Branch" label', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(<BranchSelector {...defaultProps} />);
    expect(screen.getByLabelText('Branch')).toBeInTheDocument();
  });

  it('renders with a custom label', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(<BranchSelector {...defaultProps} label="Git Branch" />);
    expect(screen.getByLabelText('Git Branch')).toBeInTheDocument();
  });

  it('fetches branches on mount', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main', 'develop', 'feature/x']);
    render(<BranchSelector {...defaultProps} />);
    await waitFor(() => {
      expect(gitService.branches).toHaveBeenCalledWith('https://dev.azure.com/org/project/_git/repo');
    });
  });

  it('shows error helper text and a plain text field when API fails', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
    render(<BranchSelector {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByText('Could not load branches. Enter branch name manually.')).toBeInTheDocument();
    });
  });

  it('does not fetch branches when repoUrl is empty', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(<BranchSelector {...defaultProps} repoUrl="" />);
    // Wait a tick to ensure no API call was made
    await waitFor(() => {
      expect(gitService.branches).not.toHaveBeenCalled();
    });
  });

  it('calls onChange when text is typed in error fallback input', async () => {
    const { default: userEvent } = await import('@testing-library/user-event');
    const user = userEvent.setup();
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    const onChange = vi.fn();
    render(<BranchSelector {...defaultProps} value="" onChange={onChange} />);

    await waitFor(() => {
      expect(screen.getByText('Could not load branches. Enter branch name manually.')).toBeInTheDocument();
    });

    const input = screen.getByLabelText('Branch');
    await user.type(input, 'feature/new');
    expect(onChange).toHaveBeenCalled();
  });
});
