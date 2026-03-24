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

interface TokenPayload {
  auth_provider?: string;
  email?: string;
}

const EVENT_TYPE_LABELS: Record<string, string> = {
  'deployment.success': 'Deployment succeeded',
  'deployment.error': 'Deployment failed',
  'deployment.stopped': 'Deployment stopped',
  'instance.deleted': 'Instance deleted',
};

const DEFAULT_EVENT_TYPES = Object.keys(EVENT_TYPE_LABELS);

const getRoleChipColor = (role: string): 'error' | 'warning' | 'default' => {
  if (role === 'admin') return 'error';
  if (role === 'devops') return 'warning';
  return 'default';
};

function decodeTokenPayload(): TokenPayload | null {
  try {
    const token = localStorage.getItem('token');
    if (!token) return null;
    const base64 = token.split('.')[1];
    const json = atob(base64);
    return JSON.parse(json);
  } catch {
    return null;
  }
}

const Profile = () => {
  const { user: currentUser, oidcConfig } = useAuth();

  const [apiKeys, setApiKeys] = useState<APIKey[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Generate key dialog
  const [generateKeyOpen, setGenerateKeyOpen] = useState(false);
  const [generateKeyForm, setGenerateKeyForm] = useState<CreateAPIKeyRequest>({ name: '' });
  const [generateKeyError, setGenerateKeyError] = useState<string | null>(null);
  const [generateKeyLoading, setGenerateKeyLoading] = useState(false);

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

  // Auth provider detection
  const [authProvider, setAuthProvider] = useState<string | null>(null);
  const [authEmail, setAuthEmail] = useState<string | null>(null);

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
    const payload = decodeTokenPayload();
    if (payload?.auth_provider) {
      setAuthProvider(payload.auth_provider);
      if (payload.email) setAuthEmail(payload.email);
    }
  }, []);

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
      const result = await apiKeyService.create(currentUser.id, generateKeyForm);
      setGenerateKeyOpen(false);
      setGenerateKeyForm({ name: '' });
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
                        {key.expires_at ? new Date(key.expires_at).toLocaleDateString() : 'Never'}
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
                      inputProps={{
                        'aria-label': `Toggle ${EVENT_TYPE_LABELS[pref.event_type] || pref.event_type}`,
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
            <TextField
              label="Expires At (optional)"
              type="date"
              value={generateKeyForm.expires_at ?? ''}
              onChange={(e) =>
                setGenerateKeyForm((prev) => ({ ...prev, expires_at: e.target.value || undefined }))
              }
              fullWidth
              size="small"
              slotProps={{ inputLabel: { shrink: true } }}
            />
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
