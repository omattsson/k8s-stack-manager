import { useState } from 'react';
import {
  Box,
  Typography,
  Paper,
  Chip,
  LinearProgress,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Collapse,
  IconButton,
  Alert,
} from '@mui/material';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import type { NamespaceStatus, ContainerStateInfo, PodInfo } from '../../types';

interface PodStatusDisplayProps {
  status: NamespaceStatus | null;
  loading?: boolean;
}

const getContainerStateColor = (state: ContainerStateInfo): 'success' | 'warning' | 'error' | 'default' => {
  if (state.state === 'running' && state.ready) return 'success';
  if (state.state === 'waiting') return 'warning';
  if (state.state === 'terminated' && state.reason === 'Completed') return 'default';
  if (state.state === 'terminated') return 'error';
  return 'default';
};

const hasContainerDetails = (pod: PodInfo): boolean => {
  if (pod.restart_count > 0) return true;
  if (!pod.container_states || pod.container_states.length === 0) return false;
  return pod.container_states.some(
    (cs) => cs.reason || cs.message || cs.exit_code !== undefined || cs.state !== 'running'
  );
};

interface PodRowProps {
  pod: PodInfo;
  podPhaseColor: (phase: string) => 'success' | 'warning' | 'error' | 'info' | 'default';
}

const PodRow = ({ pod, podPhaseColor }: PodRowProps) => {
  const [open, setOpen] = useState(false);
  const expandable = hasContainerDetails(pod);

  return (
    <>
      <TableRow>
        <TableCell sx={{ borderBottom: expandable && open ? 'none' : undefined }}>
          <Box sx={{ display: 'flex', alignItems: 'center' }}>
            {expandable && (
              <IconButton
                aria-label={open ? 'Collapse container details' : 'Expand container details'}
                size="small"
                onClick={() => setOpen(!open)}
                sx={{ mr: 0.5 }}
              >
                {open ? <KeyboardArrowUpIcon fontSize="small" /> : <KeyboardArrowDownIcon fontSize="small" />}
              </IconButton>
            )}
            <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}>
              {pod.name}
            </Typography>
          </Box>
        </TableCell>
        <TableCell sx={{ borderBottom: expandable && open ? 'none' : undefined }}>
          <Chip label={pod.phase} size="small" color={podPhaseColor(pod.phase)} />
        </TableCell>
        <TableCell sx={{ borderBottom: expandable && open ? 'none' : undefined }}>
          {pod.ready ? 'Yes' : 'No'}
        </TableCell>
        <TableCell sx={{ borderBottom: expandable && open ? 'none' : undefined }}>
          <Typography
            variant="body2"
            color={pod.restart_count > 5 ? 'error' : 'text.primary'}
          >
            {pod.restart_count}
          </Typography>
        </TableCell>
        <TableCell sx={{ borderBottom: expandable && open ? 'none' : undefined }}>
          <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}>
            {pod.image}
          </Typography>
        </TableCell>
      </TableRow>
      {expandable && (
        <TableRow>
          <TableCell colSpan={5} sx={{ py: 0, px: 2 }}>
            <Collapse in={open} timeout="auto" unmountOnExit>
              <Box sx={{ py: 1, pl: 4 }}>
                <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, mb: 0.5, display: 'block' }}>
                  Container States
                </Typography>
                {(pod.container_states || []).map((cs) => (
                  <Box key={cs.name} sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 0.5, flexWrap: 'wrap' }}>
                    <Typography variant="caption" sx={{ fontFamily: 'monospace', minWidth: 100 }}>
                      {cs.name}
                    </Typography>
                    <Chip
                      label={cs.state}
                      size="small"
                      color={getContainerStateColor(cs)}
                      variant="outlined"
                      sx={{ height: 20, fontSize: '0.7rem' }}
                    />
                    {cs.reason && (
                      <Typography variant="caption" color="warning.main" sx={{ fontWeight: 600 }}>
                        {cs.reason}
                      </Typography>
                    )}
                    {cs.message && (
                      <Typography variant="caption" color="text.secondary" sx={{ fontStyle: 'italic' }}>
                        {cs.message}
                      </Typography>
                    )}
                    {cs.state === 'terminated' && cs.exit_code !== undefined && (
                      <Typography variant="caption" color="text.secondary">
                        (exit code: {cs.exit_code})
                      </Typography>
                    )}
                    {cs.restart_count > 0 && (
                      <Typography variant="caption" color={cs.restart_count > 5 ? 'error.main' : 'text.secondary'}>
                        {cs.restart_count} restart{cs.restart_count === 1 ? '' : 's'}
                      </Typography>
                    )}
                  </Box>
                ))}
              </Box>
            </Collapse>
          </TableCell>
        </TableRow>
      )}
    </>
  );
};

