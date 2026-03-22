import { useEffect, useState, useCallback, useRef } from 'react';
import {
  Box,
  Typography,
  CircularProgress,
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
} from '../../types';

const AUTO_REFRESH_INTERVAL = 30000;

const parseResource = (value: string): number => {
  if (value.endsWith('Gi')) return parseFloat(value) * 1024;
  if (value.endsWith('Mi')) return parseFloat(value);
  if (value.endsWith('m')) return parseFloat(value);
  return parseFloat(value);
};

const resourcePercent = (used: string, total: string): number => {
  const u = parseResource(used);
  const t = parseResource(total);
  if (t === 0) return 0;
  return Math.round((u / t) * 100);
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
      const [summaryData, nodesData, namespacesData] = await Promise.all([
        clusterService.getHealthSummary(selectedCluster),
        clusterService.getNodes(selectedCluster),
        clusterService.getNamespaces(selectedCluster),
      ]);
      setSummary(summaryData);
      setNodes(nodesData);
      setNamespaces(namespacesData);
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

      {loading && (
        <Box sx={{ display: 'flex', justifyContent: 'center', mt: 4 }}>
          <CircularProgress />
        </Box>
      )}

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
        </>
      )}
    </Box>
  );
};

export default ClusterHealth;
