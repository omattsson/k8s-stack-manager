import { useCallback, useEffect, useId, useState } from 'react';
import { Alert, Box, Button, Chip, Grid, Paper, Typography } from '@mui/material';
import { instanceService } from '../../api/client';
import type { EffectiveQuota, QuotaValueSource } from '../../types';
import { useAuth } from '../../context/AuthContext';
import { useNotification } from '../../context/NotificationContext';
import { describeApiError } from '../../utils/apiError';
import { hasAtLeastRole } from '../../utils/roles';
import ConfirmDialog from '../ConfirmDialog';
import { containerFields, formatQuotaQuantity, namespaceFields, POD_LIMIT_LABEL } from '../QuotaConfigDialog/quotaFields';
import type { FieldSpec } from '../QuotaConfigDialog/quotaFields';
import InstanceQuotaDialog from './InstanceQuotaDialog';

interface InstanceQuotaSectionProps {
  instanceId: string;
}

interface QuotaRow {
  key: string;
  label: string;
  value: string;
  source: Exclude<QuotaValueSource, 'none'>;
}

function quantityRows(quota: EffectiveQuota, specs: FieldSpec[]): QuotaRow[] {
  return specs.flatMap(({ field, label }) => {
    const source = quota.sources[field];
    const raw = quota.effective[field];
    if (source !== 'override' && source !== 'cluster') return [];
    return [{ key: field, label, value: formatQuotaQuantity(field, raw), source }];
  });
}

function podRow(quota: EffectiveQuota): QuotaRow[] {
  const source = quota.sources.pod_limit;
  if (source !== 'override' && source !== 'cluster') return [];
  const pods = quota.effective.pod_limit;
  return [{ key: 'pod_limit', label: POD_LIMIT_LABEL, value: pods === 0 ? 'No limit' : String(pods), source }];
}

const RowGroup = ({ title, rows }: { title: string; rows: QuotaRow[] }) => {
  if (rows.length === 0) return null;
  return (
    <Box sx={{ mb: 2 }}>
      <Typography variant="subtitle2" component="h3" sx={{ mb: 1 }}>
        {title}
      </Typography>
      <Grid container spacing={2}>
        {rows.map((row) => (
          <Grid key={row.key} size={{ xs: 12, sm: 6, md: 4 }}>
            <Box role="group" aria-label={row.label}>
              <Typography variant="caption" color="text.secondary" component="div">
                {row.label}
              </Typography>
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                <Typography variant="body2">{row.value}</Typography>
                {row.source === 'override' ? (
                  <Chip label="Override" size="small" color="warning" />
                ) : (
                  <Chip label="Cluster" size="small" variant="outlined" />
                )}
              </Box>
            </Box>
          </Grid>
        ))}
      </Grid>
    </Box>
  );
};

/** "Resource quota" section of the instance page: the effective quota and its source, with override edit and remove. */
const InstanceQuotaSection = ({ instanceId }: InstanceQuotaSectionProps) => {
  const { user } = useAuth();
  const { showSuccess, showError } = useNotification();
  const headingId = useId();
  const [quota, setQuota] = useState<EffectiveQuota | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [reloadCount, setReloadCount] = useState(0);
  const [editOpen, setEditOpen] = useState(false);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [removing, setRemoving] = useState(false);

  useEffect(() => {
    let stale = false;
    const load = async () => {
      try {
        const data = await instanceService.getEffectiveQuota(instanceId);
        if (stale) return;
        setQuota(data ?? null);
        setLoadError(null);
      } catch (err) {
        const message = await describeApiError(err, 'Failed to load the resource quota');
        if (!stale) setLoadError(message);
      }
    };
    load();
    return () => {
      stale = true;
    };
  }, [instanceId, reloadCount]);

  const reload = useCallback(() => setReloadCount((n) => n + 1), []);

  const handleRemove = async () => {
    setRemoveOpen(false);
    setRemoving(true);
    try {
      await instanceService.deleteQuotaOverride(instanceId);
      showSuccess('Quota override removed');
      reload();
    } catch (err) {
      showError(await describeApiError(err, 'Failed to remove the quota override'));
    } finally {
      setRemoving(false);
    }
  };

  if (!quota) {
    if (!loadError) return null;
    return (
      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" component="h2" sx={{ mb: 1 }}>
          Resource quota
        </Typography>
        <Alert severity="error">{loadError}</Alert>
      </Paper>
    );
  }

  const namespaceRows = [...quantityRows(quota, namespaceFields), ...podRow(quota)];
  const containerRows = quantityRows(quota, containerFields);
  const hasValues = namespaceRows.length > 0 || containerRows.length > 0;
  if (!hasValues && !quota.can_modify) return null;

  return (
    <Paper component="section" aria-labelledby={headingId} sx={{ p: 3, mb: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 1, flexWrap: 'wrap', mb: 1 }}>
        <Typography variant="h6" component="h2" id={headingId}>
          Resource quota
        </Typography>
        {quota.can_modify && (
          <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
            <Button size="small" variant="outlined" onClick={() => setEditOpen(true)} disabled={removing}>
              Edit override
            </Button>
            {quota.override && (
              <Button size="small" color="error" onClick={() => setRemoveOpen(true)} disabled={removing}>
                Remove override
              </Button>
            )}
          </Box>
        )}
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        A deploy applies these values to the stack namespace. A change to a deployed stack applies at once.
        An override value replaces the cluster quota value.
      </Typography>
      {loadError && <Alert severity="error" sx={{ mb: 2 }}>{loadError}</Alert>}
      {quota.valid === false && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          The merged quota is not valid: {quota.invalid_reason}. A deploy of this stack fails until the quota is fixed.
        </Alert>
      )}
      {hasValues ? (
        <>
          <RowGroup title="Namespace" rows={namespaceRows} />
          <RowGroup title="Per container" rows={containerRows} />
          <Typography variant="caption" color="text.secondary" component="p">
            A container without its own request or limit gets the container default. If a resource has a quota value
            but no container default, the container gets a small fallback default: CPU 100m and 500m, memory 128Mi and
            512Mi, ephemeral storage 128Mi and 1Gi. A resource without a quota value gets no default.
          </Typography>
        </>
      ) : (
        <Typography variant="body2">No quota is configured for this stack.</Typography>
      )}

      {quota.can_modify && (
        <>
          <InstanceQuotaDialog
            open={editOpen}
            instanceId={instanceId}
            clusterQuota={quota.cluster}
            limitToCluster={!hasAtLeastRole(user?.role, 'devops')}
            onClose={() => setEditOpen(false)}
            onSaved={reload}
          />
          <ConfirmDialog
            open={removeOpen}
            title="Remove the quota override"
            message={quota.cluster
              ? 'The cluster quota applies to this stack again.'
              : 'The cluster has no quota. The namespace keeps its current quota until the stack is cleaned.'}
            confirmText="Remove"
            onConfirm={handleRemove}
            onCancel={() => setRemoveOpen(false)}
          />
        </>
      )}
    </Paper>
  );
};

export default InstanceQuotaSection;
