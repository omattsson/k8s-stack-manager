import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Alert,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  Button,
  IconButton,
  FormControl,
  InputLabel,
  Select,
  MenuItem,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  Tooltip,
  Breadcrumbs,
  Link as MuiLink,
} from '@mui/material';
import type { SelectChangeEvent } from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import EditIcon from '@mui/icons-material/Edit';
import DeleteIcon from '@mui/icons-material/Delete';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import { clusterService, sharedValuesService } from '../../api/client';
import type { Cluster, SharedValues } from '../../types';
import LoadingState from '../../components/LoadingState';
import { Link } from 'react-router-dom';
import { useNotification } from '../../context/NotificationContext';

interface FormState {
  name: string;
  description: string;
  priority: string;
  values: string;
}

const emptyForm: FormState = {
  name: '',
  description: '',
  priority: '0',
  values: '',
};

const SharedValuesPage = () => {
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [selectedCluster, setSelectedCluster] = useState<string>('');
  const [sharedValues, setSharedValues] = useState<SharedValues[]>([]);
  const [loading, setLoading] = useState(true);
  const [valuesLoading, setValuesLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Dialog state
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // Delete confirmation
  const [deleteTarget, setDeleteTarget] = useState<SharedValues | null>(null);
  const [deleting, setDeleting] = useState(false);
  const { showSuccess, showError } = useNotification();

  // Fetch clusters on mount
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

  // Fetch shared values when cluster changes
  const fetchSharedValues = useCallback(async () => {
    if (!selectedCluster) return;
    setValuesLoading(true);
    setError(null);
    try {
      const data = await sharedValuesService.list(selectedCluster);
      setSharedValues(data.sort((a, b) => a.priority - b.priority));
    } catch {
      setError('Failed to load shared values');
    } finally {
      setValuesLoading(false);
      setLoading(false);
    }
  }, [selectedCluster]);

  useEffect(() => {
    fetchSharedValues();
  }, [fetchSharedValues]);

  const handleClusterChange = (event: SelectChangeEvent<string>) => {
    setSelectedCluster(event.target.value);
  };

  const openCreateDialog = () => {
    setEditingId(null);
    setForm(emptyForm);
    setFormError(null);
    setDialogOpen(true);
  };

  const openEditDialog = (sv: SharedValues) => {
    setEditingId(sv.id);
    setForm({
      name: sv.name,
      description: sv.description,
      priority: String(sv.priority),
      values: sv.values,
    });
    setFormError(null);
    setDialogOpen(true);
  };

  const handleDialogClose = () => {
    setDialogOpen(false);
    setEditingId(null);
    setFormError(null);
  };

  const handleSave = async () => {
    if (!form.name.trim()) {
      setFormError('Name is required');
      return;
    }

    const priority = Number.parseInt(form.priority, 10);
    if (Number.isNaN(priority)) {
      setFormError('Priority must be a number');
      return;
    }

    setSaving(true);
    setFormError(null);
    try {
      const payload = {
        name: form.name.trim(),
        description: form.description.trim(),
        priority,
        values: form.values,
      };

      if (editingId) {
        await sharedValuesService.update(selectedCluster, editingId, payload);
        showSuccess('Shared values updated');
      } else {
        await sharedValuesService.create(selectedCluster, payload);
        showSuccess('Shared values created');
      }
      setDialogOpen(false);
      setEditingId(null);
      await fetchSharedValues();
    } catch {
      setFormError('Failed to save shared values');
    } finally {
      setSaving(false);
    }
  };

  const handleDeleteClick = (sv: SharedValues) => {
    setDeleteTarget(sv);
  };

  const handleDeleteConfirm = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await sharedValuesService.delete(selectedCluster, deleteTarget.id);
      showSuccess('Shared values deleted');
      setDeleteTarget(null);
      await fetchSharedValues();
    } catch {
      showError('Failed to delete shared values');
    } finally {
      setDeleting(false);
    }
  };

  const formatDate = (dateStr: string): string => {
    try {
      return new Date(dateStr).toLocaleString();
    } catch {
      return dateStr;
    }
  };

  const truncateYaml = (yaml: string, maxLen = 80): string => {
    if (yaml.length <= maxLen) return yaml;
    return yaml.substring(0, maxLen) + '…';
  };

  if (clusters.length === 0 && !loading && !error) {
    return (
      <Box>
        <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
          <MuiLink component={Link} to="/" underline="hover" color="inherit">Home</MuiLink>
          <Typography color="text.primary">Shared Values</Typography>
        </Breadcrumbs>
        <Typography variant="h4" component="h1" gutterBottom>
          Shared Values
        </Typography>
        <Alert severity="info">No clusters registered. Add a cluster first.</Alert>
      </Box>
    );
  }

  return (
    <Box>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <MuiLink component={Link} to="/" underline="hover" color="inherit">Home</MuiLink>
        <Typography color="text.primary">Shared Values</Typography>
      </Breadcrumbs>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Shared Values
        </Typography>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
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
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={openCreateDialog}
            disabled={!selectedCluster}
          >
            Add Shared Values
          </Button>
        </Box>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {(loading || valuesLoading) && <LoadingState label="Loading shared values..." />}

      {!loading && !valuesLoading && !error && sharedValues.length === 0 && selectedCluster && (
        <Alert severity="info">No shared values configured for this cluster.</Alert>
      )}

      {!loading && !valuesLoading && sharedValues.length > 0 && (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Priority</TableCell>
                <TableCell>Name</TableCell>
                <TableCell>Description</TableCell>
                <TableCell>Values</TableCell>
                <TableCell>Updated</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {sharedValues.map((sv) => (
                <TableRow key={sv.id}>
                  <TableCell>{sv.priority}</TableCell>
                  <TableCell>{sv.name}</TableCell>
                  <TableCell>{sv.description}</TableCell>
                  <TableCell>
                    <Tooltip title={sv.values} placement="top-start">
                      <Typography
                        variant="body2"
                        sx={{ fontFamily: 'monospace', whiteSpace: 'pre', maxWidth: 300, overflow: 'hidden', textOverflow: 'ellipsis' }}
                      >
                        {truncateYaml(sv.values)}
                      </Typography>
                    </Tooltip>
                  </TableCell>
                  <TableCell>{formatDate(sv.updated_at)}</TableCell>
                  <TableCell align="right">
                    <Tooltip title="Edit">
                      <IconButton size="small" onClick={() => openEditDialog(sv)} aria-label={`Edit ${sv.name}`}>
                        <EditIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Delete">
                      <IconButton size="small" onClick={() => handleDeleteClick(sv)} aria-label={`Delete ${sv.name}`}>
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Create / Edit Dialog */}
      <Dialog open={dialogOpen} onClose={handleDialogClose} maxWidth="md" fullWidth>
        <DialogTitle>{editingId ? 'Edit Shared Values' : 'Add Shared Values'}</DialogTitle>
        <DialogContent>
          {formError && <Alert severity="error" sx={{ mb: 2 }}>{formError}</Alert>}
          <TextField
            label="Name"
            value={form.name}
            onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            fullWidth
            margin="normal"
            required
            helperText={`${form.name.length}/100`}
            error={form.name.length > 100}
            slotProps={{ htmlInput: { maxLength: 100 } }}
          />
          <TextField
            label="Description"
            value={form.description}
            onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))}
            fullWidth
            margin="normal"
            helperText={`${form.description.length}/200`}
            error={form.description.length > 200}
            slotProps={{ htmlInput: { maxLength: 200 } }}
          />
          <TextField
            label="Priority"
            type="number"
            value={form.priority}
            onChange={(e) => setForm((f) => ({ ...f, priority: e.target.value }))}
            fullWidth
            margin="normal"
            helperText="Lower number = applied first (base), higher = overrides"
          />
          <TextField
            label="Values (YAML)"
            value={form.values}
            onChange={(e) => setForm((f) => ({ ...f, values: e.target.value }))}
            fullWidth
            margin="normal"
            multiline
            minRows={8}
            slotProps={{ input: { sx: { fontFamily: 'monospace' } } }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={handleDialogClose}>Cancel</Button>
          <Button onClick={handleSave} variant="contained" disabled={saving}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogTitle>Delete Shared Values</DialogTitle>
        <DialogContent>
          <Typography>
            Are you sure you want to delete &quot;{deleteTarget?.name}&quot;? This action cannot be undone.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button onClick={handleDeleteConfirm} color="error" variant="contained" disabled={deleting}>
            {deleting ? 'Deleting…' : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>

    </Box>
  );
};

export default SharedValuesPage;
