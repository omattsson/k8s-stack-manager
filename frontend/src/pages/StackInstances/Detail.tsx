import { useEffect, useState, useCallback, useRef, useMemo } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useWebSocket } from '../../hooks/useWebSocket';
import type { WsMessage, DeploymentStatusPayload } from '../../hooks/useWebSocket';
import {
  Box,
  Typography,
  Button,
  Paper,
  Alert,
  Tabs,
  Tab,
  Divider,
  Stepper,
  Step,
  StepLabel,
  Grid,
  Chip,
  Tooltip,
  Menu,
  MenuItem,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown';
import StatusBadge from '../../components/StatusBadge';
import BranchSelector from '../../components/BranchSelector';
import ConfirmDialog from '../../components/ConfirmDialog';
import DeployPreviewDialog from '../../components/DeployPreviewDialog';
import DeploymentLogViewer from '../../components/DeploymentLogViewer';
import PodStatusDisplay from '../../components/PodStatusDisplay';
import AccessUrls from '../../components/AccessUrls';
import FavoriteButton from '../../components/FavoriteButton';
import { instanceService, definitionService, branchOverrideService } from '../../api/client';
import type { RollbackResponse, StackInstance, ChartConfig, ValueOverride, ChartBranchOverride, DeploymentLog, NamespaceStatus } from '../../types';
import YamlEditor from '../../components/YamlEditor';
import TtlSelector from '../../components/TtlSelector';
import useCountdown from '../../hooks/useCountdown';
import { useNotification } from '../../context/NotificationContext';
import LoadingState from '../../components/LoadingState';
import { useUnsavedChanges } from '../../hooks/useUnsavedChanges';
import { useAuth } from '../../context/AuthContext';
import { canModifyInstance } from '../../utils/roles';
import { describeApiError } from '../../utils/apiError';
import { downloadBlob } from '../../utils/download';
import ExtendTtlMenu from '../../components/ExtendTtlMenu';
import { successfulDeploys, currentDeployLogId, defaultRollbackTarget } from '../../utils/deployHistory';
import CloneDialog from './CloneDialog';
import RollbackDialog from './RollbackDialog';

/** Statuses in which releases run, so a deploy is a redeploy and a rollback is possible. */
const DEPLOYED_STATUSES = new Set(['running', 'partial', 'error']);

const Detail = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user } = useAuth();
  const userId = user?.id;
  const userRole = user?.role;

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
  const { showSuccess } = useNotification();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [exportMenuAnchor, setExportMenuAnchor] = useState<HTMLElement | null>(null);
  const [deployPreviewOpen, setDeployPreviewOpen] = useState(false);
  const [deploying, setDeploying] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [cleaning, setCleaning] = useState(false);
  const [cleanDialogOpen, setCleanDialogOpen] = useState(false);
  const [deployLogs, setDeployLogs] = useState<DeploymentLog[]>([]);
  const [streamingLines, setStreamingLines] = useState<Record<string, string[]>>({});
  const streamingBufferRef = useRef<Record<string, string[]>>({});
  const flushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [k8sStatus, setK8sStatus] = useState<NamespaceStatus | null>(null);
  const [statusLoading, setStatusLoading] = useState(false);
  const [cloneOpen, setCloneOpen] = useState(false);
  const [rollbackOpen, setRollbackOpen] = useState(false);
  const [rollbackTargetId, setRollbackTargetId] = useState<string | undefined>(undefined);
  const [rollingBack, setRollingBack] = useState(false);
  const [savedPendingRedeploy, setSavedPendingRedeploy] = useState(false);
  // Counts deployment.status WebSocket messages. A fetched instance whose request
  // started before a newer WebSocket status must not overwrite that status.
  const wsStatusSeqRef = useRef(0);
  // The action the user started last. The backend reports a rollback with the
  // status "deploying", so the placeholder log uses this to name the action.
  const pendingActionRef = useRef<DeploymentLog['action'] | null>(null);
  const initialOverridesRef = useRef<Record<string, string>>({});
  const initialBranchRef = useRef('');
  const isDirty = useMemo(() =>
    branch !== initialBranchRef.current
    || JSON.stringify(editedOverrides) !== JSON.stringify(initialOverridesRef.current),
    [branch, editedOverrides]
  );

  useUnsavedChanges(isDirty);

  useEffect(() => {
    if (!id) return;
    const fetchData = async () => {
      setDeployLogs([]);
      setK8sStatus(null);
      try {
        const inst = await instanceService.get(id);
        setInstance(inst);
        setBranch(inst.branch);

        // Value and branch overrides are only readable by users who may modify
        // the instance (owner, admin, devops). Skip them for viewers to avoid 403s.
        const mayModify = userId !== undefined && userRole !== undefined
          && canModifyInstance({ id: userId, role: userRole }, inst);
        const [defData, overrideData, branchOverrideData] = await Promise.all([
          definitionService.get(inst.stack_definition_id),
          mayModify ? instanceService.getOverrides(id) : Promise.resolve([] as ValueOverride[]),
          mayModify ? branchOverrideService.list(id) : Promise.resolve([] as ChartBranchOverride[]),
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
        initialOverridesRef.current = { ...overrideMap };
        initialBranchRef.current = inst.branch;

        // Fetch deployment logs
        try {
          const logs = await instanceService.getDeployLog(id);
          setDeployLogs(logs);
        } catch { /* ignore — no logs yet */ }

        // Fetch pod health for active instances (includes container states + events).
        if (inst.status === 'running' || inst.status === 'partial' || inst.status === 'deploying' || inst.status === 'stabilizing' || inst.status === 'error' || inst.status === 'stopping' || inst.status === 'cleaning') {
          try {
            setStatusLoading(true);
            const status = await instanceService.getPods(id);
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
  }, [id, userId, userRole]);

  /**
   * Fetch the instance and store it. When a WebSocket status arrived while the
   * request ran, keep that newer status and its error message.
   * @param instanceId - Instance ID
   * @param adjust - Optional change to the fetched instance before it is stored
   */
  const refreshInstance = useCallback(async (
    instanceId: string,
    adjust?: (inst: StackInstance) => StackInstance,
  ) => {
    const seq = wsStatusSeqRef.current;
    const fetched = await instanceService.get(instanceId);
    const inst = adjust ? adjust(fetched) : fetched;
    setInstance((prev) => (prev && wsStatusSeqRef.current !== seq
      ? { ...inst, status: prev.status, error_message: prev.error_message }
      : inst));
  }, []);

  // Live-update instance status and deploy logs via WebSocket.
  const handleWsMessage = useCallback((msg: WsMessage) => {
    if (!id) return;
    const payload = msg.payload as DeploymentStatusPayload;
    if (payload.instance_id !== id) return;

    if (msg.type === 'deployment.status') {
      // Refresh instance data and K8s status when deployment status changes.
      const newStatus = payload.status as string;
      wsStatusSeqRef.current += 1;
      // The status message carries the error message of the instance (empty
      // when the operation clears it).
      setInstance((prev) => prev ? { ...prev, status: newStatus, error_message: payload.error_message || undefined } : prev);

      // Clear stale K8s status at the start of any operation or when resources are gone.
      // The watcher will push fresh status via instance.status messages as pods come up.
      if (newStatus === 'deploying' || newStatus === 'stopping' || newStatus === 'cleaning' || newStatus === 'stopped' || newStatus === 'draft') {
        setK8sStatus(null);
      }

      // Fetch current K8s status for terminal states where resources may exist.
      if (newStatus === 'running' || newStatus === 'partial' || newStatus === 'stabilizing' || newStatus === 'error') {
        instanceService.getPods(id).then(setK8sStatus).catch(() => {});
      }

      // On active states, insert a placeholder log entry so streaming lines
      // have an accordion to attach to before the REST refresh completes.
      const logId = payload.log_id;
      if ((newStatus === 'deploying' || newStatus === 'stabilizing' || newStatus === 'stopping' || newStatus === 'cleaning') && logId) {
        const actionMap: Record<string, DeploymentLog['action']> = {
          deploying: 'deploy', stopping: 'stop', cleaning: 'clean',
        };
        // A rollback also reports "deploying". Use the action from the message;
        // older servers omit it, then use the action the user started here.
        const action: DeploymentLog['action'] = payload.action
          ?? (newStatus === 'deploying' && pendingActionRef.current === 'rollback'
            ? 'rollback'
            : actionMap[newStatus] || 'deploy');
        setDeployLogs((prev) => {
          if (prev.some((l) => l.id === logId)) return prev;
          return [{
            id: logId,
            stack_instance_id: id,
            action,
            status: 'running' as const,
            output: '',
            started_at: new Date().toISOString(),
          }, ...prev];
        });
      }

      // Refresh deploy logs on terminal states and clear streaming lines.
      if (newStatus === 'running' || newStatus === 'partial' || newStatus === 'stopped' || newStatus === 'error' || newStatus === 'draft') {
        // Refetch the whole instance: fields such as values_drift change when
        // a rollback or a deploy finishes.
        refreshInstance(id).catch(() => {});
        instanceService.getDeployLog(id).then(setDeployLogs).catch(() => {});
        pendingActionRef.current = null;
        setStreamingLines({});
        streamingBufferRef.current = {};
        if (flushTimerRef.current) {
          clearTimeout(flushTimerRef.current);
          flushTimerRef.current = null;
        }
        setDeploying(false);
        setStopping(false);
        setCleaning(false);
        setRollingBack(false);
      }
    }

    // Real-time log line streaming from active deployments.
    // Lines are buffered and flushed in batches (50ms) to reduce GC pressure
    // and React re-renders during high-throughput output (e.g. helm --debug).
    if (msg.type === 'deployment.log') {
      const logPayload = msg.payload as { log_id?: string; line?: string };
      if (logPayload.log_id && logPayload.line !== undefined) {
        const buf = streamingBufferRef.current;
        if (!buf[logPayload.log_id!]) buf[logPayload.log_id!] = [];
        buf[logPayload.log_id!].push(logPayload.line!);

        if (!flushTimerRef.current) {
          flushTimerRef.current = setTimeout(() => {
            flushTimerRef.current = null;
            const pending = { ...streamingBufferRef.current };
            streamingBufferRef.current = {};
            setStreamingLines((prev) => {
              const MAX_STREAMING_LINES = 5000;
              const next = { ...prev };
              for (const [logId, newLines] of Object.entries(pending)) {
                const existing = next[logId] || [];
                const merged = [...existing, ...newLines];
                next[logId] = merged.length > MAX_STREAMING_LINES
                  ? merged.slice(merged.length - MAX_STREAMING_LINES)
                  : merged;
              }
              return next;
            });
          }, 50);
        }
      }
    }

    // Live K8s status updates from the watcher (pod state changes, etc.).
    // Merge with existing status to preserve events (watcher doesn't include them).
    if (msg.type === 'instance.status') {
      const nsPayload = msg.payload as { instance_id?: string; namespace_status?: NamespaceStatus };
      if (nsPayload.namespace_status) {
        setK8sStatus((prev) => {
          const incoming = nsPayload.namespace_status!;
          if (!prev || !prev.events?.length) return incoming;
          // Preserve events from the previous getPods() call.
          return { ...incoming, events: incoming.events?.length ? incoming.events : prev.events };
        });
      }
    }

  }, [id, refreshInstance]);

  const { send } = useWebSocket(handleWsMessage);

  useEffect(() => {
    if (!id) return;
    send('subscribe', { instance_id: id });
    return () => {
      send('unsubscribe', { instance_id: id });
    };
  }, [id, send]);

  const chartBranchTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({});

  const handleChartBranchChange = (chartId: string, newBranch: string) => {
    if (!id) return;
    setBranchOverrides((prev) => {
      if (!newBranch || newBranch === branch) {
        const next = { ...prev };
        delete next[chartId];
        return next;
      }
      return { ...prev, [chartId]: newBranch };
    });

    if (chartBranchTimers.current[chartId]) {
      clearTimeout(chartBranchTimers.current[chartId]);
    }

    chartBranchTimers.current[chartId] = setTimeout(async () => {
      delete chartBranchTimers.current[chartId];
      const effectiveBranch = newBranch;
      try {
        if (!effectiveBranch || effectiveBranch === branch) {
          await branchOverrideService.delete(id, chartId);
        } else {
          await branchOverrideService.set(id, chartId, effectiveBranch);
          showSuccess('Branch override saved');
        }
      } catch {
        setError('Failed to update branch override');
      }
    }, 800);
  };

  const handleSave = async () => {
    if (!id || !instance) return;
    setSaving(true);
    setError(null);
    const hadChanges = isDirty;
    try {
      // Update branch if changed
      if (branch !== instance.branch) {
        await instanceService.update(id, { branch });
        setInstance({ ...instance, branch });
      }
      initialBranchRef.current = branch;

      // Save changed overrides. A cleared editor deletes an existing
      // override instead of storing an empty one. The saved state is
      // updated per chart, so a retry after a partial failure only sends
      // what is still unsaved.
      const submitted = editedOverrides;
      const saved: Record<string, string> = { ...initialOverridesRef.current };
      for (const [chartConfigId, values] of Object.entries(submitted)) {
        const initial = saved[chartConfigId];
        if (values.trim() === '') {
          if (initial !== undefined) {
            try {
              await instanceService.deleteOverride(id, chartConfigId);
            } catch (err) {
              // 404: the override is already gone, which is the goal.
              if ((err as { response?: { status?: number } } | null)?.response?.status !== 404) throw err;
            }
            delete saved[chartConfigId];
            initialOverridesRef.current = { ...saved };
          }
          continue;
        }
        if (values !== initial) {
          await instanceService.setOverride(id, chartConfigId, { values });
          saved[chartConfigId] = values;
          initialOverridesRef.current = { ...saved };
        }
      }

      // Normalize the saved charts (drop cleared editors), but keep any
      // edit typed while the save request ran.
      setEditedOverrides((prev) => {
        const next = { ...prev };
        for (const [chartConfigId, values] of Object.entries(submitted)) {
          if (prev[chartConfigId] !== values) continue;
          if (chartConfigId in saved) {
            next[chartConfigId] = saved[chartConfigId];
          } else {
            delete next[chartConfigId];
          }
        }
        return next;
      });
      showSuccess('Changes saved successfully');
      if (hadChanges && DEPLOYED_STATUSES.has(instance.status)) setSavedPendingRedeploy(true);
    } catch (err) {
      setError(await describeApiError(err, 'Failed to save changes'));
    } finally {
      setSaving(false);
    }
  };

  const handleCloned = (cloned: StackInstance) => {
    setCloneOpen(false);
    navigate(`/stack-instances/${cloned.id}`);
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

  const handleExport = async (chart?: ChartConfig) => {
    setExportMenuAnchor(null);
    if (!id) return;
    const instanceName = instance?.name || id;
    try {
      const file = chart
        ? await instanceService.exportChartValues(id, chart.id, `${instanceName}-${chart.chart_name}-values.yaml`)
        : await instanceService.exportValues(id, instanceName);
      downloadBlob(file.blob, file.filename);
    } catch (err) {
      setError(await describeApiError(err, 'Failed to export values'));
    }
  };

  const handleDeploy = async () => {
    if (!id) return;
    pendingActionRef.current = 'deploy';
    setDeploying(true);
    setError(null);
    setSavedPendingRedeploy(false);
    try {
      await instanceService.deploy(id);
      showSuccess('Deployment started');
    } catch {
      setError('Failed to start deployment');
      return;
    } finally {
      setDeploying(false);
    }
    // Best-effort refresh — don't surface errors to the user
    try {
      await refreshInstance(id);
    } catch (e) { console.error('Failed to refresh instance after deploy', e); }
    try {
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch (e) { console.error('Failed to refresh deploy logs after deploy', e); }
  };

  const handleStop = async () => {
    if (!id) return;
    pendingActionRef.current = 'stop';
    setStopping(true);
    setError(null);
    try {
      await instanceService.stop(id);
      showSuccess('Stop initiated');
    } catch {
      setError('Failed to stop instance');
      setStopping(false);
      return;
    }
    // Best-effort refresh — don't surface errors to the user
    try {
      await refreshInstance(id);
    } catch (e) { console.error('Failed to refresh instance after stop', e); }
    try {
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch (e) { console.error('Failed to refresh deploy logs after stop', e); }
  };

  const handleClean = async () => {
    if (!id) return;
    pendingActionRef.current = 'clean';
    setCleaning(true);
    setError(null);
    try {
      await instanceService.clean(id);
      showSuccess('Namespace cleanup initiated');
    } catch {
      setError('Failed to clean namespace');
      setCleaning(false);
      return;
    }
    // Best-effort refresh — don't surface errors to the user
    try {
      await refreshInstance(id);
    } catch (e) { console.error('Failed to refresh instance after clean', e); }
    try {
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch (e) { console.error('Failed to refresh deploy logs after clean', e); }
  };

  const countdown = useCountdown(instance?.expires_at);

  const isExpiredByTtl = instance?.status === 'stopped' && (
    (instance?.expires_at != null && new Date(instance.expires_at) <= new Date()) ||
    instance?.error_message?.includes('Expired (TTL)')
  );

  const openRollback = (targetId?: string) => {
    setRollbackTargetId(targetId);
    setRollbackOpen(true);
  };

  const handleRollback = async (targetLogId: string) => {
    if (!id) return;
    setRollbackOpen(false);
    setRollingBack(true);
    setError(null);
    pendingActionRef.current = 'rollback';
    let rollbackResult: RollbackResponse | undefined;
    try {
      // The API accepts the rollback (202). A hook gate rejection shows in the
      // deployment log, not as an HTTP error.
      const result = await instanceService.rollback(id, targetLogId);
      rollbackResult = result;
      const started = 'Rollback started. Follow the deployment log.';
      showSuccess(result?.warning ? `${started} ${result.warning}` : started);
      if (result?.values_drift !== undefined) {
        setInstance((prev) => prev ? { ...prev, values_drift: result.values_drift } : prev);
      }
    } catch (err) {
      pendingActionRef.current = null;
      setError(await describeApiError(err, 'Failed to start rollback'));
      setRollingBack(false);
      return;
    }
    // Refetch once, in case the WebSocket misses the status change. While the
    // rollback runs, the API can still return the old values_drift, so keep
    // the value from the rollback response. The terminal WebSocket status
    // refetches the instance again.
    const responseDrift = rollbackResult?.values_drift;
    try {
      await refreshInstance(id, (inst) => (
        responseDrift !== undefined && inst.values_drift !== responseDrift
          ? { ...inst, values_drift: responseDrift }
          : inst
      ));
    } catch (e) { console.error('Failed to refresh instance after rollback', e); }
    // Best-effort log refresh — don't surface errors to the user
    try {
      const logs = await instanceService.getDeployLog(id);
      setDeployLogs(logs);
    } catch (e) { console.error('Failed to refresh deploy logs after rollback', e); }
  };

  const handleTtlChange = async (ttlMinutes: number) => {
    if (!id || !instance) return;
    setSaving(true);
    setError(null);
    try {
      let updated: StackInstance;
      if (ttlMinutes > 0) {
        // Sets the TTL and restarts the expiry from now.
        updated = await instanceService.update(id, { ttl_minutes: ttlMinutes });
      } else {
        // Clear TTL — send full instance so required fields are preserved
        updated = await instanceService.update(id, { ...instance, ttl_minutes: 0 });
      }
      setInstance(updated);
      showSuccess('TTL updated');
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

  const canModify = canModifyInstance(user, instance);

  const canDeploy = instance?.status === 'draft' || instance?.status === 'stopped';
  const canRedeploy = DEPLOYED_STATUSES.has(instance?.status ?? '');
  const deploys = useMemo(() => successfulDeploys(deployLogs), [deployLogs]);
  const currentLogId = useMemo(() => currentDeployLogId(deployLogs), [deployLogs]);
  const canRollback = canRedeploy && deploys.length >= 2;
  const canStop = instance?.status === 'running' || instance?.status === 'partial' || instance?.status === 'deploying' || instance?.status === 'stabilizing';
  const canClean = instance?.status === 'running' || instance?.status === 'partial' || instance?.status === 'stopped' || instance?.status === 'error';

  const renderStatusActions = (status: string) => (
    <>
      {(canDeploy || canRedeploy) && (
        <Button variant="contained" color="success" onClick={() => setDeployPreviewOpen(true)} disabled={deploying}>
          {deploying ? 'Deploying...' : canRedeploy ? 'Redeploy' : 'Deploy'}
        </Button>
      )}
      {canRollback && (
        <Button variant="outlined" color="warning" onClick={() => openRollback()} disabled={rollingBack}>
          {rollingBack ? 'Rolling back...' : 'Rollback'}
        </Button>
      )}
      {status === 'stopping' && (
        <Button variant="contained" color="warning" disabled>Stopping...</Button>
      )}
      {status === 'cleaning' && (
        <Button variant="outlined" color="error" disabled>Cleaning...</Button>
      )}
      {canStop && (
        <Button variant="contained" color="warning" onClick={handleStop} disabled={stopping}>
          {stopping ? 'Stopping...' : 'Stop'}
        </Button>
      )}
      {canClean && (
        <Button variant="outlined" color="error" onClick={() => setCleanDialogOpen(true)} disabled={cleaning}>
          {cleaning ? 'Cleaning...' : 'Clean Namespace'}
        </Button>
      )}
    </>
  );

  const LIFECYCLE_STEPS = ['draft', 'deploying', 'stabilizing', 'running'];

  /**
   * Render the status lifecycle. For an error or a partial deploy the alert
   * also shows the error message of the instance (for example the reason of
   * a pre-deploy hook that denied the deploy).
   */
  const renderLifecycle = (status: string, errorMessage?: string) => {
    const activeStep = status === 'partial' ? LIFECYCLE_STEPS.indexOf('running') : LIFECYCLE_STEPS.indexOf(status);
    const isTerminal = status === 'error' || status === 'partial' || status === 'stopped' || status === 'stopping' || status === 'cleaning';
    const showError = (status === 'error' || status === 'partial') && !!errorMessage;

    return (
      <Box sx={{ mb: 2 }}>
        <Typography variant="subtitle2" gutterBottom>Status Lifecycle</Typography>
        {isTerminal ? (
          <Alert severity={status === 'error' ? 'error' : 'warning'} sx={{ py: 0.5 }}>
            Instance is {status}
            {showError && (
              <Typography
                variant="body2"
                data-testid="instance-error-message"
                sx={{ mt: 0.5, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}
              >
                {errorMessage}
              </Typography>
            )}
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
  };

  if (loading) {
    return <LoadingState label="Loading instance..." />;
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
              <Box component="span" aria-live="polite">
                <StatusBadge status={instance.status} />
              </Box>
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
            {countdown && !countdown.isExpired && (instance.status === 'running' || instance.status === 'partial') && (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 0.5 }}>
                <Chip
                  label={`Expires in ${countdown.remaining}`}
                  size="small"
                  color={countdown.isCritical ? 'error' : countdown.isWarning ? 'warning' : 'success'}
                  icon={<span>⏱</span>}
                />
                {canModify && (
                  <ExtendTtlMenu
                    instanceId={instance.id}
                    onExtended={setInstance}
                    onError={setError}
                  />
                )}
              </Box>
            )}
          </Box>
          <Box sx={{ display: 'flex', gap: 1 }}>
            {canModify && renderStatusActions(instance.status)}
            <Button
              variant="outlined"
              id="export-values-button"
              aria-controls={exportMenuAnchor ? 'export-values-menu' : undefined}
              aria-haspopup="menu"
              aria-expanded={exportMenuAnchor ? 'true' : undefined}
              endIcon={<ArrowDropDownIcon />}
              onClick={(e) => setExportMenuAnchor(e.currentTarget)}
            >
              Export Values
            </Button>
            <Menu
              id="export-values-menu"
              anchorEl={exportMenuAnchor}
              open={Boolean(exportMenuAnchor)}
              onClose={() => setExportMenuAnchor(null)}
              slotProps={{ list: { 'aria-labelledby': 'export-values-button' } }}
            >
              <MenuItem onClick={() => handleExport()}>All charts (ZIP)</MenuItem>
              {charts.map((chart) => (
                <MenuItem key={chart.id} onClick={() => handleExport(chart)}>
                  {chart.chart_name} (YAML)
                </MenuItem>
              ))}
            </Menu>
            <Button variant="outlined" onClick={() => setCloneOpen(true)}>Clone</Button>
            {canModify && (
              <Button variant="outlined" color="error" onClick={() => setDeleteOpen(true)}>Delete</Button>
            )}
          </Box>
        </Box>

        {!canModify && (
          <Alert severity="info" sx={{ mt: 2 }}>
            Read-only: you are not the owner of this stack.
          </Alert>
        )}

        {instance.values_drift && (
          <Alert severity="warning" sx={{ mt: 2 }}>
            The running values differ from the stored overrides (after a rollback). The next deploy applies the stored overrides.
          </Alert>
        )}

        {canModify && savedPendingRedeploy && canRedeploy && (
          <Alert
            severity="info"
            sx={{ mt: 2 }}
            onClose={() => setSavedPendingRedeploy(false)}
            action={(
              <Button color="inherit" size="small" onClick={() => setDeployPreviewOpen(true)} disabled={deploying}>
                Redeploy
              </Button>
            )}
          >
            Saved. Redeploy to apply.
          </Alert>
        )}

        <Divider sx={{ my: 2 }} />

        {renderLifecycle(instance.status, instance.error_message)}

        {(instance.status === 'running' || instance.status === 'partial' || instance.status === 'deploying' || instance.status === 'stabilizing' || instance.status === 'error' || instance.status === 'stopping' || instance.status === 'cleaning') && (
          <Box sx={{ mb: 2 }}>
            <Typography variant="subtitle2" gutterBottom>Cluster Resources</Typography>
            <PodStatusDisplay status={k8sStatus} loading={statusLoading} />
          </Box>
        )}

        {k8sStatus && (instance.status === 'running' || instance.status === 'partial') && (
          <AccessUrls status={k8sStatus} />
        )}

        <Box sx={{ maxWidth: 400 }}>
          <Typography variant="subtitle2" gutterBottom>Branch</Typography>
          <BranchSelector
            repoUrl={getRepoUrl()}
            value={branch}
            onChange={setBranch}
            disabled={!canModify}
          />
        </Box>

        <Box sx={{ mt: 2 }}>
          <Typography variant="subtitle2" gutterBottom>TTL (Time to Live)</Typography>
          <TtlSelector
            value={instance.ttl_minutes ?? 0}
            onChange={handleTtlChange}
            disabled={saving || !canModify}
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
                      disabled={!canModify}
                    />
                  </Box>
                  {branchOverrides[chart.id] ? (
                    <Chip
                      label={`Override: ${branchOverrides[chart.id]}`}
                      color="warning"
                      size="small"
                      onDelete={canModify ? () => handleChartBranchChange(chart.id, '') : undefined}
                      deleteIcon={canModify ? (
                        <Tooltip title="Reset to instance branch">
                          <CloseIcon />
                        </Tooltip>
                      ) : undefined}
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
                    {canModify ? (
                      <YamlEditor
                        label="Your Overrides"
                        value={editedOverrides[chart.id] || ''}
                        onChange={(val) => setEditedOverrides({ ...editedOverrides, [chart.id]: val })}
                        height="300px"
                      />
                    ) : (
                      <Alert severity="info">
                        Overrides are visible to the owner, admins and devops users.
                      </Alert>
                    )}
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
          <DeploymentLogViewer
            logs={deployLogs}
            streamingLines={streamingLines}
            currentDeployLogId={currentLogId}
            onRollbackTo={canModify && canRollback ? (log) => openRollback(log.id) : undefined}
          />
        </Box>
      )}

      <Box sx={{ display: 'flex', gap: 2, justifyContent: 'flex-end' }}>
        <Button variant="outlined" onClick={() => navigate('/')}>
          Back to Dashboard
        </Button>
        {canModify && (
          <Button variant="contained" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving...' : 'Save Changes'}
          </Button>
        )}
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

      <DeployPreviewDialog
        open={deployPreviewOpen}
        instanceId={instance.id}
        instanceName={instance.name}
        onConfirm={() => { setDeployPreviewOpen(false); handleDeploy(); }}
        onClose={() => setDeployPreviewOpen(false)}
      />

      <CloneDialog
        open={cloneOpen}
        source={instance}
        onClose={() => setCloneOpen(false)}
        onCloned={handleCloned}
      />

      {canModify && canRollback && (
        <RollbackDialog
          open={rollbackOpen}
          instanceName={instance.name}
          deploys={deploys}
          currentLogId={currentLogId}
          initialTargetId={rollbackTargetId ?? defaultRollbackTarget(deploys, currentLogId)?.id}
          onConfirm={handleRollback}
          onClose={() => setRollbackOpen(false)}
        />
      )}

    </Box>
  );
};

export default Detail;
