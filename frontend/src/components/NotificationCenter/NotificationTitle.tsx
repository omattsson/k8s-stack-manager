import { Box } from '@mui/material';
import type { Notification } from '../../types';

interface NotificationTitleProps {
  notification: Pick<Notification, 'title' | 'is_read'>;
}

// In px as a string: in `sx`, a width of 1 means 100%. Pinned to the corner of the
// row: left where it falls, it sits after the title, and a title too long for a
// row that does not wrap would put it outside the scrolling list of the bell.
const visuallyHidden = {
  position: 'absolute',
  top: 0,
  left: 0,
  width: '1px',
  height: '1px',
  overflow: 'hidden',
  clipPath: 'inset(50%)',
  whiteSpace: 'nowrap',
} as const;

/**
 * Title of a notification row. Sighted users see an unread row by its bold title
 * and its tint. This says it in words to a screen reader.
 */
const NotificationTitle = ({ notification }: NotificationTitleProps) => (
  <>
    {notification.title}
    {!notification.is_read && <Box component="span" sx={visuallyHidden}>, unread</Box>}
  </>
);

export default NotificationTitle;
