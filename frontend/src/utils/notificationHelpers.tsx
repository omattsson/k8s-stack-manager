import CheckCircleOutline from '@mui/icons-material/CheckCircleOutline';
import ErrorOutline from '@mui/icons-material/ErrorOutline';
import StopCircleOutlined from '@mui/icons-material/StopCircleOutlined';
import DeleteOutline from '@mui/icons-material/DeleteOutline';
import InfoOutlined from '@mui/icons-material/InfoOutlined';

/**
 * Returns a human-readable relative time string for a given ISO date.
 */
export function timeAgo(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  if (!Number.isFinite(then)) return dateStr || 'unknown';
  const diffSeconds = Math.floor((now - then) / 1000);

  if (diffSeconds < 60) return 'just now';
  const diffMinutes = Math.floor(diffSeconds / 60);
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 30) return `${diffDays}d ago`;
  return new Date(dateStr).toLocaleDateString();
}

/**
 * Returns an MUI icon element appropriate for the notification type.
 */
export function notificationIcon(type: string) {
  if (type.includes('success')) return <CheckCircleOutline color="success" fontSize="small" />;
  if (type.includes('error')) return <ErrorOutline color="error" fontSize="small" />;
  if (type.includes('stopped')) return <StopCircleOutlined color="warning" fontSize="small" />;
  if (type.includes('deleted')) return <DeleteOutline color="action" fontSize="small" />;
  return <InfoOutlined color="info" fontSize="small" />;
}

/**
 * Maps a notification entity_type to a base route path for navigation.
 */
export function entityRoute(entityType?: string): string {
  switch (entityType) {
    case 'stack_instance':
      return '/stack-instances';
    case 'stack_definition':
      return '/stack-definitions';
    case 'stack_template':
      return '/templates';
    default:
      return '';
  }
}
