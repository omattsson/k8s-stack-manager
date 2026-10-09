import {
  Box,
  Chip,
  List,
  ListItem,
  ListItemText,
  Typography,
  Link as MuiLink,
} from '@mui/material';
import { Link } from 'react-router-dom';
import useCountdown from '../../../hooks/useCountdown';
import ExtendTtlMenu from '../../../components/ExtendTtlMenu';
import { useAuth } from '../../../context/AuthContext';
import { canModifyInstance } from '../../../utils/roles';
import type { DashboardExpiring } from '../../../types';

interface ExpiringRowProps {
  item: DashboardExpiring;
  onExtended: (id: string, newExpiresAt: string) => void;
}

const ExpiringRow = ({ item, onExtended }: ExpiringRowProps) => {
  const countdown = useCountdown(item.expires_at);
  const { user } = useAuth();
  // Only the owner, admin or devops may extend. When the backend does not send
  // owner_id, keep the button: the backend still enforces the rule.
  const canExtend = item.owner_id === undefined || canModifyInstance(user, { owner_id: item.owner_id });

  return (
    <ListItem
      disablePadding
      sx={{ py: 0.5, display: 'flex', justifyContent: 'space-between' }}
    >
      <ListItemText
        primary={
          <MuiLink component={Link} to={`/stack-instances/${item.id}`} underline="hover">
            {item.name}
          </MuiLink>
        }
        secondary={item.namespace}
        slotProps={{ primary: { variant: 'body2' }, secondary: { variant: 'caption' } }}
      />
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexShrink: 0 }}>
        {countdown && !countdown.isExpired && (
          <Chip
            label={countdown.remaining}
            size="small"
            color={countdown.isCritical ? 'error' : countdown.isWarning ? 'warning' : 'success'}
          />
        )}
        {canExtend && (
          <ExtendTtlMenu
            instanceId={item.id}
            instanceName={item.name}
            label="Extend TTL"
            onExtended={(updated) => onExtended(item.id, updated.expires_at ?? '')}
          />
        )}
      </Box>
    </ListItem>
  );
};

interface Props {
  instances: DashboardExpiring[];
  onExtended: (id: string, newExpiresAt: string) => void;
}

const ExpiringSoonWidget = ({ instances, onExtended }: Props) => {
  if (instances.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        No instances expiring soon.
      </Typography>
    );
  }

  return (
    <List dense disablePadding>
      {instances.map((item) => (
        <ExpiringRow key={item.id} item={item} onExtended={onExtended} />
      ))}
    </List>
  );
};

export default ExpiringSoonWidget;
