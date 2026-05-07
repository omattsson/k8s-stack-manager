import { Box, Card, CardContent, Chip, Typography } from '@mui/material';
import type { DashboardCluster } from '../../../types';

const healthColor: Record<string, 'success' | 'warning' | 'error' | 'default'> = {
  healthy: 'success',
  degraded: 'warning',
  unreachable: 'error',
};

interface Props {
  clusters: DashboardCluster[];
}

const ClusterHealthWidget = ({ clusters }: Props) => {
  if (clusters.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        No clusters registered.
      </Typography>
    );
  }

  return (
    <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap' }}>
      {clusters.map((cl) => (
        <Card key={cl.id} variant="outlined" sx={{ minWidth: 200, flex: '1 1 200px', maxWidth: 320 }}>
          <CardContent sx={{ pb: '12px !important' }}>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
              <Typography variant="subtitle2" noWrap>{cl.name}</Typography>
              <Chip
                label={cl.health_status || 'unknown'}
                color={healthColor[cl.health_status] ?? 'default'}
                size="small"
              />
            </Box>
            {cl.node_count != null && (
              <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.25 }}>
                <Typography variant="caption" color="text.secondary">
                  Nodes: {cl.ready_node_count ?? '?'}/{cl.node_count}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  CPU: {cl.allocatable_cpu ?? '?'} allocatable / {cl.total_cpu ?? '?'} total
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  Memory: {cl.allocatable_memory ?? '?'} / {cl.total_memory ?? '?'}
                </Typography>
                {cl.namespace_count != null && (
                  <Typography variant="caption" color="text.secondary">
                    Namespaces: {cl.namespace_count}
                  </Typography>
                )}
              </Box>
            )}
          </CardContent>
        </Card>
      ))}
    </Box>
  );
};

export default ClusterHealthWidget;
