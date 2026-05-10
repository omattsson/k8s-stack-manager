import React, { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Alert,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  Button,
  IconButton,
  Switch,
  Chip,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  FormControlLabel,
  FormGroup,
  Checkbox,
  Tooltip,
  Breadcrumbs,
  Link as MuiLink,
  Collapse,
  Snackbar,
  CircularProgress,
} from '@mui/material';
import AddOutlined from '@mui/icons-material/AddOutlined';
import EditOutlined from '@mui/icons-material/EditOutlined';
import DeleteOutlined from '@mui/icons-material/DeleteOutlined';
import SendOutlined from '@mui/icons-material/SendOutlined';
import ExpandMoreOutlined from '@mui/icons-material/ExpandMoreOutlined';
import ExpandLessOutlined from '@mui/icons-material/ExpandLessOutlined';
import NotificationsActiveOutlined from '@mui/icons-material/NotificationsActiveOutlined';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import { Link } from 'react-router-dom';
import { notificationChannelService } from '../../../api/client';
import { timeAgo } from '../../../utils/timeAgo';
import type {
  NotificationChannel,
  NotificationDeliveryLog,
} from '../../../types';
import LoadingState from '../../../components/LoadingState';
import { useNotification } from '../../../context/NotificationContext';

const EVENT_TYPE_CATEGORIES: Record<string, string[]> = {
  Deployment: [
    'deployment.success',
    'deployment.error',
    'deployment.partial',
    'deployment.stopped',
    'deploy.timeout',
  ],
  Instance: ['instance.created', 'instance.deleted'],
  Cleanup: ['clean.completed', 'clean.error', 'cleanup.policy.executed'],
  Rollback: ['rollback.completed', 'rollback.error'],
  Stop: ['stop.error'],
  System: ['stack.expiring', 'stack.expired', 'quota.warning', 'secret.expiring'],
};

function truncateUrl(url: string, maxLen = 50): string {
  if (url.length <= maxLen) return url;
  return url.slice(0, maxLen) + '...';
}

interface ChannelFormState {
  name: string;
  webhook_url: string;
  secret: string;
  enabled: boolean;
}

const emptyForm: ChannelFormState = {
  name: '',
  webhook_url: '',
  secret: '',
  enabled: true,
};

