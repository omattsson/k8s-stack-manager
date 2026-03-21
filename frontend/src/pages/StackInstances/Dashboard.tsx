import { useEffect, useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useWebSocket } from '../../hooks/useWebSocket';
import type { WsMessage } from '../../hooks/useWebSocket';
import {
  Box,
  Typography,
  Grid,
  Card,
  CardContent,
  CardActions,
  Button,
  CircularProgress,
  Alert,
  TextField,
  MenuItem,
  InputAdornment,
  Chip,
  Paper,
  Link,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import AddIcon from '@mui/icons-material/Add';
import StatusBadge from '../../components/StatusBadge';
import FavoriteButton from '../../components/FavoriteButton';
import ExpiryChip from './ExpiryChip';
import { instanceService, clusterService, favoriteService } from '../../api/client';
import type { StackInstance, Cluster, NamespaceStatus, UserFavorite } from '../../types';

const STATUSES = ['All', 'draft', 'deploying', 'running', 'stopped', 'error'];

const getPrimaryUrl = (status: NamespaceStatus): string | null => {
  // First ingress URL
  if (status.ingresses?.length) {
    return status.ingresses[0].url;
  }
  // First LoadBalancer external IP
  for (const chart of status.charts || []) {
    for (const svc of chart.services || []) {
      if (svc.type === 'LoadBalancer' && svc.external_ip) {
        const port = (svc.ports || [])[0]?.replace(/\/.*/, '') || '';
        return `http://${svc.external_ip}${port ? `:${port}` : ''}`;
      }
    }
  }
  return null;
};

const Dashboard = () => {
  const [instances, setInstances] = useState<StackInstance[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [favorites, setFavorites] = useState<UserFavorite[]>([]);
  const [recentInstances, setRecentInstances] = useState<StackInstance[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState('All');
  const [instanceUrls, setInstanceUrls] = useState<Record<string, string>>({});
  const navigate = useNavigate();

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [instData, clsData, favData, recentData] = await Promise.all([
          instanceService.list(),
          clusterService.list().catch(() => [] as Cluster[]),
          favoriteService.list().catch(() => [] as UserFavorite[]),
          instanceService.recent().catch(() => [] as StackInstance[]),
        ]);
        setInstances(instData || []);
        setClusters(clsData || []);
        setFavorites(favData || []);
        setRecentInstances(recentData || []);
      } catch {
        setError('Failed to load stack instances');
      } finally {
        setLoading(false);
      }
    };
    fetchData();
  }, []);

  // Phase 2: fetch status/URLs for running instances after the list is loaded
  useEffect(() => {
    const running = instances.filter(
      (i) => i.status === 'running' || i.status === 'deploying',
    );
    if (running.length === 0) return;
    Promise.allSettled(
      running.map(async (inst) => {
        const st: NamespaceStatus = await instanceService.getStatus(inst.id);
        const url = getPrimaryUrl(st);
        return { id: inst.id, url };
      }),
    ).then((settled) => {
      const urlMap: Record<string, string> = {};
      for (const r of settled) {
        if (r.status === 'fulfilled' && r.value.url) {
          urlMap[r.value.id] = r.value.url;
        }
      }
      setInstanceUrls(urlMap);
    });
  }, [instances]);

  // Live-update instance statuses via WebSocket.
  const handleWsMessage = useCallback((msg: WsMessage) => {
    if (msg.type === 'deployment.status') {
      const payload = msg.payload as { instance_id?: string; status?: string };
      if (!payload.instance_id || !payload.status) return;
      setInstances((prev) =>
        prev.map((inst) =>
          inst.id === payload.instance_id ? { ...inst, status: payload.status as string } : inst
        )
      );
    }
  }, []);

  useWebSocket(handleWsMessage);

  const clusterNameMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const c of clusters) {
      map.set(c.id, c.name);
    }
    return map;
  }, [clusters]);

  const filtered = instances.filter((inst) => {
    if (statusFilter !== 'All' && inst.status !== statusFilter) return false;
    if (search && !inst.name.toLowerCase().includes(search.toLowerCase())) return false;
    return true;
  });

  const favoriteInstanceIds = useMemo(() => {
    const ids = new Set<string>();
    for (const fav of favorites) {
      if (fav.entity_type === 'instance') ids.add(fav.entity_id);
    }
    return ids;
  }, [favorites]);

  const favoritedInstances = useMemo(() => {
    return instances.filter((inst) => favoriteInstanceIds.has(inst.id));
  }, [instances, favoriteInstanceIds]);

  if (loading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">{error}</Alert>;
  }

  return (
    <Box>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Stack Instances
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/stack-instances/new')}>
          Create Instance
        </Button>
      </Box>

      <Box sx={{ display: 'flex', gap: 2, mb: 3 }}>
        <TextField
          size="small"
          placeholder="Search instances..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          slotProps={{
            input: {
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon />
                </InputAdornment>
              ),
            },
          }}
          sx={{ minWidth: 250 }}
        />
        <TextField
          size="small"
          select
          label="Status"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          sx={{ minWidth: 150 }}
        >
          {STATUSES.map((s) => (
            <MenuItem key={s} value={s}>{s === 'All' ? 'All Statuses' : s}</MenuItem>
          ))}
        </TextField>
      </Box>

      {/* My Favorites section */}
      <Box sx={{ mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          My Favorites
        </Typography>
        {favoritedInstances.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            Star instances to add them here
          </Typography>
        ) : (
          <Box sx={{ display: 'flex', overflowX: 'auto', gap: 2, pb: 1 }}>
            {favoritedInstances.map((inst) => (
              <Card
                key={inst.id}
                variant="outlined"
                sx={{ minWidth: 200, maxWidth: 250, flexShrink: 0, cursor: 'pointer' }}
                onClick={() => navigate(`/stack-instances/${inst.id}`)}
              >
                <CardContent sx={{ py: 1.5, px: 2, '&:last-child': { pb: 1.5 } }}>
                  <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <Typography variant="subtitle2" noWrap sx={{ flex: 1 }}>
                      {inst.name}
                    </Typography>
                    <FavoriteButton entityType="instance" entityId={inst.id} size="small" initialFavorited={true} />
                  </Box>
                  <StatusBadge status={inst.status} />
                </CardContent>
              </Card>
            ))}
          </Box>
        )}
      </Box>

      {/* Recent Stacks section */}
      {recentInstances.length > 0 && (
        <Box sx={{ mb: 3 }}>
          <Typography variant="h6" gutterBottom>
            Recent Stacks
          </Typography>
          <Box sx={{ display: 'flex', overflowX: 'auto', gap: 2, pb: 1 }}>
            {recentInstances.map((inst) => (
              <Card
                key={inst.id}
                variant="outlined"
                sx={{ minWidth: 220, maxWidth: 280, flexShrink: 0, cursor: 'pointer' }}
                onClick={() => navigate(`/stack-instances/${inst.id}`)}
              >
                <CardContent sx={{ py: 1.5, px: 2, '&:last-child': { pb: 1.5 } }}>
                  <Typography variant="subtitle2" noWrap>
                    {inst.name}
                  </Typography>
                  {inst.definition && (
                    <Typography variant="caption" color="text.secondary" noWrap component="div">
                      {inst.definition.name}
                    </Typography>
                  )}
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 0.5 }}>
                    <StatusBadge status={inst.status} />
                    <Typography variant="caption" color="text.secondary">
                      {new Date(inst.updated_at).toLocaleDateString()}
                    </Typography>
                  </Box>
                </CardContent>
              </Card>
            ))}
          </Box>
        </Box>
      )}

      {clusters.length > 0 && (
        <Paper variant="outlined" sx={{ p: 1.5, mb: 3, display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
          <Typography variant="body2" color="text.secondary">
            {clusters.length} cluster{clusters.length !== 1 ? 's' : ''}:
          </Typography>
          {(['healthy', 'degraded', 'unreachable'] as const).map((status) => {
            const count = clusters.filter((c) => c.health_status === status).length;
            if (count === 0) return null;
            return (
              <Chip
                key={status}
                label={`${count} ${status}`}
                size="small"
                color={status === 'healthy' ? 'success' : status === 'degraded' ? 'warning' : 'error'}
                variant="outlined"
              />
            );
          })}
        </Paper>
      )}

      {filtered.length === 0 ? (
        <Box sx={{ textAlign: 'center', mt: 4 }}>
          <Typography color="text.secondary" gutterBottom>
            No stack instances found.
          </Typography>
          <Button variant="outlined" onClick={() => navigate('/stack-instances/new')}>
            Create your first instance
          </Button>
        </Box>
      ) : (
        <Grid container spacing={3}>
          {filtered.map((instance) => (
            <Grid key={instance.id} size={{ xs: 12, sm: 6, md: 4 }}>
              <Card sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
                <CardContent sx={{ flex: 1 }}>
                  <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, minWidth: 0 }}>
                      <FavoriteButton entityType="instance" entityId={instance.id} size="small" initialFavorited={favoriteInstanceIds.has(instance.id)} />
                      <Typography variant="h6" component="h2" noWrap>
                        {instance.name}
                      </Typography>
                    </Box>
                    <StatusBadge status={instance.status} />
                  </Box>
                  <Typography variant="body2" color="text.secondary">
                    Branch: {instance.branch}
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    Namespace: {instance.namespace}
                  </Typography>
                  {instance.definition && (
                    <Typography variant="body2" color="text.secondary">
                      Definition: {instance.definition.name}
                    </Typography>
                  )}
                  {instance.cluster_id && (() => {
                    const clusterName = clusterNameMap.get(instance.cluster_id);
                    return clusterName ? (
                      <Typography variant="body2" color="text.secondary">
                        Cluster: {clusterName}
                      </Typography>
                    ) : null;
                  })()}
                  {instanceUrls[instance.id] && (
                    <Link
                      href={instanceUrls[instance.id]}
                      target="_blank"
                      rel="noopener noreferrer"
                      variant="body2"
                      sx={{ display: 'block', mt: 0.5, fontFamily: 'monospace', fontSize: '0.75rem' }}
                      noWrap
                    >
                      {instanceUrls[instance.id]}
                    </Link>
                  )}
                  <ExpiryChip instance={instance} />
                </CardContent>
                <CardActions>
                  <Button size="small" onClick={() => navigate(`/stack-instances/${instance.id}`)}>
                    Details
                  </Button>
                </CardActions>
              </Card>
            </Grid>
          ))}
        </Grid>
      )}
    </Box>
  );
};

export default Dashboard;
