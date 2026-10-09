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
  TextField,
} from '@mui/material';
import { instanceService } from '../../api/client';
import TtlSelector from '../../components/TtlSelector';
import type { StackInstance } from '../../types';
import { describeApiError } from '../../utils/apiError';
import {
  INSTANCE_NAME_MAX_LENGTH,
  INSTANCE_NAME_RULE,
  suggestCloneName,
  validateInstanceName,
} from '../../utils/instanceName';

interface CloneDialogProps {
  open: boolean;
  /** The instance to copy. */
  source: StackInstance;
  onClose: () => void;
  /** Called with the new instance after a successful clone. */
  onCloned: (cloned: StackInstance) => void;
}

/**
 * Asks for the name, branch and TTL of a copy of an instance, then clones it.
 * The name is checked live with the instance name rule (RFC 1123 label).
 */
const CloneDialog = ({ open, source, onClose, onCloned }: CloneDialogProps) => {
  const [name, setName] = useState('');
  const [branch, setBranch] = useState('');
  const [ttlMinutes, setTtlMinutes] = useState(0);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setName(suggestCloneName(source.name));
      setBranch(source.branch);
      setTtlMinutes(source.ttl_minutes ?? 0);
      setError(null);
      setSubmitting(false);
    }
  }, [open, source.name, source.branch, source.ttl_minutes]);

  const nameError = validateInstanceName(name);
  // When the user keeps the suggested name, the server picks a free `<name>-copy[-N]`.
  const nameEdited = name !== suggestCloneName(source.name);

  const handleSubmit = async () => {
    if (nameError) return;
    setSubmitting(true);
    setError(null);
    try {
      const cloned = await instanceService.clone(source.id, {
        ...(nameEdited ? { name } : {}),
        ...(branch.trim() ? { branch: branch.trim() } : {}),
        ttl_minutes: ttlMinutes,
      });
      onCloned(cloned);
    } catch (err) {
      setError(await describeApiError(err, 'Failed to clone instance'));
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={submitting ? undefined : onClose}
      maxWidth="sm"
      fullWidth
      aria-labelledby="clone-dialog-title"
    >
      <DialogTitle id="clone-dialog-title">Clone Instance</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
          {error && <Alert severity="error">{error}</Alert>}
          <Alert severity="info">
            The copy gets the value overrides, branch overrides and quota override of &quot;{source.name}&quot;.
          </Alert>
          <TextField
            label="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            fullWidth
            error={nameError !== null}
            helperText={nameError ?? `${INSTANCE_NAME_RULE} ${name.length}/${INSTANCE_NAME_MAX_LENGTH}`}
          />
          <TextField
            label="Branch"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            fullWidth
            helperText="Leave empty to use the branch of the source"
          />
          <TtlSelector value={ttlMinutes} onChange={setTtlMinutes} disabled={submitting} />
        </Box>
      </DialogContent>
      <DialogActions sx={{ px: 3, pb: 2 }}>
        <Button onClick={onClose} disabled={submitting}>Cancel</Button>
        <Button
          variant="contained"
          onClick={handleSubmit}
          disabled={submitting || nameError !== null}
          startIcon={submitting ? <CircularProgress size={18} /> : undefined}
        >
          {submitting ? 'Cloning...' : 'Create Clone'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default CloneDialog;
