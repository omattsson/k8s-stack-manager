import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { useState } from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import BranchSelector, { detectProvider, mergeBranchLists, normalizeRepoUrls } from '../index';

vi.mock('../../../api/client', () => ({
  gitService: {
    branches: vi.fn(),
    providers: vi.fn(),
  },
}));

import { gitService } from '../../../api/client';

describe('BranchSelector', () => {
  const defaultProps = {
    repoUrl: 'https://dev.azure.com/org/project/_git/repo',
    value: 'main',
    onChange: vi.fn(),
  };

  beforeEach(() => {
    (gitService.providers as ReturnType<typeof vi.fn>).mockResolvedValue([
      { type: 'azure_devops', available: true },
      { type: 'github', available: true },
      { type: 'gitlab', available: true, origin: 'https://gitlab.com' },
    ]);
  });

  /** Wait until the branch and provider status requests settled. */
  const waitForRequests = async () => {
    await waitFor(() => expect(gitService.branches).toHaveBeenCalled());
    await waitFor(() => expect(gitService.providers).toHaveBeenCalledOnce());
    await act(async () => {
      await Promise.resolve();
    });
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
      expect(gitService.branches).toHaveBeenCalledWith(
        'https://dev.azure.com/org/project/_git/repo',
        { signal: expect.any(AbortSignal) },
      );
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

  it('announces a generic error and shows a plain text field when branch loading fails', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error('token and repository details'),
    );
    render(<BranchSelector {...defaultProps} />);

    await screen.findByText('Could not load branches. Enter the branch name and press Enter.');
    const status = screen.getByRole('status');
    expect(status).toHaveAttribute('aria-live', 'polite');
    expect(status).toHaveTextContent('Could not load branches. Enter the branch name and press Enter.');
    expect(screen.getAllByText('Could not load branches. Enter the branch name and press Enter.')).toHaveLength(1);
    expect(screen.queryByText(/token and repository details/i)).not.toBeInTheDocument();
  });

  it('ignores an out-of-order branch response for an old repository', async () => {
    const user = userEvent.setup();
    let resolveOldRequest!: (branches: string[]) => void;
    let resolveNewRequest!: (branches: string[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>).mockImplementation((repoUrl: string) => (
      new Promise<string[]>((resolve) => {
        if (repoUrl === defaultProps.repoUrl) {
          resolveOldRequest = resolve;
        } else {
          resolveNewRequest = resolve;
        }
      })
    ));

    const { rerender } = render(<BranchSelector {...defaultProps} value="" />);
    rerender(
      <BranchSelector
        {...defaultProps}
        repoUrl="https://github.com/org/new-repo"
        value=""
      />,
    );

    await act(async () => {
      resolveNewRequest(['new-main']);
    });
    await user.click(screen.getByRole('combobox'));
    expect(await screen.findByRole('option', { name: 'new-main' })).toBeInTheDocument();

    await act(async () => {
      resolveOldRequest(['old-main']);
    });
    expect(screen.getByRole('option', { name: 'new-main' })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: 'old-main' })).not.toBeInTheDocument();
  });

  it('clears old branch options while the new repository request is pending', async () => {
    const user = userEvent.setup();
    let resolveNewRequest!: (branches: string[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce(['old-main'])
      .mockImplementationOnce(() => new Promise<string[]>((resolve) => {
        resolveNewRequest = resolve;
      }));

    const { rerender } = render(<BranchSelector {...defaultProps} value="manual-branch" />);
    await user.click(screen.getByRole('combobox'));
    expect(await screen.findByRole('option', { name: 'old-main' })).toBeInTheDocument();

    rerender(
      <BranchSelector
        {...defaultProps}
        repoUrl="https://github.com/org/new-repo"
        value="manual-branch"
      />,
    );

    await waitFor(() => expect(gitService.branches).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('option', { name: 'old-main' })).not.toBeInTheDocument();
    expect(screen.getByRole('combobox')).toHaveValue('manual-branch');
    expect(screen.getByText('Loading…')).toBeInTheDocument();

    await act(async () => {
      resolveNewRequest([]);
    });
  });

  it('clears a previous branch error while the new repository request is pending', async () => {
    const user = userEvent.setup();
    let resolveNewRequest!: (branches: string[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('old repository failure'))
      .mockImplementationOnce(() => new Promise<string[]>((resolve) => {
        resolveNewRequest = resolve;
      }));

    const { rerender } = render(<BranchSelector {...defaultProps} />);
    expect(await screen.findByText(
      'Could not load branches. Enter the branch name and press Enter.',
    )).toBeInTheDocument();

    rerender(<BranchSelector {...defaultProps} repoUrl="https://github.com/org/new-repo" />);

    expect(screen.queryByText(
      'Could not load branches. Enter the branch name and press Enter.',
    )).not.toBeInTheDocument();
    expect(screen.getByRole('combobox')).toBeInTheDocument();
    await user.click(screen.getByRole('combobox'));
    expect(screen.getByText('Loading…')).toBeInTheDocument();

    await act(async () => {
      resolveNewRequest([]);
    });
  });

  it('accepts manual branch entry while the new repository request is pending', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    let resolveNewRequest!: (branches: string[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce(['old-main'])
      .mockImplementationOnce(() => new Promise<string[]>((resolve) => {
        resolveNewRequest = resolve;
      }));

    const { rerender } = render(
      <BranchSelector {...defaultProps} value="" onChange={onChange} />,
    );
    await waitFor(() => expect(gitService.branches).toHaveBeenCalledOnce());

    rerender(
      <BranchSelector
        {...defaultProps}
        repoUrl="https://github.com/org/new-repo"
        value=""
        onChange={onChange}
      />,
    );
    await waitFor(() => expect(gitService.branches).toHaveBeenCalledTimes(2));

    await user.type(screen.getByRole('combobox'), 'feature/pending');
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
    await user.keyboard('{Enter}');
    expect(onChange).toHaveBeenCalledOnce();
    expect(onChange).toHaveBeenLastCalledWith('feature/pending');
    expect(screen.getByRole('combobox')).toHaveValue('feature/pending');

    await act(async () => {
      resolveNewRequest([]);
    });
  });

  it('keeps the current branch request loading when an old repository request fails', async () => {
    const user = userEvent.setup();
    let rejectOldRequest!: (error: Error) => void;
    let resolveNewRequest!: (branches: string[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>).mockImplementation((repoUrl: string) => (
      new Promise<string[]>((resolve, reject) => {
        if (repoUrl === defaultProps.repoUrl) {
          rejectOldRequest = reject;
        } else {
          resolveNewRequest = resolve;
        }
      })
    ));

    const { rerender } = render(<BranchSelector {...defaultProps} value="" />);
    rerender(
      <BranchSelector
        {...defaultProps}
        repoUrl="https://github.com/org/new-repo"
        value=""
      />,
    );

    await act(async () => {
      rejectOldRequest(new Error('stale repository failure'));
    });
    expect(screen.queryByText('Could not load branches. Enter the branch name and press Enter.')).not.toBeInTheDocument();
    await user.click(screen.getByRole('combobox'));
    expect(screen.getByText('Loading…')).toBeInTheDocument();

    await act(async () => {
      resolveNewRequest([]);
    });
    expect(screen.queryByText('Loading…')).not.toBeInTheDocument();
  });

  it('does not update branch state after unmount', async () => {
    let resolveRequest!: (branches: string[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise<string[]>((resolve) => {
        resolveRequest = resolve;
      }),
    );

    const { unmount } = render(<BranchSelector {...defaultProps} />);
    await waitFor(() => expect(gitService.branches).toHaveBeenCalledOnce());
    unmount();

    await act(async () => {
      resolveRequest(['main']);
    });
  });

  it('handles branch rejection and cleanup after unmount without warnings', async () => {
    let rejectRequest!: (error: Error) => void;
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const unhandledRejection = vi.fn();
    window.addEventListener('unhandledrejection', unhandledRejection);
    (gitService.branches as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise<string[]>((_resolve, reject) => {
        rejectRequest = reject;
      }),
    );

    const { unmount } = render(<BranchSelector {...defaultProps} />);
    await waitFor(() => expect(gitService.branches).toHaveBeenCalledOnce());
    unmount();

    await act(async () => {
      rejectRequest(new Error('request failed after unmount'));
      await Promise.resolve();
    });

    expect(consoleError).not.toHaveBeenCalled();
    expect(unhandledRejection).not.toHaveBeenCalled();
    window.removeEventListener('unhandledrejection', unhandledRejection);
    consoleError.mockRestore();
  });

  it('passes an abort signal and aborts branch requests on repository change and unmount', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => undefined));

    const { rerender, unmount } = render(<BranchSelector {...defaultProps} />);
    await waitFor(() => expect(gitService.branches).toHaveBeenCalledOnce());
    const firstSignal = (gitService.branches as ReturnType<typeof vi.fn>).mock.calls[0][1].signal;
    expect(firstSignal).toBeInstanceOf(AbortSignal);
    expect(firstSignal.aborted).toBe(false);

    rerender(<BranchSelector {...defaultProps} repoUrl="https://github.com/org/new-repo" />);
    await waitFor(() => expect(gitService.branches).toHaveBeenCalledTimes(2));
    const secondSignal = (gitService.branches as ReturnType<typeof vi.fn>).mock.calls[1][1].signal;
    expect(firstSignal.aborted).toBe(true);
    expect(secondSignal.aborted).toBe(false);

    unmount();
    expect(secondSignal.aborted).toBe(true);
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

  it('announces while provider status is loading', async () => {
    let resolveProviderRequest!: (statuses: { type: string; available: boolean }[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
    (gitService.providers as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise((resolve) => {
        resolveProviderRequest = resolve;
      }),
    );

    render(<BranchSelector {...defaultProps} />);

    expect(screen.getByRole('status')).toHaveTextContent('Checking Azure DevOps...');

    await act(async () => {
      resolveProviderRequest([{ type: 'azure_devops', available: true }]);
    });
  });

  it.each([
    ['Azure DevOps', 'https://dev.azure.com/organization/project/_git/repository'],
    ['Azure DevOps', 'https://organization.visualstudio.com/project/_git/repo'],
    ['Azure DevOps', 'git@ssh.dev.azure.com:v3/organization/project/repository'],
    ['GitHub', 'https://github.com/organization/repository'],
    ['GitHub', 'git@github.com:organization/repository.git'],
    ['GitHub', 'ssh://git@github.com/organization/repository.git'],
    ['GitHub', 'https://GITHUB.COM/organization/repository'],
    ['GitLab', 'https://gitlab.com/organization/repository'],
    ['GitLab', 'git@GitLab.com:organization/repository.git'],
  ])('announces when %s is available for %s', async (providerName, repoUrl) => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);

    render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

    expect(await screen.findByText(`${providerName} available.`)).toBeInTheDocument();
  });

  it.each([
    'https://git.example.internal/github.com/organization/repository',
    'https://github.com.evil.example/organization/repository?source=github.com',
    'git@github.com.evil.example:organization/repository.git',
  ])('does not classify GitHub text outside the exact hostname for %s', async (repoUrl) => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);

    render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

    await waitForRequests();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it.each([
    'https://dev.azure.com.evil.example/organization/project/_git/repository',
    'https://visualstudio.com.evil.example/project/_git/repository',
    'https://git.example.internal/dev.azure.com/project?source=team.visualstudio.com',
    'https://visualstudio.com/project/_git/repository',
  ])('does not classify Azure text outside an exact supported hostname for %s', async (repoUrl) => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);

    render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

    await waitForRequests();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it.each([
    'relative/repository',
    // Not the GitLab origin of the providers response (gitlab.com).
    'https://git.example.internal/organization/repository',
    'git@gitlab.example.internal:group/repository.git',
  ])('shows no provider status for an unknown host %s', async (repoUrl) => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

    // The statuses are loaded: a custom GitLab origin could serve the host.
    await waitForRequests();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it.each([
    'not a URL',
    'git://github.com/organization/repository',
    'file:///srv/git/repository',
    // A trailing dot is never a provider host.
    'https://github.com./organization/repository',
    'https://gitlab.com./organization/repository',
    'git@github.com.:organization/repository.git',
  ])(
    'does not request provider status for an invalid URL or host %s',
    async (repoUrl) => {
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);

      render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

      await waitFor(() => expect(gitService.branches).toHaveBeenCalledOnce());
      expect(gitService.providers).not.toHaveBeenCalled();
      expect(screen.queryByRole('status')).not.toBeInTheDocument();
    },
  );

  it('coalesces simultaneous provider status requests across selectors', async () => {
    let resolveProviderRequest!: (statuses: { type: string; available: boolean }[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
    (gitService.providers as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise((resolve) => {
        resolveProviderRequest = resolve;
      }),
    );

    render(
      <>
        <BranchSelector {...defaultProps} label="First branch" />
        <BranchSelector {...defaultProps} label="Second branch" />
      </>,
    );

    expect(gitService.providers).toHaveBeenCalledOnce();

    await act(async () => {
      resolveProviderRequest([{ type: 'azure_devops', available: true }]);
    });
    expect(screen.getAllByText('Azure DevOps available.')).toHaveLength(2);
  });

  it('starts a fresh provider request on a later mount after coalesced success', async () => {
    let resolveProviderRequest!: (statuses: { type: string; available: boolean }[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
    (gitService.providers as ReturnType<typeof vi.fn>)
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveProviderRequest = resolve;
      }))
      .mockResolvedValueOnce([{ type: 'azure_devops', available: true }]);

    const coalesced = render(
      <>
        <BranchSelector {...defaultProps} label="First branch" />
        <BranchSelector {...defaultProps} label="Second branch" />
      </>,
    );
    expect(gitService.providers).toHaveBeenCalledOnce();

    await act(async () => {
      resolveProviderRequest([{ type: 'azure_devops', available: true }]);
    });
    expect(screen.getAllByText('Azure DevOps available.')).toHaveLength(2);
    coalesced.unmount();

    render(<BranchSelector {...defaultProps} label="Later branch" />);

    expect(await screen.findByText('Azure DevOps available.')).toBeInTheDocument();
    expect(gitService.providers).toHaveBeenCalledTimes(2);
  });

  it('keeps a shared provider request active when one selector unmounts', async () => {
    let resolveProviderRequest!: (statuses: { type: string; available: boolean }[]) => void;
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
    (gitService.providers as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise((resolve) => {
        resolveProviderRequest = resolve;
      }),
    );

    const first = render(<BranchSelector {...defaultProps} label="First branch" />);
    render(<BranchSelector {...defaultProps} label="Remaining branch" />);
    expect(gitService.providers).toHaveBeenCalledOnce();
    expect((gitService.providers as ReturnType<typeof vi.fn>).mock.calls[0]).toEqual([]);

    first.unmount();
    await act(async () => {
      resolveProviderRequest([{ type: 'azure_devops', available: true }]);
    });

    expect(screen.getByText('Azure DevOps available.')).toBeInTheDocument();
    expect(gitService.providers).toHaveBeenCalledOnce();
  });

  it('retries provider status on a later mount after an in-flight request fails', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
    (gitService.providers as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('provider failure'))
      .mockResolvedValueOnce([{ type: 'azure_devops', available: true }]);

    const first = render(<BranchSelector {...defaultProps} />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(
      'Could not check Azure DevOps. Enter a branch name manually.',
    ));
    first.unmount();

    render(<BranchSelector {...defaultProps} />);

    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Azure DevOps available.'));
    expect(gitService.providers).toHaveBeenCalledTimes(2);
  });

  it('keeps manual entry available when the provider is unavailable', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (gitService.providers as ReturnType<typeof vi.fn>).mockResolvedValue([
      { type: 'azure_devops', available: false },
      { type: 'github', available: true },
      { type: 'gitlab', available: true },
    ]);

    render(<BranchSelector {...defaultProps} value="" onChange={onChange} />);

    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(
      'Azure DevOps unavailable. Enter a branch name manually.',
    ));
    expect(gitService.branches).toHaveBeenCalledWith(
      defaultProps.repoUrl,
      { signal: expect.any(AbortSignal) },
    );

    await user.type(screen.getByRole('combobox'), 'feature/manual{Enter}');
    expect(onChange).toHaveBeenCalledOnce();
    expect(onChange).toHaveBeenLastCalledWith('feature/manual');
  });

  it('falls back to manual entry when provider status cannot be loaded', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (gitService.providers as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error('token and repository details'),
    );

    render(<BranchSelector {...defaultProps} value="" onChange={onChange} />);

    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(
      'Could not check Azure DevOps. Enter a branch name manually.',
    ));
    expect(screen.queryByText(/token and repository details/i)).not.toBeInTheDocument();

    await user.type(screen.getByRole('combobox'), 'fallback-branch');
    await user.tab();
    expect(onChange).toHaveBeenCalledOnce();
    expect(onChange).toHaveBeenLastCalledWith('fallback-branch');
  });

  describe('with a custom GitLab origin', () => {
    const customStatuses = (available: boolean) => [
      { type: 'azure_devops', available: true },
      { type: 'github', available: true },
      { type: 'gitlab', available, origin: 'https://git.example.com:8443' },
    ];

    it.each([
      'https://git.example.com:8443/group/repository',
      'git@git.example.com:group/repository.git',
      'ssh://git@GIT.example.com:2222/group/repository.git',
    ])('announces GitLab for %s on the origin', async (repoUrl) => {
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
      (gitService.providers as ReturnType<typeof vi.fn>).mockResolvedValue(customStatuses(true));

      render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

      await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('GitLab available.'));
    });

    it.each([
      'https://gitlab.com/group/repository',
      'git@gitlab.com:group/repository.git',
      // Same host, another port.
      'https://git.example.com/group/repository',
    ])('shows no GitLab status for %s off the origin', async (repoUrl) => {
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
      (gitService.providers as ReturnType<typeof vi.fn>).mockResolvedValue(customStatuses(true));

      render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

      await waitForRequests();
      expect(screen.queryByRole('status')).not.toBeInTheDocument();
      expect(screen.queryByText(/GitLab/)).not.toBeInTheDocument();
    });

    it('shows no GitLab status for gitlab.com while the statuses load', async () => {
      let resolveProviderRequest!: (statuses: { type: string; available: boolean; origin?: string }[]) => void;
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
      (gitService.providers as ReturnType<typeof vi.fn>).mockReturnValue(
        new Promise((resolve) => {
          resolveProviderRequest = resolve;
        }),
      );

      render(
        <BranchSelector
          {...defaultProps}
          repoUrl={['https://github.com/org/a', 'https://gitlab.com/group/b']}
        />,
      );

      // GitHub does not depend on the origin; GitLab waits for it.
      expect(screen.getByRole('status')).toHaveTextContent('Checking GitHub...');
      expect(screen.getByRole('status')).not.toHaveTextContent('GitLab');

      await act(async () => {
        resolveProviderRequest(customStatuses(true));
      });
      expect(screen.getByRole('status')).toHaveTextContent('GitHub available.');
      expect(screen.getByRole('status')).not.toHaveTextContent('GitLab');
    });

    it('announces an unavailable GitLab for a URL on the origin', async () => {
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (gitService.providers as ReturnType<typeof vi.fn>).mockResolvedValue(customStatuses(false));

      render(<BranchSelector {...defaultProps} repoUrl="https://git.example.com:8443/group/repository" />);

      await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(
        'GitLab unavailable. Enter a branch name manually.',
      ));
    });

    it('falls back to the default GitLab rule when the statuses do not load', async () => {
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
      (gitService.providers as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('down'));

      const { unmount } = render(<BranchSelector {...defaultProps} repoUrl="https://gitlab.com/group/repository" />);
      await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(
        'Could not check GitLab. Enter a branch name manually.',
      ));
      unmount();

      render(<BranchSelector {...defaultProps} repoUrl="https://git.example.com:8443/group/repository" />);
      await waitFor(() => expect(gitService.providers).toHaveBeenCalledTimes(2));
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.queryByRole('status')).not.toBeInTheDocument();
    });

    it('applies the provider response to the current URL list after a change', async () => {
      let resolveProviderRequest!: (statuses: { type: string; available: boolean; origin?: string }[]) => void;
      (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
      (gitService.providers as ReturnType<typeof vi.fn>).mockReturnValue(
        new Promise((resolve) => {
          resolveProviderRequest = resolve;
        }),
      );

      const { rerender } = render(
        <BranchSelector {...defaultProps} repoUrl="https://gitlab.com/group/old" />,
      );
      rerender(<BranchSelector {...defaultProps} repoUrl="https://git.example.com:8443/group/new" />);
      expect(screen.queryByRole('status')).not.toBeInTheDocument();

      // One shared request; its result is read with the origin for the new URL.
      await act(async () => {
        resolveProviderRequest(customStatuses(false));
      });
      expect(gitService.providers).toHaveBeenCalledOnce();
      expect(screen.getByRole('status')).toHaveTextContent('GitLab unavailable.');
    });
  });

  describe('detectProvider with a GitLab origin', () => {
    const custom = 'https://git.example.com:8443';

    it('uses gitlab.com for the default origin', () => {
      expect(detectProvider('https://gitlab.com/group/repo', 'https://gitlab.com')).toBe('gitlab');
      expect(detectProvider('git@gitlab.com:group/repo.git', 'https://gitlab.com')).toBe('gitlab');
      expect(detectProvider('https://git.example.com/group/repo', 'https://gitlab.com')).toBeNull();
      // gitlab.com with a port is still the default rule (as on the backend).
      expect(detectProvider('https://gitlab.com/group/repo', 'https://gitlab.com:8443')).toBe('gitlab');
    });

    it('matches the host and effective port of a custom origin', () => {
      expect(detectProvider('https://git.example.com:8443/group/repo', custom)).toBe('gitlab');
      expect(detectProvider('https://GIT.EXAMPLE.COM:8443/group/repo', custom)).toBe('gitlab');
      expect(detectProvider('http://git.example.com:8443/group/repo', custom)).toBe('gitlab');
      expect(detectProvider('https://git.example.com:08443/group/repo', custom)).toBe('gitlab');
      // Port mismatch, or no port (443).
      expect(detectProvider('https://git.example.com:9443/group/repo', custom)).toBeNull();
      expect(detectProvider('https://git.example.com/group/repo', custom)).toBeNull();
      // Another host.
      expect(detectProvider('https://other.example.com:8443/group/repo', custom)).toBeNull();
      expect(detectProvider('https://sub.git.example.com:8443/group/repo', custom)).toBeNull();
    });

    it('compares the effective port, not the scheme', () => {
      const https = 'https://git.example.com';
      expect(detectProvider('https://git.example.com/group/repo', https)).toBe('gitlab');
      expect(detectProvider('https://git.example.com:443/group/repo', https)).toBe('gitlab');
      expect(detectProvider('https://git.example.com:0443/group/repo', https)).toBe('gitlab');
      expect(detectProvider('http://git.example.com:443/group/repo', https)).toBe('gitlab');
      expect(detectProvider('http://git.example.com/group/repo', https)).toBeNull();
      expect(detectProvider('https://git.example.com:0/group/repo', https)).toBeNull();

      const http = 'http://git.example.com';
      expect(detectProvider('http://git.example.com/group/repo', http)).toBe('gitlab');
      expect(detectProvider('https://git.example.com:80/group/repo', http)).toBe('gitlab');
      expect(detectProvider('https://git.example.com/group/repo', http)).toBeNull();
      expect(detectProvider('https://git.example.com/group/repo', 'https://git.example.com:443')).toBe('gitlab');
    });

    it('matches ssh and scp URLs on the custom host only', () => {
      expect(detectProvider('git@git.example.com:group/repo.git', custom)).toBe('gitlab');
      expect(detectProvider('git@GIT.example.com:group/repo.git', custom)).toBe('gitlab');
      expect(detectProvider('ssh://git@git.example.com/group/repo.git', custom)).toBe('gitlab');
      expect(detectProvider('ssh://git@git.example.com:2222/group/repo.git', custom)).toBe('gitlab');
      expect(detectProvider('git@other.example.com:group/repo.git', custom)).toBeNull();
    });

    it('does not treat gitlab.com as GitLab with a custom origin', () => {
      expect(detectProvider('https://gitlab.com/group/repo', custom)).toBeNull();
      expect(detectProvider('git@gitlab.com:group/repo.git', custom)).toBeNull();
      expect(detectProvider('ssh://git@gitlab.com/group/repo.git', custom)).toBeNull();
    });

    it('keeps GitHub and Azure DevOps with a custom origin', () => {
      expect(detectProvider('https://github.com/org/repo', custom)).toBe('github');
      expect(detectProvider('https://dev.azure.com/org/project/_git/repo', custom)).toBe('azure_devops');
    });

    it('never matches a trailing-dot host', () => {
      expect(detectProvider('https://git.example.com.:8443/group/repo', custom)).toBeNull();
      expect(detectProvider('git@git.example.com.:group/repo.git', custom)).toBeNull();
      expect(detectProvider('https://gitlab.com./group/repo', 'https://gitlab.com')).toBeNull();
    });

    it.each([undefined, '', 'not a url', 'ftp://git.example.com', 'https://', 'https://git.example.com.'])(
      'falls back to the default rule for the missing or malformed origin %s',
      (origin) => {
        expect(detectProvider('https://gitlab.com/group/repo', origin)).toBe('gitlab');
        expect(detectProvider('https://git.example.com/group/repo', origin)).toBeNull();
      },
    );
  });

  it('detects the provider by exact hostname', () => {
    expect(detectProvider('https://github.com/org/repo')).toBe('github');
    expect(detectProvider('https://GitHub.com/org/repo')).toBe('github');
    expect(detectProvider('http://github.com/org/repo')).toBe('github');
    expect(detectProvider('github.com/org/repo')).toBe('github');
    expect(detectProvider('https://dev.azure.com/org/project/_git/repo')).toBe('azure_devops');
    expect(detectProvider('https://org.visualstudio.com/project/_git/repo')).toBe('azure_devops');
    expect(detectProvider('https://gitlab.com/group/repo')).toBe('gitlab');
    expect(detectProvider('https://github.com.evil.example/org/repo')).toBeNull();
    expect(detectProvider('')).toBeNull();
    expect(detectProvider('   ')).toBeNull();
  });

  it('detects the provider for uppercase hosts', () => {
    expect(detectProvider('https://GITHUB.COM/org/repo')).toBe('github');
    expect(detectProvider('https://DEV.AZURE.COM/org/project/_git/repo')).toBe('azure_devops');
    expect(detectProvider('https://Org.VisualStudio.com/project/_git/repo')).toBe('azure_devops');
    expect(detectProvider('https://GitLab.com/group/repo')).toBe('gitlab');
    expect(detectProvider('ssh://git@GitHub.com/org/repo.git')).toBe('github');
    expect(detectProvider('git@GITHUB.COM:org/repo.git')).toBe('github');
    expect(detectProvider('git@SSH.DEV.AZURE.COM:v3/org/project/repo')).toBe('azure_devops');
  });

  it('detects the provider of ssh:// and scp-style URLs', () => {
    expect(detectProvider('git@github.com:org/repo.git')).toBe('github');
    expect(detectProvider('ssh://git@github.com/org/repo.git')).toBe('github');
    expect(detectProvider('ssh://git@github.com:22/org/repo.git')).toBe('github');
    expect(detectProvider('git@ssh.dev.azure.com:v3/org/project/repo')).toBe('azure_devops');
    expect(detectProvider('ssh://git@ssh.dev.azure.com/v3/org/project/repo')).toBe('azure_devops');
    expect(detectProvider('org@vs-ssh.visualstudio.com:v3/org/project/repo')).toBe('azure_devops');
    expect(detectProvider('git@gitlab.com:group/repo.git')).toBe('gitlab');
    expect(detectProvider('ssh://git@gitlab.com/group/repo.git')).toBe('gitlab');
    expect(detectProvider('git@github.com.evil.example:org/repo.git')).toBeNull();
    expect(detectProvider('git://github.com/org/repo.git')).toBeNull();
  });

  it('gives no provider for an unknown host', () => {
    // A self-hosted GitLab is only known to the backend (GITLAB_BASE_URL).
    expect(detectProvider('https://gitlab.example.internal/group/repo')).toBeNull();
    expect(detectProvider('https://gitlab.example.internal:8443/group/repo')).toBeNull();
    expect(detectProvider('git@gitlab.example.internal:group/repo.git')).toBeNull();
    expect(detectProvider('ssh://git@gitlab.example.internal:2222/group/repo.git')).toBeNull();
    expect(detectProvider('https://visualstudio.com/project/_git/repo')).toBeNull();
    expect(detectProvider('https://dev.azure.com.evil.example/org/project/_git/repo')).toBeNull();
    expect(detectProvider('https://gitlab.com.evil.example/group/repo')).toBeNull();
    expect(detectProvider('not a URL')).toBeNull();
  });

  it('gives no provider for a host with a trailing dot or an empty label', () => {
    expect(detectProvider('https://github.com./org/repo')).toBeNull();
    expect(detectProvider('git@github.com.:org/repo.git')).toBeNull();
    expect(detectProvider('ssh://git@github.com./org/repo.git')).toBeNull();
    expect(detectProvider('https://dev.azure.com./org/project/_git/repo')).toBeNull();
    expect(detectProvider('https://org.visualstudio.com./project/_git/repo')).toBeNull();
    expect(detectProvider('git@ssh.dev.azure.com.:v3/org/project/repo')).toBeNull();
    expect(detectProvider('https://gitlab.com./group/repo')).toBeNull();
    expect(detectProvider('https://.visualstudio.com/project/_git/repo')).toBeNull();
    expect(detectProvider('https://org..visualstudio.com/project/_git/repo')).toBeNull();
  });

  it('announces the status of each provider for a URL list', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);
    (gitService.providers as ReturnType<typeof vi.fn>).mockResolvedValue([
      { type: 'azure_devops', available: true },
      { type: 'github', available: true },
      { type: 'gitlab', available: false },
    ]);

    render(
      <BranchSelector
        {...defaultProps}
        repoUrl={[
          'https://github.com/org/a',
          'https://gitlab.com/group/b',
          'https://git.example.internal/group/unknown',
          'https://github.com/org/c',
          'not a URL',
        ]}
      />,
    );

    await waitFor(() => {
      expect(screen.getByRole('status')).toHaveTextContent(
        'GitHub available. GitLab unavailable. Enter a branch name manually.',
      );
    });
    expect(gitService.providers).toHaveBeenCalledOnce();
    expect(gitService.branches).toHaveBeenCalledTimes(5);
  });

  it('shows the provider status together with the branch warning', async () => {
    const user = userEvent.setup();
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main', 'develop']);

    render(<BranchSelector {...defaultProps} />);
    expect(await screen.findByText('Azure DevOps available.')).toBeInTheDocument();

    const input = screen.getByRole('combobox');
    await user.clear(input);
    await user.type(input, 'no-such-branch');

    expect(screen.getByText('Branch "no-such-branch" is not in the loaded branch list.')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Azure DevOps available.');
  });

  it('shows the provider status together with the partial load text', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockImplementation(async (url: string) => {
      if (url === 'https://github.com/org/b') throw new Error('no access');
      return ['main'];
    });

    render(<BranchSelector {...defaultProps} repoUrl={['https://github.com/org/a', 'https://github.com/org/b']} />);

    expect(await screen.findByText('Branches of 1 of 2 repositories did not load.')).toBeInTheDocument();
    expect(await screen.findByText('GitHub available.')).toBeInTheDocument();
  });

  it('aborts the requests of all repositories in a URL list on unmount', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => undefined));

    const { unmount } = render(
      <BranchSelector {...defaultProps} repoUrl={['https://github.com/org/a', 'https://github.com/org/b']} />,
    );
    await waitFor(() => expect(gitService.branches).toHaveBeenCalledTimes(2));
    const signals = (gitService.branches as ReturnType<typeof vi.fn>).mock.calls
      .map((call) => (call[1] as { signal: AbortSignal }).signal);
    expect(signals.every((signal) => !signal.aborted)).toBe(true);

    unmount();
    expect(signals.every((signal) => signal.aborted)).toBe(true);
  });

  it('disables the error fallback field', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));

    render(<BranchSelector {...defaultProps} disabled />);

    await screen.findByText('Could not load branches. Enter the branch name and press Enter.');
    expect(screen.getByLabelText('Branch')).toBeDisabled();
  });
});
