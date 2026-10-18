import { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  TextField,
  Typography,
} from '@mui/material';
import { templateService } from '../../api/client';
import { getApiErrorInfo } from '../../utils/apiError';
import { suggestPublishVersion } from '../../utils/templateVersion';
import type { PublishTemplateResult, StackTemplate } from '../../types';

interface PublishDialogProps {
  open: boolean;
  template: StackTemplate;
  onClose: () => void;
  /** Called after a successful publish call (also when the API created no new snapshot). */
  onPublished: (result: PublishTemplateResult, requestedVersion: string) => void;
}

const publishIntro = (template: StackTemplate): string => {
  if (!template.published_version) return 'Publish creates the first release from the working copy.';
  if (template.has_unpublished_changes === false) {
    return `No changes since version ${template.published_version}. Publish the same version to make the template visible again.`;
  }
  return `Users get version ${template.published_version} now. Publish creates a new release from the working copy.`;
};

/**
 * Publish the working copy of a template as a release.
 * Asks for the release version (prefilled) and an optional change summary.
 */
const PublishDialog = ({ open, template, onClose, onPublished }: PublishDialogProps) => {
  const [version, setVersion] = useState('');
  const [changeSummary, setChangeSummary] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setVersion(suggestPublishVersion(template.version, template.published_version, template.has_unpublished_changes));
    setChangeSummary('');
    setError(null);
    setSubmitting(false);
  }, [open, template.version, template.published_version, template.has_unpublished_changes]);

  const trimmedVersion = version.trim();

  const handlePublish = async () => {
    if (!trimmedVersion) return;
    setSubmitting(true);
    setError(null);
    try {
      const result = await templateService.publish(template.id, {
        version: trimmedVersion,
        change_summary: changeSummary.trim() || undefined,
      });
      onPublished(result, trimmedVersion);
    } catch (err) {
      const { status, message } = getApiErrorInfo(err);
      if (status === 409) {
        setError(message || `Version ${trimmedVersion} already exists. Enter a new version.`);
      } else if (status === 400 && message) {
        setError(message);
      } else {
        setError('Failed to publish template');
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onClose={submitting ? undefined : onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Publish {template.name}</DialogTitle>
      <DialogContent>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          {publishIntro(template)}
        </Typography>
        {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
        <TextField
          label="Version"
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          required
          fullWidth
          error={!trimmedVersion}
          helperText={trimmedVersion ? 'Each release needs a new version.' : 'Enter a version'}
          sx={{ mb: 2 }}
        />
        <TextField
          label="Change summary"
          value={changeSummary}
          onChange={(e) => setChangeSummary(e.target.value)}
          fullWidth
          multiline
          rows={2}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={submitting}>
          Cancel
        </Button>
        <Button variant="contained" onClick={handlePublish} disabled={submitting || !trimmedVersion}>
          {submitting ? 'Publishing...' : 'Publish'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default PublishDialog;
