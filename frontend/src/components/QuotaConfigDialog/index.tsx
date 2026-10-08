import { useEffect, useMemo, useState } from 'react';
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
import { parseCpuMillicores, parseMemoryBytes, parseQuantity } from '../../utils/quantity';
import { describeApiError } from '../../utils/apiError';

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

type QuotaForm = typeof emptyQuota;
type QuantityField = 'cpu_request' | 'cpu_limit' | 'memory_request' | 'memory_limit' | 'storage_limit';
type QuotaErrors = Partial<Record<QuantityField | 'pod_limit', string>>;

const quantityFields: QuantityField[] = ['cpu_request', 'cpu_limit', 'memory_request', 'memory_limit', 'storage_limit'];

const formatHint: Record<QuantityField, string> = {
  cpu_request: 'Use a CPU quantity such as 500m, 1 or 1.5',
  cpu_limit: 'Use a CPU quantity such as 500m, 1 or 1.5',
  memory_request: 'Use a memory quantity such as 256Mi, 1Gi or 1G',
  memory_limit: 'Use a memory quantity such as 256Mi, 1Gi or 1G',
  storage_limit: 'Use a storage quantity such as 10Gi or 50G',
};

/** Return a copy of the form with surrounding whitespace removed from every quantity field. */
function trimQuota(form: QuotaForm): QuotaForm {
  const trimmed = { ...form };
  for (const field of quantityFields) {
    trimmed[field] = form[field].trim();
  }
  return trimmed;
}

/** Validate the quota form. An empty quantity means "not set" and is valid. */
function validateQuota(input: QuotaForm): QuotaErrors {
  const form = trimQuota(input);
  const errors: QuotaErrors = {};
  for (const field of quantityFields) {
    const raw = form[field];
    if (!raw) continue;
    const value = parseQuantity(raw);
    if (value === null) {
      errors[field] = `Invalid quantity. ${formatHint[field]}`;
    } else if (value < 0) {
      errors[field] = 'Must not be negative';
    }
  }
  const pairs: Array<[QuantityField, QuantityField, (v: string) => number | null, string]> = [
    ['cpu_request', 'cpu_limit', parseCpuMillicores, 'CPU request must not be higher than the CPU limit'],
    ['memory_request', 'memory_limit', parseMemoryBytes, 'Memory request must not be higher than the memory limit'],
  ];
  for (const [requestField, limitField, parse, message] of pairs) {
    if (errors[requestField] || errors[limitField]) continue;
    const request = form[requestField] ? parse(form[requestField]) : null;
    const limit = form[limitField] ? parse(form[limitField]) : null;
    if (request !== null && limit !== null && request > limit) {
      errors[requestField] = message;
    }
  }
  if (form.pod_limit < 0) {
    errors.pod_limit = 'Must not be negative';
  }
  return errors;
}

const QuotaConfigDialog = ({ open, onClose, clusterId, clusterName }: QuotaConfigDialogProps) => {
  const [form, setForm] = useState(emptyQuota);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [hasExisting, setHasExisting] = useState(false);
  const [touched, setTouched] = useState<Partial<Record<QuantityField | 'pod_limit', boolean>>>({});
  const fieldErrors = useMemo(() => validateQuota(form), [form]);
  const hasFieldErrors = Object.keys(fieldErrors).length > 0;
  const { showSuccess, showError } = useNotification();

  useEffect(() => {
    if (!open || !clusterId) return;
    const fetchQuotas = async () => {
      setLoading(true);
      setError(null);
      setTouched({});
      try {
        const config = await clusterService.getQuotas(clusterId);
        if (config) {
          const loaded: QuotaForm = {
            cpu_request: config.cpu_request ?? '',
            cpu_limit: config.cpu_limit ?? '',
            memory_request: config.memory_request ?? '',
            memory_limit: config.memory_limit ?? '',
            storage_limit: config.storage_limit ?? '',
            pod_limit: config.pod_limit ?? 0,
          };
          setForm(loaded);
          // Show errors for stored values that are invalid right away, so the
          // admin sees why Save is disabled.
          const loadedErrors = validateQuota(loaded);
          setTouched(Object.fromEntries(Object.keys(loadedErrors).map((field) => [field, true])));
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
    if (hasFieldErrors) {
      setTouched({ cpu_request: true, cpu_limit: true, memory_request: true, memory_limit: true, storage_limit: true, pod_limit: true });
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await clusterService.updateQuotas(clusterId, {
        cluster_id: clusterId,
        ...trimQuota(form),
      });
      showSuccess('Resource quotas saved');
      onClose();
    } catch (err) {
      const message = await describeApiError(err, 'Failed to save quota configuration');
      setError(message);
      showError(message);
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

  /** Props that show a field's validation error (after blur or a save attempt) in place of its help text. */
  const fieldProps = (field: QuantityField | 'pod_limit', help: string) => {
    // A request-above-limit error shows under the request field, so it
    // becomes visible when either field of the pair is touched.
    const pairedLimit = field === 'cpu_request' ? 'cpu_limit' : field === 'memory_request' ? 'memory_limit' : null;
    const isTouched = Boolean(touched[field] || (pairedLimit && touched[pairedLimit]));
    const visible = Boolean(isTouched && fieldErrors[field]);
    return {
      error: visible,
      helperText: visible ? fieldErrors[field] : help,
      onBlur: () => setTouched((prev) => ({ ...prev, [field]: true })),
    };
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
                {...fieldProps('cpu_request', 'Default CPU request per namespace (e.g., 500m, 1, 2000m)')}
                fullWidth
                placeholder="500m"
              />
              <TextField
                label="CPU Limit"
                value={form.cpu_limit}
                onChange={(e) => setForm({ ...form, cpu_limit: e.target.value })}
                {...fieldProps('cpu_limit', 'Maximum CPU per namespace (e.g., 2000m, 4)')}
                fullWidth
                placeholder="2000m"
              />
              <TextField
                label="Memory Request"
                value={form.memory_request}
                onChange={(e) => setForm({ ...form, memory_request: e.target.value })}
                {...fieldProps('memory_request', 'Default memory request per namespace (e.g., 256Mi, 1Gi)')}
                fullWidth
                placeholder="256Mi"
              />
              <TextField
                label="Memory Limit"
                value={form.memory_limit}
                onChange={(e) => setForm({ ...form, memory_limit: e.target.value })}
                {...fieldProps('memory_limit', 'Maximum memory per namespace (e.g., 1Gi, 2Gi)')}
                fullWidth
                placeholder="1Gi"
              />
              <TextField
                label="Storage Limit"
                value={form.storage_limit}
                onChange={(e) => setForm({ ...form, storage_limit: e.target.value })}
                {...fieldProps('storage_limit', 'Maximum storage per namespace (e.g., 10Gi, 50Gi)')}
                fullWidth
                placeholder="10Gi"
              />
              <TextField
                label="Pod Limit"
                type="number"
                value={form.pod_limit}
                onChange={(e) => setForm({ ...form, pod_limit: Number.parseInt(e.target.value, 10) || 0 })}
                {...fieldProps('pod_limit', 'Maximum number of pods per namespace (0 = unlimited)')}
                fullWidth
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
          disabled={saving || loading || hasFieldErrors}
        >
          {saving ? <CircularProgress size={20} /> : 'Save'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default QuotaConfigDialog;
