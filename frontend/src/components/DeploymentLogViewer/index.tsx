import { useState, useEffect, useRef } from 'react';
import {
  Box,
  Typography,
  Chip,
  Accordion,
  AccordionSummary,
  AccordionDetails,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import type { DeploymentLog } from '../../types';

interface DeploymentLogViewerProps {
  logs: DeploymentLog[];
  loading?: boolean;
}

const statusColor = (status: string): 'success' | 'error' | 'info' | 'default' => {
  switch (status) {
    case 'success': return 'success';
    case 'error': return 'error';
    case 'running': return 'info';
    default: return 'default';
  }
};

const actionColor = (action: string): 'primary' | 'warning' | 'error' => {
  switch (action) {
    case 'deploy': return 'primary';
    case 'stop': return 'warning';
    case 'clean': return 'error';
    default: return 'primary';
  }
};

const formatDuration = (startedAt: string, completedAt: string): string => {
  const start = new Date(startedAt).getTime();
  const end = new Date(completedAt).getTime();
  const seconds = Math.round((end - start) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  return `${minutes}m ${remainingSeconds}s`;
};

const DeploymentLogViewer = ({ logs, loading }: DeploymentLogViewerProps) => {
  const bottomRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState<string | false>(false);

  // Expand the most recent log by default, but only when there's no user selection
  // or the previously expanded log no longer exists in the list.
  useEffect(() => {
    if (logs.length > 0) {
      setExpanded((prev) => {
        // No selection yet — default to most recent.
        if (!prev) return logs[0].id;
        // Current selection still exists — keep it.
        if (logs.some((l) => l.id === prev)) return prev;
        // Current selection gone — fall back to most recent.
        return logs[0].id;
      });
    }
  }, [logs]);

  // Auto-scroll when a running log's output updates
  useEffect(() => {
    const activeLog = logs.find((l) => l.status === 'running');
    if (activeLog && expanded === activeLog.id) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [logs, expanded]);

  if (logs.length === 0 && !loading) {
    return (
      <Typography variant="body2" color="text.secondary">
        No deployment history
      </Typography>
    );
  }

  const handleAccordionChange = (logId: string) => (_event: React.SyntheticEvent, isExpanded: boolean) => {
    setExpanded(isExpanded ? logId : false);
  };

  return (
    <Box>
      {logs.map((log) => (
        <Accordion
          key={log.id}
          expanded={expanded === log.id}
          onChange={handleAccordionChange(log.id)}
          sx={{ mb: 1, '&:before': { display: 'none' } }}
        >
          <AccordionSummary expandIcon={<ExpandMoreIcon />}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, width: '100%', mr: 1 }}>
              <Chip
                label={log.action}
                size="small"
                color={actionColor(log.action)}
                variant="outlined"
              />
              <Chip
                label={log.status}
                size="small"
                color={statusColor(log.status)}
              />
              <Typography variant="caption" color="text.secondary" sx={{ ml: 'auto' }}>
                {new Date(log.started_at).toLocaleString()}
                {log.completed_at && ` (${formatDuration(log.started_at, log.completed_at)})`}
              </Typography>
            </Box>
          </AccordionSummary>
          <AccordionDetails sx={{ p: 0 }}>
            {log.output ? (
              <Box
                sx={{
                  p: 2,
                  bgcolor: '#1e1e1e',
                  color: '#d4d4d4',
                  fontFamily: 'monospace',
                  fontSize: '0.8rem',
                  lineHeight: 1.6,
                  maxHeight: 300,
                  overflow: 'auto',
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-all',
                }}
              >
                {log.output}
                {log.error_message && (
                  <Box sx={{ color: '#f44336', mt: 1 }}>
                    Error: {log.error_message}
                  </Box>
                )}
              </Box>
            ) : (
              <Box sx={{ p: 2 }}>
                <Typography variant="body2" color="text.secondary">
                  {log.status === 'running' ? 'Waiting for output...' : 'No output recorded'}
                </Typography>
              </Box>
            )}
          </AccordionDetails>
        </Accordion>
      ))}
      <div ref={bottomRef} />
    </Box>
  );
};

export default DeploymentLogViewer;
