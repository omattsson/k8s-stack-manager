import { useEffect, useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Typography,
  Grid,
  Card,
  CardContent,
  CardActions,
  Button,
  TextField,
  Chip,
  Alert,
  Tabs,
  Tab,
  InputAdornment,
  Checkbox,
  Paper,
  CircularProgress,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  List,
  ListItem,
  ListItemText,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import AddIcon from '@mui/icons-material/Add';
import RocketLaunchIcon from '@mui/icons-material/RocketLaunch';
import DeleteIcon from '@mui/icons-material/Delete';
import PublishIcon from '@mui/icons-material/Publish';
import UnpublishedIcon from '@mui/icons-material/Unpublished';
import { templateService, favoriteService } from '../../api/client';
import FavoriteButton from '../../components/FavoriteButton';
import QuickDeployDialog from '../../components/QuickDeployDialog';
import ConfirmDialog from '../../components/ConfirmDialog';
import { useAuth } from '../../context/AuthContext';
import { useNotification } from '../../context/NotificationContext';
import type { StackTemplate, BulkTemplateResponse } from '../../types';
import LoadingState from '../../components/LoadingState';
import EmptyState from '../../components/EmptyState';
import { getRecentTemplates } from '../../utils/recentTemplates';

const CATEGORIES = ['All', 'Web', 'API', 'Data', 'Infrastructure', 'Other'];

type BulkAction = 'delete' | 'publish' | 'unpublish';

const BULK_ACTION_LABELS: Record<BulkAction, string> = {
  delete: 'Delete',
  publish: 'Publish',
  unpublish: 'Unpublish',
};

const Gallery = () => {
  const [templates, setTemplates] = useState<StackTemplate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [category, setCategory] = useState('All');
  const [tab, setTab] = useState(0);
  const { user } = useAuth();
  const navigate = useNavigate();
  const { showSuccess, showError } = useNotification();
  const [quickDeployTemplate, setQuickDeployTemplate] = useState<StackTemplate | null>(null);
  const isDevOps = user?.role === 'devops' || user?.role === 'admin';

  // Bulk operation state
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [bulkConfirmOpen, setBulkConfirmOpen] = useState(false);
  const [bulkAction, setBulkAction] = useState<BulkAction | null>(null);
  const [bulkLoading, setBulkLoading] = useState(false);
  const [bulkResultOpen, setBulkResultOpen] = useState(false);
  const [bulkResult, setBulkResult] = useState<BulkTemplateResponse | null>(null);

  const recentTemplates = useMemo(() => getRecentTemplates(), []);
  const [favoriteIds, setFavoriteIds] = useState<Set<string>>(new Set());

  const fetchTemplates = useCallback(async () => {
    try {
      setLoading(true);
      const [data, favs] = await Promise.all([
        templateService.list(),
        favoriteService.list().catch(() => []),
      ]);
      setTemplates(data || []);
      setFavoriteIds(new Set(favs.filter((f) => f.entity_type === 'template').map((f) => f.entity_id)));
    } catch {
      setError('Failed to load templates');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchTemplates();
  }, [fetchTemplates]);

  // Clear selection when switching tabs
  useEffect(() => {
    setSelectedIds(new Set());
  }, [tab]);

  const filtered = useMemo(() => {
    return templates.filter((t) => {
      // Tab filtering
      if (isDevOps) {
        if (tab === 0 && !t.is_published) return false;
        if (tab === 1 && t.owner_id !== user?.id) return false;
        if (tab === 2 && t.is_published) return false;
      } else if (!t.is_published) {
        return false;
      }

      if (category !== 'All' && t.category !== category) return false;
      if (
        search &&
        !t.name.toLowerCase().includes(search.toLowerCase()) &&
        !t.description.toLowerCase().includes(search.toLowerCase())
      )
        return false;
      return true;
    });
  }, [templates, tab, category, search, isDevOps, user?.id]);

  // Bulk selection is enabled on My Templates (tab 1) and All Drafts (tab 2) for devops users
  const bulkSelectionEnabled = isDevOps && (tab === 1 || tab === 2);

  const toggleSelect = useCallback((id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }, []);

  const toggleSelectAll = useCallback(() => {
    setSelectedIds((prev) => {
      const allFilteredIds = new Set(filtered.map((t) => t.id));
      const allFilteredSelected = filtered.every((t) => prev.has(t.id));
      if (allFilteredSelected) {
        // Remove only filtered IDs
        const next = new Set(prev);
        for (const id of allFilteredIds) {
          next.delete(id);
        }
        return next;
      }
      // Add all filtered IDs
      return new Set([...prev, ...allFilteredIds]);
    });
  }, [filtered]);

  const selectedTemplates = useMemo(() => {
    return templates.filter((t) => selectedIds.has(t.id));
  }, [templates, selectedIds]);

  const handleBulkActionClick = useCallback((action: BulkAction) => {
    setBulkAction(action);
    setBulkConfirmOpen(true);
  }, []);

  const handleBulkConfirm = useCallback(async () => {
    if (!bulkAction || selectedIds.size === 0) return;

    setBulkConfirmOpen(false);
    setBulkLoading(true);

    const ids = Array.from(selectedIds);
    try {
      let result: BulkTemplateResponse;
      switch (bulkAction) {
        case 'delete':
          result = await templateService.bulkDelete(ids);
          break;
        case 'publish':
          result = await templateService.bulkPublish(ids);
          break;
        case 'unpublish':
          result = await templateService.bulkUnpublish(ids);
          break;
      }
      setBulkResult(result);
      setBulkResultOpen(true);

      if (result.failed === 0) {
        showSuccess(`Bulk ${bulkAction}: ${result.succeeded}/${result.total} succeeded`);
      } else {
        showError(`Bulk ${bulkAction}: ${result.failed}/${result.total} failed`);
      }
      await fetchTemplates();
    } catch {
      showError(`Bulk ${bulkAction} failed`);
    } finally {
      setBulkLoading(false);
    }
  }, [bulkAction, selectedIds, showSuccess, showError, fetchTemplates]);

  const handleBulkResultClose = useCallback(() => {
    setBulkResultOpen(false);
    setBulkResult(null);
    setSelectedIds(new Set());
    setBulkAction(null);
  }, []);

  const handleBulkConfirmCancel = useCallback(() => {
    setBulkConfirmOpen(false);
    setBulkAction(null);
  }, []);

  const selectedFilteredCount = useMemo(
    () => filtered.filter((t) => selectedIds.has(t.id)).length,
    [filtered, selectedIds],
  );
  const allFilteredSelected = filtered.length > 0 && selectedFilteredCount === filtered.length;
  const someFilteredSelected = selectedFilteredCount > 0 && selectedFilteredCount < filtered.length;

  // Recent templates that match loaded templates (to verify they still exist)
  const resolvedRecentTemplates = useMemo(() => {
    const templateMap = new Map(templates.map((t) => [t.id, t]));
    return recentTemplates
      .filter((rt) => templateMap.has(rt.id))
      .map((rt) => templateMap.get(rt.id)!);
  }, [templates, recentTemplates]);

  if (loading) {
    return <LoadingState label="Loading templates..." />;
  }

  if (error) {
    return <Alert severity="error">{error}</Alert>;
  }

  const templatePlural = selectedTemplates.length === 1 ? '' : 's';
  const bulkActionLabel = bulkAction ? BULK_ACTION_LABELS[bulkAction] : '';
  const bulkConfirmMessage = bulkAction === 'delete'
    ? `You are about to permanently delete ${selectedTemplates.length} template${templatePlural}: ${selectedTemplates.map((t) => t.name).join(', ')}. This action cannot be undone.`
    : `${bulkActionLabel} ${selectedTemplates.length} template${templatePlural}?`;

  return (
    <Box>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Template Gallery
        </Typography>
        {isDevOps && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/templates/new')}>
            Create Template
          </Button>
        )}
      </Box>

      {isDevOps ? (
        <Tabs value={tab} onChange={(_e, v: number) => setTab(v)} sx={{ mb: 2 }}>
          <Tab label="Published" />
          <Tab label="My Templates" />
          <Tab label="All Drafts" />
        </Tabs>
      ) : null}

      <Box sx={{ display: 'flex', gap: 2, mb: 3, flexWrap: 'wrap', alignItems: 'center' }}>
        <TextField
          size="small"
          placeholder="Search templates..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          slotProps={{
            input: {
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon />
                </InputAdornment>
              ),
            },
          }}
          sx={{ minWidth: 250 }}
        />
        <Box sx={{ display: 'flex', gap: 0.5 }}>
          {CATEGORIES.map((cat) => (
            <Chip
              key={cat}
              label={cat}
              onClick={() => setCategory(cat)}
              color={category === cat ? 'primary' : 'default'}
              variant={category === cat ? 'filled' : 'outlined'}
            />
          ))}
        </Box>
      </Box>

      {/* Bulk Action Toolbar */}
      {selectedIds.size > 0 && (
        <Paper
          sx={{
            p: 1.5,
            mb: 3,
            display: 'flex',
            alignItems: 'center',
            gap: 2,
            bgcolor: 'primary.light',
            color: 'primary.contrastText',
          }}
          role="toolbar"
          aria-label="Bulk actions"
        >
          <Typography variant="body2" sx={{ fontWeight: 'bold', mr: 1 }}>
            {selectedIds.size} selected
          </Typography>
          {/* Publish: only on All Drafts */}
          {tab === 2 && (
            <Button
              size="small"
              variant="contained"
              color="success"
              startIcon={bulkLoading ? <CircularProgress size={16} color="inherit" /> : <PublishIcon />}
              onClick={() => handleBulkActionClick('publish')}
              disabled={bulkLoading}
            >
              Publish ({selectedIds.size})
            </Button>
          )}
          {/* Unpublish: on My Templates tab */}
          {tab === 1 && (
            <Button
              size="small"
              variant="contained"
              color="warning"
              startIcon={bulkLoading ? <CircularProgress size={16} color="inherit" /> : <UnpublishedIcon />}
              onClick={() => handleBulkActionClick('unpublish')}
              disabled={bulkLoading}
            >
              Unpublish ({selectedIds.size})
            </Button>
          )}
          <Button
            size="small"
            variant="contained"
            color="error"
            startIcon={bulkLoading ? <CircularProgress size={16} color="inherit" /> : <DeleteIcon />}
            onClick={() => handleBulkActionClick('delete')}
            disabled={bulkLoading}
          >
            Delete ({selectedIds.size})
          </Button>
          <Box sx={{ flex: 1 }} />
          <Button
            size="small"
            variant="outlined"
            sx={{ color: 'primary.contrastText', borderColor: 'primary.contrastText' }}
            onClick={() => setSelectedIds(new Set())}
            disabled={bulkLoading}
          >
            Clear Selection
          </Button>
        </Paper>
      )}

      {/* Recently Used section — only on Published tab */}
      {tab === 0 && resolvedRecentTemplates.length > 0 && (
        <Box sx={{ mb: 3 }}>
          <Typography variant="h6" gutterBottom>
            Recently Used
          </Typography>
          <Box sx={{ display: 'flex', overflowX: 'auto', gap: 2, pb: 1 }}>
            {resolvedRecentTemplates.map((template) => (
              <Card
                key={template.id}
                variant="outlined"
                sx={{ minWidth: 200, maxWidth: 250, flexShrink: 0, cursor: 'pointer' }}
                onClick={() => navigate(`/templates/${template.id}`)}
                tabIndex={0}
                role="button"
                aria-label={`Open template ${template.name}`}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); navigate(`/templates/${template.id}`); } }}
              >
                <CardContent sx={{ py: 1.5, px: 2, '&:last-child': { pb: 1.5 } }}>
                  <Typography variant="subtitle2" component="div" noWrap>
                    {template.name}
                  </Typography>
                  {template.category && (
                    <Typography variant="caption" color="text.secondary" noWrap component="div">
                      {template.category}
                    </Typography>
                  )}
                </CardContent>
              </Card>
            ))}
          </Box>
        </Box>
      )}

      {/* Select All header for bulk selection */}
      {bulkSelectionEnabled && filtered.length > 0 && (
        <Box sx={{ display: 'flex', alignItems: 'center', mb: 1, ml: 1 }}>
          <Checkbox
            checked={allFilteredSelected}
            indeterminate={someFilteredSelected}
            onChange={toggleSelectAll}
            slotProps={{ input: { 'aria-label': 'Select all templates' } }}
            size="small"
          />
          <Typography variant="body2" color="text.secondary">
            {allFilteredSelected ? 'Deselect all' : 'Select all'} ({filtered.length})
          </Typography>
        </Box>
      )}

      {filtered.length === 0 ? (
        <EmptyState
          title="No templates found"
          description="Try adjusting your search or filters."
        />
      ) : (
        <Grid container spacing={3}>
          {filtered.map((template) => (
            <Grid key={template.id} size={{ xs: 12, sm: 6, md: 4 }}>
              <Card
                sx={{
                  height: '100%',
                  display: 'flex',
                  flexDirection: 'column',
                  outline: selectedIds.has(template.id) ? 2 : 0,
                  outlineColor: 'primary.main',
                  outlineStyle: 'solid',
                }}
              >
                <CardContent sx={{ flex: 1 }}>
                  <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 1 }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, minWidth: 0 }}>
                      {bulkSelectionEnabled && (
                        <Checkbox
                          checked={selectedIds.has(template.id)}
                          onChange={() => toggleSelect(template.id)}
                          onClick={(e) => e.stopPropagation()}
                          slotProps={{ input: { 'aria-label': `Select ${template.name}` } }}
                          size="small"
                        />
                      )}
                      <FavoriteButton entityType="template" entityId={template.id} size="small" initialFavorited={favoriteIds.has(template.id)} />
                      <Typography variant="h6" component="h2" noWrap>
                        {template.name}
                      </Typography>
                    </Box>
                    <Box sx={{ display: 'flex', gap: 0.5, alignItems: 'center' }}>
                      {!template.is_published && <Chip label="Draft" size="small" color="warning" />}
                    </Box>
                  </Box>
                  <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                    {template.description || 'No description'}
                  </Typography>
                  {template.owner_username && (
                    <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                      By {template.owner_username}
                    </Typography>
                  )}
                  <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
                    {template.category && (
                      <Chip label={template.category} size="small" variant="outlined" />
                    )}
                    {template.version && (
                      <Chip label={`v${template.version}`} size="small" variant="outlined" />
                    )}
                    {template.charts && (
                      <Chip label={`${template.charts.length} charts`} size="small" variant="outlined" />
                    )}
                    {template.definition_count != null && template.definition_count > 0 && (
                      <Chip
                        label={`Used by ${template.definition_count} definition${template.definition_count === 1 ? '' : 's'}`}
                        size="small"
                        variant="outlined"
                      />
                    )}
                  </Box>
                </CardContent>
                <CardActions>
                  <Button size="small" onClick={() => navigate(`/templates/${template.id}`)}>
                    View
                  </Button>
                  {template.is_published && (
                    <Button
                      size="small"
                      color="primary"
                      startIcon={<RocketLaunchIcon />}
                      onClick={() => setQuickDeployTemplate(template)}
                    >
                      Quick Deploy
                    </Button>
                  )}
                  {template.is_published && (
                    <Button size="small" onClick={() => navigate(`/templates/${template.id}/use`)}>
                      Use Template
                    </Button>
                  )}
                  {isDevOps && template.owner_id === user?.id && (
                    <Button size="small" onClick={() => navigate(`/templates/${template.id}/edit`)}>
                      Edit
                    </Button>
                  )}
                </CardActions>
              </Card>
            </Grid>
          ))}
        </Grid>
      )}

      <QuickDeployDialog
        open={!!quickDeployTemplate}
        onClose={() => setQuickDeployTemplate(null)}
        template={quickDeployTemplate}
      />

      {/* Bulk Confirm Dialog */}
      <ConfirmDialog
        open={bulkConfirmOpen}
        title={`Confirm Bulk ${bulkActionLabel}`}
        message={bulkConfirmMessage}
        onConfirm={handleBulkConfirm}
        onCancel={handleBulkConfirmCancel}
        confirmText={bulkActionLabel || 'Confirm'}
      />

      {/* Bulk Results Dialog */}
      <Dialog open={bulkResultOpen} onClose={handleBulkResultClose} maxWidth="sm" fullWidth>
        <DialogTitle>
          Bulk Operation Results
        </DialogTitle>
        <DialogContent>
          {bulkResult && (
            <Box>
              <Box sx={{ display: 'flex', gap: 2, mb: 2 }}>
                <Alert severity="success" sx={{ flex: 1 }}>
                  {bulkResult.succeeded} succeeded
                </Alert>
                {bulkResult.failed > 0 && (
                  <Alert severity="error" sx={{ flex: 1 }}>
                    {bulkResult.failed} failed
                  </Alert>
                )}
              </Box>
              <List dense>
                {bulkResult.results.map((item) => (
                  <ListItem key={item.template_id}>
                    <ListItemText
                      primary={item.template_name}
                      secondary={item.status === 'error' ? item.error : 'Success'}
                      slotProps={{
                        secondary: {
                          sx: { color: item.status === 'error' ? 'error.main' : 'success.main' },
                        },
                      }}
                    />
                  </ListItem>
                ))}
              </List>
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={handleBulkResultClose} variant="contained">
            Close
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};

export default Gallery;
