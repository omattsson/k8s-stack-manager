import { Chip } from '@mui/material';
import AccessTimeIcon from '@mui/icons-material/AccessTime';
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
  // The same statuses as the countdown of the stack page: the releases run, so
  // the expiry stops them.
  const isDeployed = instance.status === 'running' || instance.status === 'partial';

  // A fixed stop time is exact also before the deploy, so show it for every
  // status except stopped.
  if (hasFixedStopTime(instance) && instance.status !== 'stopped') {
    return (
      <Chip
        label={`Stops at ${formatStopTimeWhen(new Date(instance.expires_at as string))}`}
        icon={<AccessTimeIcon />}
        title={`Stops in ${countdown.remaining}`}
        size="small"
        color={isDeployed ? color : 'default'}
        sx={{ mt: 0.5 }}
      />
    );
  }

  if (!isDeployed) return null;

  return (
    <Chip
      label={`Expires in ${countdown.remaining}`}
      icon={<AccessTimeIcon />}
      size="small"
      color={color}
      sx={{ mt: 0.5 }}
    />
  );
};

export default ExpiryChip;
