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

const AUTO_REFRESH_INTERVAL = 30000;

const parseResource = (value: string): number => {
  if (!value) return 0;
  if (value.endsWith('Gi')) return Number.parseFloat(value) * 1024;
  if (value.endsWith('Mi')) return Number.parseFloat(value);
  if (value.endsWith('m')) return Number.parseFloat(value);
  const n = Number.parseFloat(value);
  return Number.isFinite(n) ? n : 0;
};

const resourcePercent = (used: string, total: string): number => {
  if (!used || !total) return 0;
  const u = parseResource(used);
  const t = parseResource(total);
  if (t === 0) return 0;
  return Math.round((u / t) * 100);
};

const hasQuota = (used: string, limit: string): boolean => !!(used || limit);

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
              <Card>
                <CardContent>
                  <Typography color="text.secondary" gutterBottom>
                    CPU
                  </Typography>
                  <Typography variant="h5">
                    {summary.allocatable_cpu} / {summary.total_cpu}
                  </Typography>
                  <LinearProgress
                    variant="determinate"
                    value={resourcePercent(summary.allocatable_cpu, summary.total_cpu)}
                    sx={{ mt: 1 }}
                  />
                </CardContent>
              </Card>
            </Grid>
            <Grid size={{ xs: 12, sm: 6, md: 3 }}>
              <Card>
                <CardContent>
                  <Typography color="text.secondary" gutterBottom>
                    Memory
                  </Typography>
                  <Typography variant="h5">
                    {summary.allocatable_memory} / {summary.total_memory}
                  </Typography>
                  <LinearProgress
                    variant="determinate"
                    value={resourcePercent(summary.allocatable_memory, summary.total_memory)}
                    sx={{ mt: 1 }}
                  />
                </CardContent>
              </Card>
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
                    <TableCell>{node.capacity.cpu}</TableCell>
                    <TableCell>{node.capacity.memory}</TableCell>
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
                      <TableCell>CPU Usage</TableCell>
                      <TableCell>Memory Usage</TableCell>
                      <TableCell>Pods</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {utilization.map((ns) => {
                      const hasCpuQuota = hasQuota(ns.cpu_used, ns.cpu_limit);
                      const hasMemQuota = hasQuota(ns.memory_used, ns.memory_limit);
                      const cpuPercent = resourcePercent(ns.cpu_used, ns.cpu_limit);
                      const memPercent = resourcePercent(ns.memory_used, ns.memory_limit);
                      const podPercent = ns.pod_limit > 0 ? Math.round((ns.pod_count / ns.pod_limit) * 100) : 0;
                      return (
                        <TableRow key={ns.namespace}>
                          <TableCell>
                            <Typography variant="body2" fontWeight="medium">
                              {ns.namespace}
                            </Typography>
                          </TableCell>
                          <TableCell>
                            {hasCpuQuota ? (
                              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                <Box sx={{ flexGrow: 1, minWidth: 80 }}>
                                  <LinearProgress
                                    variant="determinate"
                                    value={Math.min(cpuPercent, 100)}
                                    color={cpuPercent > 90 ? 'error' : cpuPercent > 70 ? 'warning' : 'success'}
                                  />
                                </Box>
                                <Typography variant="body2" sx={{ minWidth: 110, textAlign: 'right' }}>
                                  {ns.cpu_used} / {ns.cpu_limit} ({cpuPercent}%)
                                </Typography>
                              </Box>
                            ) : (
                              <Typography variant="body2" color="text.secondary">No quota</Typography>
                            )}
                          </TableCell>
                          <TableCell>
                            {hasMemQuota ? (
                              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                <Box sx={{ flexGrow: 1, minWidth: 80 }}>
                                  <LinearProgress
                                    variant="determinate"
                                    value={Math.min(memPercent, 100)}
                                    color={memPercent > 90 ? 'error' : memPercent > 70 ? 'warning' : 'success'}
                                  />
                                </Box>
                                <Typography variant="body2" sx={{ minWidth: 130, textAlign: 'right' }}>
                                  {ns.memory_used} / {ns.memory_limit} ({memPercent}%)
                                </Typography>
                              </Box>
                            ) : (
                              <Typography variant="body2" color="text.secondary">No quota</Typography>
                            )}
                          </TableCell>
                          <TableCell>
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                              {ns.pod_limit > 0 ? (
                                <>
                                  <Box sx={{ flexGrow: 1, minWidth: 60 }}>
                                    <LinearProgress
                                      variant="determinate"
                                      value={Math.min(podPercent, 100)}
                                      color={podPercent > 90 ? 'error' : podPercent > 70 ? 'warning' : 'success'}
                                    />
                                  </Box>
                                  <Typography variant="body2" sx={{ minWidth: 70, textAlign: 'right' }}>
                                    {ns.pod_count} / {ns.pod_limit}
                                  </Typography>
                                </>
                              ) : (
                                <Typography variant="body2">
                                  {ns.pod_count} (no limit)
                                </Typography>
                              )}
                            </Box>
                          </TableCell>
                        </TableRow>
                      );
                    })}
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
