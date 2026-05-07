import {
  List,
  ListItem,
  ListItemText,
  Typography,
  Link as MuiLink,
} from '@mui/material';
import { Link } from 'react-router-dom';
import { timeAgo } from '../../../utils/timeAgo';
import type { DashboardFailing } from '../../../types';

interface Props {
  instances: DashboardFailing[];
}

const FailingInstancesWidget = ({ instances }: Props) => {
  if (instances.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        No failing instances.
      </Typography>
    );
  }

  return (
    <List dense disablePadding>
      {instances.map((inst) => (
        <ListItem key={inst.id} disablePadding sx={{ py: 0.5 }}>
          <ListItemText
            primary={
              <MuiLink component={Link} to={`/stack-instances/${inst.id}`} underline="hover">
                {inst.name}
              </MuiLink>
            }
            secondary={
              <>
                {inst.error_message
                  ? inst.error_message.length > 120
                    ? inst.error_message.slice(0, 120) + '...'
                    : inst.error_message
                  : 'Unknown error'}
                {' — '}
                {timeAgo(inst.updated_at)}
              </>
            }
            slotProps={{
              primary: { variant: 'body2' },
              secondary: { variant: 'caption', color: 'error' },
            }}
          />
        </ListItem>
      ))}
    </List>
  );
};

export default FailingInstancesWidget;
