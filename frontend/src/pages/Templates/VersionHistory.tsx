import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Paper,
  Chip,
  Button,
  Alert,
  CircularProgress,
  List,
  ListItem,
  ListItemText,
  Collapse,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Checkbox,
  Divider,
  IconButton,
  Tooltip,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import CompareArrowsIcon from '@mui/icons-material/CompareArrows';
import HistoryIcon from '@mui/icons-material/History';
import ReactDiffViewer, { DiffMethod } from 'react-diff-viewer-continued';
import { templateService } from '../../api/client';
import type { TemplateVersion, VersionDiffResponse } from '../../types';

interface VersionHistoryProps {
  templateId: string;
}

const formatRelativeTime = (dateString: string): string => {
  const date = new Date(dateString);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMinutes = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMinutes / 60);
  const diffDays = Math.floor(diffHours / 24);

  if (diffMinutes < 1) return 'just now';
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  if (diffHours < 24) return `${diffHours}h ago`;
  if (diffDays < 30) return `${diffDays}d ago`;
  return date.toLocaleDateString();
};

const changeTypeColor = (changeType: string): 'success' | 'error' | 'info' | 'default' => {
  switch (changeType) {
    case 'added': return 'success';
    case 'removed': return 'error';
    case 'modified': return 'info';
    default: return 'default';
  }
};

