import { useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  TextField,
  Typography,
} from '@mui/material';
import { definitionService, templateService } from '../../api/client';
import { getApiErrorInfo } from '../../utils/apiError';
import type { StackDefinition, StackTemplate, TemplateVersion } from '../../types';

interface TemplateLinkDialogProps {
  definitionId: string;
  open: boolean;
  /** Template ID of the current link, if any. */
  currentTemplateId?: string;
  /** Template version of the current link, if any. */
  currentVersion?: string;
  onClose: () => void;
  /** Called with the updated definition after a link change. */
  onLinked: (definition: StackDefinition) => void;
}

/** Value of the version select for "the latest published version". */
const LATEST = '';

/**
 * Links a stack definition to a published template version, or removes the
 * link (PUT /stack-definitions/:id/template-link). The charts and the values
 * of the definition do not change.
 */
const TemplateLinkDialog = ({
  definitionId,
  open,
  currentTemplateId,
  currentVersion,
  onClose,
  onLinked,
}: TemplateLinkDialogProps) => {
  const [templates, setTemplates] = useState<StackTemplate[]>([]);
  const [versions, setVersions] = useState<TemplateVersion[]>([]);
  const [templateId, setTemplateId] = useState('');
  const [version, setVersion] = useState(LATEST);
  const [loading, setLoading] = useState(false);
  const [loadingVersions, setLoadingVersions] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setTemplateId(currentTemplateId ?? '');
    setVersion(currentVersion ?? LATEST);
    setError(null);
    let cancelled = false;
    const load = async () => {
      setLoading(true);
      try {
        const all = await templateService.listAll();
        if (!cancelled) setTemplates(all.filter((t) => t.is_published));
      } catch {
        if (!cancelled) setError('Failed to load templates');
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    load();
    return () => {
      cancelled = true;
    };
  }, [open, currentTemplateId, currentVersion]);

  useEffect(() => {
    if (!open || !templateId) {
      setVersions([]);
      return;
    }
    let cancelled = false;
    const load = async () => {
      setLoadingVersions(true);
      try {
        const list = await templateService.listVersions(templateId);
        if (!cancelled) setVersions(list);
      } catch {
        if (!cancelled) setError('Failed to load the template versions');
      } finally {
        if (!cancelled) setLoadingVersions(false);
      }
    };
    load();
    return () => {
      cancelled = true;
    };
  }, [open, templateId]);

  const save = async (nextTemplateId: string, nextVersion: string) => {
    setSaving(true);
    setError(null);
    try {
      const updated = await definitionService.setTemplateLink(definitionId, nextTemplateId, nextVersion);
      onLinked(updated);
    } catch (err) {
      setError(getApiErrorInfo(err).message ?? 'Failed to change the template link');
    } finally {
      setSaving(false);
    }
  };

  const latestVersion = versions[0]?.version;
  // A linked template that is no longer published is not in the list.
  const currentMissing = Boolean(currentTemplateId) && !loading && !templates.some((t) => t.id === currentTemplateId);

  return (
    <Dialog open={open} onClose={saving ? undefined : onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Template link</DialogTitle>
      <DialogContent>
        <Alert severity="info" sx={{ mb: 2 }}>
          The link does not change the charts or the values of this definition. A linked definition gets the
          template rules: required charts stay, the locked values of the latest published version apply at each
          deploy, and the upgrade check compares the charts with the template. The version that you select is only
          the base of the upgrade check. The analytics template usage counts the definition.
        </Alert>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}
        {currentMissing && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            The linked template is not published now. Select another template, or remove the link.
          </Alert>
        )}
        {loading ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 3 }}>
            <CircularProgress size={28} />
          </Box>
        ) : (
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, pt: 1 }}>
            <TextField
              select
              label="Template"
              value={templates.some((t) => t.id === templateId) ? templateId : ''}
              onChange={(e) => {
                setTemplateId(e.target.value);
                setVersion(LATEST);
              }}
              helperText={templates.length === 0 ? 'No published templates' : 'Published templates only'}
              fullWidth
            >
              {templates.map((t) => (
                <MenuItem key={t.id} value={t.id}>
                  {t.name}
                </MenuItem>
              ))}
            </TextField>
            <TextField
              select
              label="Version"
              value={versions.some((v) => v.version === version) ? version : LATEST}
              onChange={(e) => setVersion(e.target.value)}
              disabled={!templateId || loadingVersions}
              // Show "Latest" for the empty value.
              slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
              fullWidth
            >
              <MenuItem value={LATEST}>
                {latestVersion ? `Latest (${latestVersion})` : 'Latest'}
              </MenuItem>
              {versions.map((v) => (
                <MenuItem key={v.id} value={v.version}>
                  {v.version}
                </MenuItem>
              ))}
            </TextField>
            {currentTemplateId && (
              <Typography variant="body2" color="text.secondary">
                Current link: {templates.find((t) => t.id === currentTemplateId)?.name ?? currentTemplateId}
                {currentVersion && ` (v${currentVersion})`}
              </Typography>
            )}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        {currentTemplateId && (
          <Button color="error" onClick={() => save('', '')} disabled={saving} sx={{ mr: 'auto' }}>
            Remove link
          </Button>
        )}
        <Button onClick={onClose} disabled={saving}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={() => save(templateId, version)}
          disabled={saving || loading || !templates.some((t) => t.id === templateId)}
        >
          {saving ? 'Saving...' : 'Save link'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default TemplateLinkDialog;
