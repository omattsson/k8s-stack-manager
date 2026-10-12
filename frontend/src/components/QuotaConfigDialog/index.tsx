import { useEffect, useMemo, useState } from 'react';
import {
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
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

type QuantityField =
  | 'cpu_request'
  | 'cpu_limit'
  | 'memory_request'
  | 'memory_limit'
  | 'storage_limit'
  | 'ephemeral_storage_limit'
  | 'container_cpu_request'
  | 'container_cpu_limit'
  | 'container_cpu_max'
  | 'container_memory_request'
  | 'container_memory_limit'
  | 'container_memory_max'
  | 'container_ephemeral_storage_request'
  | 'container_ephemeral_storage_limit';

type QuotaForm = Record<QuantityField, string> & { pod_limit: number };
type QuotaErrors = Partial<Record<QuantityField | 'pod_limit', string>>;

const emptyQuota: QuotaForm = {
  cpu_request: '',
  cpu_limit: '',
  memory_request: '',
  memory_limit: '',
  storage_limit: '',
  ephemeral_storage_limit: '',
  container_cpu_request: '',
  container_cpu_limit: '',
  container_cpu_max: '',
  container_memory_request: '',
  container_memory_limit: '',
  container_memory_max: '',
  container_ephemeral_storage_request: '',
  container_ephemeral_storage_limit: '',
  pod_limit: 0,
};

const quantityFields = Object.keys(emptyQuota).filter((key) => key !== 'pod_limit') as QuantityField[];

type ResourceKind = 'cpu' | 'memory' | 'storage';

const fieldKind: Record<QuantityField, ResourceKind> = {
  cpu_request: 'cpu',
  cpu_limit: 'cpu',
  memory_request: 'memory',
  memory_limit: 'memory',
  storage_limit: 'storage',
  ephemeral_storage_limit: 'storage',
  container_cpu_request: 'cpu',
  container_cpu_limit: 'cpu',
  container_cpu_max: 'cpu',
  container_memory_request: 'memory',
  container_memory_limit: 'memory',
  container_memory_max: 'memory',
  container_ephemeral_storage_request: 'storage',
  container_ephemeral_storage_limit: 'storage',
};

const kindHint: Record<ResourceKind, string> = {
  cpu: 'Use a CPU quantity such as 500m, 1 or 1.5',
  memory: 'Use a memory quantity such as 256Mi, 1Gi or 1G',
  storage: 'Use a storage quantity such as 10Gi or 50G',
};

const kindParser: Record<ResourceKind, (v: string) => number | null> = {
  cpu: parseCpuMillicores,
  memory: parseMemoryBytes,
  storage: parseMemoryBytes,
};

/**
 * Order rules: the first field must not be higher than the second when both
 * are set. The same rules as the backend (models.quotaOrderRules). The error
 * shows under the first field.
 */
const orderRules: Array<[QuantityField, QuantityField, string]> = [
  ['cpu_request', 'cpu_limit', 'CPU request must not be higher than the CPU limit'],
  ['memory_request', 'memory_limit', 'Memory request must not be higher than the memory limit'],
  ['container_cpu_request', 'container_cpu_limit', 'Must not be higher than the container CPU limit'],
  ['container_cpu_limit', 'container_cpu_max', 'Must not be higher than the container CPU max'],
  ['container_cpu_request', 'container_cpu_max', 'Must not be higher than the container CPU max'],
  ['container_memory_request', 'container_memory_limit', 'Must not be higher than the container memory limit'],
  ['container_memory_limit', 'container_memory_max', 'Must not be higher than the container memory max'],
  ['container_memory_request', 'container_memory_max', 'Must not be higher than the container memory max'],
  ['container_ephemeral_storage_request', 'container_ephemeral_storage_limit', 'Must not be higher than the container ephemeral storage limit'],
  ['container_cpu_request', 'cpu_request', 'Must not be higher than the namespace CPU request'],
  ['container_cpu_request', 'cpu_limit', 'Must not be higher than the namespace CPU limit'],
  ['container_cpu_limit', 'cpu_limit', 'Must not be higher than the namespace CPU limit'],
  ['container_memory_request', 'memory_request', 'Must not be higher than the namespace memory request'],
  ['container_memory_request', 'memory_limit', 'Must not be higher than the namespace memory limit'],
  ['container_memory_limit', 'memory_limit', 'Must not be higher than the namespace memory limit'],
  ['container_ephemeral_storage_request', 'ephemeral_storage_limit', 'Must not be higher than the namespace ephemeral storage limit'],
  ['container_ephemeral_storage_limit', 'ephemeral_storage_limit', 'Must not be higher than the namespace ephemeral storage limit'],
];

/** Fields that share an order rule with each field (the error shows when either is touched). */
const relatedFields: Partial<Record<QuantityField, QuantityField[]>> = {};
for (const [low, high] of orderRules) {
  relatedFields[low] = [...(relatedFields[low] ?? []), high];
}

interface FieldSpec {
  field: QuantityField;
  label: string;
  help: string;
  placeholder: string;
}

const namespaceFields: FieldSpec[] = [
  { field: 'cpu_request', label: 'CPU Request', help: 'Default CPU request per namespace (e.g., 500m, 1, 2000m)', placeholder: '500m' },
  { field: 'cpu_limit', label: 'CPU Limit', help: 'Maximum CPU per namespace (e.g., 2000m, 4)', placeholder: '2000m' },
  { field: 'memory_request', label: 'Memory Request', help: 'Default memory request per namespace (e.g., 256Mi, 1Gi)', placeholder: '256Mi' },
  { field: 'memory_limit', label: 'Memory Limit', help: 'Maximum memory per namespace (e.g., 1Gi, 2Gi)', placeholder: '1Gi' },
  { field: 'storage_limit', label: 'Storage Limit', help: 'Maximum storage per namespace (e.g., 10Gi, 50Gi)', placeholder: '10Gi' },
  {
    field: 'ephemeral_storage_limit',
    label: 'Ephemeral Storage Limit',
    help: 'Maximum ephemeral storage (container logs, emptyDir) per namespace, for requests and limits (e.g., 20Gi)',
    placeholder: '20Gi',
  },
];

const containerFields: FieldSpec[] = [
  { field: 'container_cpu_request', label: 'Container CPU Request', help: 'Default CPU request of a container without its own (e.g., 100m)', placeholder: '100m' },
  { field: 'container_cpu_limit', label: 'Container CPU Limit', help: 'Default CPU limit of a container without its own (e.g., 500m)', placeholder: '500m' },
  { field: 'container_cpu_max', label: 'Container CPU Max', help: 'Highest CPU limit one container may set (e.g., 2)', placeholder: '2' },
  { field: 'container_memory_request', label: 'Container Memory Request', help: 'Default memory request of a container without its own (e.g., 128Mi)', placeholder: '128Mi' },
  { field: 'container_memory_limit', label: 'Container Memory Limit', help: 'Default memory limit of a container without its own (e.g., 512Mi)', placeholder: '512Mi' },
  { field: 'container_memory_max', label: 'Container Memory Max', help: 'Highest memory limit one container may set (e.g., 4Gi)', placeholder: '4Gi' },
  {
    field: 'container_ephemeral_storage_request',
    label: 'Container Ephemeral Storage Request',
    help: 'Default ephemeral storage request of a container (e.g., 128Mi)',
    placeholder: '128Mi',
  },
  {
    field: 'container_ephemeral_storage_limit',
    label: 'Container Ephemeral Storage Limit',
    help: 'Default ephemeral storage limit of a container (e.g., 1Gi)',
    placeholder: '1Gi',
  },
];

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
      errors[field] = `Invalid quantity. ${kindHint[fieldKind[field]]}`;
    } else if (value < 0) {
      errors[field] = 'Must not be negative';
    }
  }
  for (const [lowField, highField, message] of orderRules) {
    if (errors[lowField] || errors[highField]) continue;
    const parse = kindParser[fieldKind[lowField]];
    const low = form[lowField] ? parse(form[lowField]) : null;
    const high = form[highField] ? parse(form[highField]) : null;
    if (low !== null && high !== null && low > high) {
      errors[lowField] = message;
    }
  }
  if (form.pod_limit < 0) {
    errors.pod_limit = 'Must not be negative';
  }
  return errors;
}