const VersionHistory = ({ templateId }: VersionHistoryProps) => {
  const [versions, setVersions] = useState<TemplateVersion[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expandedVersion, setExpandedVersion] = useState<string | null>(null);
  const [expandedSnapshot, setExpandedSnapshot] = useState<TemplateVersion | null>(null);
  const [snapshotLoading, setSnapshotLoading] = useState(false);
  const [selectedForCompare, setSelectedForCompare] = useState<string[]>([]);
  const [diffDialogOpen, setDiffDialogOpen] = useState(false);
  const [diffData, setDiffData] = useState<VersionDiffResponse | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [diffError, setDiffError] = useState<string | null>(null);

  useEffect(() => {
    const fetchVersions = async () => {
      try {
        const data = await templateService.listVersions(templateId);
        setVersions(data || []);
      } catch {
        setError('Failed to load version history');
      } finally {
        setLoading(false);
      }
    };
    fetchVersions();
  }, [templateId]);

  const handleToggleExpand = useCallback(async (version: TemplateVersion) => {
    if (expandedVersion === version.id) {
      setExpandedVersion(null);
      setExpandedSnapshot(null);
      return;
    }

    setExpandedVersion(version.id);
    if (version.snapshot) {
      setExpandedSnapshot(version);
      return;
    }

    setSnapshotLoading(true);
    try {
      const fullVersion = await templateService.getVersion(templateId, version.id);
      setExpandedSnapshot(fullVersion);
    } catch {
      setExpandedSnapshot(null);
    } finally {
      setSnapshotLoading(false);
    }
  }, [expandedVersion, templateId]);

  const handleToggleCompare = (versionId: string) => {
    setSelectedForCompare((prev) => {
      if (prev.includes(versionId)) {
        return prev.filter((id) => id !== versionId);
      }
      if (prev.length >= 2) {
        return [prev[1], versionId];
      }
      return [...prev, versionId];
    });
  };

  const handleCompare = async () => {
    if (selectedForCompare.length !== 2) return;

    setDiffDialogOpen(true);
    setDiffLoading(true);
    setDiffError(null);
    setDiffData(null);

    try {
      const data = await templateService.diffVersions(
        templateId,
        selectedForCompare[0],
        selectedForCompare[1],
      );
      setDiffData(data);
    } catch {
      setDiffError('Failed to load version diff');
    } finally {
      setDiffLoading(false);
    }
  };

  const handleCloseDiff = () => {
    setDiffDialogOpen(false);
    setDiffData(null);
    setDiffError(null);
  };

  if (loading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
        <CircularProgress role="progressbar" />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">{error}</Alert>;
  }

  if (versions.length === 0) {
    return (
      <Paper sx={{ p: 3, textAlign: 'center' }}>
        <HistoryIcon sx={{ fontSize: 48, color: 'text.secondary', mb: 1 }} />
        <Typography color="text.secondary">
          No versions yet. Versions are created when the template is published.
        </Typography>
      </Paper>
    );
  }

  return (
    <Box>
      {selectedForCompare.length === 2 && (
        <Box sx={{ display: 'flex', justifyContent: 'flex-end', mb: 2 }}>
          <Button
            variant="contained"
            startIcon={<CompareArrowsIcon />}
            onClick={handleCompare}
          >
            Compare Selected Versions
          </Button>
        </Box>
      )}

      {selectedForCompare.length === 1 && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Select one more version to compare.
        </Alert>
      )}

      <List disablePadding>
        {versions.map((version, index) => (
          <Paper key={version.id} sx={{ mb: 1 }}>
            <ListItem
              secondaryAction={
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <Tooltip title="Select for comparison">
                    <Checkbox
                      checked={selectedForCompare.includes(version.id)}
                      onChange={() => handleToggleCompare(version.id)}
                      onClick={(e) => e.stopPropagation()}
                      size="small"
                      slotProps={{ input: { 'aria-label': `Select version ${version.version} for comparison` } }}
                    />
                  </Tooltip>
                  <IconButton
                    onClick={() => handleToggleExpand(version)}
                    size="small"
                    aria-label={expandedVersion === version.id ? 'Collapse version details' : 'Expand version details'}
                  >
                    {expandedVersion === version.id ? <ExpandLessIcon /> : <ExpandMoreIcon />}
                  </IconButton>
                </Box>
              }
              sx={{ cursor: 'pointer' }}
              onClick={() => handleToggleExpand(version)}
            >
              <ListItemText
                primary={
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                    <Chip
                      label={`v${version.version}`}
                      size="small"
                      color={index === 0 ? 'primary' : 'default'}
                      variant={index === 0 ? 'filled' : 'outlined'}
                    />
                    <Typography variant="body1" component="span">
                      {version.change_summary || 'No summary'}
                    </Typography>
                  </Box>
                }
                secondary={
                  <Typography variant="caption" color="text.secondary" component="span">
                    by {version.created_by} {formatRelativeTime(version.created_at)}
                    {' '}({new Date(version.created_at).toLocaleString()})
                  </Typography>
                }
              />
            </ListItem>

            <Collapse in={expandedVersion === version.id}>
              <Box sx={{ px: 3, pb: 2 }}>
                <Divider sx={{ mb: 2 }} />
                {snapshotLoading ? (
                  <Box sx={{ display: 'flex', justifyContent: 'center', py: 2 }}>
                    <CircularProgress size={24} />
                  </Box>
                  ) : !expandedSnapshot?.snapshot ? (
                    <Typography color="text.secondary" variant="body2">
                      Snapshot data not available.
                    </Typography>
                  ) : (
                  <Box>
                    <Typography variant="subtitle2" gutterBottom>
                      Template Snapshot
                    </Typography>
                    <Box sx={{ display: 'flex', gap: 1, mb: 2, flexWrap: 'wrap' }}>
                      <Chip label={expandedSnapshot.snapshot.template.name} size="small" />
                      {expandedSnapshot.snapshot.template.category && (
                        <Chip label={expandedSnapshot.snapshot.template.category} size="small" variant="outlined" />
                      )}
                      <Chip
                        label={expandedSnapshot.snapshot.template.is_published ? 'Published' : 'Draft'}
                        size="small"
                        color={expandedSnapshot.snapshot.template.is_published ? 'success' : 'default'}
                      />
                    </Box>

                    <Typography variant="subtitle2" gutterBottom>
                      Charts ({expandedSnapshot.snapshot.charts.length})
                    </Typography>
                    {expandedSnapshot.snapshot.charts.map((chart) => (
                      <Paper key={chart.chart_name} variant="outlined" sx={{ p: 2, mb: 1 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 0.5 }}>
                          <Typography variant="body2" sx={{ fontWeight: 'bold' }}>
                            {chart.chart_name}
                          </Typography>
                          {chart.is_required && (
                            <Chip label="Required" size="small" color="primary" />
                          )}
                          <Chip label={`Order: ${chart.sort_order}`} size="small" variant="outlined" />
                        </Box>
                        {chart.repo_url && (
                          <Typography variant="caption" color="text.secondary">
                            Repo: {chart.repo_url}
                          </Typography>
                        )}
                      </Paper>
                    ))}
                  </Box>
                )}
              </Box>
            </Collapse>
          </Paper>
        ))}
      </List>

      <Dialog
        open={diffDialogOpen}
        onClose={handleCloseDiff}
        maxWidth="lg"
        fullWidth
      >
        <DialogTitle>
          Version Comparison
          {diffData && (
            <Typography variant="body2" color="text.secondary">
              v{diffData.left.version.version} vs v{diffData.right.version.version}
            </Typography>
          )}
        </DialogTitle>
        <DialogContent dividers>
          {diffLoading && (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
              <CircularProgress />
            </Box>
          )}
          {diffError && <Alert severity="error">{diffError}</Alert>}
          {diffData && (
            <Box>
              {diffData.chart_diffs.length === 0 ? (
                <Typography color="text.secondary">No differences found.</Typography>
              ) : (
                diffData.chart_diffs.map((chartDiff) => (
                  <Box key={chartDiff.chart_name} sx={{ mb: 3 }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                      <Typography variant="subtitle1" sx={{ fontWeight: 'bold' }}>
                        {chartDiff.chart_name}
                      </Typography>
                      <Chip
                        label={chartDiff.change_type}
                        size="small"
                        color={changeTypeColor(chartDiff.change_type)}
                      />
                    </Box>
                    {chartDiff.has_differences ? (
                      <Box sx={{ border: 1, borderColor: 'divider', borderRadius: 1, overflow: 'hidden' }}>
                        <ReactDiffViewer
                          oldValue={chartDiff.left_values || ''}
                          newValue={chartDiff.right_values || ''}
                          splitView={true}
                          compareMethod={DiffMethod.LINES}
                          leftTitle={`v${diffData.left.version.version}`}
                          rightTitle={`v${diffData.right.version.version}`}
                        />
                      </Box>
                    ) : (
                      <Typography variant="body2" color="text.secondary">
                        No value changes.
                      </Typography>
                    )}
                  </Box>
                ))
              )}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={handleCloseDiff}>Close</Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};

export default VersionHistory;