const NotificationChannels = () => {
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { showSuccess, showError } = useNotification();

  // Create/Edit dialog
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<ChannelFormState>(emptyForm);
  const [formError, setFormError] = useState<string | null>(null);
  const [formTouched, setFormTouched] = useState<Record<string, boolean>>({});
  const [saving, setSaving] = useState(false);

  // Delete confirmation
  const [deleteTarget, setDeleteTarget] = useState<NotificationChannel | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Subscriptions dialog
  const [subsTarget, setSubsTarget] = useState<NotificationChannel | null>(null);
  const [allEventTypes, setAllEventTypes] = useState<string[]>([]);
  const [selectedEventTypes, setSelectedEventTypes] = useState<string[]>([]);
  const [subsLoading, setSubsLoading] = useState(false);
  const [subsSaving, setSubsSaving] = useState(false);

  // Subscription counts per channel (loaded alongside channels)
  const [subsCounts, setSubsCounts] = useState<Record<string, number>>({});

  // Test snackbar
  const [testSnackbar, setTestSnackbar] = useState<{ open: boolean; message: string; severity: 'success' | 'error' }>({
    open: false,
    message: '',
    severity: 'success',
  });
  const [testingId, setTestingId] = useState<string | null>(null);

  // Delivery logs (expandable row)
  const [expandedChannelId, setExpandedChannelId] = useState<string | null>(null);
  const [deliveryLogs, setDeliveryLogs] = useState<NotificationDeliveryLog[]>([]);
  const [logsLoading, setLogsLoading] = useState(false);
  const activeLogRequest = React.useRef<string | null>(null);

  const fetchChannels = useCallback(async () => {
    setError(null);
    try {
      const data = await notificationChannelService.list();
      setChannels(data);

      const counts: Record<string, number> = {};
      for (const ch of data) {
        counts[ch.id] = ch.subscription_count ?? 0;
      }
      setSubsCounts(counts);
    } catch {
      setError('Failed to load notification channels');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchChannels();
  }, [fetchChannels]);

  // --- Create/Edit ---
  const openCreateDialog = useCallback(() => {
    setEditingId(null);
    setForm(emptyForm);
    setFormError(null);
    setFormTouched({});
    setDialogOpen(true);
  }, []);

  const openEditDialog = useCallback((channel: NotificationChannel) => {
    setEditingId(channel.id);
    setForm({
      name: channel.name,
      webhook_url: channel.webhook_url,
      secret: '',
      enabled: channel.enabled,
    });
    setFormError(null);
    setFormTouched({});
    setDialogOpen(true);
  }, []);

  const handleDialogClose = useCallback(() => {
    setDialogOpen(false);
    setEditingId(null);
    setFormError(null);
    setFormTouched({});
  }, []);

  const handleSave = useCallback(async () => {
    if (!form.name.trim()) {
      setFormError('Name is required');
      return;
    }
    if (!form.webhook_url.trim()) {
      setFormError('Webhook URL is required');
      return;
    }
    if (!form.webhook_url.startsWith('https://')) {
      setFormError('Webhook URL must start with https://');
      return;
    }

    setSaving(true);
    setFormError(null);
    try {
      if (editingId) {
        const payload: Partial<NotificationChannel> & { secret?: string } = {
          name: form.name.trim(),
          webhook_url: form.webhook_url.trim(),
          enabled: form.enabled,
        };
        if (form.secret) {
          payload.secret = form.secret;
        }
        await notificationChannelService.update(editingId, payload);
        showSuccess('Channel updated');
      } else {
        const payload: { name: string; webhook_url: string; secret?: string; enabled?: boolean } = {
          name: form.name.trim(),
          webhook_url: form.webhook_url.trim(),
          enabled: form.enabled,
        };
        if (form.secret) {
          payload.secret = form.secret;
        }
        await notificationChannelService.create(payload);
        showSuccess('Channel created');
      }
      setDialogOpen(false);
      setEditingId(null);
      await fetchChannels();
    } catch {
      setFormError('Failed to save channel');
    } finally {
      setSaving(false);
    }
  }, [form, editingId, fetchChannels, showSuccess]);

  // --- Toggle enabled ---
  const handleToggleEnabled = useCallback(async (channel: NotificationChannel) => {
    try {
      await notificationChannelService.update(channel.id, { enabled: !channel.enabled });
      setChannels((prev) =>
        prev.map((c) => (c.id === channel.id ? { ...c, enabled: !c.enabled } : c)),
      );
    } catch {
      showError('Failed to toggle channel');
    }
  }, [showError]);

  // --- Delete ---
  const handleDeleteClick = useCallback((channel: NotificationChannel) => {
    setDeleteTarget(channel);
  }, []);

  const handleDeleteConfirm = useCallback(async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await notificationChannelService.delete(deleteTarget.id);
      showSuccess('Channel deleted');
      setDeleteTarget(null);
      await fetchChannels();
    } catch {
      showError('Failed to delete channel');
    } finally {
      setDeleting(false);
    }
  }, [deleteTarget, fetchChannels, showSuccess, showError]);

  // --- Test ---
  const handleTest = useCallback(async (channel: NotificationChannel) => {
    setTestingId(channel.id);
    try {
      const result = await notificationChannelService.test(channel.id);
      setTestSnackbar({
        open: true,
        message: result.message || 'Test completed',
        severity: result.success ? 'success' : 'error',
      });
    } catch {
      setTestSnackbar({
        open: true,
        message: 'Failed to send test notification',
        severity: 'error',
      });
    } finally {
      setTestingId(null);
    }
  }, []);

  // --- Subscriptions ---
  const openSubscriptionsDialog = useCallback(async (channel: NotificationChannel) => {
    setSubsTarget(channel);
    setSubsLoading(true);
    try {
      const [eventTypes, subs] = await Promise.all([
        notificationChannelService.eventTypes(),
        notificationChannelService.getSubscriptions(channel.id),
      ]);
      setAllEventTypes(eventTypes);
      setSelectedEventTypes(subs);
    } catch {
      showError('Failed to load subscriptions');
      setSubsTarget(null);
    } finally {
      setSubsLoading(false);
    }
  }, [showError]);

  const handleSubsSave = useCallback(async () => {
    if (!subsTarget) return;
    setSubsSaving(true);
    try {
      await notificationChannelService.updateSubscriptions(subsTarget.id, selectedEventTypes);
      showSuccess('Subscriptions updated');
      setSubsTarget(null);
      await fetchChannels();
    } catch {
      showError('Failed to update subscriptions');
    } finally {
      setSubsSaving(false);
    }
  }, [subsTarget, selectedEventTypes, fetchChannels, showSuccess, showError]);

  const handleEventTypeToggle = useCallback((eventType: string) => {
    setSelectedEventTypes((prev) =>
      prev.includes(eventType) ? prev.filter((e) => e !== eventType) : [...prev, eventType],
    );
  }, []);

  const handleSelectAll = useCallback(() => {
    setSelectedEventTypes([...allEventTypes]);
  }, [allEventTypes]);

  const handleDeselectAll = useCallback(() => {
    setSelectedEventTypes([]);
  }, []);

  // --- Delivery Logs (expandable row) ---
  const handleToggleExpand = useCallback(async (channelId: string) => {
    if (expandedChannelId === channelId) {
      setExpandedChannelId(null);
      activeLogRequest.current = null;
      return;
    }
    setExpandedChannelId(channelId);
    setDeliveryLogs([]);
    setLogsLoading(true);
    activeLogRequest.current = channelId;
    try {
      const result = await notificationChannelService.deliveryLogs(channelId, 20, 0);
      if (activeLogRequest.current === channelId) {
        setDeliveryLogs(result.logs ?? []);
      }
    } catch {
      if (activeLogRequest.current === channelId) {
        setDeliveryLogs([]);
      }
    } finally {
      setLogsLoading(false);
    }
  }, [expandedChannelId]);

  // Group event types into categories, including any from the API not in our static map
  const categorizedEventTypes = useCallback((): Record<string, string[]> => {
    const known = new Set(Object.values(EVENT_TYPE_CATEGORIES).flat());
    const categories = { ...EVENT_TYPE_CATEGORIES };
    const uncategorized = allEventTypes.filter((et) => !known.has(et));
    if (uncategorized.length > 0) {
      categories['Other'] = uncategorized;
    }
    return categories;
  }, [allEventTypes]);

  return (
    <Box>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <MuiLink component={Link} to="/" underline="hover" color="inherit">Home</MuiLink>
        <Typography color="text.primary">Notification Channels</Typography>
      </Breadcrumbs>

      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 3 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <NotificationsActiveOutlined sx={{ fontSize: 32, color: 'primary.main' }} />
          <Typography variant="h4" component="h1">
            Notification Channels
          </Typography>
        </Box>
        <Button variant="contained" startIcon={<AddOutlined />} onClick={openCreateDialog}>
          Create Channel
        </Button>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading && <LoadingState label="Loading notification channels..." />}

      {!loading && !error && channels.length === 0 && (
        <Alert severity="info">
          No notification channels configured. Create one to receive webhook notifications for deployment events.
        </Alert>
      )}

      {!loading && channels.length > 0 && (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell padding="checkbox" />
                <TableCell>Name</TableCell>
                <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>Webhook URL</TableCell>
                <TableCell>Enabled</TableCell>
                <TableCell>Subscriptions</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {channels.map((channel) => (
                <React.Fragment key={channel.id}>
                  <TableRow>
                    <TableCell padding="checkbox">
                      <IconButton
                        size="small"
                        onClick={() => handleToggleExpand(channel.id)}
                        aria-label={expandedChannelId === channel.id ? 'Collapse delivery logs' : 'Expand delivery logs'}
                      >
                        {expandedChannelId === channel.id ? <ExpandLessOutlined /> : <ExpandMoreOutlined />}
                      </IconButton>
                    </TableCell>
                    <TableCell>{channel.name}</TableCell>
                    <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>
                      <Tooltip title={channel.webhook_url}>
                        <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem' }}>
                          {truncateUrl(channel.webhook_url)}
                        </Typography>
                      </Tooltip>
                    </TableCell>
                    <TableCell>
                      <Switch
                        checked={channel.enabled}
                        onChange={() => handleToggleEnabled(channel)}
                        size="small"
                        slotProps={{ input: { 'aria-label': `Toggle ${channel.name}` } }}
                      />
                    </TableCell>
                    <TableCell>
                      <Chip
                        label={subsCounts[channel.id] ?? 0}
                        size="small"
                        color={subsCounts[channel.id] ? 'primary' : 'default'}
                        variant="outlined"
                        onClick={() => openSubscriptionsDialog(channel)}
                        sx={{ cursor: 'pointer' }}
                      />
                    </TableCell>
                    <TableCell align="right">
                      <Tooltip title="Edit">
                        <IconButton size="small" onClick={() => openEditDialog(channel)} aria-label={`Edit ${channel.name}`}>
                          <EditOutlined />
                        </IconButton>
                      </Tooltip>
                      <Tooltip title="Test">
                        <span>
                          <IconButton
                            size="small"
                            onClick={() => handleTest(channel)}
                            disabled={testingId === channel.id}
                            aria-label={`Test ${channel.name}`}
                          >
                            {testingId === channel.id ? <CircularProgress size={18} /> : <SendOutlined />}
                          </IconButton>
                        </span>
                      </Tooltip>
                      <Tooltip title="Delete">
                        <IconButton size="small" onClick={() => handleDeleteClick(channel)} aria-label={`Delete ${channel.name}`}>
                          <DeleteOutlined />
                        </IconButton>
                      </Tooltip>
                    </TableCell>
                  </TableRow>

                  {/* Expandable delivery logs row */}
                  <TableRow key={`${channel.id}-logs`}>
                    <TableCell colSpan={6} sx={{ py: 0, borderBottom: expandedChannelId === channel.id ? undefined : 'none' }}>
                      <Collapse in={expandedChannelId === channel.id} timeout="auto" unmountOnExit>
                        <Box sx={{ py: 2, px: 1 }}>
                          <Typography variant="subtitle2" sx={{ mb: 1 }}>
                            Delivery Logs (last 20)
                          </Typography>
                          {logsLoading && <CircularProgress size={20} />}
                          {!logsLoading && deliveryLogs.length === 0 && (
                            <Typography variant="body2" color="text.secondary">No delivery logs yet.</Typography>
                          )}
                          {!logsLoading && deliveryLogs.length > 0 && (
                            <Table size="small">
                              <TableHead>
                                <TableRow>
                                  <TableCell>Event Type</TableCell>
                                  <TableCell>Status</TableCell>
                                  <TableCell>Status Code</TableCell>
                                  <TableCell>Error</TableCell>
                                  <TableCell>Time</TableCell>
                                </TableRow>
                              </TableHead>
                              <TableBody>
                                {deliveryLogs.map((log) => (
                                  <TableRow key={log.id}>
                                    <TableCell>
                                      <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem' }}>
                                        {log.event_type}
                                      </Typography>
                                    </TableCell>
                                    <TableCell>
                                      <Chip
                                        label={log.status}
                                        size="small"
                                        color={log.status === 'success' || log.status === 'ok' ? 'success' : 'error'}
                                      />
                                    </TableCell>
                                    <TableCell>{log.status_code}</TableCell>
                                    <TableCell>
                                      {log.error_message && (
                                        <Typography variant="body2" color="error" sx={{ maxWidth: 300, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                                          {log.error_message}
                                        </Typography>
                                      )}
                                    </TableCell>
                                    <TableCell>
                                      <Tooltip title={new Date(log.created_at).toLocaleString()}>
                                        <Typography variant="body2">{timeAgo(log.created_at)}</Typography>
                                      </Tooltip>
                                    </TableCell>
                                  </TableRow>
                                ))}
                              </TableBody>
                            </Table>
                          )}
                        </Box>
                      </Collapse>
                    </TableCell>
                  </TableRow>
                </React.Fragment>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Create/Edit Dialog */}
      <Dialog open={dialogOpen} onClose={handleDialogClose} maxWidth="sm" fullWidth>
        <DialogTitle>{editingId ? 'Edit Channel' : 'Create Notification Channel'}</DialogTitle>
        <DialogContent>
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
            {formError && <Alert severity="error">{formError}</Alert>}
            <TextField
              label="Name"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              onBlur={() => setFormTouched((prev) => ({ ...prev, name: true }))}
              fullWidth
              required
              error={formTouched.name === true && !form.name.trim()}
              helperText={formTouched.name === true && !form.name.trim() ? 'Name is required' : ''}
            />
            <TextField
              label="Webhook URL"
              value={form.webhook_url}
              onChange={(e) => setForm({ ...form, webhook_url: e.target.value })}
              onBlur={() => setFormTouched((prev) => ({ ...prev, webhook_url: true }))}
              fullWidth
              required
              error={formTouched.webhook_url === true && (!form.webhook_url.trim() || !form.webhook_url.startsWith('https://'))}
              helperText={
                formTouched.webhook_url === true && !form.webhook_url.trim()
                  ? 'Webhook URL is required'
                  : formTouched.webhook_url === true && !form.webhook_url.startsWith('https://')
                    ? 'Must start with https://'
                    : ''
              }
              placeholder="https://hooks.example.com/webhook"
            />
            <TextField
              label="Secret"
              value={form.secret}
              onChange={(e) => setForm({ ...form, secret: e.target.value })}
              fullWidth
              type="password"
              helperText={editingId ? 'Leave empty to keep existing secret' : 'Optional. Used for HMAC signing of webhook payloads.'}
            />
            <FormControlLabel
              control={
                <Switch
                  checked={form.enabled}
                  onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                />
              }
              label="Enabled"
            />
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={handleDialogClose}>Cancel</Button>
          <Button variant="contained" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving...' : editingId ? 'Update' : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogTitle>Delete Channel</DialogTitle>
        <DialogContent>
          <Typography>
            Are you sure you want to delete the channel &quot;{deleteTarget?.name}&quot;? This will remove all subscriptions and delivery logs.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button color="error" variant="contained" onClick={handleDeleteConfirm} disabled={deleting}>
            {deleting ? 'Deleting...' : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Subscriptions Dialog */}
      <Dialog open={!!subsTarget} onClose={() => setSubsTarget(null)} maxWidth="sm" fullWidth>
        <DialogTitle>Subscriptions: {subsTarget?.name}</DialogTitle>
        <DialogContent>
          {subsLoading && <CircularProgress sx={{ display: 'block', mx: 'auto', my: 2 }} />}
          {!subsLoading && (
            <Box sx={{ mt: 1 }}>
              <Box sx={{ display: 'flex', gap: 1, mb: 2 }}>
                <Button size="small" variant="outlined" onClick={handleSelectAll}>
                  Select All
                </Button>
                <Button size="small" variant="outlined" onClick={handleDeselectAll}>
                  Deselect All
                </Button>
              </Box>
              {Object.entries(categorizedEventTypes()).map(([category, events]) => {
                // Only show categories that have event types from the API
                const relevantEvents = events.filter((et) => allEventTypes.includes(et));
                if (relevantEvents.length === 0) return null;
                return (
                  <Box key={category} sx={{ mb: 2 }}>
                    <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 0.5 }}>
                      {category}
                    </Typography>
                    <FormGroup>
                      {relevantEvents.map((eventType) => (
                        <FormControlLabel
                          key={eventType}
                          control={
                            <Checkbox
                              checked={selectedEventTypes.includes(eventType)}
                              onChange={() => handleEventTypeToggle(eventType)}
                              size="small"
                            />
                          }
                          label={
                            <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem' }}>
                              {eventType}
                            </Typography>
                          }
                        />
                      ))}
                    </FormGroup>
                  </Box>
                );
              })}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setSubsTarget(null)}>Cancel</Button>
          <Button variant="contained" onClick={handleSubsSave} disabled={subsSaving}>
            {subsSaving ? 'Saving...' : 'Save'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Test result snackbar */}
      <Snackbar
        open={testSnackbar.open}
        autoHideDuration={4000}
        onClose={() => setTestSnackbar((prev) => ({ ...prev, open: false }))}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'center' }}
      >
        <Alert
          onClose={() => setTestSnackbar((prev) => ({ ...prev, open: false }))}
          severity={testSnackbar.severity}
          sx={{ width: '100%' }}
        >
          {testSnackbar.message}
        </Alert>
      </Snackbar>
    </Box>
  );
};

export default NotificationChannels;