/** Map a stored quota config to the form (missing fields become empty). */
function toForm(config: ResourceQuotaConfig): QuotaForm {
  const form: QuotaForm = { ...emptyQuota, pod_limit: config.pod_limit ?? 0 };
  for (const field of quantityFields) {
    form[field] = config[field] ?? '';
  }
  return form;
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
          const loaded = toForm(config);
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
      setTouched(Object.fromEntries([...quantityFields, 'pod_limit'].map((field) => [field, true])));
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
    // An order error shows under the lower field, so it becomes visible when
    // either field of the rule is touched.
    const related = field === 'pod_limit' ? [] : relatedFields[field] ?? [];
    const isTouched = Boolean(touched[field] || related.some((other) => touched[other]));
    const visible = Boolean(isTouched && fieldErrors[field]);
    return {
      error: visible,
      helperText: visible ? fieldErrors[field] : help,
      onBlur: () => setTouched((prev) => ({ ...prev, [field]: true })),
    };
  };

  const renderQuantityField = ({ field, label, help, placeholder }: FieldSpec) => (
    <TextField
      key={field}
      label={label}
      value={form[field]}
      onChange={(e) => setForm({ ...form, [field]: e.target.value })}
      {...fieldProps(field, help)}
      fullWidth
      placeholder={placeholder}
    />
  );

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
              {namespaceFields.map(renderQuantityField)}
              <TextField
                label="Pod Limit"
                type="number"
                value={form.pod_limit}
                onChange={(e) => setForm({ ...form, pod_limit: Number.parseInt(e.target.value, 10) || 0 })}
                {...fieldProps('pod_limit', 'Maximum number of pods per namespace (0 = unlimited)')}
                fullWidth
              />
            </Box>

            <Divider sx={{ my: 3 }} />
            <Typography variant="subtitle1" component="h2">
              Per container
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Values for one container, set in the namespace LimitRange. A container without its own
              requests or limits gets the defaults. If you leave the defaults empty, the namespace gets
              small fallback defaults (CPU 100m request and 500m limit, memory 128Mi and 512Mi,
              ephemeral storage 128Mi and 1Gi), never the namespace total. The max is the highest limit
              one container may set.
            </Typography>
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              {containerFields.map(renderQuantityField)}
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
