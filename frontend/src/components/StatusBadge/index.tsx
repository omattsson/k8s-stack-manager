import { Chip } from '@mui/material';
import type { StackStatus } from '../../types';

const statusColors: Record<StackStatus, 'default' | 'info' | 'success' | 'warning' | 'error'> = {
  draft: 'default',
  deploying: 'info',
  stabilizing: 'info',
  running: 'success',
  partial: 'warning',
  stopped: 'warning',
  stopping: 'warning',
  cleaning: 'warning',
  error: 'error',
};

interface StatusBadgeProps {
  status: string;
}

const StatusBadge = ({ status }: StatusBadgeProps) => {
  const color = statusColors[status as StackStatus] || 'default';
  return <Chip label={status} color={color} size="small" />;
};

export default StatusBadge;
