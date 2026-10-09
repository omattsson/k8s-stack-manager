import { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControl,
  FormControlLabel,
  FormLabel,
  Radio,
  RadioGroup,
} from '@mui/material';
import type { DeploymentLog } from '../../types';
import { describeDeploy } from '../../utils/deployHistory';

interface RollbackDialogProps {
  open: boolean;
  instanceName: string;
  /** Successful deploys of the instance, newest first. */
  deploys: DeploymentLog[];
  /** ID of the deploy whose values run now, if known. It cannot be selected. */
  currentLogId?: string;
  /** Preselected target (default: the previous deploy). */
  initialTargetId?: string;
  onConfirm: (targetLogId: string) => void;
  onClose: () => void;
}

/** Lets the user pick a successful deploy to roll back to, and confirm the rollback. */
const RollbackDialog = ({
  open,
  instanceName,
  deploys,
  currentLogId,
  initialTargetId,
  onConfirm,
  onClose,
}: RollbackDialogProps) => {
  const [targetId, setTargetId] = useState(initialTargetId ?? '');

  useEffect(() => {
    if (open) setTargetId(initialTargetId ?? '');
  }, [open, initialTargetId]);

  const target = deploys.find((d) => d.id === targetId);

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth aria-labelledby="rollback-dialog-title">
      <DialogTitle id="rollback-dialog-title">Roll Back Instance</DialogTitle>
      <DialogContent>
        <FormControl sx={{ mb: 2 }}>
          <FormLabel id="rollback-target-label">Roll back to</FormLabel>
          <RadioGroup
            aria-labelledby="rollback-target-label"
            value={targetId}
            onChange={(e) => setTargetId(e.target.value)}
          >
            {deploys.map((d) => (
              <FormControlLabel
                key={d.id}
                value={d.id}
                disabled={d.id === currentLogId}
                control={<Radio />}
                label={
                  <>
                    {describeDeploy(d)}
                    {d.id === currentLogId && <Chip label="current" size="small" sx={{ ml: 1 }} />}
                  </>
                }
              />
            ))}
          </RadioGroup>
        </FormControl>
        {target && (
          <DialogContentText sx={{ mb: 2 }}>
            Roll back &quot;{instanceName}&quot; to the deploy of {describeDeploy(target)}?
          </DialogContentText>
        )}
        <Alert severity="info">
          The rollback does not change the stored overrides. The next deploy applies them again.
        </Alert>
        <Alert severity="warning" sx={{ mt: 1 }}>
          This restores the stored values of that deploy, not its images. Image tags that are branch names can point to newer images. Pre-deploy checks do not run for a rollback. Pre-rollback hooks run.
        </Alert>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          color="warning"
          disabled={!target}
          onClick={() => target && onConfirm(target.id)}
        >
          Roll Back
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default RollbackDialog;
