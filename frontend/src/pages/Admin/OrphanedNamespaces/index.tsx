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
  Chip,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Tooltip,
  Breadcrumbs,
  Link as MuiLink,
} from '@mui/material';
import DeleteIcon from '@mui/icons-material/Delete';
import RefreshIcon from '@mui/icons-material/Refresh';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import { adminService } from '../../../api/client';
import { useAuth } from '../../../context/AuthContext';
import { useNotification } from '../../../context/NotificationContext';
import type { OrphanedNamespace } from '../../../types';
import LoadingState from '../../../components/LoadingState';
import { Link } from 'react-router-dom';

const formatAge = (dateStr: string): string => {
  const created = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - created.getTime();
  const days = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (days > 0) return `${days}d ago`;
  const hours = Math.floor(diffMs / (1000 * 60 * 60));
  if (hours > 0) return `${hours}h ago`;
  const minutes = Math.floor(diffMs / (1000 * 60));
  return `${minutes}m ago`;
};

const OrphanedNamespaces = () => {
  const { user } = useAuth();

  const [namespaces, setNamespaces] = useState<OrphanedNamespace[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<OrphanedNamespace | null>(null);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const { showSuccess, showError } = useNotification();

  const fetchNamespaces = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await adminService.listOrphanedNamespaces();
      setNamespaces(data || []);
    } catch {
      setError('Failed to load orphaned namespaces');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (user?.role === 'admin') {
      fetchNamespaces();
    }
  }, [fetchNamespaces, user]);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleteLoading(true);
    try {
      await adminService.deleteOrphanedNamespace(deleteTarget.name);
      showSuccess(`Namespace "${deleteTarget.name}" deleted successfully`);
      setDeleteTarget(null);
      await fetchNamespaces();
    } catch {
      showError(`Failed to delete namespace "${deleteTarget.name}"`);
      setDeleteTarget(null);
    } finally {
      setDeleteLoading(false);
    }
  };

  // Access guard
  if (user?.role !== 'admin') {
    return (
      <Box sx={{ mt: 4 }}>
        <Alert severity="error">You do not have permission to access this page. Admin role required.</Alert>
      </Box>
    );
  }

  return (
    <Box>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <MuiLink component={Link} to="/" underline="hover" color="inherit">Home</MuiLink>
        <Typography color="text.primary">Orphaned Namespaces</Typography>
      </Breadcrumbs>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Orphaned Namespaces
        </Typography>
        <Button
          variant="outlined"
          startIcon={<RefreshIcon />}
          onClick={fetchNamespaces}
          disabled={loading}
        >
          Refresh
        </Button>
      </Box>

      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Namespaces matching the <code>stack-*</code> pattern that have no corresponding stack instance in the database.
        These may have been left behind after an instance was deleted or created before a naming convention change.
      </Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <LoadingState label="Loading namespaces..." />
      ) : namespaces.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <Typography color="text.secondary">
            No orphaned namespaces found. All stack namespaces have matching instances.
          </Typography>
        </Paper>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Namespace</TableCell>
                <TableCell>Age</TableCell>
                <TableCell>Phase</TableCell>
                <TableCell>Pods</TableCell>
                <TableCell>Deployments</TableCell>
                <TableCell>Services</TableCell>
                <TableCell>Helm Releases</TableCell>
                <TableCell>Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {namespaces.map((ns) => (
                <TableRow key={ns.name} hover>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontWeight: 'medium', fontFamily: 'monospace' }}>
                      {ns.name}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Tooltip title={new Date(ns.created_at).toLocaleString()}>
                      <span>{formatAge(ns.created_at)}</span>
                    </Tooltip>
                  </TableCell>
                  <TableCell>
                    <Chip
                      label={ns.phase}
                      size="small"
                      color={ns.phase === 'Active' ? 'success' : 'warning'}
                      variant="outlined"
                    />
                  </TableCell>
                  <TableCell>{ns.resource_counts?.pods ?? '-'}</TableCell>
                  <TableCell>{ns.resource_counts?.deployments ?? '-'}</TableCell>
                  <TableCell>{ns.resource_counts?.services ?? '-'}</TableCell>
                  <TableCell>
                    {ns.helm_releases.length > 0 ? (
                      <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap' }}>
                        {ns.helm_releases.map((r) => (
                          <Chip key={r} label={r} size="small" variant="outlined" />
                        ))}
                      </Box>
                    ) : (
                      <Typography variant="body2" color="text.secondary">None</Typography>
                    )}
                  </TableCell>
                  <TableCell>
                    <Button
                      size="small"
                      color="error"
                      variant="outlined"
                      startIcon={<DeleteIcon />}
                      onClick={() => setDeleteTarget(ns)}
                    >
                      Delete
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Delete confirmation dialog */}
      <Dialog open={Boolean(deleteTarget)} onClose={() => !deleteLoading && setDeleteTarget(null)}>
        <DialogTitle>Delete Orphaned Namespace</DialogTitle>
        <DialogContent>
          <Typography>
            Are you sure you want to delete namespace <strong>{deleteTarget?.name}</strong>?
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            This will uninstall all Helm releases and delete the Kubernetes namespace.
            This action cannot be undone.
          </Typography>
          {deleteTarget && deleteTarget.helm_releases.length > 0 && (
            <Alert severity="warning" sx={{ mt: 2 }}>
              {deleteTarget.helm_releases.length} Helm release(s) will be uninstalled:
              {' '}{deleteTarget.helm_releases.join(', ')}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)} disabled={deleteLoading}>Cancel</Button>
          <Button
            variant="contained"
            color="error"
            onClick={handleDelete}
            disabled={deleteLoading}
          >
            {deleteLoading ? <CircularProgress size={20} /> : 'Delete Namespace'}
          </Button>
        </DialogActions>
      </Dialog>


    </Box>
  );
};

export default OrphanedNamespaces;
