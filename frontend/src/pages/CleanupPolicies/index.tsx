import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  CircularProgress,
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
  Switch,
  Checkbox,
  Chip,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  FormControl,
  InputLabel,
  Select,
  MenuItem,
  FormControlLabel,
  Snackbar,
  Tooltip,
} from '@mui/material';
import type { SelectChangeEvent } from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import EditIcon from '@mui/icons-material/Edit';
import DeleteIcon from '@mui/icons-material/Delete';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import { cleanupPolicyService, clusterService } from '../../api/client';
import type { CleanupPolicy, CleanupResult, Cluster } from '../../types';

type ConditionPreset = 'idle_days' | 'stopped_age' | 'ttl_expired' | 'custom';

interface PolicyFormState {
  name: string;
  cluster_id: string;
  action: string;
  conditionPreset: ConditionPreset;
  idleDays: string;
  stoppedAgeDays: string;
  customCondition: string;
  schedule: string;
  enabled: boolean;
  dry_run: boolean;
}

const emptyForm: PolicyFormState = {
  name: '',
  cluster_id: 'all',
  action: 'stop',
  conditionPreset: 'idle_days',
  idleDays: '7',
  stoppedAgeDays: '14',
  customCondition: '',
  schedule: '0 2 * * *',
  enabled: true,
  dry_run: true,
};

const actionColors: Record<string, 'primary' | 'warning' | 'error'> = {
  stop: 'primary',
  clean: 'warning',
  delete: 'error',
};

const actionDescriptions: Record<string, string> = {
  stop: 'Stop running instances (uninstall Helm releases)',
  clean: 'Clean namespace resources',
  delete: 'Delete stack instances permanently',
};

function buildConditionString(form: PolicyFormState): string {
  switch (form.conditionPreset) {
    case 'idle_days':
      return `idle_days:${form.idleDays}`;
    case 'stopped_age':
      return `status:stopped,age_days:${form.stoppedAgeDays}`;
    case 'ttl_expired':
      return 'ttl_expired';
    case 'custom':
      return form.customCondition;
    default:
      return '';
  }
}

function parseConditionToForm(condition: string): Pick<PolicyFormState, 'conditionPreset' | 'idleDays' | 'stoppedAgeDays' | 'customCondition'> {
  if (condition === 'ttl_expired') {
    return { conditionPreset: 'ttl_expired', idleDays: '7', stoppedAgeDays: '14', customCondition: '' };
  }
  const idleMatch = condition.match(/^idle_days:(\d+)$/);
  if (idleMatch) {
    return { conditionPreset: 'idle_days', idleDays: idleMatch[1], stoppedAgeDays: '14', customCondition: '' };
  }
  const stoppedMatch = condition.match(/^status:stopped,age_days:(\d+)$/);
  if (stoppedMatch) {
    return { conditionPreset: 'stopped_age', idleDays: '7', stoppedAgeDays: stoppedMatch[1], customCondition: '' };
  }
  return { conditionPreset: 'custom', idleDays: '7', stoppedAgeDays: '14', customCondition: condition };
}

function formatCondition(condition: string): string {
  if (condition === 'ttl_expired') return 'TTL expired';
  const idleMatch = condition.match(/^idle_days:(\d+)$/);
  if (idleMatch) return `Idle > ${idleMatch[1]} days`;
  const stoppedMatch = condition.match(/^status:stopped,age_days:(\d+)$/);
  if (stoppedMatch) return `Stopped, Age > ${stoppedMatch[1]} days`;
  return condition;
}

function describeCron(cron: string): string {
  const parts = cron.trim().split(/\s+/);
  if (parts.length !== 5) return cron;
  const [min, hour, dom, mon, dow] = parts;
  if (dom === '*' && mon === '*' && dow === '*') {
    return `Daily at ${hour}:${min.padStart(2, '0')}`;
  }
  if (dom === '*' && mon === '*' && dow !== '*') {
    const days = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
    const dayName = days[parseInt(dow)] ?? dow;
    return `Weekly on ${dayName} at ${hour}:${min.padStart(2, '0')}`;
  }
  return cron;
}

