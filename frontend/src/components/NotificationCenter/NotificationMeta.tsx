import { Box, Typography } from '@mui/material';
import { formatTrigger, timeAgo } from '../../utils/notificationHelpers';
import type { Notification } from '../../types';

interface NotificationMetaProps {
  /** Creation time of the notification (ISO 8601). */
  createdAt: string;
  /** Who did the action; no "by" text when it is missing. */
  trigger?: Notification['trigger'];
  /** ID of the signed-in user, for "by you". */
  currentUserId?: string;
}

/**
 * Time and "by" line of a notification, for example "5m ago by alice" (#652).
 * Inline elements only: it goes into the secondary text of a ListItemText.
 */
const NotificationMeta = ({ createdAt, trigger, currentUserId }: NotificationMetaProps) => {
  const byText = formatTrigger(trigger, currentUserId);
  return (
    <Box
      component="span"
      sx={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', columnGap: 0.75, minWidth: 0 }}
    >
      <Typography variant="caption" color="text.disabled" component="span">
        {timeAgo(createdAt)}
      </Typography>
      {byText && (
        <Typography
          variant="caption"
          color="text.secondary"
          component="span"
          sx={{ minWidth: 0, overflowWrap: 'anywhere' }}
        >
          {byText}
        </Typography>
      )}
    </Box>
  );
};

export default NotificationMeta;
