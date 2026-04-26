import CheckCircleOutlined from '@mui/icons-material/CheckCircleOutlined';
import ErrorOutlined from '@mui/icons-material/ErrorOutlined';
import StopCircleOutlined from '@mui/icons-material/StopCircleOutlined';
import DeleteOutlined from '@mui/icons-material/DeleteOutlined';
import AddCircleOutlined from '@mui/icons-material/AddCircleOutlined';
import InfoOutlined from '@mui/icons-material/InfoOutlined';
import TimerOffOutlined from '@mui/icons-material/TimerOffOutlined';
import WarningAmberOutlined from '@mui/icons-material/WarningAmberOutlined';
import CleaningServicesOutlined from '@mui/icons-material/CleaningServicesOutlined';
import VpnKeyOutlined from '@mui/icons-material/VpnKeyOutlined';

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
  if (type === 'deploy.timeout') return <TimerOffOutlined color="error" fontSize="small" />;
  if (type === 'stack.expiring') return <TimerOffOutlined color="warning" fontSize="small" />;
  if (type === 'quota.warning') return <WarningAmberOutlined color="warning" fontSize="small" />;
  if (type === 'secret.expiring') return <VpnKeyOutlined color="warning" fontSize="small" />;
  if (type.includes('cleanup') || type === 'stack.expired') return <CleaningServicesOutlined color="action" fontSize="small" />;
  if (type.includes('error')) return <ErrorOutlined color="error" fontSize="small" />;
  if (type.includes('success') || type.includes('completed')) return <CheckCircleOutlined color="success" fontSize="small" />;
  if (type.includes('stopped')) return <StopCircleOutlined color="warning" fontSize="small" />;
  if (type.includes('deleted')) return <DeleteOutlined color="action" fontSize="small" />;
  if (type.includes('created')) return <AddCircleOutlined color="success" fontSize="small" />;
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
    case 'cluster':
    case 'cleanup_policy':
      return '/settings';
    default:
      return '';
  }
}
