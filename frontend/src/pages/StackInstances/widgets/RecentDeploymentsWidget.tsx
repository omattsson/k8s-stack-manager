import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
  Chip,
  Link as MuiLink,
} from '@mui/material';
import { Link } from 'react-router-dom';
import { timeAgo } from '../../../utils/timeAgo';
import type { DashboardDeployment } from '../../../types';

const deployStatusColor: Record<string, 'default' | 'info' | 'success' | 'error'> = {
  running: 'info',
  success: 'success',
  error: 'error',
};

interface Props {
  deployments: DashboardDeployment[];
}

const RecentDeploymentsWidget = ({ deployments }: Props) => {
  if (deployments.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        No recent deployments.
      </Typography>
    );
  }

  return (
    <TableContainer>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Instance</TableCell>
            <TableCell>Action</TableCell>
            <TableCell>Status</TableCell>
            <TableCell>Owner</TableCell>
            <TableCell>When</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {deployments.map((d) => (
            <TableRow key={d.id} hover>
              <TableCell>
                <MuiLink component={Link} to={`/stack-instances/${d.stack_instance_id}`} underline="hover">
                  {d.instance_name}
                </MuiLink>
              </TableCell>
              <TableCell>
                <Chip label={d.action} size="small" variant="outlined" />
              </TableCell>
              <TableCell>
                <Chip label={d.status} size="small" color={deployStatusColor[d.status] || 'default'} />
              </TableCell>
              <TableCell>
                <Typography variant="body2" noWrap>{d.owner_username || '-'}</Typography>
              </TableCell>
              <TableCell>
                <Typography variant="body2" color="text.secondary" noWrap>
                  {timeAgo(d.started_at)}
                </Typography>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
};

export default RecentDeploymentsWidget;
