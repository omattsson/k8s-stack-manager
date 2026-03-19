import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
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
} from '@mui/material';
import StatusBadge from '../../components/StatusBadge';
import BranchSelector from '../../components/BranchSelector';
import ConfirmDialog from '../../components/ConfirmDialog';
import DeploymentLogViewer from '../../components/DeploymentLogViewer';
import PodStatusDisplay from '../../components/PodStatusDisplay';
import { instanceService, definitionService } from '../../api/client';
import type { StackInstance, ChartConfig, ValueOverride, DeploymentLog, NamespaceStatus } from '../../types';
import YamlEditor from '../../components/YamlEditor';

const Detail = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const [instance, setInstance] = useState<StackInstance | null>(null);
  const [charts, setCharts] = useState<ChartConfig[]>([]);
  const [, setOverrides] = useState<ValueOverride[]>([]);
  const [branch, setBranch] = useState('');
  const [activeTab, setActiveTab] = useState(0);
  const [editedOverrides, setEditedOverrides] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [snackbar, setSnackbar] = useState<string | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deploying, setDeploying] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [deployLogs, setDeployLogs] = useState<DeploymentLog[]>([]);
  const [k8sStatus, setK8sStatus] = useState<NamespaceStatus | null>(null);
  const [statusLoading, setStatusLoading] = useState(false);

  useEffect(() => {
    if (!id) return;
    const fetchData = async () => {
      try {
        const inst = await instanceService.get(id);
        setInstance(inst);
        setBranch(inst.branch);

        const [defData, overrideData] = await Promise.all([
          definitionService.get(inst.stack_definition_id),
          instanceService.getOverrides(id),
        ]);
        setCharts(defData.charts || []);
        setOverrides(overrideData || []);

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
        if (inst.status === 'running' || inst.status === 'deploying' || inst.status === 'error') {
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
        await instanceService.setOverride(id, {
          chart_config_id: chartConfigId,
          values,
        });
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
      // Refresh instance to get new status
      const inst = await instanceService.get(id);
      setInstance(inst);
      // Refresh logs
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch {
      setError('Failed to start deployment');
    } finally {
      setDeploying(false);
    }
  };

  const handleStop = async () => {
    if (!id) return;
    setStopping(true);
    setError(null);
    try {
      await instanceService.stop(id);
      setSnackbar('Stop initiated');
      const inst = await instanceService.get(id);
      setInstance(inst);
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch {
      setError('Failed to stop instance');
    } finally {
      setStopping(false);
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
              <StatusBadge status={instance.status} />
            </Box>
            <Typography variant="body2" color="text.secondary">
              Namespace: {instance.namespace}
            </Typography>
            <Typography variant="body2" color="text.secondary">
              Owner: {instance.owner_id}
            </Typography>
          </Box>
          <Box sx={{ display: 'flex', gap: 1 }}>
            {(instance.status === 'draft' || instance.status === 'stopped' || instance.status === 'error') && (
              <Button variant="contained" color="success" onClick={handleDeploy} disabled={deploying}>
                {deploying ? 'Deploying...' : 'Deploy'}
              </Button>
            )}
            {(instance.status === 'running' || instance.status === 'deploying') && (
              <Button variant="contained" color="warning" onClick={handleStop} disabled={stopping}>
                {stopping ? 'Stopping...' : 'Stop'}
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

          return (
            <Box sx={{ mb: 2 }}>
              <Typography variant="subtitle2" gutterBottom>Status Lifecycle</Typography>
              {isError || isStopped ? (
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

        {(instance.status === 'running' || instance.status === 'deploying' || instance.status === 'error') && (
          <Box sx={{ mb: 2 }}>
            <Typography variant="subtitle2" gutterBottom>Cluster Resources</Typography>
            <PodStatusDisplay status={k8sStatus} loading={statusLoading} />
          </Box>
        )}

        <Box sx={{ maxWidth: 400 }}>
          <Typography variant="subtitle2" gutterBottom>Branch</Typography>
          <BranchSelector
            repoUrl={getRepoUrl()}
            value={branch}
            onChange={setBranch}
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
        <Paper sx={{ p: 3, mb: 3 }}>
          <Typography variant="h6" gutterBottom>Deployment History</Typography>
          <DeploymentLogViewer logs={deployLogs} />
        </Paper>
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
