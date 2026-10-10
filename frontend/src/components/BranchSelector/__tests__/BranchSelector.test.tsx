import { describe, it, expect, vi, afterEach } from 'vitest';
import { useState } from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import BranchSelector, { mergeBranchLists, normalizeRepoUrls } from '../index';

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

  it('lists the branches of all repositories for a URL list', async () => {
    const user = userEvent.setup();
    (gitService.branches as ReturnType<typeof vi.fn>).mockImplementation(async (url: string) => {
      if (url === 'https://git/a') return ['main', 'feature/a'];
      if (url === 'https://git/b') throw new Error('no access');
      return ['main', 'feature/c'];
    });
    render(<BranchSelector {...defaultProps} repoUrl={['', 'https://git/a', 'https://git/b', 'https://git/c', 'https://git/a']} />);

    await waitFor(() => {
      expect(gitService.branches).toHaveBeenCalledTimes(3);
    });
    const input = screen.getByRole('combobox');
    await user.click(input);
    await user.clear(input);
    const options = await screen.findAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual(['main', 'feature/a', 'feature/c']);
  });

  it('says how many repositories did not load when some fail', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockImplementation(async (url: string) => {
      if (url === 'https://git/b') throw new Error('no access');
      return ['main'];
    });
    render(<BranchSelector {...defaultProps} repoUrl={['https://git/a', 'https://git/b', 'https://git/c']} />);

    expect(await screen.findByText('Branches of 1 of 3 repositories did not load.')).toBeInTheDocument();
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('loads nothing for an empty URL list', () => {
    render(<BranchSelector {...defaultProps} repoUrl={['', ' ']} />);
    expect(gitService.branches).not.toHaveBeenCalled();
  });

  it('normalizes repository URLs and merges branch lists', () => {
    expect(normalizeRepoUrls(' https://x ')).toEqual(['https://x']);
    expect(normalizeRepoUrls(['https://x', '', 'https://y', 'https://x'])).toEqual(['https://x', 'https://y']);
    expect(mergeBranchLists([['main', 'dev'], ['dev', 'x']])).toEqual(['main', 'dev', 'x']);
  });

  it('shows error helper text and a plain text field when API fails', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
    render(<BranchSelector {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByText('Could not load branches. Enter the branch name and press Enter.')).toBeInTheDocument();
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

  it('commits text in the error fallback input only on Enter or blur', async () => {
    const user = userEvent.setup();
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    const onChange = vi.fn();
    render(<BranchSelector {...defaultProps} value="" onChange={onChange} />);

    await waitFor(() => {
      expect(screen.getByText('Could not load branches. Enter the branch name and press Enter.')).toBeInTheDocument();
    });

    const input = screen.getByLabelText('Branch');
    await user.type(input, 'feature/new');
    expect(onChange).not.toHaveBeenCalled();

    await user.keyboard('{Enter}');
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith('feature/new');
  });

  describe('typing, clearing and selecting', () => {
    /** Parent that stores the committed branch, as the detail page does. */
    const Controlled = ({ onCommit, initial = 'main' }: { onCommit: (b: string) => void; initial?: string }) => {
      const [branch, setBranch] = useState(initial);
      return (
        <BranchSelector
          repoUrl={defaultProps.repoUrl}
          value={branch}
          onChange={(b) => { onCommit(b); setBranch(b); }}
        />
      );
    };

    const setup = async (onCommit = vi.fn()) => {
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main', 'develop', 'fix/login-page']);
      const user = userEvent.setup();
      render(<Controlled onCommit={onCommit} />);
      await waitFor(() => {
        expect(gitService.branches).toHaveBeenCalled();
      });
      return { user, onCommit, input: screen.getByRole('combobox') };
    };

    it('keeps the box empty after the user clears it', async () => {
      const { user, onCommit, input } = await setup();

      await user.clear(input);

      expect(input).toHaveValue('');
      expect(onCommit).not.toHaveBeenCalled();
    });

    it('does not commit while the user types', async () => {
      const { user, onCommit, input } = await setup();

      await user.clear(input);
      await user.type(input, 'fix/lo');

      expect(input).toHaveValue('fix/lo');
      expect(onCommit).not.toHaveBeenCalled();
      // Text that matches a branch does not show a warning while typing.
      expect(screen.queryByText(/is not in the loaded branch list/)).not.toBeInTheDocument();
    });

    it('shows how to commit or cancel typed text', async () => {
      const { user, input } = await setup();

      await user.clear(input);
      await user.type(input, 'dev');

      expect(screen.getByText('Press Enter to use typed text. Escape restores the current branch.')).toBeInTheDocument();
      await user.keyboard('{Escape}{Escape}');
      expect(screen.queryByText(/Press Enter to use typed text/)).not.toBeInTheDocument();
    });

    it('commits exactly the selected option', async () => {
      const { user, onCommit, input } = await setup();

      await user.clear(input);
      await user.type(input, 'fix/lo');
      await user.click(await screen.findByRole('option', { name: 'fix/login-page' }));

      expect(onCommit).toHaveBeenCalledTimes(1);
      expect(onCommit).toHaveBeenCalledWith('fix/login-page');
      expect(input).toHaveValue('fix/login-page');
    });

    it('restores the selected branch when the box is empty on blur', async () => {
      const { user, onCommit, input } = await setup();

      await user.clear(input);
      await user.tab();

      expect(input).toHaveValue('main');
      expect(onCommit).not.toHaveBeenCalled();
    });

    it('restores the selected branch on Escape', async () => {
      const { user, onCommit, input } = await setup();

      await user.clear(input);
      await user.type(input, 'dev');
      await user.keyboard('{Escape}{Escape}');

      expect(input).toHaveValue('main');
      expect(onCommit).not.toHaveBeenCalled();
    });

    it('warns about free text that is not a branch and commits it on Enter', async () => {
      const { user, onCommit, input } = await setup();

      await user.clear(input);
      await user.type(input, 'no-such-branch');

      // No branch matches: the warning shows before the commit.
      expect(screen.getByText('Branch "no-such-branch" is not in the loaded branch list.')).toBeInTheDocument();
      expect(onCommit).not.toHaveBeenCalled();

      await user.keyboard('{Enter}');

      expect(onCommit).toHaveBeenCalledTimes(1);
      expect(onCommit).toHaveBeenCalledWith('no-such-branch');
      expect(screen.getByText('Branch "no-such-branch" is not in the loaded branch list.')).toBeInTheDocument();
    });

    it('warns after a blur commits a partial branch name', async () => {
      const { user, onCommit, input } = await setup();

      await user.clear(input);
      await user.type(input, 'fix/lo');
      await user.tab();

      expect(onCommit).toHaveBeenCalledWith('fix/lo');
      expect(screen.getByText('Branch "fix/lo" is not in the loaded branch list.')).toBeInTheDocument();
    });

    it('does not warn for a known branch', async () => {
      await setup();
      await waitFor(() => {
        expect(screen.getByRole('combobox')).toHaveValue('main');
      });
      expect(screen.queryByText(/is not in the loaded branch list/)).not.toBeInTheDocument();
    });
  });
});
