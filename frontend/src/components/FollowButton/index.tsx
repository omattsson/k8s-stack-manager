import { useEffect, useState } from 'react';
import { Button, Tooltip } from '@mui/material';
import NotificationsActiveIcon from '@mui/icons-material/NotificationsActive';
import NotificationsNoneIcon from '@mui/icons-material/NotificationsNone';
import { instanceService } from '../../api/client';
import type { FollowState } from '../../types';

interface FollowButtonProps {
  instanceId: string;
  /** Follow state from GET /stack-instances/:id. The button is hidden when it is undefined (following not available). */
  following?: boolean;
  /** Follower count from GET /stack-instances/:id. */
  followerCount?: number;
  /** Called with the new state after a successful follow or unfollow. */
  onChange?: (state: FollowState) => void;
  /** Called with a message when a follow or unfollow request fails. */
  onError?: (message: string) => void;
}

/**
 * Follow toggle for a stack instance. A follower gets the in-app
 * notifications of the instance, as the owner does.
 */
const FollowButton = ({ instanceId, following, followerCount, onChange, onError }: FollowButtonProps) => {
  const [isFollowing, setIsFollowing] = useState(following ?? false);
  const [count, setCount] = useState(followerCount ?? 0);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    setIsFollowing(following ?? false);
    setCount(followerCount ?? 0);
  }, [instanceId, following, followerCount]);

  if (following === undefined) return null;

  const handleToggle = async () => {
    setLoading(true);
    try {
      const state = isFollowing
        ? await instanceService.unfollow(instanceId)
        : await instanceService.follow(instanceId);
      setIsFollowing(state.following);
      setCount(state.follower_count);
      onChange?.(state);
    } catch {
      onError?.(isFollowing ? 'Failed to unfollow the stack' : 'Failed to follow the stack');
    } finally {
      setLoading(false);
    }
  };

  const followers = `${count} ${count === 1 ? 'follower' : 'followers'}`;
  const tooltip = isFollowing
    ? `You get the notifications of this stack. ${followers}.`
    : `Get the notifications of this stack. ${followers}.`;

  return (
    <Tooltip title={tooltip}>
      <span>
        <Button
          size="small"
          variant={isFollowing ? 'contained' : 'outlined'}
          startIcon={isFollowing ? <NotificationsActiveIcon /> : <NotificationsNoneIcon />}
          onClick={handleToggle}
          disabled={loading}
          aria-pressed={isFollowing}
          aria-label={isFollowing ? 'Unfollow stack' : 'Follow stack'}
        >
          {isFollowing ? 'Following' : 'Follow'}
          {count > 0 ? ` (${count})` : ''}
        </Button>
      </span>
    </Tooltip>
  );
};

export default FollowButton;
