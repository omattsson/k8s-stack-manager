import { Box, Typography, Paper, Chip, LinearProgress, Table, TableBody, TableCell, TableContainer, TableHead, TableRow } from '@mui/material';
import type { NamespaceStatus } from '../../types';

interface PodStatusDisplayProps {
  status: NamespaceStatus | null;
  loading?: boolean;
}

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

  const podPhaseColor = (phase: string) => {
    switch (phase) {
      case 'Running': return 'success';
      case 'Pending': return 'warning';
      case 'Failed': return 'error';
      case 'Succeeded': return 'info';
      default: return 'default';
    }
  };

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
                    <TableRow key={pod.name}>
                      <TableCell>
                        <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}>
                          {pod.name}
                        </Typography>
                      </TableCell>
                      <TableCell>
                        <Chip label={pod.phase} size="small" color={podPhaseColor(pod.phase) as 'success' | 'warning' | 'error' | 'info' | 'default'} />
                      </TableCell>
                      <TableCell>{pod.ready ? 'Yes' : 'No'}</TableCell>
                      <TableCell>
                        <Typography
                          variant="body2"
                          color={pod.restart_count > 5 ? 'error' : 'text.primary'}
                        >
                          {pod.restart_count}
                        </Typography>
                      </TableCell>
                      <TableCell>
                        <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}>
                          {pod.image}
                        </Typography>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </Paper>
      ))}
    </Box>
  );
};

export default PodStatusDisplay;
