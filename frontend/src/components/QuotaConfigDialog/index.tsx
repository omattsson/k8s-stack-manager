import { useEffect, useState } from 'react';
import {
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  TextField,
  Typography,
  Alert,
} from '@mui/material';
import { clusterService } from '../../api/client';
import type { ResourceQuotaConfig } from '../../types';
import { useNotification } from '../../context/NotificationContext';

interface QuotaConfigDialogProps {
  open: boolean;
  onClose: () => void;
  clusterId: string;
  clusterName: string;
}

const emptyQuota: Omit<ResourceQuotaConfig, 'id' | 'cluster_id'> = {
  cpu_request: '',
  cpu_limit: '',
  memory_request: '',
  memory_limit: '',
  storage_limit: '',
  pod_limit: 0,
};

const QuotaConfigDialog = ({ open, onClose, clusterId, clusterName }: QuotaConfigDialogProps) => {
  const [form, setForm] = useState(emptyQuota);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [hasExisting, setHasExisting] = useState(false);
  const { showSuccess, showError } = useNotification();

  useEffect(() => {
    if (!open || !clusterId) return;
    const fetchQuotas = async () => {
      setLoading(true);
      setError(null);
      try {
        const config = await clusterService.getQuotas(clusterId);
        if (config) {
          setForm({
            cpu_request: config.cpu_request,
            cpu_limit: config.cpu_limit,
            memory_request: config.memory_request,
            memory_limit: config.memory_limit,
            storage_limit: config.storage_limit,
            pod_limit: config.pod_limit,
          });
          setHasExisting(true);
        } else {
          setForm(emptyQuota);
          setHasExisting(false);
        }
      } catch {
        setError('Failed to load quota configuration');
      } finally {
        setLoading(false);
      }
    };
    fetchQuotas();
  }, [open, clusterId]);

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    try {
      await clusterService.updateQuotas(clusterId, {
        cluster_id: clusterId,
        ...form,
      });
      showSuccess('Resource quotas saved');
      onClose();
    } catch {
      setError('Failed to save quota configuration');
      showError('Failed to save quota configuration');
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    setSaving(true);
    setError(null);
    try {
      await clusterService.deleteQuotas(clusterId);
      showSuccess('Resource quotas removed');
      setForm(emptyQuota);
      setHasExisting(false);
      onClose();
    } catch {
      setError('Failed to remove quota configuration');
      showError('Failed to remove quota configuration');
    } finally {
      setSaving(false);
    }
  };

  const handleClose = () => {
    setError(null);
    onClose();
  };

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>Resource Quotas for {clusterName}</DialogTitle>
      <DialogContent>
        {loading ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress />
          </Box>
        ) : (
          <>
            {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

            {!hasExisting && !error && (
              <Alert severity="info" sx={{ mb: 2 }}>
                No quotas configured for this cluster. Fill in the fields below to set resource limits per namespace.
              </Alert>
            )}

            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Resource quotas define default limits applied to namespaces created on this cluster.
              Use Kubernetes resource quantity format (e.g., 500m for CPU, 256Mi for memory).
            </Typography>

            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
              <TextField
                label="CPU Request"
                value={form.cpu_request}
                onChange={(e) => setForm({ ...form, cpu_request: e.target.value })}
                fullWidth
                helperText="Default CPU request per namespace (e.g., 500m, 1, 2000m)"
                placeholder="500m"
              />
              <TextField
                label="CPU Limit"
                value={form.cpu_limit}
                onChange={(e) => setForm({ ...form, cpu_limit: e.target.value })}
                fullWidth
                helperText="Maximum CPU per namespace (e.g., 2000m, 4)"
                placeholder="2000m"
              />
              <TextField
                label="Memory Request"
                value={form.memory_request}
                onChange={(e) => setForm({ ...form, memory_request: e.target.value })}
                fullWidth
                helperText="Default memory request per namespace (e.g., 256Mi, 1Gi)"
                placeholder="256Mi"
              />
              <TextField
                label="Memory Limit"
                value={form.memory_limit}
                onChange={(e) => setForm({ ...form, memory_limit: e.target.value })}
                fullWidth
                helperText="Maximum memory per namespace (e.g., 1Gi, 2Gi)"
                placeholder="1Gi"
              />
              <TextField
                label="Storage Limit"
                value={form.storage_limit}
                onChange={(e) => setForm({ ...form, storage_limit: e.target.value })}
                fullWidth
                helperText="Maximum storage per namespace (e.g., 10Gi, 50Gi)"
                placeholder="10Gi"
              />
              <TextField
                label="Pod Limit"
                type="number"
                value={form.pod_limit}
                onChange={(e) => setForm({ ...form, pod_limit: Number.parseInt(e.target.value, 10) || 0 })}
                fullWidth
                helperText="Maximum number of pods per namespace (0 = unlimited)"
              />
            </Box>
          </>
        )}
      </DialogContent>
      <DialogActions>
        {hasExisting && (
          <Button
            color="error"
            onClick={handleDelete}
            disabled={saving || loading}
            sx={{ mr: 'auto' }}
          >
            Remove Quotas
          </Button>
        )}
        <Button onClick={handleClose}>Cancel</Button>
        <Button
          variant="contained"
          onClick={handleSave}
          disabled={saving || loading}
        >
          {saving ? <CircularProgress size={20} /> : 'Save'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default QuotaConfigDialog;
