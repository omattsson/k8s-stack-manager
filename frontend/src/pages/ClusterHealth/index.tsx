import { useEffect, useState, useCallback, useRef } from 'react';
import {
  Box,
  Typography,
  Alert,
  Card,
  CardContent,
  Grid,
  Chip,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  FormControl,
  InputLabel,
  Select,
  MenuItem,
  FormControlLabel,
  Switch,
  LinearProgress,
} from '@mui/material';
import type { SelectChangeEvent } from '@mui/material';
import { clusterService } from '../../api/client';
import type {
  Cluster,
  ClusterSummary,
  NodeStatusInfo,
  ClusterNamespaceInfo,
  NamespaceResourceUsage,
} from '../../types';
import LoadingState from '../../components/LoadingState';
import { formatCpuCores, formatMemoryQuantity, quantityPercent } from '../../utils/quantity';

const AUTO_REFRESH_INTERVAL = 30000;

const ResourceBar = ({ percent, used, limit, minLabelWidth }: { percent: number; used: string; limit: string; minLabelWidth: number }) => (
  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
    <Box sx={{ flexGrow: 1, minWidth: 80 }}>
      <LinearProgress variant="determinate" value={Math.min(percent, 100)} color={getUsageColor(percent)} />
    </Box>
    <Typography variant="body2" sx={{ minWidth: minLabelWidth, textAlign: 'right' }}>
      {used} / {limit} ({percent}%)
    </Typography>
  </Box>
);

interface SummaryResourceCardProps {
  title: string;
  requested?: string;
  allocatable: string;
  capacity: string;
  format: (quantity: string) => string;
}

/**
 * Summary card for one resource. It shows the sum of the pod requests against
 * the allocatable amount. Requests are not real use. When the API sends no
 * requested value, the card shows only the allocatable amount and no bar.
 */
const SummaryResourceCard = ({ title, requested, allocatable, capacity, format }: SummaryResourceCardProps) => {
  const percent = requested ? quantityPercent(requested, allocatable) : 0;
  return (
    <Card>
      <CardContent>
        <Typography color="text.secondary" gutterBottom>
          {title}
        </Typography>
        <Typography variant="h5">
          {requested ? `${format(requested)} / ${format(allocatable)}` : format(allocatable)}
        </Typography>
        {requested && (
          <LinearProgress
            variant="determinate"
            value={Math.min(percent, 100)}
            color={getUsageColor(percent)}
            aria-label={`${title} requested ${percent}% of allocatable`}
            sx={{ mt: 1 }}
          />
        )}
        <Typography variant="caption" color="text.secondary">
          {requested
            ? `Requested ${percent}% of allocatable. Capacity ${format(capacity)}.`
            : `Allocatable. Capacity ${format(capacity)}.`}
        </Typography>
      </CardContent>
    </Card>
  );
};

const NamespaceUsageRow = ({ ns }: { ns: NamespaceResourceUsage }) => {
  const hasCpuQuota = hasQuota(ns.cpu_used, ns.cpu_limit);
  const hasMemQuota = hasQuota(ns.memory_used, ns.memory_limit);
  const cpuPercent = quantityPercent(ns.cpu_used, ns.cpu_limit);
  const memPercent = quantityPercent(ns.memory_used, ns.memory_limit);
  const podPercent = ns.pod_limit > 0 ? Math.round((ns.pod_count / ns.pod_limit) * 100) : 0;

  return (
    <TableRow>
      <TableCell>
        <Typography variant="body2" sx={{ fontWeight: 'medium' }}>{ns.namespace}</Typography>
      </TableCell>
      <TableCell>
        {hasCpuQuota
          ? <ResourceBar percent={cpuPercent} used={formatCpuCores(ns.cpu_used || '0')} limit={ns.cpu_limit ? `${formatCpuCores(ns.cpu_limit)} cores` : 'no limit'} minLabelWidth={140} />
          : <Typography variant="body2" color="text.secondary">No quota</Typography>}
      </TableCell>
      <TableCell>
        {hasMemQuota
          ? <ResourceBar percent={memPercent} used={formatMemoryQuantity(ns.memory_used || '0')} limit={ns.memory_limit ? formatMemoryQuantity(ns.memory_limit) : 'no limit'} minLabelWidth={160} />
          : <Typography variant="body2" color="text.secondary">No quota</Typography>}
      </TableCell>
      <TableCell>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          {ns.pod_limit > 0 ? (
            <>
              <Box sx={{ flexGrow: 1, minWidth: 60 }}>
                <LinearProgress variant="determinate" value={Math.min(podPercent, 100)} color={getUsageColor(podPercent)} />
              </Box>
              <Typography variant="body2" sx={{ minWidth: 70, textAlign: 'right' }}>
                {ns.pod_count} / {ns.pod_limit}
              </Typography>
            </>
          ) : (
            <Typography variant="body2">{ns.pod_count} (no limit)</Typography>
          )}
        </Box>
      </TableCell>
    </TableRow>
  );
};

