import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
  Link as MuiLink,
} from '@mui/material';
import { Link } from 'react-router-dom';
import { timeAgo } from '../../../utils/timeAgo';
import StatusBadge from '../../../components/StatusBadge';
import type { DashboardDeployment } from '../../../types';

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
            <TableCell>User</TableCell>
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
                <StatusBadge status={d.action} />
              </TableCell>
              <TableCell>
                <StatusBadge status={d.status} />
              </TableCell>
              <TableCell>
                <Typography variant="body2" noWrap>{d.username || '-'}</Typography>
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
