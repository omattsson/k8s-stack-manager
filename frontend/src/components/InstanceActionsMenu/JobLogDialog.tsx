import { useId } from 'react';
import { Button, Dialog, DialogActions, DialogContent, DialogTitle } from '@mui/material';
import JobLogPanel from './JobLogPanel';
import { useJobLog } from './useJobLog';

/** Text when the subscriber does not know the job any more (HTTP 404). */
export const JOB_LOG_GONE_TEXT = 'The job log is no longer available.';

interface JobLogDialogProps {
  instanceId: string;
  /** Action name of the run. */
  actionName: string;
  /** Action label for the title. */
  label: string;
  jobId: string;
  /** Known job status of the run, shown until the first poll answers. */
  initialStatus?: string;
  /** Called once with the final status when the job log reaches its end. */
  onDone?: (status: string) => void;
  /** Called when the user closes the dialog. Stops the polling. */
  onClose: () => void;
}

/**
 * Dialog with the job log of a past action run. It polls at once and then
 * like the action dialog, until the job ends or the user closes the dialog.
 */
const JobLogDialog = ({ instanceId, actionName, label, jobId, initialStatus, onDone, onClose }: JobLogDialogProps) => {
  const titleId = useId();
  const job = useJobLog({
    instanceId,
    actionName,
    jobId,
    initialDelayMs: 0,
    initialStatus: initialStatus || undefined,
    notFoundMessage: JOB_LOG_GONE_TEXT,
    onDone,
  });

  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="md" aria-labelledby={titleId}>
      <DialogTitle id={titleId} sx={{ overflowWrap: 'anywhere' }}>{`Job log: ${label}`}</DialogTitle>
      <DialogContent>
        <JobLogPanel jobId={jobId} job={job} />
      </DialogContent>
      <DialogActions>
        {job.following && (
          <Button color="warning" onClick={job.stopFollowing}>Stop following</Button>
        )}
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
};

export default JobLogDialog;
