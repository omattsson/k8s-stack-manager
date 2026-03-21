import { Chip } from '@mui/material';
import useCountdown from '../../hooks/useCountdown';
import type { StackInstance } from '../../types';

interface ExpiryChipProps {
  instance: StackInstance;
}

const ExpiryChip = ({ instance }: ExpiryChipProps) => {
  const countdown = useCountdown(instance.expires_at);

  const isExpiredByTtl = instance.status === 'stopped' && 
    instance.expires_at != null && 
    new Date(instance.expires_at) <= new Date();

  if (isExpiredByTtl) {
    return <Chip label="Expired" color="error" size="small" sx={{ mt: 0.5 }} />;
  }

  if (!countdown || countdown.isExpired || instance.status !== 'running') return null;

  return (
    <Chip
      label={`⏱ ${countdown.remaining}`}
      size="small"
      color={countdown.isCritical ? 'error' : countdown.isWarning ? 'warning' : 'success'}
      sx={{ mt: 0.5 }}
    />
  );
};

export default ExpiryChip;
