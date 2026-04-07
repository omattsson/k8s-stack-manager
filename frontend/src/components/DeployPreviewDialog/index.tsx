import { useState, useEffect, useMemo } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Box,
  Tabs,
  Tab,
  Chip,
  Alert,
  CircularProgress,
  Typography,
} from '@mui/material';
import ReactDiffViewer from 'react-diff-viewer-continued';
import { instanceService } from '../../api/client';
import { useThemeMode } from '../../context/ThemeContext';
import type { DeployPreviewResponse, ChartDeployPreview } from '../../types';

interface DeployPreviewDialogProps {
  open: boolean;
  instanceId: string | number;
  instanceName: string;
  onConfirm: () => void;
  onClose: () => void;
}

const DeployPreviewDialog = ({
  open,
  instanceId,
  instanceName,
  onConfirm,
  onClose,
}: DeployPreviewDialogProps) => {
  const { mode } = useThemeMode();
  const [preview, setPreview] = useState<DeployPreviewResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState(0);

  useEffect(() => {
    if (!open) return;

    let cancelled = false;
    setLoading(true);
    setError(null);
    setPreview(null);
    setActiveTab(0);

    instanceService
      .deployPreview(instanceId)
      .then((data) => {
        if (!cancelled) setPreview(data);
      })
      .catch(() => {
        if (!cancelled) setError('Failed to load deploy preview');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => { cancelled = true; };
  }, [open, instanceId]);

  const chartsWithChanges = useMemo(
    () => (preview?.charts ?? []).filter((c) => c.has_changes),
    [preview],
  );

  const chartsUnchanged = useMemo(
    () => (preview?.charts ?? []).filter((c) => !c.has_changes),
    [preview],
  );

  const activeChart: ChartDeployPreview | undefined = chartsWithChanges[activeTab];

  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="lg">
      <DialogTitle>Review Changes — {instanceName}</DialogTitle>
      <DialogContent dividers>
        {loading && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
            <CircularProgress />
          </Box>
        )}

        {error && (
          <Alert severity="warning">
            Failed to load deploy preview. You can still proceed with deployment.
          </Alert>
        )}

        {preview && !loading && !error && (
          <Box>
            {/* Summary chips */}
            <Box sx={{ display: 'flex', gap: 1, mb: 2, flexWrap: 'wrap' }}>
              {chartsWithChanges.length > 0 && (
                <Chip
                  label={`${chartsWithChanges.length} chart${chartsWithChanges.length > 1 ? 's' : ''} changed`}
                  color="warning"
                  size="small"
                  variant="outlined"
                />
              )}
              {chartsUnchanged.length > 0 && (
                <Chip
                  label={`${chartsUnchanged.length} unchanged`}
                  color="success"
                  size="small"
                  variant="outlined"
                />
              )}
            </Box>

            {chartsWithChanges.length === 0 ? (
              <Alert severity="info">
                No value changes detected. The deployment will use the same values as the last deploy.
              </Alert>
            ) : (
              <Box>
                {chartsWithChanges.length > 1 && (
                  <Tabs
                    value={activeTab}
                    onChange={(_e, v: number) => setActiveTab(v)}
                    variant="scrollable"
                    scrollButtons="auto"
                    sx={{ mb: 2, borderBottom: 1, borderColor: 'divider' }}
                  >
                    {chartsWithChanges.map((chart, index) => (
                      <Tab
                        key={chart.chart_name}
                        label={chart.chart_name}
                        id={`preview-tab-${index}`}
                        aria-controls={`preview-tabpanel-${index}`}
                      />
                    ))}
                  </Tabs>
                )}

                {activeChart && (
                  <Box
                    role="tabpanel"
                    id={`preview-tabpanel-${activeTab}`}
                    aria-labelledby={`preview-tab-${activeTab}`}
                  >
                    {chartsWithChanges.length === 1 && (
                      <Typography variant="subtitle2" sx={{ mb: 1 }}>
                        {activeChart.chart_name}
                      </Typography>
                    )}
                    <Box sx={{ border: 1, borderColor: 'divider', borderRadius: 1, overflow: 'auto' }}>
                      <ReactDiffViewer
                        oldValue={activeChart.previous_values || ''}
                        newValue={activeChart.pending_values || ''}
                        splitView
                        leftTitle="Previous Values"
                        rightTitle="Pending Values"
                        showDiffOnly={false}
                        useDarkTheme={mode === 'dark'}
                      />
                    </Box>
                  </Box>
                )}
              </Box>
            )}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          color="success"
          onClick={onConfirm}
        >
          {loading ? 'Deploy anyway' : 'Deploy'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default DeployPreviewDialog;
