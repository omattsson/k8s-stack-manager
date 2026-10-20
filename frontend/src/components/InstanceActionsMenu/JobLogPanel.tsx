import { useEffect, useRef } from 'react';
import { Alert, Box, Chip, CircularProgress, Stack, Typography } from '@mui/material';
import { statusColor } from '../../utils/actionRuns';
import type { JobLogState } from './useJobLog';

interface JobLogPanelProps {
  jobId: string;
  /** State of {@link useJobLog}. */
  job: JobLogState;
}

/** Job ID, status chip, log text and the alerts of a followed job log. */
const JobLogPanel = ({ jobId, job }: JobLogPanelProps) => {
  const logRef = useRef<HTMLElement | null>(null);

  // Keep the newest log lines in view.
  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [job.log]);

  return (
    <Box>
      <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: 'center', flexWrap: 'wrap', mb: 1 }}>
        <Typography variant="subtitle2" sx={{ overflowWrap: 'anywhere', minWidth: 0 }}>Job {jobId}</Typography>
        <Chip size="small" label={job.status} color={statusColor(job.status)} data-testid="job-status" />
        {job.following && <CircularProgress size={14} aria-label="Job running" />}
        {job.following && job.waiting && (
          <Typography variant="body2" color="text.secondary" data-testid="job-waiting">
            Waiting…
          </Typography>
        )}
      </Stack>
      <Box
        component="pre"
        ref={logRef}
        data-testid="job-log"
        sx={{ m: 0, p: 1, bgcolor: 'grey.900', color: 'grey.100', borderRadius: 1, overflow: 'auto', height: 300, fontSize: 12 }}
      >
        {job.log || 'Waiting for log output...'}
      </Box>
      {job.error && <Alert severity="error" sx={{ mt: 1 }}>{job.error}</Alert>}
      {job.notice && <Alert severity="info" sx={{ mt: 1 }}>{job.notice}</Alert>}
    </Box>
  );
};

export default JobLogPanel;