const hasQuota = (used: string, limit: string): boolean => !!(used || limit);

const getUsageColor = (percent: number): 'error' | 'warning' | 'success' => {
  if (percent > 90) return 'error';
  if (percent > 70) return 'warning';
  return 'success';
};

const nodeHealthColor = (ready: number, total: number): 'success' | 'warning' | 'error' => {
  if (total === 0) return 'error';
  if (ready === total) return 'success';
  if (ready > 0) return 'warning';
  return 'error';
};

const ClusterHealth = () => {
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [selectedCluster, setSelectedCluster] = useState<string>('');
  const [summary, setSummary] = useState<ClusterSummary | null>(null);
  const [nodes, setNodes] = useState<NodeStatusInfo[]>([]);
  const [namespaces, setNamespaces] = useState<ClusterNamespaceInfo[]>([]);
  const [utilization, setUtilization] = useState<NamespaceResourceUsage[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(false);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Fetch cluster list
  useEffect(() => {
    const fetchClusters = async () => {
      try {
        const data = await clusterService.list();
        setClusters(data);
        if (data.length > 0) {
          const defaultCluster = data.find((c) => c.is_default);
          setSelectedCluster(defaultCluster ? defaultCluster.id : data[0].id);
        } else {
          setLoading(false);
        }
      } catch {
        setError('Failed to load clusters');
        setLoading(false);
      }
    };
    fetchClusters();
  }, []);

  // Fetch health data when cluster changes
  const fetchHealthData = useCallback(async () => {
    if (!selectedCluster) return;
    setLoading(true);
    setError(null);
    try {
      const [summaryData, nodesData, namespacesData, utilizationData] = await Promise.all([
        clusterService.getHealthSummary(selectedCluster),
        clusterService.getNodes(selectedCluster),
        clusterService.getNamespaces(selectedCluster),
        clusterService.getUtilization(selectedCluster).catch(() => ({ namespaces: [] })),
      ]);
      setSummary(summaryData);
      setNodes(nodesData ?? []);
      setNamespaces(namespacesData ?? []);
      setUtilization(utilizationData.namespaces ?? []);
    } catch {
      setError('Failed to load cluster health data');
    } finally {
      setLoading(false);
    }
  }, [selectedCluster]);

  useEffect(() => {
    fetchHealthData();
  }, [fetchHealthData]);

  // Auto-refresh
  useEffect(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
    if (autoRefresh && selectedCluster) {
      intervalRef.current = setInterval(fetchHealthData, AUTO_REFRESH_INTERVAL);
    }
    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
      }
    };
  }, [autoRefresh, selectedCluster, fetchHealthData]);

  const handleClusterChange = (event: SelectChangeEvent<string>) => {
    setSelectedCluster(event.target.value);
  };

  const formatDate = (dateStr: string): string => {
    try {
      return new Date(dateStr).toLocaleString();
    } catch {
      return dateStr;
    }
  };

  const getNodeConditionChips = (node: NodeStatusInfo) => {
    const warnings = node.conditions.filter(
      (c) => c.type !== 'Ready' && c.status === 'True',
    );
    if (warnings.length === 0) {
      return <Chip label="All OK" size="small" color="success" variant="outlined" />;
    }
    return (
      <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap' }}>
        {warnings.map((c) => (
          <Chip key={c.type} label={c.type} size="small" color="warning" />
        ))}
      </Box>
    );
  };

  if (clusters.length === 0 && !loading && !error) {
    return (
      <Box>
        <Typography variant="h4" component="h1" gutterBottom>
          Cluster Health
        </Typography>
        <Alert severity="info">No clusters registered. Add a cluster first.</Alert>
      </Box>
    );
  }

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Cluster Health
        </Typography>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
          <FormControlLabel
            control={
              <Switch
                checked={autoRefresh}
                onChange={(e) => setAutoRefresh(e.target.checked)}
              />
            }
            label="Auto-refresh"
          />
          {clusters.length > 0 && (
            <FormControl size="small" sx={{ minWidth: 200 }}>
              <InputLabel id="cluster-select-label">Cluster</InputLabel>
              <Select
                labelId="cluster-select-label"
                value={selectedCluster}
                label="Cluster"
                onChange={handleClusterChange}
              >
                {clusters.map((c) => (
                  <MenuItem key={c.id} value={c.id}>
                    {c.name}{c.is_default ? ' (default)' : ''}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          )}
        </Box>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading && <LoadingState label="Loading health data..." />}

      {!loading && summary && (
        <>
          {/* Summary Cards */}
          <Grid container spacing={2} sx={{ mb: 3 }}>
            <Grid size={{ xs: 12, sm: 6, md: 3 }}>
              <Card>
                <CardContent>
                  <Typography color="text.secondary" gutterBottom>
                    Nodes
                  </Typography>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                    <Typography variant="h5">
                      {summary.ready_node_count} / {summary.node_count}
                    </Typography>
                    <Chip
                      label={summary.ready_node_count === summary.node_count ? 'Healthy' : 'Degraded'}
                      size="small"
                      color={nodeHealthColor(summary.ready_node_count, summary.node_count)}
                    />
                  </Box>
                </CardContent>
              </Card>
            </Grid>
            <Grid size={{ xs: 12, sm: 6, md: 3 }}>
              <SummaryResourceCard
                title="CPU"
                requested={summary.requested_cpu}
                allocatable={summary.allocatable_cpu}
                capacity={summary.total_cpu}
                format={(v) => `${formatCpuCores(v)} cores`}
              />
            </Grid>
            <Grid size={{ xs: 12, sm: 6, md: 3 }}>
              <SummaryResourceCard
                title="Memory"
                requested={summary.requested_memory}
                allocatable={summary.allocatable_memory}
                capacity={summary.total_memory}
                format={formatMemoryQuantity}
              />
            </Grid>
            <Grid size={{ xs: 12, sm: 6, md: 3 }}>
              <Card>
                <CardContent>
                  <Typography color="text.secondary" gutterBottom>
                    Namespaces
                  </Typography>
                  <Typography variant="h5">
                    {summary.namespace_count}
                  </Typography>
                </CardContent>
              </Card>
            </Grid>
          </Grid>

          {/* Nodes Table */}
          <Typography variant="h6" sx={{ mb: 1 }}>
            Nodes
          </Typography>
          <TableContainer component={Paper} sx={{ mb: 3 }}>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>Status</TableCell>
                  <TableCell>CPU Capacity</TableCell>
                  <TableCell>Memory Capacity</TableCell>
                  <TableCell>Pods</TableCell>
                  <TableCell>Conditions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {nodes.map((node) => (
                  <TableRow key={node.name}>
                    <TableCell>{node.name}</TableCell>
                    <TableCell>
                      <Chip
                        label={node.status}
                        size="small"
                        color={node.status === 'Ready' ? 'success' : 'error'}
                      />
                    </TableCell>
                    <TableCell>{formatCpuCores(node.capacity.cpu)} cores</TableCell>
                    <TableCell>{formatMemoryQuantity(node.capacity.memory)}</TableCell>
                    <TableCell>{node.pod_count}</TableCell>
                    <TableCell>{getNodeConditionChips(node)}</TableCell>
                  </TableRow>
                ))}
                {nodes.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} align="center">
                      No nodes found
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </TableContainer>

          {/* Namespaces Table */}
          <Typography variant="h6" sx={{ mb: 1 }}>
            Namespaces
          </Typography>
          <TableContainer component={Paper}>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>Phase</TableCell>
                  <TableCell>Created At</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {namespaces.map((ns) => (
                  <TableRow key={ns.name}>
                    <TableCell>{ns.name}</TableCell>
                    <TableCell>
                      <Chip
                        label={ns.phase}
                        size="small"
                        color={ns.phase === 'Active' ? 'success' : 'default'}
                        variant="outlined"
                      />
                    </TableCell>
                    <TableCell>{formatDate(ns.created_at)}</TableCell>
                  </TableRow>
                ))}
                {namespaces.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={3} align="center">
                      No namespaces found
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </TableContainer>

          {/* Namespace Resource Usage */}
          {utilization.length > 0 && (
            <>
              <Typography variant="h6" sx={{ mb: 1, mt: 3 }}>
                Namespace Resource Usage
              </Typography>
              <TableContainer component={Paper}>
                <Table size="small">
                  <TableHead>
                    <TableRow>
                      <TableCell>Namespace</TableCell>
                      <TableCell>CPU Requests / Quota</TableCell>
                      <TableCell>Memory Requests / Quota</TableCell>
                      <TableCell>Pods</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {utilization.map((ns) => (
                      <NamespaceUsageRow key={ns.namespace} ns={ns} />
                    ))}
                  </TableBody>
                </Table>
              </TableContainer>
            </>
          )}
        </>
      )}
    </Box>
  );
};

export default ClusterHealth;
