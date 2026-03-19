import { useEffect, useRef } from 'react';
import { Box, Typography, Paper, Chip } from '@mui/material';
import type { DeploymentLog } from '../../types';

interface DeploymentLogViewerProps {
  logs: DeploymentLog[];
  loading?: boolean;
}

const DeploymentLogViewer = ({ logs, loading }: DeploymentLogViewerProps) => {
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [logs]);

  // Status color mapping
  const statusColor = (status: string) => {
    switch (status) {
      case 'success': return 'success';
      case 'error': return 'error';
      case 'running': return 'info';
      default: return 'default';
    }
  };

  if (logs.length === 0 && !loading) {
    return (
      <Typography variant="body2" color="text.secondary">
        No deployment history
      </Typography>
    );
  }

  return (
    <Box>
      {logs.map((log) => (
        <Paper key={log.id} sx={{ mb: 2, overflow: 'hidden' }}>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, p: 1.5, bgcolor: 'grey.100' }}>
            <Chip
              label={log.action}
              size="small"
              color={log.action === 'deploy' ? 'primary' : 'warning'}
              variant="outlined"
            />
            <Chip
              label={log.status}
              size="small"
              color={statusColor(log.status) as 'success' | 'error' | 'info' | 'default'}
            />
            <Typography variant="caption" color="text.secondary" sx={{ ml: 'auto' }}>
              {new Date(log.started_at).toLocaleString()}
              {log.completed_at && ` — ${new Date(log.completed_at).toLocaleString()}`}
            </Typography>
          </Box>
          {log.output && (
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
          )}
        </Paper>
      ))}
      <div ref={bottomRef} />
    </Box>
  );
};

export default DeploymentLogViewer;
