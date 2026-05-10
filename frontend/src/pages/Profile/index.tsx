import { useEffect, useState, useCallback, useRef } from 'react';
import {
  Box,
  Typography,
  Button,
  CircularProgress,
  Alert,
  Paper,
  Chip,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  IconButton,
  Tooltip,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  Switch,
  RadioGroup,
  Radio,
  FormControlLabel,
  FormControl,
  FormLabel,
  MenuItem,
} from '@mui/material';
import KeyIcon from '@mui/icons-material/Key';
import DeleteIcon from '@mui/icons-material/Delete';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import CheckIcon from '@mui/icons-material/Check';
import SaveIcon from '@mui/icons-material/Save';
import LockOutlinedIcon from '@mui/icons-material/LockOutlined';
import SecurityOutlinedIcon from '@mui/icons-material/SecurityOutlined';
import { apiKeyService, notificationService } from '../../api/client';
import { useAuth } from '../../context/AuthContext';
import { useNotification } from '../../context/NotificationContext';
import type { APIKey, CreateAPIKeyRequest, CreateAPIKeyResponse, NotificationPreference } from '../../types';
import LoadingState from '../../components/LoadingState';

const EVENT_TYPE_LABELS: Record<string, string> = {
  'deployment.success': 'Deployment succeeded',
  'deployment.error': 'Deployment failed',
  'deployment.stopped': 'Stack stopped',
  'stop.error': 'Stop failed',
  'instance.created': 'Stack created',
  'instance.deleted': 'Stack deleted',
  'clean.completed': 'Cleanup completed',
  'clean.error': 'Cleanup failed',
  'rollback.completed': 'Rollback completed',
  'rollback.error': 'Rollback failed',
};

const DEFAULT_EVENT_TYPES = Object.keys(EVENT_TYPE_LABELS);

type ExpiryMode = 'preset' | 'custom';

const PRESET_DURATIONS = [
  { value: 30, label: '30 days' },
  { value: 60, label: '60 days' },
  { value: 90, label: '90 days' },
  { value: 180, label: '180 days' },
  { value: 365, label: '365 days' },
];

const getExpiryStatus = (expiresAt: string | undefined): 'expired' | 'expiring-soon' | 'ok' | 'never' => {
  if (!expiresAt) return 'never';
  const now = new Date();
  const expiry = new Date(expiresAt);
  if (expiry <= now) return 'expired';
  const daysUntilExpiry = (expiry.getTime() - now.getTime()) / (1000 * 60 * 60 * 24);
  if (daysUntilExpiry <= 30) return 'expiring-soon';
  return 'ok';
};

const computeExpiryDate = (days: number): Date => {
  const date = new Date();
  date.setDate(date.getDate() + days);
  return date;
};

const getRoleChipColor = (role: string): 'error' | 'warning' | 'default' => {
  if (role === 'admin') return 'error';
  if (role === 'devops') return 'warning';
  return 'default';
};

