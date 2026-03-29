import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Button,
  CircularProgress,
  Alert,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  IconButton,
  Chip,
  Tooltip,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  FormControlLabel,
  Checkbox,
  Breadcrumbs,
  Link as MuiLink,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import EditIcon from '@mui/icons-material/Edit';
import DeleteIcon from '@mui/icons-material/Delete';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import StarIcon from '@mui/icons-material/Star';
import StarBorderIcon from '@mui/icons-material/StarBorder';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import TuneIcon from '@mui/icons-material/Tune';
import { clusterService } from '../../../api/client';
import type { Cluster, CreateClusterRequest, UpdateClusterRequest, ClusterTestResult } from '../../../types';
import LoadingState from '../../../components/LoadingState';
import QuotaConfigDialog from '../../../components/QuotaConfigDialog';
import { Link } from 'react-router-dom';
import { useNotification } from '../../../context/NotificationContext';

const emptyCreateForm: CreateClusterRequest = {
  name: '',
  description: '',
  api_server_url: '',
  kubeconfig_data: '',
  kubeconfig_path: '',
  region: '',
  max_namespaces: 0,
  max_instances_per_user: 0,
  is_default: false,
};

const healthColor = (status: string): 'success' | 'warning' | 'error' | 'default' => {
  if (status === 'healthy') return 'success';
  if (status === 'degraded') return 'warning';
  if (status === 'unreachable') return 'error';
  return 'default';
};

const healthLabel = (status: string): string => {
  if (!status || status === '') return 'unknown';
  return status;
};

const isValidClusterUrl = (url: string): boolean => {
  if (!url.trim()) return true; // empty is handled by required check
  return url.startsWith('https://');
};