const PodStatusDisplay = ({ status, loading }: PodStatusDisplayProps) => {
  if (loading) return <LinearProgress />;
  if (!status) return <Typography variant="body2" color="text.secondary">No status available</Typography>;

  const chartStatusColor = (s: string) => {
    switch (s) {
      case 'healthy': return 'success';
      case 'progressing': return 'info';
      case 'degraded': return 'warning';
      case 'error': return 'error';
      default: return 'default';
    }
  };

  const podPhaseColor = (phase: string): 'success' | 'warning' | 'error' | 'info' | 'default' => {
    switch (phase) {
      case 'Running': return 'success';
      case 'Pending': return 'warning';
      case 'Failed': return 'error';
      case 'Succeeded': return 'info';
      default: return 'default';
    }
  };

  const warningEvents = (status.events || [])
    .filter((e) => e.type === 'Warning')
    .sort((a, b) => new Date(b.last_seen).getTime() - new Date(a.last_seen).getTime());

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 2 }}>
        <Typography variant="subtitle2">Cluster Status:</Typography>
        <Chip label={status.status} size="small" color={chartStatusColor(status.status)} />
        <Typography variant="caption" color="text.secondary" sx={{ ml: 'auto' }}>
          Last checked: {new Date(status.last_checked).toLocaleString()}
        </Typography>
      </Box>

      {(status.charts || []).map((chart) => (
        <Paper key={chart.release_name} sx={{ p: 2, mb: 2 }} variant="outlined">
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
            <Typography variant="subtitle2">{chart.chart_name}</Typography>
            <Chip label={chart.status} size="small" color={chartStatusColor(chart.status)} />
          </Box>

          {(chart.deployments || []).length > 0 && (
            <Box sx={{ mb: 1 }}>
              <Typography variant="caption" color="text.secondary">Deployments</Typography>
              {(chart.deployments || []).map((dep) => (
                <Box key={dep.name} sx={{ display: 'flex', alignItems: 'center', gap: 1, ml: 1 }}>
                  <Typography variant="body2">{dep.name}</Typography>
                  <Chip
                    label={`${dep.ready_replicas}/${dep.desired_replicas} ready`}
                    size="small"
                    color={dep.available ? 'success' : 'warning'}
                    variant="outlined"
                  />
                </Box>
              ))}
            </Box>
          )}

          {(chart.pods || []).length > 0 && (
            <TableContainer>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Pod</TableCell>
                    <TableCell>Status</TableCell>
                    <TableCell>Ready</TableCell>
                    <TableCell>Restarts</TableCell>
                    <TableCell>Image</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {(chart.pods || []).map((pod) => (
                    <PodRow key={pod.name} pod={pod} podPhaseColor={podPhaseColor} />
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </Paper>
      ))}

      {warningEvents.length > 0 && (
        <Box sx={{ mt: 2 }}>
          <Typography variant="subtitle2" gutterBottom>Recent Warnings</Typography>
          {warningEvents.slice(0, 10).map((event) => (
            <Alert key={`${event.object}-${event.reason}-${event.last_seen}`} severity="warning" sx={{ mb: 0.5, py: 0, '& .MuiAlert-message': { py: 0.5 } }}>
              <Typography variant="caption" sx={{ fontWeight: 600 }}>{event.reason}</Typography>
              {' \u2014 '}
              <Typography variant="caption">{event.message}</Typography>
              <Typography variant="caption" color="text.secondary" sx={{ ml: 1 }}>
                ({event.object}, x{event.count})
              </Typography>
            </Alert>
          ))}
        </Box>
      )}
    </Box>
  );
};

export default PodStatusDisplay;