const Profile = () => {
  const { user: currentUser, oidcConfig, authProvider, authEmail } = useAuth();

  const [apiKeys, setApiKeys] = useState<APIKey[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Generate key dialog
  const [generateKeyOpen, setGenerateKeyOpen] = useState(false);
  const [generateKeyForm, setGenerateKeyForm] = useState<CreateAPIKeyRequest>({ name: '' });
  const [generateKeyError, setGenerateKeyError] = useState<string | null>(null);
  const [generateKeyLoading, setGenerateKeyLoading] = useState(false);
  const [expiryMode, setExpiryMode] = useState<ExpiryMode>('preset');
  const [presetDays, setPresetDays] = useState<number>(90);

  // Raw key modal
  const [rawKeyData, setRawKeyData] = useState<CreateAPIKeyResponse | null>(null);
  const [keyCopied, setKeyCopied] = useState(false);
  const copyTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Revoke key confirm
  const [revokeKeyTarget, setRevokeKeyTarget] = useState<APIKey | null>(null);

  // Notification preferences
  const { showSuccess: showPrefSuccess, showError: showPrefError } = useNotification();
  const [notifPrefs, setNotifPrefs] = useState<NotificationPreference[]>([]);
  const [notifPrefsLoading, setNotifPrefsLoading] = useState(true);
  const [notifPrefsSaving, setNotifPrefsSaving] = useState(false);

  const fetchApiKeys = useCallback(async () => {
    if (!currentUser) return;
    setLoading(true);
    setError(null);
    try {
      const keys = await apiKeyService.list(currentUser.id);
      setApiKeys(keys || []);
    } catch {
      setError('Failed to load API keys');
    } finally {
      setLoading(false);
    }
  }, [currentUser]);

  useEffect(() => {
    fetchApiKeys();
  }, [fetchApiKeys]);

  useEffect(() => {
    return () => {
      if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
    };
  }, []);

  const fetchNotifPrefs = useCallback(async () => {
    setNotifPrefsLoading(true);
    try {
      const prefs = await notificationService.getPreferences();
      if (prefs && prefs.length > 0) {
        setNotifPrefs(prefs);
      } else {
        // Initialize with defaults (all enabled)
        setNotifPrefs(DEFAULT_EVENT_TYPES.map((et) => ({ event_type: et, enabled: true })));
      }
    } catch {
      // Initialize with defaults on error
      setNotifPrefs(DEFAULT_EVENT_TYPES.map((et) => ({ event_type: et, enabled: true })));
    } finally {
      setNotifPrefsLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchNotifPrefs();
  }, [fetchNotifPrefs]);

  const handleTogglePref = (eventType: string) => {
    setNotifPrefs((prev) =>
      prev.map((p) => (p.event_type === eventType ? { ...p, enabled: !p.enabled } : p)),
    );
  };

  const handleSavePrefs = async () => {
    setNotifPrefsSaving(true);
    try {
      await notificationService.updatePreferences(
        notifPrefs.map((p) => ({ event_type: p.event_type, enabled: p.enabled })),
      );
      showPrefSuccess('Notification preferences saved');
    } catch {
      showPrefError('Failed to save notification preferences');
    } finally {
      setNotifPrefsSaving(false);
    }
  };

  const handleGenerateKey = async () => {
    if (!currentUser || !generateKeyForm.name.trim()) {
      setGenerateKeyError('Key name is required');
      return;
    }
    setGenerateKeyLoading(true);
    setGenerateKeyError(null);
    try {
      const request: CreateAPIKeyRequest = { name: generateKeyForm.name.trim() };
      if (expiryMode === 'custom' && generateKeyForm.expires_at) {
        request.expires_at = generateKeyForm.expires_at;
      } else {
        request.expires_in_days = presetDays;
      }
      const result = await apiKeyService.create(currentUser.id, request);
      setGenerateKeyOpen(false);
      setGenerateKeyForm({ name: '' });
      setExpiryMode('preset');
      setPresetDays(90);
      setRawKeyData(result);
      await fetchApiKeys();
    } catch {
      setGenerateKeyError('Failed to generate API key');
    } finally {
      setGenerateKeyLoading(false);
    }
  };

  const handleRevokeKey = async () => {
    if (!currentUser || !revokeKeyTarget) return;
    try {
      await apiKeyService.delete(currentUser.id, revokeKeyTarget.id);
      setRevokeKeyTarget(null);
      await fetchApiKeys();
    } catch {
      setError('Failed to revoke API key');
      setRevokeKeyTarget(null);
    }
  };

  const handleCopyKey = async () => {
    if (!rawKeyData) return;
    try {
      await navigator.clipboard.writeText(rawKeyData.raw_key);
      if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
      setKeyCopied(true);
      copyTimeoutRef.current = setTimeout(() => setKeyCopied(false), 2000);
    } catch {
      // Clipboard API unavailable in this environment
    }
  };

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        My Profile
      </Typography>

      {currentUser && (
        <Paper sx={{ p: 3, mb: 4 }}>
          <Typography variant="h6" gutterBottom>
            Account Details
          </Typography>
          <Box sx={{ display: 'grid', gridTemplateColumns: 'auto 1fr', gap: 1, alignItems: 'center' }}>
            <Typography color="text.secondary">Username:</Typography>
            <Typography>{currentUser.username}</Typography>
            <Typography color="text.secondary">Display Name:</Typography>
            <Typography>{currentUser.display_name || '—'}</Typography>
            <Typography color="text.secondary">Role:</Typography>
            <Box>
              <Chip label={currentUser.role} size="small" color={getRoleChipColor(currentUser.role)} />
            </Box>
            <Typography color="text.secondary">Authentication:</Typography>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
              {authProvider ? (
                <Chip
                  icon={<SecurityOutlinedIcon />}
                  label={`SSO via ${oidcConfig?.provider_name || authProvider}`}
                  size="small"
                  color="info"
                  variant="outlined"
                />
              ) : (
                <Chip
                  icon={<LockOutlinedIcon />}
                  label="Local account"
                  size="small"
                  variant="outlined"
                />
              )}
            </Box>
            {authEmail && (
              <>
                <Typography color="text.secondary">Email:</Typography>
                <Typography>{authEmail}</Typography>
              </>
            )}
          </Box>
        </Paper>
      )}

      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
        <Typography variant="h5">API Keys</Typography>
        <Button
          variant="outlined"
          startIcon={<KeyIcon />}
          onClick={() => {
            setGenerateKeyOpen(true);
            setGenerateKeyForm({ name: '' });
            setExpiryMode('preset');
            setPresetDays(90);
            setGenerateKeyError(null);
          }}
        >
          Generate API Key
        </Button>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <LoadingState label="Loading API keys..." />
      ) : (
        <Paper>
          {apiKeys.length === 0 ? (
            <Box sx={{ p: 3, textAlign: 'center' }}>
              <Typography color="text.secondary">
                No API keys yet. Generate one to use with automated tools.
              </Typography>
            </Box>
          ) : (
            <TableContainer>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Name</TableCell>
                    <TableCell>Prefix</TableCell>
                    <TableCell>Created</TableCell>
                    <TableCell>Last Used</TableCell>
                    <TableCell>Expires</TableCell>
                    <TableCell>Actions</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {apiKeys.map((key) => (
                    <TableRow key={key.id}>
                      <TableCell>{key.name}</TableCell>
                      <TableCell>
                        <Typography sx={{ fontFamily: 'monospace', fontSize: 12 }}>
                          {key.prefix}...
                        </Typography>
                      </TableCell>
                      <TableCell>{new Date(key.created_at).toLocaleDateString()}</TableCell>
                      <TableCell>
                        {key.last_used_at ? new Date(key.last_used_at).toLocaleDateString() : '—'}
                      </TableCell>
                      <TableCell>
                        {(() => {
                          const status = getExpiryStatus(key.expires_at);
                          if (status === 'never') return 'Never';
                          const dateStr = new Date(key.expires_at!).toLocaleDateString();
                          if (status === 'expired') {
                            return (
                              <Chip label={`Expired ${dateStr}`} size="small" color="error" variant="outlined" />
                            );
                          }
                          if (status === 'expiring-soon') {
                            return (
                              <Chip label={dateStr} size="small" color="warning" variant="outlined" />
                            );
                          }
                          return dateStr;
                        })()}
                      </TableCell>
                      <TableCell>
                        <Tooltip title="Revoke key">
                          <IconButton
                            size="small"
                            color="error"
                            onClick={() => setRevokeKeyTarget(key)}
                            aria-label={`Revoke key ${key.name}`}
                          >
                            <DeleteIcon fontSize="small" />
                          </IconButton>
                        </Tooltip>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </Paper>
      )}

      {/* ── Notification Preferences ──────────────────────────────── */}
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2, mt: 4 }}>
        <Typography variant="h5">Notification Preferences</Typography>
        <Button
          variant="outlined"
          startIcon={notifPrefsSaving ? <CircularProgress size={16} /> : <SaveIcon />}
          onClick={handleSavePrefs}
          disabled={notifPrefsSaving || notifPrefsLoading}
        >
          Save
        </Button>
      </Box>

      {notifPrefsLoading ? (
        <LoadingState label="Loading preferences..." />
      ) : (
        <Paper>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Event Type</TableCell>
                <TableCell align="right">Enabled</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {notifPrefs.map((pref) => (
                <TableRow key={pref.event_type}>
                  <TableCell>
                    {EVENT_TYPE_LABELS[pref.event_type] || pref.event_type}
                  </TableCell>
                  <TableCell align="right">
                    <Switch
                      checked={pref.enabled}
                      onChange={() => handleTogglePref(pref.event_type)}
                      slotProps={{
                        input: {
                          'aria-label': `Toggle ${EVENT_TYPE_LABELS[pref.event_type] || pref.event_type}`,
                        },
                      }}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Paper>
      )}

      {/* ── Generate API Key Dialog ──────────────────────────────── */}
      <Dialog open={generateKeyOpen} onClose={() => setGenerateKeyOpen(false)} maxWidth="xs" fullWidth>
        <DialogTitle>Generate API Key</DialogTitle>
        <DialogContent>
          {generateKeyError && (
            <Alert severity="error" sx={{ mb: 2, mt: 1 }}>{generateKeyError}</Alert>
          )}
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
            <TextField
              label="Key Name"
              value={generateKeyForm.name}
              onChange={(e) => setGenerateKeyForm((prev) => ({ ...prev, name: e.target.value }))}
              required
              fullWidth
              size="small"
              autoFocus
              helperText={`${generateKeyForm.name.length}/50`}
              error={generateKeyForm.name.length > 50}
              slotProps={{ htmlInput: { maxLength: 50 } }}
            />
            <FormControl>
              <FormLabel id="expiry-mode-label">Expiration</FormLabel>
              <RadioGroup
                aria-labelledby="expiry-mode-label"
                value={expiryMode}
                onChange={(e) => setExpiryMode(e.target.value as ExpiryMode)}
              >
                <FormControlLabel value="preset" control={<Radio />} label="Preset duration" />
                <FormControlLabel value="custom" control={<Radio />} label="Custom date" />
              </RadioGroup>
            </FormControl>
            {expiryMode === 'preset' && (
              <Box>
                <TextField
                  select
                  label="Duration"
                  value={presetDays}
                  onChange={(e) => setPresetDays(Number(e.target.value))}
                  fullWidth
                  size="small"
                >
                  {PRESET_DURATIONS.map((d) => (
                    <MenuItem key={d.value} value={d.value}>
                      {d.label}
                    </MenuItem>
                  ))}
                </TextField>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
                  Expires: {computeExpiryDate(presetDays).toLocaleDateString(undefined, { year: 'numeric', month: 'long', day: 'numeric' })}
                </Typography>
              </Box>
            )}
            {expiryMode === 'custom' && (
              <TextField
                label="Expires At"
                type="date"
                value={generateKeyForm.expires_at ?? ''}
                onChange={(e) =>
                  setGenerateKeyForm((prev) => ({ ...prev, expires_at: e.target.value || undefined }))
                }
                fullWidth
                size="small"
                slotProps={{ inputLabel: { shrink: true } }}
              />
            )}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setGenerateKeyOpen(false)}>Cancel</Button>
          <Button variant="contained" onClick={handleGenerateKey} disabled={generateKeyLoading}>
            {generateKeyLoading ? <CircularProgress size={20} /> : 'Generate'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* ── Raw Key Modal ────────────────────────────────────────── */}
      <Dialog open={Boolean(rawKeyData)} maxWidth="sm" fullWidth>
        <DialogTitle>API Key Generated</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            This key will not be shown again. Copy it now.
          </Alert>
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1,
              p: 2,
              bgcolor: 'grey.100',
              borderRadius: 1,
            }}
          >
            <Typography
              sx={{ fontFamily: 'monospace', fontSize: 14, flexGrow: 1, wordBreak: 'break-all' }}
            >
              {rawKeyData?.raw_key}
            </Typography>
            <Tooltip title={keyCopied ? 'Copied!' : 'Copy to clipboard'}>
              <IconButton onClick={handleCopyKey} size="small" aria-label="Copy API key">
                {keyCopied ? (
                  <CheckIcon color="success" fontSize="small" />
                ) : (
                  <ContentCopyIcon fontSize="small" />
                )}
              </IconButton>
            </Tooltip>
          </Box>
        </DialogContent>
        <DialogActions>
          <Button
            variant="contained"
            onClick={() => {
              setRawKeyData(null);
              setKeyCopied(false);
            }}
          >
            Done
          </Button>
        </DialogActions>
      </Dialog>

      {/* ── Revoke Key Confirm ───────────────────────────────────── */}
      <Dialog open={Boolean(revokeKeyTarget)} onClose={() => setRevokeKeyTarget(null)}>
        <DialogTitle>Revoke API Key</DialogTitle>
        <DialogContent>
          <Typography>
            Revoke key <strong>{revokeKeyTarget?.name}</strong>? It will stop working immediately.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRevokeKeyTarget(null)}>Cancel</Button>
          <Button variant="contained" color="error" onClick={handleRevokeKey}>
            Revoke
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};

export default Profile;
