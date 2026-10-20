import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
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
} from '@mui/material';
import { instanceService } from '../../api/client';
import type { InstanceQuotaOverride, ResourceQuotaConfig } from '../../types';
import { useNotification } from '../../context/NotificationContext';
import { describeApiError } from '../../utils/apiError';
import {
  containerFields,
  emptyQuantities,
  namespaceFields,
  POD_LIMIT_LABEL,
  quantityFields,
  trimQuantities,
  validateQuantities,
} from '../QuotaConfigDialog/quotaFields';
import type { FieldSpec, QuantityErrors, QuantityValues } from '../QuotaConfigDialog/quotaFields';

interface InstanceQuotaDialogProps {
  open: boolean;
  instanceId: string;
  /** Cluster quota of the instance, or null when the cluster has none. */
  clusterQuota: ResourceQuotaConfig | null;
  /** True when the caller cannot set a value above the cluster quota (role user). */
  limitToCluster: boolean;
  onClose: () => void;
  /** Called after a successful save. */
  onSaved: () => void;
}

/** pod_limit is text: empty means no override (the cluster value applies). */
type OverrideForm = QuantityValues & { pod_limit: string };
type OverrideErrors = QuantityErrors & { pod_limit?: string };

const emptyForm: OverrideForm = { ...emptyQuantities, pod_limit: '' };

function toForm(override: InstanceQuotaOverride | null): OverrideForm {
  if (!override) return emptyForm;
  const form: OverrideForm = { ...emptyForm, pod_limit: override.pod_limit == null ? '' : String(override.pod_limit) };
  for (const field of quantityFields) {
    form[field] = override[field] ?? '';
  }
  return form;
}

function validateForm(form: OverrideForm): OverrideErrors {
  const errors: OverrideErrors = validateQuantities(form);
  const pods = form.pod_limit.trim();
  if (pods !== '' && !/^\d+$/.test(pods)) {
    errors.pod_limit = 'Use a whole number, 0 or more';
  }
  return errors;
}

const InstanceQuotaDialog = ({ open, instanceId, clusterQuota, limitToCluster, onClose, onSaved }: InstanceQuotaDialogProps) => {
  const [form, setForm] = useState<OverrideForm>(emptyForm);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A save after a failed load would replace the stored override with an empty form.
  const [loadFailed, setLoadFailed] = useState(false);
  const fieldErrors = useMemo(() => validateForm(form), [form]);
  const hasFieldErrors = Object.keys(fieldErrors).length > 0;
  const { showSuccess } = useNotification();

  useEffect(() => {
    if (!open) return;
    // A late response must not overwrite the edits (see QuotaConfigDialog, #596).
    let stale = false;
    const load = async () => {
      setLoading(true);
      setError(null);
      setLoadFailed(false);
      try {
        const override = await instanceService.getQuotaOverride(instanceId);
        if (!stale) setForm(toForm(override));
      } catch (err) {
        const message = await describeApiError(err, 'Failed to load the quota override');
        if (!stale) {
          setError(message);
          setLoadFailed(true);
        }
      } finally {
        if (!stale) setLoading(false);
      }
    };
    load();
    return () => {
      stale = true;
    };
  }, [open, instanceId]);

  const handleSave = async () => {
    if (hasFieldErrors || loadFailed) return;
    setSaving(true);
    setError(null);
    const pods = form.pod_limit.trim();
    try {
      await instanceService.setQuotaOverride(instanceId, {
        ...trimQuantities(form),
        pod_limit: pods === '' ? null : Number.parseInt(pods, 10),
      });
      showSuccess('Quota override saved');
      onSaved();
      onClose();
    } catch (err) {
      setError(await describeApiError(err, 'Failed to save the quota override'));
    } finally {
      setSaving(false);
    }
  };

  const clusterHelp = (clusterValue: string, help: string) => {
    if (!clusterValue) return help;
    return limitToCluster ? `At most ${clusterValue} (cluster quota)` : `Cluster quota: ${clusterValue}`;
  };

  const renderQuantityField = ({ field, label, help }: FieldSpec) => (
    <TextField
      key={field}
      label={label}
      value={form[field]}
      onChange={(e) => setForm({ ...form, [field]: e.target.value })}
      error={Boolean(fieldErrors[field])}
      helperText={fieldErrors[field] ?? clusterHelp(clusterQuota?.[field] ?? '', help)}
      fullWidth
    />
  );

  const clusterPods = clusterQuota?.pod_limit ?? 0;
  let podHelp = 'No cluster quota. 0 = no limit.';
  if (clusterPods > 0) {
    podHelp = limitToCluster ? `At most ${clusterPods} (cluster quota)` : `Cluster quota: ${clusterPods}. 0 = no limit.`;
  }

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Quota override</DialogTitle>
      <DialogContent>
        {loading ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress />
          </Box>
        ) : (
          <>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Save replaces the whole override. An empty field uses the cluster quota.
              {limitToCluster && ' You can set a value up to the cluster quota. An admin or a devops user can set a higher value.'}
            </Typography>
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
              {namespaceFields.map(renderQuantityField)}
              <TextField
                label={POD_LIMIT_LABEL}
                type="number"
                value={form.pod_limit}
                onChange={(e) => setForm({ ...form, pod_limit: e.target.value })}
                error={Boolean(fieldErrors.pod_limit)}
                helperText={fieldErrors.pod_limit ?? podHelp}
                slotProps={{ htmlInput: { min: 0 } }}
                fullWidth
              />
            </Box>
            <Divider sx={{ my: 3 }} />
            <Typography variant="subtitle1" component="h2" sx={{ mb: 2 }}>
              Per container
            </Typography>
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              {containerFields.map(renderQuantityField)}
            </Box>
          </>
        )}
      </DialogContent>
      {error && <Alert severity="error" sx={{ mx: 3, mb: 1 }}>{error}</Alert>}
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={handleSave} disabled={saving || loading || loadFailed || hasFieldErrors}>
          {saving ? <CircularProgress size={20} /> : 'Save'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default InstanceQuotaDialog;
