import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import BranchSelector from '../index';

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
      { type: 'gitlab', available: true },
    ]);
  });

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

  it('announces a generic error and shows a plain text field when branch loading fails', async () => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error('token and repository details'),
    );
    render(<BranchSelector {...defaultProps} />);

    await screen.findByText('Could not load branches. Enter a branch name manually.');
    const status = screen.getByRole('status');
    expect(status).toHaveAttribute('aria-live', 'polite');
    expect(status).toHaveTextContent('Could not load branches. Enter a branch name manually.');
    expect(screen.getAllByText('Could not load branches. Enter a branch name manually.')).toHaveLength(1);
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
      'Could not load branches. Enter a branch name manually.',
    )).toBeInTheDocument();

    rerender(<BranchSelector {...defaultProps} repoUrl="https://github.com/org/new-repo" />);

    expect(screen.queryByText(
      'Could not load branches. Enter a branch name manually.',
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
    expect(onChange).toHaveBeenLastCalledWith('feature/pending');
    expect(screen.getByRole('combobox')).toHaveValue('feature/pending');
    expect(screen.getByText('Loading…')).toBeInTheDocument();

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
    expect(screen.queryByText('Could not load branches. Enter a branch name manually.')).not.toBeInTheDocument();
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

  it('calls onChange when text is typed in error fallback input', async () => {
    const user = userEvent.setup();
    (gitService.branches as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
    const onChange = vi.fn();
    render(<BranchSelector {...defaultProps} value="" onChange={onChange} />);

    await waitFor(() => {
      expect(screen.getByText('Could not load branches. Enter a branch name manually.')).toBeInTheDocument();
    });

    const input = screen.getByLabelText('Branch');
    await user.type(input, 'feature/new');
    expect(onChange).toHaveBeenCalled();
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
    ['GitHub', 'https://github.com/organization/repository'],
    ['GitLab', 'https://gitlab.com/organization/repository'],
    ['GitLab', 'https://git.example.internal/organization/repository'],
  ])('announces when %s is available', async (providerName, repoUrl) => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);

    render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

    expect(await screen.findByText(`${providerName} available.`)).toBeInTheDocument();
  });

  it.each([
    'https://git.example.internal/github.com/organization/repository',
    'https://github.com.evil.example/organization/repository?source=github.com',
  ])('does not classify GitHub text outside the exact hostname for %s', async (repoUrl) => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);

    render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

    expect(await screen.findByText('GitLab available.')).toBeInTheDocument();
    expect(screen.getByRole('status')).not.toHaveTextContent('GitHub available.');
  });

  it.each([
    'https://dev.azure.com.evil.example/organization/project/_git/repository',
    'https://visualstudio.com.evil.example/project/_git/repository',
    'https://git.example.internal/dev.azure.com/project?source=team.visualstudio.com',
  ])('does not classify Azure text outside an exact supported hostname for %s', async (repoUrl) => {
    (gitService.branches as ReturnType<typeof vi.fn>).mockResolvedValue(['main']);

    render(<BranchSelector {...defaultProps} repoUrl={repoUrl} />);

    expect(await screen.findByText('GitLab available.')).toBeInTheDocument();
    expect(screen.getByRole('status')).not.toHaveTextContent('Azure DevOps available.');
  });

  it.each(['relative/repository', 'not a URL', 'ssh://github.com/organization/repository'])(
    'does not request provider status for invalid or non-HTTP URL %s',
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
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Could not check Azure DevOps. Enter a branch name manually.',
    );
    first.unmount();

    render(<BranchSelector {...defaultProps} />);

    expect(await screen.findByRole('status')).toHaveTextContent('Azure DevOps available.');
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

    expect(await screen.findByRole('status')).toHaveTextContent(
      'Azure DevOps unavailable. Enter a branch name manually.',
    );
    expect(gitService.branches).toHaveBeenCalledWith(
      defaultProps.repoUrl,
      { signal: expect.any(AbortSignal) },
    );

    await user.type(screen.getByRole('combobox'), 'feature/manual');
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

    expect(await screen.findByRole('status')).toHaveTextContent(
      'Could not check Azure DevOps. Enter a branch name manually.',
    );
    expect(screen.queryByText(/token and repository details/i)).not.toBeInTheDocument();

    await user.type(screen.getByRole('combobox'), 'fallback-branch');
    expect(onChange).toHaveBeenLastCalledWith('fallback-branch');
  });
});