function timeAgo(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

const CleanupPolicies = () => {
  const [policies, setPolicies] = useState<CleanupPolicy[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Create/Edit dialog
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<PolicyFormState>(emptyForm);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // Delete confirmation
  const [deleteTarget, setDeleteTarget] = useState<CleanupPolicy | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Run results dialog
  const [runTarget, setRunTarget] = useState<CleanupPolicy | null>(null);
  const [runResults, setRunResults] = useState<CleanupResult[] | null>(null);
  const [running, setRunning] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);

  // Snackbar
  const [snackbar, setSnackbar] = useState<{ open: boolean; message: string; severity: 'success' | 'error' }>({
    open: false,
    message: '',
    severity: 'success',
  });

  const fetchData = useCallback(async () => {
    setError(null);
    try {
      const [policiesData, clustersData] = await Promise.all([
        cleanupPolicyService.list(),
        clusterService.list(),
      ]);
      setPolicies(policiesData);
      setClusters(clustersData);
    } catch {
      setError('Failed to load cleanup policies');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const clusterName = (clusterId: string): string => {
    if (clusterId === 'all') return 'All Clusters';
    return clusters.find((c) => c.id === clusterId)?.name ?? clusterId;
  };

  const openCreateDialog = () => {
    setEditingId(null);
    setForm(emptyForm);
    setFormError(null);
    setDialogOpen(true);
  };

  const openEditDialog = (policy: CleanupPolicy) => {
    setEditingId(policy.id);
    const condParts = parseConditionToForm(policy.condition);
    setForm({
      name: policy.name,
      cluster_id: policy.cluster_id,
      action: policy.action,
      ...condParts,
      schedule: policy.schedule,
      enabled: policy.enabled,
      dry_run: policy.dry_run,
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
    const condition = buildConditionString(form);
    if (!condition) {
      setFormError('Condition is required');
      return;
    }
    if (!form.schedule.trim()) {
      setFormError('Schedule is required');
      return;
    }

    setSaving(true);
    setFormError(null);
    try {
      const payload: Partial<CleanupPolicy> = {
        name: form.name.trim(),
        cluster_id: form.cluster_id,
        action: form.action,
        condition,
        schedule: form.schedule.trim(),
        enabled: form.enabled,
        dry_run: form.dry_run,
      };
      if (editingId) {
        await cleanupPolicyService.update(editingId, payload);
        setSnackbar({ open: true, message: 'Policy updated', severity: 'success' });
      } else {
        await cleanupPolicyService.create(payload);
        setSnackbar({ open: true, message: 'Policy created', severity: 'success' });
      }
      setDialogOpen(false);
      setEditingId(null);
      await fetchData();
    } catch {
      setFormError('Failed to save policy');
    } finally {
      setSaving(false);
    }
  };

  const handleToggleEnabled = async (policy: CleanupPolicy) => {
    try {
      await cleanupPolicyService.update(policy.id, { enabled: !policy.enabled });
      setPolicies((prev) =>
        prev.map((p) => (p.id === policy.id ? { ...p, enabled: !p.enabled } : p)),
      );
    } catch {
      setSnackbar({ open: true, message: 'Failed to toggle policy', severity: 'error' });
    }
  };

  const handleDeleteClick = (policy: CleanupPolicy) => {
    setDeleteTarget(policy);
  };

  const handleDeleteConfirm = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await cleanupPolicyService.delete(deleteTarget.id);
      setSnackbar({ open: true, message: 'Policy deleted', severity: 'success' });
      setDeleteTarget(null);
      await fetchData();
    } catch {
      setSnackbar({ open: true, message: 'Failed to delete policy', severity: 'error' });
    } finally {
      setDeleting(false);
    }
  };

  const handleRunClick = (policy: CleanupPolicy) => {
    setRunTarget(policy);
    setRunResults(null);
    setRunError(null);
  };

  const handleRunExecute = async (dryRun: boolean) => {
    if (!runTarget) return;
    setRunning(true);
    setRunError(null);
    try {
      const results = await cleanupPolicyService.run(runTarget.id, dryRun);
      setRunResults(results);
      if (!dryRun) {
        await fetchData();
      }
    } catch {
      setRunError('Failed to run cleanup policy');
    } finally {
      setRunning(false);
    }
  };

  const handleRunClose = () => {
    setRunTarget(null);
    setRunResults(null);
    setRunError(null);
  };

  const statusColor = (status: string): 'success' | 'error' | 'info' => {
    if (status === 'success') return 'success';
    if (status === 'error') return 'error';
    return 'info';
  };

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Cleanup Policies
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={openCreateDialog}>
          Add Policy
        </Button>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading && (
        <Box sx={{ display: 'flex', justifyContent: 'center', my: 4 }}>
          <CircularProgress />
        </Box>
      )}

      {!loading && !error && policies.length === 0 && (
        <Alert severity="info">No cleanup policies configured. Create one to automate instance lifecycle management.</Alert>
      )}

      {!loading && policies.length > 0 && (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Cluster</TableCell>
                <TableCell>Action</TableCell>
                <TableCell>Condition</TableCell>
                <TableCell>Schedule</TableCell>
                <TableCell>Enabled</TableCell>
                <TableCell>Dry Run</TableCell>
                <TableCell>Last Run</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {policies.map((policy) => (
                <TableRow key={policy.id}>
                  <TableCell>{policy.name}</TableCell>
                  <TableCell>
                    {policy.cluster_id === 'all' ? (
                      <Chip label="All Clusters" size="small" variant="outlined" />
                    ) : (
                      clusterName(policy.cluster_id)
                    )}
                  </TableCell>
                  <TableCell>
                    <Chip
                      label={policy.action}
                      size="small"
                      color={actionColors[policy.action] ?? 'default'}
                    />
                  </TableCell>
                  <TableCell>{formatCondition(policy.condition)}</TableCell>
                  <TableCell>
                    <Tooltip title={policy.schedule}>
                      <Typography variant="body2">{describeCron(policy.schedule)}</Typography>
                    </Tooltip>
                  </TableCell>
                  <TableCell>
                    <Switch
                      checked={policy.enabled}
                      onChange={() => handleToggleEnabled(policy)}
                      size="small"
                      inputProps={{ 'aria-label': `Toggle ${policy.name}` }}
                    />
                  </TableCell>
                  <TableCell>
                    <Checkbox checked={policy.dry_run} disabled size="small" />
                  </TableCell>
                  <TableCell>
                    {policy.last_run_at ? timeAgo(policy.last_run_at) : 'Never'}
                  </TableCell>
                  <TableCell align="right">
                    <Tooltip title="Run Now">
                      <IconButton size="small" onClick={() => handleRunClick(policy)} aria-label={`Run ${policy.name}`}>
                        <PlayArrowIcon />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Edit">
                      <IconButton size="small" onClick={() => openEditDialog(policy)} aria-label={`Edit ${policy.name}`}>
                        <EditIcon />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Delete">
                      <IconButton size="small" onClick={() => handleDeleteClick(policy)} aria-label={`Delete ${policy.name}`}>
                        <DeleteIcon />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Create/Edit Dialog */}
      <Dialog open={dialogOpen} onClose={handleDialogClose} maxWidth="sm" fullWidth>
        <DialogTitle>{editingId ? 'Edit Policy' : 'Create Cleanup Policy'}</DialogTitle>
        <DialogContent>
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
            {formError && <Alert severity="error">{formError}</Alert>}
            <TextField
              label="Name"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              fullWidth
              required
            />
            <FormControl fullWidth>
              <InputLabel id="cluster-label">Cluster</InputLabel>
              <Select
                labelId="cluster-label"
                value={form.cluster_id}
                label="Cluster"
                onChange={(e: SelectChangeEvent) => setForm({ ...form, cluster_id: e.target.value })}
              >
                <MenuItem value="all">All Clusters</MenuItem>
                {clusters.map((c) => (
                  <MenuItem key={c.id} value={c.id}>{c.name}</MenuItem>
                ))}
              </Select>
            </FormControl>
            <FormControl fullWidth>
              <InputLabel id="action-label">Action</InputLabel>
              <Select
                labelId="action-label"
                value={form.action}
                label="Action"
                onChange={(e: SelectChangeEvent) => setForm({ ...form, action: e.target.value })}
              >
                {Object.entries(actionDescriptions).map(([key, desc]) => (
                  <MenuItem key={key} value={key}>{key.charAt(0).toUpperCase() + key.slice(1)} — {desc}</MenuItem>
                ))}
              </Select>
            </FormControl>

            {/* Condition builder */}
            <FormControl fullWidth>
              <InputLabel id="condition-preset-label">Condition</InputLabel>
              <Select
                labelId="condition-preset-label"
                value={form.conditionPreset}
                label="Condition"
                onChange={(e: SelectChangeEvent) =>
                  setForm({ ...form, conditionPreset: e.target.value as ConditionPreset })
                }
              >
                <MenuItem value="idle_days">Idle for X days</MenuItem>
                <MenuItem value="stopped_age">Stopped for X days</MenuItem>
                <MenuItem value="ttl_expired">TTL expired</MenuItem>
                <MenuItem value="custom">Custom</MenuItem>
              </Select>
            </FormControl>

            {form.conditionPreset === 'idle_days' && (
              <TextField
                label="Idle Days"
                type="number"
                value={form.idleDays}
                onChange={(e) => setForm({ ...form, idleDays: e.target.value })}
                inputProps={{ min: 1 }}
                helperText={`Matches instances idle for more than ${form.idleDays} days`}
              />
            )}
            {form.conditionPreset === 'stopped_age' && (
              <TextField
                label="Days Since Stopped"
                type="number"
                value={form.stoppedAgeDays}
                onChange={(e) => setForm({ ...form, stoppedAgeDays: e.target.value })}
                inputProps={{ min: 1 }}
                helperText={`Matches stopped instances older than ${form.stoppedAgeDays} days`}
              />
            )}
            {form.conditionPreset === 'ttl_expired' && (
              <Alert severity="info" sx={{ py: 0.5 }}>
                Matches instances whose TTL has expired.
              </Alert>
            )}
            {form.conditionPreset === 'custom' && (
              <TextField
                label="Custom Condition"
                value={form.customCondition}
                onChange={(e) => setForm({ ...form, customCondition: e.target.value })}
                fullWidth
                helperText="e.g., idle_days:3 or status:stopped,age_days:7"
              />
            )}

            <Typography variant="body2" color="text.secondary">
              Preview: <strong>{formatCondition(buildConditionString(form))}</strong>
            </Typography>

            <TextField
              label="Schedule (Cron)"
              value={form.schedule}
              onChange={(e) => setForm({ ...form, schedule: e.target.value })}
              fullWidth
              required
              helperText={`${describeCron(form.schedule)} — Format: minute hour day month weekday`}
            />

            <Box sx={{ display: 'flex', gap: 2 }}>
              <FormControlLabel
                control={
                  <Switch
                    checked={form.enabled}
                    onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                  />
                }
                label="Enabled"
              />
              <FormControlLabel
                control={
                  <Switch
                    checked={form.dry_run}
                    onChange={(e) => setForm({ ...form, dry_run: e.target.checked })}
                  />
                }
                label="Dry Run"
              />
            </Box>
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={handleDialogClose}>Cancel</Button>
          <Button variant="contained" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving…' : editingId ? 'Update' : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogTitle>Delete Policy</DialogTitle>
        <DialogContent>
          <Typography>
            Are you sure you want to delete the policy &quot;{deleteTarget?.name}&quot;?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button color="error" variant="contained" onClick={handleDeleteConfirm} disabled={deleting}>
            {deleting ? 'Deleting…' : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Run Results Dialog */}
      <Dialog open={!!runTarget} onClose={handleRunClose} maxWidth="md" fullWidth>
        <DialogTitle>Run Policy: {runTarget?.name}</DialogTitle>
        <DialogContent>
          {!runResults && !running && !runError && (
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
              <Typography>Choose how to run this cleanup policy:</Typography>
              <Box sx={{ display: 'flex', gap: 2 }}>
                <Button variant="outlined" onClick={() => handleRunExecute(true)} disabled={running}>
                  Dry Run
                </Button>
                <Button variant="contained" color="warning" onClick={() => handleRunExecute(false)} disabled={running}>
                  Live Run
                </Button>
              </Box>
            </Box>
          )}

          {running && (
            <Box sx={{ display: 'flex', justifyContent: 'center', my: 4 }}>
              <CircularProgress />
            </Box>
          )}

          {runError && <Alert severity="error" sx={{ mt: 1 }}>{runError}</Alert>}

          {runResults && (
            <Box sx={{ mt: 1 }}>
              {runResults.length === 0 ? (
                <Alert severity="info">No instances matched this policy.</Alert>
              ) : (
                <TableContainer component={Paper} variant="outlined">
                  <Table size="small">
                    <TableHead>
                      <TableRow>
                        <TableCell>Instance</TableCell>
                        <TableCell>Namespace</TableCell>
                        <TableCell>Action</TableCell>
                        <TableCell>Status</TableCell>
                        <TableCell>Error</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {runResults.map((result) => (
                        <TableRow key={result.instance_id}>
                          <TableCell>{result.instance_name}</TableCell>
                          <TableCell>{result.namespace}</TableCell>
                          <TableCell>{result.action}</TableCell>
                          <TableCell>
                            <Chip
                              label={result.status}
                              size="small"
                              color={statusColor(result.status)}
                            />
                          </TableCell>
                          <TableCell>
                            {result.error && (
                              <Typography variant="body2" color="error">
                                {result.error}
                              </Typography>
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </TableContainer>
              )}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={handleRunClose}>Close</Button>
        </DialogActions>
      </Dialog>

      {/* Snackbar */}
      <Snackbar
        open={snackbar.open}
        autoHideDuration={4000}
        onClose={() => setSnackbar({ ...snackbar, open: false })}
        message={snackbar.message}
      />
    </Box>
  );
};

export default CleanupPolicies;
