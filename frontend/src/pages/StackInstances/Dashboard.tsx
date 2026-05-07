import { useEffect, useState, useCallback, useMemo, useRef } from 'react';
import { timeAgo } from '../../utils/timeAgo';
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
  Alert,
  TextField,
  InputAdornment,
  Chip,
  Paper,
  Link,
  Checkbox,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  List,
  ListItem,
  ListItemText,
  CircularProgress,
  Tooltip,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import AddIcon from '@mui/icons-material/Add';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import StopIcon from '@mui/icons-material/Stop';
import CleaningServicesIcon from '@mui/icons-material/CleaningServices';
import DeleteIcon from '@mui/icons-material/Delete';
import CompareArrowsIcon from '@mui/icons-material/CompareArrows';
import StatusBadge from '../../components/StatusBadge';
import FavoriteButton from '../../components/FavoriteButton';
import ExpiryChip from './ExpiryChip';
import DashboardWidgets from './widgets/DashboardWidgets';
import { instanceService, clusterService, favoriteService } from '../../api/client';
import type { StackInstance, Cluster, NamespaceStatus, UserFavorite, BulkOperationResponse } from '../../types';
import LoadingState from '../../components/LoadingState';
import EmptyState from '../../components/EmptyState';
import { useNotification } from '../../context/NotificationContext';

const STATUSES = ['All', 'draft', 'deploying', 'stabilizing', 'running', 'partial', 'stopped', 'error'];

type BulkAction = 'deploy' | 'stop' | 'clean' | 'delete';

const BULK_ACTION_LABELS: Record<BulkAction, string> = {
  deploy: 'Deploy',
  stop: 'Stop',
  clean: 'Clean',
  delete: 'Delete',
};

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
        const portSuffix = port ? `:${port}` : '';
        return `http://${svc.external_ip}${portSuffix}`;
      }
    }
  }
  return null;
};

type K8sHealthStatus = NamespaceStatus['status'];

const PodHealthDot = ({ status }: { status?: K8sHealthStatus }) => {
  if (!status) return null;
  const colorMap: Record<string, string> = {
    healthy: 'success.main',
    progressing: 'info.main',
    degraded: 'warning.main',
    error: 'error.main',
    not_found: 'grey.400',
  };
  return (
    <Tooltip title={`Health: ${status}`} arrow>
      <Box
        component="span"
        role="status"
        aria-label={`Pod health: ${status}`}
        sx={{
          width: 8,
          height: 8,
          borderRadius: '50%',
          bgcolor: colorMap[status] || 'grey.400',
          display: 'inline-block',
          ml: 1,
          flexShrink: 0,
        }}
      />
    </Tooltip>
  );
};

interface InstanceCardProps {
  instance: StackInstance;
  isSelected: boolean;
  isFavorite: boolean;
  clusterName?: string;
  url?: string;
  k8sHealth?: K8sHealthStatus;
  onToggleSelect: (id: string) => void;
  onNavigate: (path: string) => void;
}

const InstanceCard = ({ instance, isSelected, isFavorite, clusterName, url, k8sHealth, onToggleSelect, onNavigate }: InstanceCardProps) => (
  <Card
    sx={{
      height: '100%',
      display: 'flex',
      flexDirection: 'column',
      outline: isSelected ? 2 : 0,
      outlineColor: 'primary.main',
      outlineStyle: 'solid',
    }}
  >
    <CardContent sx={{ flex: 1 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, minWidth: 0 }}>
          <Checkbox
            checked={isSelected}
            onChange={() => onToggleSelect(instance.id)}
            onClick={(e) => e.stopPropagation()}
            slotProps={{ input: { 'aria-label': `Select ${instance.name}` } }}
            size="small"
          />
          <FavoriteButton entityType="instance" entityId={instance.id} size="small" initialFavorited={isFavorite} />
          <Typography variant="h6" component="h2" noWrap>
            {instance.name}
          </Typography>
        </Box>
        <Box sx={{ display: 'flex', alignItems: 'center' }}>
          <StatusBadge status={instance.status} />
          {(instance.status === 'running' || instance.status === 'partial' || instance.status === 'deploying' || instance.status === 'stabilizing') && k8sHealth && (
            <PodHealthDot status={k8sHealth} />
          )}
        </Box>
      </Box>
      <Typography variant="body2" color="text.secondary">Branch: {instance.branch}</Typography>
      <Typography variant="body2" color="text.secondary">Namespace: {instance.namespace}</Typography>
      {instance.definition && (
        <Typography variant="body2" color="text.secondary">Definition: {instance.definition.name}</Typography>
      )}
      {clusterName && (
        <Typography variant="body2" color="text.secondary">Cluster: {clusterName}</Typography>
      )}
      {url && (
        <Link
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          variant="body2"
          sx={{ display: 'block', mt: 0.5, fontFamily: 'monospace', fontSize: '0.75rem' }}
          noWrap
        >
          {url}
        </Link>
      )}
      <ExpiryChip instance={instance} />
      {instance.last_deployed_at && (
        <Tooltip title={new Date(instance.last_deployed_at).toLocaleString()} arrow>
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
            Deployed {timeAgo(instance.last_deployed_at)}
          </Typography>
        </Tooltip>
      )}
    </CardContent>
    <CardActions>
      <Button size="small" onClick={() => onNavigate(`/stack-instances/${instance.id}`)}>
        Details
      </Button>
    </CardActions>
  </Card>
);

