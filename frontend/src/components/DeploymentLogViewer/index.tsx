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
  streamingLines?: Record<string, string[]>;
}

const statusColor = (status: string): 'success' | 'error' | 'info' | 'default' => {
  switch (status) {
    case 'success': return 'success';
    case 'error': return 'error';
    case 'running': return 'info';
    default: return 'default';
  }
};

const actionColor = (action: string): 'primary' | 'warning' | 'error' | 'info' => {
  switch (action) {
    case 'deploy': return 'primary';
    case 'stop': return 'warning';
    case 'clean': return 'error';
    case 'rollback': return 'info';
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

const terminalSx = {
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
} as const;

const DeploymentLogViewer = ({ logs, loading, streamingLines }: DeploymentLogViewerProps) => {
  const bottomRef = useRef<HTMLDivElement>(null);
  const streamContainerRef = useRef<HTMLDivElement>(null);
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

  // Auto-scroll streaming output to bottom as new lines arrive.
  useEffect(() => {
    const el = streamContainerRef.current;
    if (el) {
      el.scrollTop = el.scrollHeight;
    }
  }, [streamingLines, expanded]);

  // Auto-scroll when a running log's output updates
  useEffect(() => {
    const activeLog = logs.find((l) => l.status === 'running');
    if (expanded === activeLog?.id) {
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
      {logs.map((log) => {
        const lines = streamingLines?.[log.id];
        const isStreaming = log.status === 'running' && lines && lines.length > 0;
        const isExpanded = expanded === log.id;

        return (
          <Accordion
            key={log.id}
            expanded={isExpanded}
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
                {isStreaming && (
                  <Chip
                    label="LIVE"
                    size="small"
                    color="info"
                    variant="filled"
                    sx={{
                      animation: 'pulse 2s infinite',
                      '@keyframes pulse': {
                        '0%, 100%': { opacity: 1 },
                        '50%': { opacity: 0.5 },
                      },
                    }}
                  />
                )}
                <Typography variant="caption" color="text.secondary" sx={{ ml: 'auto' }}>
                  {new Date(log.started_at).toLocaleString()}
                  {log.completed_at && ` (${formatDuration(log.started_at, log.completed_at)})`}
                </Typography>
              </Box>
            </AccordionSummary>
            <AccordionDetails sx={{ p: 0 }}>
              {isStreaming ? (
                <Box
                  ref={isExpanded ? streamContainerRef : undefined}
                  sx={terminalSx}
                >
                  {lines.map((line, i) => (
                    <div key={i}>{line || '\u00A0'}</div>
                  ))}
                </Box>
              ) : log.output ? (
                <Box sx={terminalSx}>
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
        );
      })}
      <div ref={bottomRef} />
    </Box>
  );
};

export default DeploymentLogViewer;
