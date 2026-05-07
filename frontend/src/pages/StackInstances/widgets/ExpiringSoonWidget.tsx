import { useState } from 'react';
import {
  Box,
  Button,
  Chip,
  List,
  ListItem,
  ListItemText,
  Typography,
  Link as MuiLink,
  CircularProgress,
} from '@mui/material';
import { Link } from 'react-router-dom';
import useCountdown from '../../../hooks/useCountdown';
import { instanceService } from '../../../api/client';
import type { DashboardExpiring } from '../../../types';

interface ExpiringRowProps {
  item: DashboardExpiring;
  onExtended: (id: string, newExpiresAt: string) => void;
}

const ExpiringRow = ({ item, onExtended }: ExpiringRowProps) => {
  const [extending, setExtending] = useState(false);
  const countdown = useCountdown(item.expires_at);

  const handleExtend = async () => {
    setExtending(true);
    try {
      const updated = await instanceService.extend(item.id);
      onExtended(item.id, updated.expires_at ?? '');
    } catch {
      // notification handled by caller
    } finally {
      setExtending(false);
    }
  };

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
        <Button
          size="small"
          variant="outlined"
          onClick={handleExtend}
          disabled={extending}
          sx={{ minWidth: 'auto', whiteSpace: 'nowrap' }}
        >
          {extending ? <CircularProgress size={16} /> : 'Extend TTL'}
        </Button>
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