const Dashboard = () => {
  const [instances, setInstances] = useState<StackInstance[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [favorites, setFavorites] = useState<UserFavorite[]>([]);
  const [recentInstances, setRecentInstances] = useState<StackInstance[]>([]);
  const [loading, setLoading] = useState(true);
  const searchRef = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState('All');
  const [instanceUrls, setInstanceUrls] = useState<Record<string, string>>({});
  const [instanceHealth, setInstanceHealth] = useState<Record<string, K8sHealthStatus>>({});
  const navigate = useNavigate();
  const { showSuccess, showError } = useNotification();

  // Bulk operation state
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [bulkConfirmOpen, setBulkConfirmOpen] = useState(false);
  const [bulkAction, setBulkAction] = useState<BulkAction | null>(null);
  const [bulkLoading, setBulkLoading] = useState(false);
  const [bulkResultOpen, setBulkResultOpen] = useState(false);
  const [bulkResult, setBulkResult] = useState<BulkOperationResponse | null>(null);

  const refreshInstances = useCallback(async () => {
    try {
      const [instData, recentData] = await Promise.all([
        instanceService.list(),
        instanceService.recent().catch(() => [] as StackInstance[]),
      ]);
      setInstances(instData || []);
      setRecentInstances(recentData || []);
    } catch {
      // Silently fail on refresh - data is already loaded
    }
  }, []);

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

  // Track which instance IDs have a confirmed URL (won't retry)
  const fetchedStatusIdsRef = useRef<Set<string>>(new Set());
  // Track currently in-flight fetches to avoid duplicate requests
  const inFlightIdsRef = useRef<Set<string>>(new Set());

  // Phase 2: fetch status/URLs for newly running/deploying instances
  useEffect(() => {
    const running = instances.filter(
      (i) => i.status === 'running' || i.status === 'partial' || i.status === 'deploying' || i.status === 'stabilizing',
    );
    const newRunning = running.filter(
      (i) => !fetchedStatusIdsRef.current.has(i.id) && !inFlightIdsRef.current.has(i.id),
    );
    if (newRunning.length === 0) return;

    let cancelled = false;

    // Mark as in-flight to avoid duplicate requests during async window
    for (const inst of newRunning) {
      inFlightIdsRef.current.add(inst.id);
    }

    Promise.allSettled(
      newRunning.map(async (inst) => {
        const st: NamespaceStatus = await instanceService.getStatus(inst.id);
        const url = getPrimaryUrl(st);
        return { id: inst.id, url, health: st.status };
      }),
    ).then((settled) => {
      if (cancelled) return;
      const newUrls: Record<string, string> = {};
      const newHealth: Record<string, K8sHealthStatus> = {};
      settled.forEach((r, idx) => {
        if (r.status === 'fulfilled') {
          inFlightIdsRef.current.delete(r.value.id);
          newHealth[r.value.id] = r.value.health;
          if (r.value.url) {
            fetchedStatusIdsRef.current.add(r.value.id);
            newUrls[r.value.id] = r.value.url;
          }
        } else {
          const id = newRunning[idx]?.id;
          if (id) inFlightIdsRef.current.delete(id);
        }
      });
      if (Object.keys(newUrls).length > 0) {
        setInstanceUrls((prev) => ({ ...prev, ...newUrls }));
      }
      if (Object.keys(newHealth).length > 0) {
        setInstanceHealth((prev) => ({ ...prev, ...newHealth }));
      }
    });

    return () => { cancelled = true; };
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
    if (msg.type === 'instance.status') {
      const payload = msg.payload as {
        instance_id?: string;
        status?: string;
        namespace_status?: { status?: K8sHealthStatus };
      };
      const nextHealth = payload.namespace_status?.status;
      if (payload.instance_id && nextHealth) {
        setInstanceHealth((prev) => ({ ...prev, [payload.instance_id!]: nextHealth }));
      }
    }
  }, []);

  useWebSocket(handleWsMessage);

  // Keyboard shortcuts
  useEffect(() => {
    const isFormOrEditable = (el: HTMLElement | null | undefined) =>
      !!el &&
      (el.tagName === 'INPUT' ||
        el.tagName === 'TEXTAREA' ||
        el.tagName === 'SELECT' ||
        el.isContentEditable);

    const handleKeyDown = (e: KeyboardEvent) => {
      // Don't intercept when typing in inputs
      const target = e.target as HTMLElement;
      if (isFormOrEditable(target) || isFormOrEditable(document.activeElement as HTMLElement | null)) {
        return;
      }

      if (e.key === '/') {
        e.preventDefault();
        searchRef.current?.focus();
      }

      if (e.key === 'Escape') {
        // Clear selection if any, otherwise clear search
        if (selectedIds.size > 0) {
          setSelectedIds(new Set());
        } else if (search) {
          setSearch('');
        }
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [selectedIds.size, search]);

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
  }).sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime());

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

  // Bulk selection helpers
  const toggleSelect = useCallback((id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }, []);

  const toggleSelectAll = useCallback(() => {
    setSelectedIds((prev) => {
      if (prev.size === filtered.length && filtered.length > 0) {
        return new Set();
      }
      return new Set(filtered.map((inst) => inst.id));
    });
  }, [filtered]);

  const selectedInstances = useMemo(() => {
    return instances.filter((inst) => selectedIds.has(inst.id));
  }, [instances, selectedIds]);

  const handleBulkActionClick = useCallback((action: BulkAction) => {
    setBulkAction(action);
    setBulkConfirmOpen(true);
  }, []);

  const handleBulkConfirm = useCallback(async () => {
    if (!bulkAction || selectedIds.size === 0) return;

    setBulkConfirmOpen(false);
    setBulkLoading(true);

    const ids = Array.from(selectedIds);
    try {
      let result: BulkOperationResponse;
      switch (bulkAction) {
        case 'deploy':
          result = await instanceService.bulkDeploy(ids);
          break;
        case 'stop':
          result = await instanceService.bulkStop(ids);
          break;
        case 'clean':
          result = await instanceService.bulkClean(ids);
          break;
        case 'delete':
          result = await instanceService.bulkDelete(ids);
          break;
      }

      setBulkResult(result);
      setBulkResultOpen(true);

      if (result.failed === 0) {
        showSuccess(`Bulk ${bulkAction}: ${result.succeeded}/${result.total} succeeded`);
      } else {
        showError(`Bulk ${bulkAction}: ${result.failed}/${result.total} failed`);
      }

      // Refresh instances to reflect new statuses
      await refreshInstances();
    } catch {
      showError(`Bulk ${bulkAction} failed`);
    } finally {
      setBulkLoading(false);
    }
  }, [bulkAction, selectedIds, showSuccess, showError, refreshInstances]);

  const handleBulkResultClose = useCallback(() => {
    setBulkResultOpen(false);
    setBulkResult(null);
    setSelectedIds(new Set());
    setBulkAction(null);
  }, []);

  const handleBulkConfirmCancel = useCallback(() => {
    setBulkConfirmOpen(false);
    setBulkAction(null);
  }, []);

  if (loading) {
    return <LoadingState label="Loading instances..." />;
  }

  if (error) {
    return <Alert severity="error">{error}</Alert>;
  }

  const allFilteredSelected = filtered.length > 0 && selectedIds.size === filtered.length;
  const someFilteredSelected = selectedIds.size > 0 && selectedIds.size < filtered.length;

  return (
    <Box>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Stack Instances
        </Typography>
        <Box sx={{ display: 'flex', gap: 1 }}>
          <Button
            variant="outlined"
            startIcon={<CompareArrowsIcon />}
            onClick={() => navigate('/stack-instances/compare')}
          >
            Compare
          </Button>
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/stack-instances/new')}>
            Create Instance
          </Button>
        </Box>
      </Box>

      <DashboardWidgets />

      <Box sx={{ display: 'flex', gap: 2, mb: 3, alignItems: 'center', flexWrap: 'wrap' }}>
        <TextField
          size="small"
          placeholder="Search instances..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          inputRef={searchRef}
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
        <Typography variant="caption" color="text.secondary" sx={{ ml: 0.5, display: { xs: 'none', md: 'inline' } }}>
          Press <Box component="kbd" sx={{ fontFamily: 'monospace', px: 0.5, border: '1px solid', borderRadius: 3, fontSize: '0.75rem' }}>/</Box> to search
        </Typography>
        <Box sx={{ display: 'flex', gap: 1 }}>
          {STATUSES.map((s) => (
            <Chip
              key={s}
              label={s === 'All' ? 'All Statuses' : s}
              onClick={() => setStatusFilter(s)}
              color={statusFilter === s ? 'primary' : 'default'}
              variant={statusFilter === s ? 'filled' : 'outlined'}
            />
          ))}
        </Box>
      </Box>

      {/* Bulk Action Toolbar */}
      {selectedIds.size > 0 && (
        <Paper
          sx={{
            p: 1.5,
            mb: 3,
            display: 'flex',
            alignItems: 'center',
            gap: 2,
            bgcolor: 'primary.light',
            color: 'primary.contrastText',
          }}
          role="toolbar"
          aria-label="Bulk actions"
        >
          <Typography variant="body2" sx={{ fontWeight: 'bold', mr: 1 }}>
            {selectedIds.size} selected
          </Typography>
          <Button
            size="small"
            variant="contained"
            color="success"
            startIcon={bulkLoading ? <CircularProgress size={16} color="inherit" /> : <PlayArrowIcon />}
            onClick={() => handleBulkActionClick('deploy')}
            disabled={bulkLoading}
          >
            Deploy
          </Button>
          <Button
            size="small"
            variant="contained"
            color="warning"
            startIcon={bulkLoading ? <CircularProgress size={16} color="inherit" /> : <StopIcon />}
            onClick={() => handleBulkActionClick('stop')}
            disabled={bulkLoading}
          >
            Stop
          </Button>
          <Button
            size="small"
            variant="contained"
            color="info"
            startIcon={bulkLoading ? <CircularProgress size={16} color="inherit" /> : <CleaningServicesIcon />}
            onClick={() => handleBulkActionClick('clean')}
            disabled={bulkLoading}
          >
            Clean
          </Button>
          <Button
            size="small"
            variant="contained"
            color="error"
            startIcon={bulkLoading ? <CircularProgress size={16} color="inherit" /> : <DeleteIcon />}
            onClick={() => handleBulkActionClick('delete')}
            disabled={bulkLoading}
          >
            Delete
          </Button>
          <Box sx={{ flex: 1 }} />
          <Button
            size="small"
            variant="outlined"
            sx={{ color: 'primary.contrastText', borderColor: 'primary.contrastText' }}
            onClick={() => setSelectedIds(new Set())}
            disabled={bulkLoading}
          >
            Clear Selection
          </Button>
        </Paper>
      )}

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
                    <Typography variant="subtitle2" component="div" noWrap sx={{ flex: 1 }}>
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
                  <Typography variant="subtitle2" component="div" noWrap>
                    {inst.name}
                  </Typography>
                  {inst.definition && (
                    <Typography variant="caption" color="text.secondary" noWrap component="div">
                      {inst.definition.name}
                    </Typography>
                  )}
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 0.5 }}>
                    <StatusBadge status={inst.status} />
                    <Tooltip title={new Date(inst.updated_at).toLocaleString()} arrow>
                      <Typography variant="caption" color="text.secondary">
                        {timeAgo(inst.updated_at)}
                      </Typography>
                    </Tooltip>
                  </Box>
                </CardContent>
              </Card>
            ))}
          </Box>
        </Box>
      )}

      {filtered.length === 0 ? (
        <EmptyState
          title="No stack instances found"
          description="Create a new instance to get started."
          action={
            <Button variant="outlined" onClick={() => navigate('/stack-instances/new')}>
              Create your first instance
            </Button>
          }
        />
      ) : (
        <Box>
          {/* Select All header */}
          <Box sx={{ display: 'flex', alignItems: 'center', mb: 1, ml: 1 }}>
            <Checkbox
              checked={allFilteredSelected}
              indeterminate={someFilteredSelected}
              onChange={toggleSelectAll}
              slotProps={{ input: { 'aria-label': 'Select all instances' } }}
              size="small"
            />
            <Typography variant="body2" color="text.secondary">
              {allFilteredSelected ? 'Deselect all' : 'Select all'} ({filtered.length})
            </Typography>
          </Box>
          <Grid container spacing={3} aria-live="polite">
            {filtered.map((instance) => (
              <Grid key={instance.id} size={{ xs: 12, sm: 6, md: 4 }}>
                <InstanceCard
                  instance={instance}
                  isSelected={selectedIds.has(instance.id)}
                  isFavorite={favoriteInstanceIds.has(instance.id)}
                  clusterName={instance.cluster_id ? clusterNameMap.get(instance.cluster_id) : undefined}
                  url={instanceUrls[instance.id]}
                  k8sHealth={instanceHealth[instance.id]}
                  onToggleSelect={toggleSelect}
                  onNavigate={navigate}
                />
              </Grid>
            ))}
          </Grid>
        </Box>
      )}

      {/* Bulk Confirm Dialog */}
      <Dialog open={bulkConfirmOpen} onClose={handleBulkConfirmCancel} maxWidth="sm" fullWidth>
        <DialogTitle>
          Confirm Bulk {bulkAction ? BULK_ACTION_LABELS[bulkAction] : ''}
        </DialogTitle>
        <DialogContent>
          {bulkAction === 'delete' && (
            <Alert severity="warning" sx={{ mb: 2 }}>
              This action cannot be undone. Selected instances will be permanently deleted.
            </Alert>
          )}
          <Typography variant="body1" sx={{ mb: 1 }}>
            {bulkAction ? BULK_ACTION_LABELS[bulkAction] : ''} {selectedInstances.length} instance{selectedInstances.length === 1 ? '' : 's'}:
          </Typography>
          <List dense>
            {selectedInstances.map((inst) => (
              <ListItem key={inst.id}>
                <ListItemText
                  primary={inst.name}
                  secondary={`Status: ${inst.status}`}
                />
              </ListItem>
            ))}
          </List>
        </DialogContent>
        <DialogActions>
          <Button onClick={handleBulkConfirmCancel}>Cancel</Button>
          <Button
            onClick={handleBulkConfirm}
            variant="contained"
            color={bulkAction === 'delete' ? 'error' : 'primary'}
          >
            {bulkAction ? BULK_ACTION_LABELS[bulkAction] : 'Confirm'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Bulk Results Dialog */}
      <Dialog open={bulkResultOpen} onClose={handleBulkResultClose} maxWidth="sm" fullWidth>
        <DialogTitle>
          Bulk Operation Results
        </DialogTitle>
        <DialogContent>
          {bulkResult && (
            <Box>
              <Box sx={{ display: 'flex', gap: 2, mb: 2 }}>
                <Alert severity="success" sx={{ flex: 1 }}>
                  {bulkResult.succeeded} succeeded
                </Alert>
                {bulkResult.failed > 0 && (
                  <Alert severity="error" sx={{ flex: 1 }}>
                    {bulkResult.failed} failed
                  </Alert>
                )}
              </Box>
              <List dense>
                {bulkResult.results.map((item) => (
                  <ListItem key={item.instance_id}>
                    <ListItemText
                      primary={item.instance_name}
                      secondary={item.status === 'error' ? item.error : 'Success'}
                      slotProps={{
                        secondary: {
                          sx: { color: item.status === 'error' ? 'error.main' : 'success.main' },
                        },
                      }}
                    />
                  </ListItem>
                ))}
              </List>
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={handleBulkResultClose} variant="contained">
            Close
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};

export default Dashboard;
