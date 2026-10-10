import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import FollowButton from '../index';

vi.mock('../../../api/client', () => ({
  instanceService: {
    follow: vi.fn(),
    unfollow: vi.fn(),
  },
}));

import { instanceService } from '../../../api/client';

describe('FollowButton', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders nothing when following is not available', () => {
    const { container } = render(<FollowButton instanceId="i1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows Follow with the follower count when not following', () => {
    render(<FollowButton instanceId="i1" following={false} followerCount={2} />);
    const button = screen.getByRole('button', { name: 'Follow stack' });
    expect(button).toHaveTextContent('Follow (2)');
    expect(button).toHaveAttribute('aria-pressed', 'false');
  });

  it('follows on click and shows the new state', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    vi.mocked(instanceService.follow).mockResolvedValue({ following: true, follower_count: 1 });
    render(<FollowButton instanceId="i1" following={false} followerCount={0} onChange={onChange} />);

    await user.click(screen.getByRole('button', { name: 'Follow stack' }));

    await waitFor(() => {
      expect(instanceService.follow).toHaveBeenCalledWith('i1');
      expect(screen.getByRole('button', { name: 'Unfollow stack' })).toHaveTextContent('Following (1)');
    });
    expect(onChange).toHaveBeenCalledWith({ following: true, follower_count: 1 });
  });

  it('unfollows on click', async () => {
    const user = userEvent.setup();
    vi.mocked(instanceService.unfollow).mockResolvedValue({ following: false, follower_count: 0 });
    render(<FollowButton instanceId="i1" following followerCount={1} />);

    await user.click(screen.getByRole('button', { name: 'Unfollow stack' }));

    await waitFor(() => {
      expect(instanceService.unfollow).toHaveBeenCalledWith('i1');
      expect(screen.getByRole('button', { name: 'Follow stack' })).toHaveTextContent('Follow');
    });
  });

  it('keeps the state and reports an error when the request fails', async () => {
    const user = userEvent.setup();
    const onError = vi.fn();
    vi.mocked(instanceService.follow).mockRejectedValue(new Error('network'));
    render(<FollowButton instanceId="i1" following={false} followerCount={0} onError={onError} />);

    await user.click(screen.getByRole('button', { name: 'Follow stack' }));

    await waitFor(() => {
      expect(onError).toHaveBeenCalledWith('Failed to follow the stack');
    });
    expect(screen.getByRole('button', { name: 'Follow stack' })).toBeEnabled();
  });
});