const Clusters = () => {
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Create / Edit dialog
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingCluster, setEditingCluster] = useState<Cluster | null>(null);
  const [form, setForm] = useState<CreateClusterRequest>(emptyCreateForm);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [dialogLoading, setDialogLoading] = useState(false);

  // Delete confirm
  const [deleteTarget, setDeleteTarget] = useState<Cluster | null>(null);
  // Quota config dialog
  const [quotaTarget, setQuotaTarget] = useState<Cluster | null>(null);
  const { showSuccess, showError } = useNotification();

  const fetchClusters = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await clusterService.list();
      setClusters(data || []);
    } catch {
      setError('Failed to load clusters');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchClusters();
  }, [fetchClusters]);

  const openCreateDialog = () => {
    setEditingCluster(null);
    setForm(emptyCreateForm);
    setDialogError(null);
    setDialogOpen(true);
  };

  const openEditDialog = (cluster: Cluster) => {
    setEditingCluster(cluster);
    setForm({
      name: cluster.name,
      description: cluster.description,
      api_server_url: cluster.api_server_url,
      kubeconfig_data: '',
      kubeconfig_path: '',
      region: cluster.region,
      max_namespaces: cluster.max_namespaces,
      max_instances_per_user: cluster.max_instances_per_user,
      is_default: cluster.is_default,
    });
    setDialogError(null);
    setDialogOpen(true);
  };

  const handleSave = async () => {
    if (!form.name.trim() || !form.api_server_url.trim()) {
      setDialogError('Name and API Server URL are required');
      return;
    }
    if (!isValidClusterUrl(form.api_server_url)) {
      setDialogError('API Server URL must start with https://');
      return;
    }
    if (!editingCluster && !(form.kubeconfig_data ?? '').trim() && !(form.kubeconfig_path ?? '').trim()) {
      setDialogError('Either kubeconfig data or kubeconfig path is required when creating a cluster');
      return;
    }

    setDialogLoading(true);
    setDialogError(null);
    try {
      if (editingCluster) {
        const update: UpdateClusterRequest = {
          name: form.name,
          description: form.description,
          api_server_url: form.api_server_url,
          region: form.region,
          max_namespaces: form.max_namespaces,
          max_instances_per_user: form.max_instances_per_user,
          is_default: form.is_default,
        };
        if ((form.kubeconfig_data ?? '').trim()) {
          update.kubeconfig_data = form.kubeconfig_data;
        }
        if ((form.kubeconfig_path ?? '').trim()) {
          update.kubeconfig_path = form.kubeconfig_path;
        }
        await clusterService.update(editingCluster.id, update);
        showSuccess('Cluster updated');
      } else {
        await clusterService.create(form);
        showSuccess('Cluster created');
      }
      setDialogOpen(false);
      await fetchClusters();
    } catch {
      setDialogError(editingCluster ? 'Failed to update cluster' : 'Failed to create cluster');
    } finally {
      setDialogLoading(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await clusterService.delete(deleteTarget.id);
      setDeleteTarget(null);
      showSuccess('Cluster deleted');
      await fetchClusters();
    } catch {
      showError('Failed to delete cluster. It may still have instances.');
      setDeleteTarget(null);
    }
  };

  const handleTestConnection = async (cluster: Cluster) => {
    try {
      const result: ClusterTestResult = await clusterService.testConnection(cluster.id);
      const version = result.server_version ? ` (${result.server_version})` : '';
      const message = `${cluster.name}: ${result.message}${version}`;
      if (result.status === 'success') {
        showSuccess(message);
      } else {
        showError(message);
      }
    } catch {
      showError(`Failed to test connection for ${cluster.name}`);
    }
  };

  const handleSetDefault = async (cluster: Cluster) => {
    try {
      await clusterService.setDefault(cluster.id);
      showSuccess(`${cluster.name} set as default`);
      await fetchClusters();
    } catch {
      showError('Failed to set default cluster');
    }
  };

  if (loading) {
    return <LoadingState label="Loading clusters..." />;
  }

  if (error) {
    return <Alert severity="error">{error}</Alert>;
  }

  return (
    <Box>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <MuiLink component={Link} to="/" underline="hover" color="inherit">Home</MuiLink>
        <Typography color="text.primary">Clusters</Typography>
      </Breadcrumbs>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Cluster Management
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={openCreateDialog}>
          Add Cluster
        </Button>
      </Box>

      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>Region</TableCell>
              <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>API Server URL</TableCell>
              <TableCell>Health Status</TableCell>
              <TableCell>Default</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {clusters.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} align="center">
                  <Typography color="text.secondary" sx={{ py: 2 }}>
                    No clusters configured. Add one to get started.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              clusters.map((cluster) => (
                <TableRow key={cluster.id}>
                  <TableCell>
                    <Typography fontWeight="medium">{cluster.name}</Typography>
                    {cluster.description && (
                      <Typography variant="body2" color="text.secondary">{cluster.description}</Typography>
                    )}
                  </TableCell>
                  <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>{cluster.region || '—'}</TableCell>
                  <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>
                    <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem' }}>
                      {cluster.api_server_url}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Chip
                      label={healthLabel(cluster.health_status)}
                      color={healthColor(cluster.health_status)}
                      size="small"
                    />
                  </TableCell>
                  <TableCell>
                    {cluster.is_default ? (
                      <Chip label="Default" icon={<StarIcon />} color="primary" size="small" variant="outlined" />
                    ) : (
                      <Tooltip title="Set as default">
                        <IconButton size="small" aria-label={`Set ${cluster.name} as default`} onClick={() => handleSetDefault(cluster)}>
                          <StarBorderIcon />
                        </IconButton>
                      </Tooltip>
                    )}
                  </TableCell>
                  <TableCell align="right">
                    <Tooltip title="Resource Quotas">
                      <IconButton size="small" aria-label={`Resource quotas for ${cluster.name}`} onClick={() => setQuotaTarget(cluster)}>
                        <TuneIcon />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Test Connection">
                      <IconButton size="small" aria-label={`Test connection for ${cluster.name}`} onClick={() => handleTestConnection(cluster)}>
                        <CheckCircleIcon />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Edit">
                      <IconButton size="small" aria-label={`Edit ${cluster.name}`} onClick={() => openEditDialog(cluster)}>
                        <EditIcon />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Delete">
                      <IconButton size="small" aria-label={`Delete ${cluster.name}`} onClick={() => setDeleteTarget(cluster)} color="error">
                        <DeleteIcon />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>

      {/* Create / Edit Dialog */}
      <Dialog open={dialogOpen} onClose={() => setDialogOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>{editingCluster ? 'Edit Cluster' : 'Add Cluster'}</DialogTitle>
        <DialogContent>
          {dialogError && <Alert severity="error" sx={{ mb: 2 }}>{dialogError}</Alert>}
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
            <TextField
              label="Name"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              required
              fullWidth
            />
            <TextField
              label="Description"
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
              fullWidth
            />
            <TextField
              label="API Server URL"
              value={form.api_server_url}
              onChange={(e) => setForm({ ...form, api_server_url: e.target.value })}
              required
              fullWidth
              placeholder="https://my-cluster.example.com:6443"
              error={!!form.api_server_url.trim() && !isValidClusterUrl(form.api_server_url)}
              helperText={form.api_server_url.trim() && !isValidClusterUrl(form.api_server_url) ? 'URL must start with https://' : ''}
            />
            <TextField
              label={editingCluster ? 'Kubeconfig Data (leave blank to keep current)' : 'Kubeconfig Data'}
              value={form.kubeconfig_data}
              onChange={(e) => setForm({ ...form, kubeconfig_data: e.target.value, kubeconfig_path: '' })}
              fullWidth
              multiline
              minRows={4}
              maxRows={10}
              placeholder="Paste kubeconfig YAML here"
              disabled={!!(form.kubeconfig_path ?? '').trim()}
              slotProps={{ htmlInput: { style: { fontFamily: 'monospace', fontSize: '0.85rem' } } }}
              helperText="Paste kubeconfig content directly, or use the path field below"
            />
            <TextField
              label={editingCluster ? 'Kubeconfig Path (leave blank to keep current)' : 'Kubeconfig Path'}
              value={form.kubeconfig_path}
              onChange={(e) => setForm({ ...form, kubeconfig_path: e.target.value, kubeconfig_data: '' })}
              fullWidth
              placeholder="/path/to/kubeconfig"
              disabled={!!(form.kubeconfig_data ?? '').trim()}
              helperText="Path to kubeconfig file on the backend server"
            />
            <TextField
              label="Region"
              value={form.region}
              onChange={(e) => setForm({ ...form, region: e.target.value })}
              fullWidth
            />
            <TextField
              label="Max Namespaces"
              type="number"
              value={form.max_namespaces}
              onChange={(e) => setForm({ ...form, max_namespaces: Number.parseInt(e.target.value, 10) || 0 })}
              fullWidth
              helperText="0 = unlimited"
            />
            <TextField
              label="Max Instances Per User"
              type="number"
              value={form.max_instances_per_user ?? 0}
              onChange={(e) => setForm({ ...form, max_instances_per_user: Number.parseInt(e.target.value, 10) || 0 })}
              fullWidth
              helperText="Maximum stack instances a single user can create on this cluster (0 = unlimited)"
            />
            <FormControlLabel
              control={
                <Checkbox
                  checked={form.is_default}
                  onChange={(e) => setForm({ ...form, is_default: e.target.checked })}
                />
              }
              label="Set as default cluster"
            />
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDialogOpen(false)}>Cancel</Button>
          <Button variant="contained" onClick={handleSave} disabled={dialogLoading}>
            {dialogLoading ? <CircularProgress size={20} /> : editingCluster ? 'Update' : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Delete Confirm Dialog */}
      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogTitle>Delete Cluster</DialogTitle>
        <DialogContent>
          <Typography>
            Are you sure you want to delete <strong>{deleteTarget?.name}</strong>?
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            This will fail if there are stack instances still using this cluster.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button variant="contained" color="error" onClick={handleDelete}>
            Delete
          </Button>
        </DialogActions>
      </Dialog>

      {/* Quota Config Dialog */}
      {quotaTarget && (
        <QuotaConfigDialog
          open={!!quotaTarget}
          onClose={() => setQuotaTarget(null)}
          clusterId={quotaTarget.id}
          clusterName={quotaTarget.name}
        />
      )}

    </Box>
  );
};

export default Clusters;
