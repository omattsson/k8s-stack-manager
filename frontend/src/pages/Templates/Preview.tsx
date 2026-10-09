import { useCallback, useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Typography,
  Paper,
  Chip,
  Button,
  Alert,
  Divider,
  Tabs,
  Tab,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  CircularProgress,
  ToggleButton,
  ToggleButtonGroup,
} from '@mui/material';
import { templateService } from '../../api/client';
import { useAuth } from '../../context/AuthContext';
import type { PublishTemplateResult, StackTemplate, VersionDiffResponse } from '../../types';
import LoadingState from '../../components/LoadingState';
import ChartDiffList from '../../components/ChartDiffList';
import ConfirmDialog from '../../components/ConfirmDialog';
import VersionHistory from './VersionHistory';
import PublishDialog from './PublishDialog';

interface StatusMessage {
  severity: 'success' | 'info' | 'error';
  text: string;
}

const Preview = () => {
  const { id } = useParams<{ id: string }>();
  const [template, setTemplate] = useState<StackTemplate | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { user } = useAuth();
  const navigate = useNavigate();

  const [activeTab, setActiveTab] = useState(0);
  const [publishOpen, setPublishOpen] = useState(false);
  const [unpublishing, setUnpublishing] = useState(false);
  const [statusMessage, setStatusMessage] = useState<StatusMessage | null>(null);
  const [historyKey, setHistoryKey] = useState(0);
  const [changesOpen, setChangesOpen] = useState(false);
  const [changesLoading, setChangesLoading] = useState(false);
  const [changesError, setChangesError] = useState<string | null>(null);
  const [changes, setChanges] = useState<VersionDiffResponse | null>(null);
  const [confirmUnpublishOpen, setConfirmUnpublishOpen] = useState(false);
  /** Chart view chosen by a manager. Null: use the default for the user. */
  const [chartsViewChoice, setChartsViewChoice] = useState<'working' | 'released' | null>(null);

  const isDevOps = user?.role === 'devops' || user?.role === 'admin';
  const isOwner = Boolean(template && user && template.owner_id === user.id);
  const canManage = isDevOps && (isOwner || user?.role === 'admin');

  const loadTemplate = useCallback(async () => {
    if (!id) return;
    try {
      const data = await templateService.get(id);
      setTemplate(data);
    } catch {
      setError('Failed to load template');
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    loadTemplate();
  }, [loadTemplate]);

  const handleClone = async () => {
    if (!id) return;
    try {
      const cloned = await templateService.clone(id);
      navigate(`/templates/${cloned.id}/edit`);
    } catch {
      setError('Failed to clone template');
    }
  };

  const handlePublished = async (result: PublishTemplateResult, requestedVersion: string) => {
    setPublishOpen(false);
    const releasedVersion = result.template.published_version || template?.published_version || requestedVersion;
    setStatusMessage(
      result.snapshotCreated
        ? { severity: 'success', text: `Published version ${releasedVersion}.` }
        : { severity: 'info', text: `No changes since version ${releasedVersion}. No new version was created.` },
    );
    setHistoryKey((k) => k + 1);
    await loadTemplate();
  };

  const handleUnpublish = async () => {
    if (!id) return;
    setConfirmUnpublishOpen(false);
    setUnpublishing(true);
    try {
      await templateService.unpublish(id);
      setStatusMessage({ severity: 'info', text: 'Template unpublished. Users cannot use it until you publish it again.' });
      await loadTemplate();
    } catch {
      setStatusMessage({ severity: 'error', text: 'Failed to unpublish template' });
    } finally {
      setUnpublishing(false);
    }
  };

  const handleShowChanges = async () => {
    if (!id || !template?.published_version) return;
    setChangesOpen(true);
    setChangesLoading(true);
    setChangesError(null);
    setChanges(null);
    try {
      let releaseId = template.published_version_id ?? undefined;
      if (!releaseId) {
        const versions = await templateService.listVersions(id);
        releaseId = (versions.find((v) => v.version === template.published_version) ?? versions[0])?.id;
      }
      if (!releaseId) {
        setChangesError('No release found to compare with.');
        return;
      }
      setChanges(await templateService.diffVersions(id, releaseId));
    } catch {
      setChangesError('Failed to load changes');
    } finally {
      setChangesLoading(false);
    }
  };

  if (loading) {
    return <LoadingState label="Loading template..." />;
  }

  if (error || !template) {
    return <Alert severity="error">{error || 'Template not found'}</Alert>;
  }

  // Users who cannot manage the template see what they get (the release).
  // Managers see the working copy and can switch to the release.
  const releasedCharts = Array.isArray(template.published_charts) ? template.published_charts : null;
  const chartsView: 'working' | 'released' = releasedCharts
    ? (canManage ? (chartsViewChoice ?? 'working') : 'released')
    : 'working';
  const displayedCharts = (chartsView === 'released' ? releasedCharts : template.charts) ?? [];
  const releasedLabel = `Released (v${template.published_version ?? '?'})`;

  return (
    <Box>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', mb: 3 }}>
        <Box>
          <Typography variant="h4" component="h1">
            {template.name}
          </Typography>
          <Box sx={{ display: 'flex', gap: 1, mt: 1, flexWrap: 'wrap' }}>
            <Chip label={template.is_published ? 'Published' : 'Draft'} color={template.is_published ? 'success' : 'default'} size="small" />
            {template.category && <Chip label={template.category} variant="outlined" size="small" />}
            {/* The working copy version is for managers only; other users get the release. */}
            {canManage && template.version && (
              <Chip label={`Working copy: v${template.version}`} variant="outlined" size="small" />
            )}
            {!canManage && !template.published_version && template.version && (
              <Chip label={`v${template.version}`} variant="outlined" size="small" />
            )}
            {template.published_version && (
              <Chip label={`Released: v${template.published_version}`} color="primary" variant="outlined" size="small" />
            )}
          </Box>
        </Box>
        <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
          {template.is_published && (
            <Button variant="contained" onClick={() => navigate(`/templates/${id}/use`)}>
              Use Template
            </Button>
          )}
          {canManage && (
            <>
              <Button variant="outlined" onClick={() => setPublishOpen(true)}>
                Publish
              </Button>
              {template.is_published && (
                <Button variant="outlined" color="warning" onClick={() => setConfirmUnpublishOpen(true)} disabled={unpublishing}>
                  {unpublishing ? 'Unpublishing...' : 'Unpublish'}
                </Button>
              )}
            </>
          )}
          {isDevOps && (
            <>
              <Button variant="outlined" onClick={handleClone}>
                Clone as Template
              </Button>
              {canManage && (
                <Button variant="outlined" onClick={() => navigate(`/templates/${id}/edit`)}>
                  Edit
                </Button>
              )}
            </>
          )}
        </Box>
      </Box>

      {statusMessage && (
        <Alert severity={statusMessage.severity} sx={{ mb: 2 }} onClose={() => setStatusMessage(null)}>
          {statusMessage.text}
        </Alert>
      )}

      {canManage && template.has_unpublished_changes && template.published_version && (
        <Alert
          severity="info"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" onClick={handleShowChanges}>
              Show changes
            </Button>
          }
        >
          Unpublished changes. Users get version {template.published_version}.
        </Alert>
      )}

      {template.description && (
        <Typography variant="body1" sx={{ mb: 3 }} color="text.secondary">
          {template.description}
        </Typography>
      )}

      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Default Branch: {template.default_branch}
      </Typography>

      <Tabs
        value={activeTab}
        onChange={(_e, newValue: number) => setActiveTab(newValue)}
        sx={{ mb: 3, borderBottom: 1, borderColor: 'divider' }}
      >
        <Tab label={`Charts (${displayedCharts.length})`} />
        <Tab label="Version History" />
      </Tabs>

      {activeTab === 0 && (
        <Box>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, mb: 2 }}>
            <Typography variant="subtitle2" component="p" color="text.secondary">
              {chartsView === 'released' ? releasedLabel : 'Working copy'}
            </Typography>
            {canManage && releasedCharts && (
              <ToggleButtonGroup
                size="small"
                exclusive
                value={chartsView}
                onChange={(_e, value: 'working' | 'released' | null) => {
                  if (value) setChartsViewChoice(value);
                }}
                aria-label="Chart version shown"
              >
                <ToggleButton value="working">Working copy</ToggleButton>
                <ToggleButton value="released">{releasedLabel}</ToggleButton>
              </ToggleButtonGroup>
            )}
          </Box>
          {displayedCharts.length === 0 ? (
            <Typography color="text.secondary">No charts configured.</Typography>
          ) : (
            displayedCharts.map((chart) => (
              <Paper key={chart.id || chart.chart_name} sx={{ p: 3, mb: 2 }}>
                <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
                  <Typography variant="h6">{chart.chart_name}</Typography>
                  <Box sx={{ display: 'flex', gap: 1 }}>
                    {chart.required && <Chip label="Required" color="primary" size="small" />}
                    <Chip label={`Order: ${chart.deploy_order}`} variant="outlined" size="small" />
                  </Box>
                </Box>
                {chart.repository_url && (
                  <Typography variant="body2" color="text.secondary">
                    Repo: {chart.repository_url}
                  </Typography>
                )}
                {chart.chart_path && (
                  <Typography variant="body2" color="text.secondary">
                    Path: {chart.chart_path} {chart.chart_version && `(v${chart.chart_version})`}
                  </Typography>
                )}
                {chart.default_values && (
                  <>
                    <Divider sx={{ my: 2 }} />
                    <Typography variant="subtitle2" gutterBottom>Default Values</Typography>
                    <Paper variant="outlined" sx={{ p: 2, bgcolor: 'grey.50' }}>
                      <Typography variant="body2" component="pre" sx={{ fontFamily: 'monospace', fontSize: 13, whiteSpace: 'pre-wrap', m: 0 }}>
                        {chart.default_values}
                      </Typography>
                    </Paper>
                  </>
                )}
                {chart.locked_values && (
                  <>
                    <Divider sx={{ my: 2 }} />
                    <Typography variant="subtitle2" gutterBottom>
                      Locked Values
                      <Chip label="Read-only" size="small" color="warning" sx={{ ml: 1 }} />
                    </Typography>
                    <Paper variant="outlined" sx={{ p: 2, bgcolor: 'warning.50', borderColor: 'warning.main' }}>
                      <Typography variant="body2" component="pre" sx={{ fontFamily: 'monospace', fontSize: 13, whiteSpace: 'pre-wrap', m: 0 }}>
                        {chart.locked_values}
                      </Typography>
                    </Paper>
                  </>
                )}
              </Paper>
            ))
          )}
        </Box>
      )}

      {activeTab === 1 && id && (
        <VersionHistory key={historyKey} templateId={id} />
      )}

      {canManage && (
        <PublishDialog
          open={publishOpen}
          template={template}
          onClose={() => setPublishOpen(false)}
          onPublished={handlePublished}
        />
      )}

      <ConfirmDialog
        open={confirmUnpublishOpen}
        title="Unpublish template?"
        message="Users cannot use this template (Use Template and Quick Deploy) until you publish it again. Existing definitions and instances do not change."
        confirmText="Unpublish"
        onConfirm={handleUnpublish}
        onCancel={() => setConfirmUnpublishOpen(false)}
      />

      <Dialog open={changesOpen} onClose={() => setChangesOpen(false)} maxWidth="lg" fullWidth>
        <DialogTitle>
          Unpublished changes
          <Typography variant="body2" color="text.secondary" component="span" sx={{ display: 'block' }}>
            Version {template.published_version} vs working copy
          </Typography>
        </DialogTitle>
        <DialogContent dividers>
          {changesLoading && (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
              <CircularProgress />
            </Box>
          )}
          {changesError && <Alert severity="error">{changesError}</Alert>}
          {changes && (
            <ChartDiffList
              chartDiffs={changes.chart_diffs}
              leftTitle={`v${changes.left.version}`}
              rightTitle="Working copy"
              emptyMessage="No chart value changes. Template details (for example the description) can differ."
            />
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setChangesOpen(false)}>Close</Button>
        </DialogActions>
      </Dialog>

      <Button variant="outlined" onClick={() => navigate('/templates')} sx={{ mt: 2 }}>
        Back to Gallery
      </Button>
    </Box>
  );
};

export default Preview;
