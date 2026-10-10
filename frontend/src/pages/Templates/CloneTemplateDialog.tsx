import { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  TextField,
} from '@mui/material';
import { templateService } from '../../api/client';
import { describeApiError } from '../../utils/apiError';
import type { StackTemplate } from '../../types';

/** Longest template name the API stores. */
export const TEMPLATE_NAME_MAX_LENGTH = 255;

interface CloneTemplateDialogProps {
  open: boolean;
  /** The template to copy. */
  template: StackTemplate;
  /** Called when the user cancels. The dialog creates nothing. */
  onClose: () => void;
  /** Called with the new template after the clone call succeeds. */
  onCloned: (cloned: StackTemplate) => void;
}

/**
 * Ask for the name of the copy, then clone the template. The name is
 * prefilled with "<name> (Copy)". Cancel creates nothing.
 */
const CloneTemplateDialog = ({ open, template, onClose, onCloned }: CloneTemplateDialogProps) => {
  const [name, setName] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setName(`${template.name} (Copy)`);
    setError(null);
    setSubmitting(false);
  }, [open, template.name]);

  const trimmed = name.trim();
  const tooLong = trimmed.length > TEMPLATE_NAME_MAX_LENGTH;
  const invalid = trimmed === '' || tooLong;

  const handleClone = async () => {
    if (invalid) return;
    setSubmitting(true);
    setError(null);
    try {
      const cloned = await templateService.clone(template.id, { name: trimmed });
      onCloned(cloned);
    } catch (err) {
      setError(await describeApiError(err, 'Failed to clone template'));
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={submitting ? undefined : onClose}
      maxWidth="sm"
      fullWidth
      aria-labelledby="clone-template-title"
    >
      <DialogTitle id="clone-template-title">Clone as Template</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ mb: 2 }}>
          The copy gets the charts and the settings of &quot;{template.name}&quot;. The copy is not published.
        </DialogContentText>
        <TextField
          fullWidth
          required
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              void handleClone();
            }
          }}
          error={invalid}
          helperText={
            trimmed === ''
              ? 'Name is required'
              : tooLong
                ? `Use ${TEMPLATE_NAME_MAX_LENGTH} characters or fewer`
                : undefined
          }
          disabled={submitting}
        />
        {error && <Alert severity="error" sx={{ mt: 2 }}>{error}</Alert>}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={submitting}>Cancel</Button>
        <Button
          variant="contained"
          onClick={handleClone}
          disabled={submitting || invalid}
          startIcon={submitting ? <CircularProgress size={14} /> : undefined}
        >
          {submitting ? 'Cloning...' : 'Clone'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default CloneTemplateDialog;
