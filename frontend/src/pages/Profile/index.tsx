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
} from '@mui/material';
import KeyIcon from '@mui/icons-material/Key';
import DeleteIcon from '@mui/icons-material/Delete';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import CheckIcon from '@mui/icons-material/Check';
import { apiKeyService } from '../../api/client';
import { useAuth } from '../../context/AuthContext';
import type { APIKey, CreateAPIKeyRequest, CreateAPIKeyResponse } from '../../types';

const getRoleChipColor = (role: string): 'error' | 'warning' | 'default' => {
  if (role === 'admin') return 'error';
  if (role === 'devops') return 'warning';
  return 'default';
};

const Profile = () => {
  const { user: currentUser } = useAuth();

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
        <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
          <CircularProgress />
        </Box>
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
