import { useEffect, useState, useCallback } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useWebSocket } from '../../hooks/useWebSocket';
import type { WsMessage } from '../../hooks/useWebSocket';
import {
  Box,
  Typography,
  Button,
  Paper,
  CircularProgress,
  Alert,
  Tabs,
  Tab,
  Divider,
  Snackbar,
  Stepper,
  Step,
  StepLabel,
  Grid,
  Chip,
  Tooltip,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import StatusBadge from '../../components/StatusBadge';
import BranchSelector from '../../components/BranchSelector';
import ConfirmDialog from '../../components/ConfirmDialog';
import DeploymentLogViewer from '../../components/DeploymentLogViewer';
import PodStatusDisplay from '../../components/PodStatusDisplay';
import AccessUrls from '../../components/AccessUrls';
import FavoriteButton from '../../components/FavoriteButton';
import { instanceService, definitionService, branchOverrideService } from '../../api/client';
import type { StackInstance, ChartConfig, ValueOverride, DeploymentLog, NamespaceStatus } from '../../types';
import YamlEditor from '../../components/YamlEditor';
import TtlSelector from '../../components/TtlSelector';
import useCountdown from '../../hooks/useCountdown';

const Detail = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const [instance, setInstance] = useState<StackInstance | null>(null);
  const [charts, setCharts] = useState<ChartConfig[]>([]);
  const [, setOverrides] = useState<ValueOverride[]>([]);
  const [branch, setBranch] = useState('');
  const [branchOverrides, setBranchOverrides] = useState<Record<string, string>>({});
  const [activeTab, setActiveTab] = useState(0);
  const [editedOverrides, setEditedOverrides] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [snackbar, setSnackbar] = useState<string | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deploying, setDeploying] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [cleaning, setCleaning] = useState(false);
  const [cleanDialogOpen, setCleanDialogOpen] = useState(false);
  const [deployLogs, setDeployLogs] = useState<DeploymentLog[]>([]);
  const [k8sStatus, setK8sStatus] = useState<NamespaceStatus | null>(null);
  const [statusLoading, setStatusLoading] = useState(false);
  const [extending, setExtending] = useState(false);

  useEffect(() => {
    if (!id) return;
    const fetchData = async () => {
      setDeployLogs([]);
      setK8sStatus(null);
      try {
        const inst = await instanceService.get(id);
        setInstance(inst);
        setBranch(inst.branch);

        const [defData, overrideData, branchOverrideData] = await Promise.all([
          definitionService.get(inst.stack_definition_id),
          instanceService.getOverrides(id),
          branchOverrideService.list(id),
        ]);
        setCharts(defData.charts || []);
        setOverrides(overrideData || []);

        // Pre-populate branch overrides map (chartConfigId → branch)
        const boMap: Record<string, string> = {};
        (branchOverrideData || []).forEach((bo) => {
          boMap[bo.chart_config_id] = bo.branch;
        });
        setBranchOverrides(boMap);

        // Pre-populate edited overrides with existing values
        const overrideMap: Record<string, string> = {};
        (overrideData || []).forEach((o: ValueOverride) => {
          overrideMap[o.chart_config_id] = o.values;
        });
        setEditedOverrides(overrideMap);

        // Fetch deployment logs
        try {
          const logs = await instanceService.getDeployLog(id);
          setDeployLogs(logs);
        } catch { /* ignore — no logs yet */ }

        // Fetch K8s status if instance is running or deploying
        if (inst.status === 'running' || inst.status === 'deploying' || inst.status === 'error' || inst.status === 'stopping' || inst.status === 'cleaning') {
          try {
            setStatusLoading(true);
            const status = await instanceService.getStatus(id);
            setK8sStatus(status);
          } catch { /* ignore */ }
          finally { setStatusLoading(false); }
        }
      } catch {
        setError('Failed to load instance details');
      } finally {
        setLoading(false);
      }
    };
    fetchData();
  }, [id]);

  // Live-update instance status and deploy logs via WebSocket.
  const handleWsMessage = useCallback((msg: WsMessage) => {
    if (!id) return;
    const payload = msg.payload as { instance_id?: string; status?: string };
    if (payload.instance_id !== id) return;

    if (msg.type === 'deployment.status') {
      // Refresh instance data and K8s status when deployment status changes.
      const newStatus = payload.status as string;
      setInstance((prev) => prev ? { ...prev, status: newStatus } : prev);

      // Clear stale K8s status at the start of any operation or when resources are gone.
      // The watcher will push fresh status via instance.status messages as pods come up.
      if (newStatus === 'deploying' || newStatus === 'stopping' || newStatus === 'cleaning' || newStatus === 'stopped' || newStatus === 'draft') {
        setK8sStatus(null);
      }

      // Fetch current K8s status for terminal states where resources may exist.
      if (newStatus === 'running' || newStatus === 'error') {
        instanceService.getStatus(id).then(setK8sStatus).catch(() => {});
      }

      // Refresh deploy logs on terminal states.
      if (newStatus === 'running' || newStatus === 'stopped' || newStatus === 'error' || newStatus === 'draft') {
        instanceService.getDeployLog(id).then(setDeployLogs).catch(() => {});
        setDeploying(false);
        setStopping(false);
        setCleaning(false);
      }
    }

    // Live K8s status updates from the watcher (pod state changes, etc.).
    if (msg.type === 'instance.status') {
      const nsPayload = msg.payload as { instance_id?: string; namespace_status?: NamespaceStatus };
      if (nsPayload.namespace_status) {
        setK8sStatus(nsPayload.namespace_status);
      }
    }

  }, [id]);

  useWebSocket(handleWsMessage);

  const handleChartBranchChange = async (chartId: string, newBranch: string) => {
    if (!id) return;
    // If the new branch matches the instance-level branch (or is empty), remove the override
    if (!newBranch || newBranch === branch) {
      const previousValue = branchOverrides[chartId];
      setBranchOverrides((prev) => {
        const next = { ...prev };
        delete next[chartId];
        return next;
      });
      try {
        await branchOverrideService.delete(id, chartId);
      } catch {
        // Restore previous override on failure
        if (previousValue !== undefined) {
          setBranchOverrides((prev) => ({ ...prev, [chartId]: previousValue }));
        }
        setError('Failed to remove branch override');
      }
    } else {
      const previousValue = branchOverrides[chartId];
      // Optimistic update
      setBranchOverrides((prev) => ({ ...prev, [chartId]: newBranch }));
      try {
        await branchOverrideService.set(id, chartId, newBranch);
        setSnackbar('Branch override saved');
      } catch {
        // Revert to previous value on failure
        if (previousValue !== undefined) {
          setBranchOverrides((prev) => ({ ...prev, [chartId]: previousValue }));
        } else {
          setBranchOverrides((prev) => {
            const next = { ...prev };
            delete next[chartId];
            return next;
          });
        }
        setError('Failed to set branch override');
      }
    }
  };

  const handleSave = async () => {
    if (!id || !instance) return;
    setSaving(true);
    setError(null);
    try {
      // Update branch if changed
      if (branch !== instance.branch) {
        await instanceService.update(id, { branch });
        setInstance({ ...instance, branch });
      }

      // Save overrides
      for (const [chartConfigId, values] of Object.entries(editedOverrides)) {
        await instanceService.setOverride(id, chartConfigId, { values });
      }

      setSnackbar('Changes saved successfully');
    } catch {
      setError('Failed to save changes');
    } finally {
      setSaving(false);
    }
  };

  const handleClone = async () => {
    if (!id) return;
    try {
      const cloned = await instanceService.clone(id);
      navigate(`/stack-instances/${cloned.id}`);
    } catch {
      setError('Failed to clone instance');
    }
  };

  const handleDelete = async () => {
    if (!id) return;
    try {
      await instanceService.delete(id);
      navigate('/');
    } catch {
      setError('Failed to delete instance');
    }
    setDeleteOpen(false);
  };

  const handleExport = async () => {
    if (!id) return;
    try {
      const values = await instanceService.exportValues(id);
      const blob = new Blob([typeof values === 'string' ? values : JSON.stringify(values, null, 2)], { type: 'text/yaml' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${instance?.name || 'values'}-export.yaml`;
      a.click();
      URL.revokeObjectURL(url);
    } catch {
      setError('Failed to export values');
    }
  };

  const handleDeploy = async () => {
    if (!id) return;
    setDeploying(true);
    setError(null);
    try {
      await instanceService.deploy(id);
      setSnackbar('Deployment started');
    } catch {
      setError('Failed to start deployment');
      return;
    } finally {
      setDeploying(false);
    }
    // Best-effort refresh — don't surface errors to the user
    try {
      const inst = await instanceService.get(id);
      setInstance(inst);
    } catch (e) { console.error('Failed to refresh instance after deploy', e); }
    try {
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch (e) { console.error('Failed to refresh deploy logs after deploy', e); }
  };

  const handleStop = async () => {
    if (!id) return;
    setStopping(true);
    setError(null);
    try {
      await instanceService.stop(id);
      setSnackbar('Stop initiated');
    } catch {
      setError('Failed to stop instance');
      setStopping(false);
      return;
    }
    // Best-effort refresh — don't surface errors to the user
    try {
      const inst = await instanceService.get(id);
      setInstance(inst);
    } catch (e) { console.error('Failed to refresh instance after stop', e); }
    try {
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch (e) { console.error('Failed to refresh deploy logs after stop', e); }
  };

  const handleClean = async () => {
    if (!id) return;
    setCleaning(true);
    setError(null);
    try {
      await instanceService.clean(id);
      setSnackbar('Namespace cleanup initiated');
    } catch {
      setError('Failed to clean namespace');
      setCleaning(false);
      return;
    }
    // Best-effort refresh — don't surface errors to the user
    try {
      const inst = await instanceService.get(id);
      setInstance(inst);
    } catch (e) { console.error('Failed to refresh instance after clean', e); }
    try {
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch (e) { console.error('Failed to refresh deploy logs after clean', e); }
  };

  const countdown = useCountdown(instance?.expires_at);

  const isExpiredByTtl = instance?.status === 'stopped' && 
    instance?.expires_at != null && 
    new Date(instance.expires_at) <= new Date();

  const handleExtend = async () => {
    if (!id) return;
    setExtending(true);
    try {
      const updated = await instanceService.extend(id);
      setInstance(updated);
      setSnackbar('TTL extended');
    } catch {
      setError('Failed to extend TTL');
    } finally {
      setExtending(false);
    }
  };

  const handleTtlChange = async (ttlMinutes: number) => {
    if (!id || !instance) return;
    setSaving(true);
    setError(null);
    try {
      let updated: StackInstance;
      if (ttlMinutes > 0) {
        updated = await instanceService.extend(id, ttlMinutes);
      } else {
        // Clear TTL — send full instance so required fields are preserved
        updated = await instanceService.update(id, { ...instance, ttl_minutes: 0 });
      }
      setInstance(updated);
      setSnackbar('TTL updated');
    } catch {
      setError('Failed to update TTL');
    } finally {
      setSaving(false);
    }
  };

  const getRepoUrl = (): string => {
    if (charts.length > 0 && charts[0].source_repo_url) {
      return charts[0].source_repo_url;
    }
    return '';
  };

  if (loading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
        <CircularProgress />
      </Box>
    );
  }

  if (error && !instance) {
    return <Alert severity="error">{error}</Alert>;
  }

  if (!instance) return null;

  return (
    <Box>
      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      <Paper sx={{ p: 3, mb: 3 }}>
        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
          <Box>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, mb: 1 }}>
              <Typography variant="h4" component="h1">
                {instance.name}
              </Typography>
              <FavoriteButton entityType="instance" entityId={instance.id} size="medium" />
              <StatusBadge status={instance.status} />
              {isExpiredByTtl && (
                <Chip label="Expired" color="error" size="small" />
              )}
            </Box>
            <Typography variant="body2" color="text.secondary">
              Namespace: {instance.namespace}
            </Typography>
            <Typography variant="body2" color="text.secondary">
              Owner: {instance.owner_id}
            </Typography>
            {countdown && !countdown.isExpired && instance.status === 'running' && (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 0.5 }}>
                <Chip
                  label={`Expires in ${countdown.remaining}`}
                  size="small"
                  color={countdown.isCritical ? 'error' : countdown.isWarning ? 'warning' : 'success'}
                  icon={<span>⏱</span>}
                />
                <Button
                  variant="outlined"
                  size="small"
                  onClick={handleExtend}
                  disabled={extending}
                >
                  {extending ? 'Extending...' : 'Extend'}
                </Button>
              </Box>
            )}
          </Box>
          <Box sx={{ display: 'flex', gap: 1 }}>
            {(instance.status === 'draft' || instance.status === 'stopped' || instance.status === 'error') && (
              <Button variant="contained" color="success" onClick={handleDeploy} disabled={deploying}>
                {deploying ? 'Deploying...' : 'Deploy'}
              </Button>
            )}
            {instance.status === 'stopping' && (
              <Button variant="contained" color="warning" disabled>
                Stopping...
              </Button>
            )}
            {instance.status === 'cleaning' && (
              <Button variant="outlined" color="error" disabled>
                Cleaning...
              </Button>
            )}
            {(instance.status === 'running' || instance.status === 'deploying') && (
              <Button variant="contained" color="warning" onClick={handleStop} disabled={stopping}>
                {stopping ? 'Stopping...' : 'Stop'}
              </Button>
            )}
            {(instance.status === 'running' || instance.status === 'stopped' || instance.status === 'error') && (
              <Button variant="outlined" color="error" onClick={() => setCleanDialogOpen(true)} disabled={cleaning}>
                {cleaning ? 'Cleaning...' : 'Clean Namespace'}
              </Button>
            )}
            <Button variant="outlined" onClick={handleExport}>Export Values</Button>
            <Button variant="outlined" onClick={handleClone}>Clone</Button>
            <Button variant="outlined" color="error" onClick={() => setDeleteOpen(true)}>Delete</Button>
          </Box>
        </Box>

        <Divider sx={{ my: 2 }} />

        {(() => {
          const LIFECYCLE_STEPS = ['draft', 'deploying', 'running'];
          const activeStep = LIFECYCLE_STEPS.indexOf(instance.status);
          const isError = instance.status === 'error';
          const isStopped = instance.status === 'stopped';
          const isStopping = instance.status === 'stopping';
          const isCleaning = instance.status === 'cleaning';

          return (
            <Box sx={{ mb: 2 }}>
              <Typography variant="subtitle2" gutterBottom>Status Lifecycle</Typography>
              {isError || isStopped || isStopping || isCleaning ? (
                <Alert severity={isError ? 'error' : 'warning'} sx={{ py: 0.5 }}>
                  Instance is {instance.status}
                </Alert>
              ) : (
                <Stepper activeStep={activeStep} alternativeLabel>
                  {LIFECYCLE_STEPS.map((label) => (
                    <Step key={label} completed={activeStep > LIFECYCLE_STEPS.indexOf(label)}>
                      <StepLabel>{label}</StepLabel>
                    </Step>
                  ))}
                </Stepper>
              )}
            </Box>
          );
        })()}

        {(instance.status === 'running' || instance.status === 'deploying' || instance.status === 'error' || instance.status === 'stopping' || instance.status === 'cleaning') && (
          <Box sx={{ mb: 2 }}>
            <Typography variant="subtitle2" gutterBottom>Cluster Resources</Typography>
            <PodStatusDisplay status={k8sStatus} loading={statusLoading} />
          </Box>
        )}

        {k8sStatus && instance.status === 'running' && (
          <AccessUrls status={k8sStatus} />
        )}

        <Box sx={{ maxWidth: 400 }}>
          <Typography variant="subtitle2" gutterBottom>Branch</Typography>
          <BranchSelector
            repoUrl={getRepoUrl()}
            value={branch}
            onChange={setBranch}
          />
        </Box>

        <Box sx={{ mt: 2 }}>
          <Typography variant="subtitle2" gutterBottom>TTL (Time to Live)</Typography>
          <TtlSelector
            value={instance.ttl_minutes ?? 0}
            onChange={handleTtlChange}
            disabled={saving}
          />
        </Box>
      </Paper>

      {charts.length > 0 && (
        <Paper sx={{ mb: 3 }}>
          <Tabs value={activeTab} onChange={(_e, v: number) => setActiveTab(v)} variant="scrollable">
            {charts.map((chart) => (
              <Tab key={chart.id} label={chart.chart_name} />
            ))}
          </Tabs>
          <Box sx={{ p: 3 }}>
            {charts.map((chart, index) => (
              <Box key={chart.id} sx={{ display: activeTab === index ? 'block' : 'none' }}>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                  {chart.repository_url && `Repo: ${chart.repository_url}`}
                  {chart.chart_path && ` | Path: ${chart.chart_path}`}
                  {chart.chart_version && ` | Version: ${chart.chart_version}`}
                </Typography>

                <Box sx={{ mb: 2, display: 'flex', alignItems: 'center', gap: 2 }}>
                  <Box sx={{ maxWidth: 300, flex: 1 }}>
                    <BranchSelector
                      repoUrl={chart.source_repo_url || getRepoUrl()}
                      value={branchOverrides[chart.id] || branch}
                      onChange={(newBranch) => handleChartBranchChange(chart.id, newBranch)}
                      label="Chart Branch"
                    />
                  </Box>
                  {branchOverrides[chart.id] ? (
                    <Chip
                      label={`Override: ${branchOverrides[chart.id]}`}
                      color="warning"
                      size="small"
                      onDelete={() => handleChartBranchChange(chart.id, '')}
                      deleteIcon={
                        <Tooltip title="Reset to instance branch">
                          <CloseIcon />
                        </Tooltip>
                      }
                    />
                  ) : (
                    <Chip label="Using instance branch" size="small" variant="outlined" />
                  )}
                </Box>

                <Grid container spacing={2}>
                  <Grid size={{ xs: 12, md: 6 }}>
                    <YamlEditor
                      label="Default Values"
                      value={chart.default_values || ''}
                      onChange={() => {}}
                      readOnly={true}
                      height="300px"
                    />
                  </Grid>
                  <Grid size={{ xs: 12, md: 6 }}>
                    <YamlEditor
                      label="Your Overrides"
                      value={editedOverrides[chart.id] || ''}
                      onChange={(val) => setEditedOverrides({ ...editedOverrides, [chart.id]: val })}
                      height="300px"
                    />
                  </Grid>
                </Grid>
              </Box>
            ))}
          </Box>
        </Paper>
      )}

      {deployLogs.length > 0 && (
        <Box sx={{ mb: 3 }}>
          <Typography variant="h6" sx={{ mb: 1 }}>
            Deployment History ({deployLogs.length})
          </Typography>
          <DeploymentLogViewer logs={deployLogs} />
        </Box>
      )}

      <Box sx={{ display: 'flex', gap: 2 }}>
        <Button variant="contained" onClick={handleSave} disabled={saving}>
          {saving ? 'Saving...' : 'Save Changes'}
        </Button>
        <Button variant="outlined" onClick={() => navigate('/')}>
          Back to Dashboard
        </Button>
      </Box>

      <ConfirmDialog
        open={deleteOpen}
        title="Delete Instance"
        message={`Are you sure you want to delete "${instance.name}"? This action cannot be undone.`}
        onConfirm={handleDelete}
        onCancel={() => setDeleteOpen(false)}
        confirmText="Delete"
      />

      <ConfirmDialog
        open={cleanDialogOpen}
        title="Clean Namespace?"
        message="This will uninstall all Helm releases and delete the Kubernetes namespace. The instance will return to draft status. This action cannot be undone."
        onConfirm={() => { setCleanDialogOpen(false); handleClean(); }}
        onCancel={() => setCleanDialogOpen(false)}
        confirmText="Clean"
      />

      <Snackbar
        open={Boolean(snackbar)}
        autoHideDuration={3000}
        onClose={() => setSnackbar(null)}
        message={snackbar}
      />
    </Box>
  );
};

export default Detail;
