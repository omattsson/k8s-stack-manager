/** Chip color of a job status. */
export type JobStatusColor = 'info' | 'success' | 'error';

/**
 * Chip color of a job status: `succeeded` is success, an active status
 * (`running`, `pending`, `queued`, `started`) is info, any other status is error.
 * @param status - Job status from the job log or the action run
 * @returns The MUI color of the status chip
 */
export const statusColor = (status: string): JobStatusColor => {
  if (status === 'succeeded') return 'success';
  if (['running', 'pending', 'queued', 'started'].includes(status)) return 'info';
  return 'error';
};
