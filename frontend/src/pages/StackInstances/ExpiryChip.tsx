import { Chip } from '@mui/material';
import useCountdown from '../../hooks/useCountdown';
import { formatStopTimeWhen, hasFixedStopTime } from '../../utils/stopTime';
import type { StackInstance } from '../../types';

interface ExpiryChipProps {
  instance: StackInstance;
}

const ExpiryChip = ({ instance }: ExpiryChipProps) => {
  const countdown = useCountdown(instance.expires_at);

  const isExpiredByTtl = instance.status === 'stopped' && (
    (instance.expires_at != null && new Date(instance.expires_at) <= new Date()) ||
    instance.error_message?.includes('Expired (TTL)')
  );

  if (isExpiredByTtl) {
    return <Chip label="Expired" color="error" size="small" sx={{ mt: 0.5 }} />;
  }

  if (!countdown || countdown.isExpired) return null;

  const color = countdown.isCritical ? 'error' : countdown.isWarning ? 'warning' : 'success';

  // A fixed stop time is exact also before the deploy, so show it for every
  // status except stopped.
  if (hasFixedStopTime(instance) && instance.status !== 'stopped') {
    return (
      <Chip
        label={`⏱ Stops at ${formatStopTimeWhen(new Date(instance.expires_at as string))}`}
        title={`Stops in ${countdown.remaining}`}
        size="small"
        color={instance.status === 'running' ? color : 'default'}
        sx={{ mt: 0.5 }}
      />
    );
  }

  if (instance.status !== 'running') return null;

  return (
    <Chip
      label={`⏱ ${countdown.remaining}`}
      size="small"
      color={color}
      sx={{ mt: 0.5 }}
    />
  );
};

export default ExpiryChip;
