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
import { describeApiError } from '../../utils/apiError';
import {
  containerFields,
  emptyQuantities,
  namespaceFields,
  quantityFields,
  relatedFields,
  trimQuantities,
  validateQuantities,
} from './quotaFields';
import type { FieldSpec, QuantityErrors, QuantityField, QuantityValues } from './quotaFields';

interface QuotaConfigDialogProps {
  open: boolean;
  onClose: () => void;
  clusterId: string;
  clusterName: string;
}

type QuotaForm = QuantityValues & { pod_limit: number };
type QuotaErrors = QuantityErrors & { pod_limit?: string };

const emptyQuota: QuotaForm = { ...emptyQuantities, pod_limit: 0 };

/** Validate the quota form. An empty quantity means "not set" and is valid. */
function validateQuota(form: QuotaForm): QuotaErrors {
  const errors: QuotaErrors = validateQuantities(form);
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
    // Ignore a stale response: React StrictMode (development) runs this
    // effect twice, and the dialog can close or switch clusters before a
    // request returns. A late response would overwrite the edits (#596).
    let stale = false;
    const fetchQuotas = async () => {
      setLoading(true);
      setError(null);
      setTouched({});
      try {
        const config = await clusterService.getQuotas(clusterId);
        if (stale) return;
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
        if (!stale) setError('Failed to load quota configuration');
      } finally {
        if (!stale) setLoading(false);
      }
    };
    fetchQuotas();
    return () => {
      stale = true;
    };
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
        ...trimQuantities(form),
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
