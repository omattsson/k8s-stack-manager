import { Fragment, useEffect, useState, useCallback, useRef } from 'react';
import {
  Box,
  Typography,
  Button,
  CircularProgress,
  Alert,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  IconButton,
  Chip,
  Collapse,
  Tooltip,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  MenuItem,
  Breadcrumbs,
  Link as MuiLink,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import DeleteIcon from '@mui/icons-material/Delete';
import KeyIcon from '@mui/icons-material/Key';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import CheckIcon from '@mui/icons-material/Check';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import LockResetIcon from '@mui/icons-material/LockReset';
import { userService, apiKeyService } from '../../../api/client';
import { useAuth } from '../../../context/AuthContext';
import type { User, APIKey, CreateUserRequest, CreateAPIKeyRequest, CreateAPIKeyResponse } from '../../../types';
import LoadingState from '../../../components/LoadingState';
import { Link } from 'react-router-dom';

const defaultKeyForm = (): CreateAPIKeyRequest => ({ name: '', expires_in_days: 90 });

const getRoleChipColor = (role: string): 'error' | 'warning' | 'default' => {
  if (role === 'admin') return 'error';
  if (role === 'devops') return 'warning';
  return 'default';
};

const AdminUsers = () => {
  const { user: currentUser } = useAuth();

  // Users list
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Expanded rows & lazy-loaded API keys
  const [expandedUsers, setExpandedUsers] = useState<Set<string>>(new Set());
  const [apiKeysMap, setApiKeysMap] = useState<Record<string, APIKey[]>>({});
  const [apiKeysLoadingMap, setApiKeysLoadingMap] = useState<Record<string, boolean>>({});
  const [apiKeysErrorMap, setApiKeysErrorMap] = useState<Record<string, string>>({});

  // Create user dialog
  const [createUserOpen, setCreateUserOpen] = useState(false);
  const [createUserForm, setCreateUserForm] = useState<CreateUserRequest>({
    username: '',
    password: '',
    display_name: '',
    role: 'user',
  });
  const [createUserError, setCreateUserError] = useState<string | null>(null);
  const [createUserLoading, setCreateUserLoading] = useState(false);

  // Delete user confirm
  const [deleteUserTarget, setDeleteUserTarget] = useState<User | null>(null);

  // Generate API key dialog
  const [generateKeyUserId, setGenerateKeyUserId] = useState<string | null>(null);
  const [generateKeyForm, setGenerateKeyForm] = useState<CreateAPIKeyRequest>(defaultKeyForm());
  const [generateKeyError, setGenerateKeyError] = useState<string | null>(null);
  const [generateKeyLoading, setGenerateKeyLoading] = useState(false);

  // Raw key modal (shown after successful key creation)
  const [rawKeyData, setRawKeyData] = useState<CreateAPIKeyResponse | null>(null);
  const [keyCopied, setKeyCopied] = useState(false);
  const copyTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Password reset dialog
  const [resetPasswordTarget, setResetPasswordTarget] = useState<User | null>(null);
  const [resetPasswordValue, setResetPasswordValue] = useState('');
  const [resetPasswordError, setResetPasswordError] = useState<string | null>(null);
  const [resetPasswordLoading, setResetPasswordLoading] = useState(false);

  // Revoke API key confirm
  const [revokeKeyTarget, setRevokeKeyTarget] = useState<{ userId: string; key: APIKey } | null>(null);

  const fetchUsers = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await userService.list();
      setUsers(data || []);
    } catch {
      setError('Failed to load users');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (currentUser?.role === 'admin') {
      fetchUsers();
    }
  }, [fetchUsers, currentUser]);

  useEffect(() => {
    return () => {
      if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
    };
  }, []);

  const loadApiKeys = useCallback(async (userId: string) => {
    setApiKeysLoadingMap((prev) => ({ ...prev, [userId]: true }));
    setApiKeysErrorMap((prev) => ({ ...prev, [userId]: '' }));
    try {
      const keys = await apiKeyService.list(userId);
      setApiKeysMap((prev) => ({ ...prev, [userId]: keys || [] }));
    } catch {
      setApiKeysErrorMap((prev) => ({ ...prev, [userId]: 'Failed to load API keys' }));
    } finally {
      setApiKeysLoadingMap((prev) => ({ ...prev, [userId]: false }));
    }
  }, []);

  const handleToggleExpand = (userId: string) => {
    // Lazy-load keys on first expand
    if (!expandedUsers.has(userId) && !apiKeysLoadingMap[userId] && !apiKeysMap[userId]) {
      loadApiKeys(userId);
    }
    setExpandedUsers((prev) => {
      const next = new Set(prev);
      if (next.has(userId)) {
        next.delete(userId);
      } else {
        next.add(userId);
      }
      return next;
    });
  };

  const handleCreateUser = async () => {
    if (!createUserForm.username.trim() || !createUserForm.password.trim()) {
      setCreateUserError('Username and password are required');
      return;
    }
    setCreateUserLoading(true);
    setCreateUserError(null);
    try {
      await userService.create(createUserForm);
      setCreateUserOpen(false);
      setCreateUserForm({ username: '', password: '', display_name: '', role: 'user' });
      await fetchUsers();
    } catch {
      setCreateUserError('Failed to create user');
    } finally {
      setCreateUserLoading(false);
    }
  };

  const handleDeleteUser = async () => {
    if (!deleteUserTarget) return;
    const userId = deleteUserTarget.id;
    try {
      await userService.delete(userId);
      setDeleteUserTarget(null);
      setExpandedUsers((prev) => { const n = new Set(prev); n.delete(userId); return n; });
      setApiKeysMap((prev) => { const n = { ...prev }; delete n[userId]; return n; });
      await fetchUsers();
    } catch {
      setError('Failed to delete user');
      setDeleteUserTarget(null);
    }
  };

  const handleResetPassword = async () => {
    if (!resetPasswordTarget || resetPasswordValue.length < 8) {
      setResetPasswordError('Password must be at least 8 characters');
      return;
    }
    setResetPasswordLoading(true);
    setResetPasswordError(null);
    try {
      await userService.resetPassword(resetPasswordTarget.id, resetPasswordValue);
      setResetPasswordTarget(null);
      setResetPasswordValue('');
    } catch {
      setResetPasswordError('Failed to reset password');
    } finally {
      setResetPasswordLoading(false);
    }
  };

  const handleGenerateKey = async () => {
    if (!generateKeyUserId || !generateKeyForm.name.trim()) {
      setGenerateKeyError('Key name is required');
      return;
    }
    setGenerateKeyLoading(true);
    setGenerateKeyError(null);
    const targetUserId = generateKeyUserId;
    try {
      const result = await apiKeyService.create(targetUserId, generateKeyForm);
      setGenerateKeyUserId(null);
      setGenerateKeyForm(defaultKeyForm());
      setRawKeyData(result);
      await loadApiKeys(targetUserId);
    } catch {
      setGenerateKeyError('Failed to generate API key');
    } finally {
      setGenerateKeyLoading(false);
    }
  };

  const handleRevokeKey = async () => {
    if (!revokeKeyTarget) return;
    try {
      await apiKeyService.delete(revokeKeyTarget.userId, revokeKeyTarget.key.id);
      setRevokeKeyTarget(null);
      await loadApiKeys(revokeKeyTarget.userId);
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

  // Access guard (defence-in-depth — ProtectedRoute also blocks non-admins)
  if (currentUser?.role !== 'admin') {
    return (
      <Box sx={{ mt: 4 }}>
        <Alert severity="error">You do not have permission to access this page. Admin role required.</Alert>
      </Box>
    );
  }

  return (
    <Box>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <MuiLink component={Link} to="/" underline="hover" color="inherit">Home</MuiLink>
        <Typography color="text.primary">Users</Typography>
      </Breadcrumbs>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          User Management
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreateUserOpen(true)}>
          Add User
        </Button>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <LoadingState label="Loading users..." />
      ) : (
        <TableContainer component={Paper} sx={{ maxHeight: 600 }}>
          <Table stickyHeader>
            <TableHead>
              <TableRow>
                <TableCell width={48} />
                <TableCell>Username</TableCell>
                <TableCell>Display Name</TableCell>
                <TableCell>Role</TableCell>
                <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>Created At</TableCell>
                <TableCell>Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {users.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} align="center">
                    <Typography color="text.secondary" sx={{ py: 2 }}>
                      No users found.
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                users.map((u) => (
                  <Fragment key={u.id}>
                    <TableRow hover>
                      <TableCell>
                        <IconButton
                          size="small"
                          onClick={() => handleToggleExpand(u.id)}
                          aria-label={expandedUsers.has(u.id) ? 'Collapse API keys' : 'Expand API keys'}
                        >
                          {expandedUsers.has(u.id) ? <ExpandLessIcon /> : <ExpandMoreIcon />}
                        </IconButton>
                      </TableCell>
                      <TableCell>
                        <Typography variant="body2" sx={{ fontWeight: 'medium' }}>
                          {u.username}
                        </Typography>
                      </TableCell>
                      <TableCell>{u.display_name || '—'}</TableCell>
                      <TableCell>
                        <Chip label={u.role} size="small" color={getRoleChipColor(u.role)} />
                      </TableCell>
                      <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>{new Date(u.created_at).toLocaleDateString()}</TableCell>
                      <TableCell>
                        {(!u.auth_provider || u.auth_provider === 'local') && (
                          <Tooltip title="Reset password">
                            <IconButton
                              size="small"
                              onClick={() => {
                                setResetPasswordTarget(u);
                                setResetPasswordValue('');
                                setResetPasswordError(null);
                              }}
                              aria-label={`Reset password for ${u.username}`}
                            >
                              <LockResetIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                        <Tooltip title={u.id === currentUser.id ? 'Cannot delete your own account' : 'Delete user'}>
                          <span>
                            <IconButton
                              size="small"
                              color="error"
                              disabled={u.id === currentUser.id}
                              onClick={() => setDeleteUserTarget(u)}
                              aria-label={`Delete user ${u.username}`}
                            >
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </span>
                        </Tooltip>
                      </TableCell>
                    </TableRow>
                    <TableRow>
                      <TableCell
                        colSpan={6}
                        sx={{ py: 0, borderBottom: expandedUsers.has(u.id) ? undefined : 'none' }}
                      >
                        <Collapse in={expandedUsers.has(u.id)} timeout="auto" unmountOnExit>
                          <Box sx={{ px: 4, py: 2 }}>
                            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
                              <Typography variant="subtitle2">API Keys</Typography>
                              <Button
                                size="small"
                                variant="outlined"
                                startIcon={<KeyIcon />}
                                onClick={() => {
                                  setGenerateKeyUserId(u.id);
                                  setGenerateKeyForm(defaultKeyForm());
                                  setGenerateKeyError(null);
                                }}
                              >
                                Generate API Key
                              </Button>
                            </Box>

                            {apiKeysLoadingMap[u.id] && (
                              <Box sx={{ display: 'flex', justifyContent: 'center', py: 2 }}>
                                <CircularProgress size={24} />
                              </Box>
                            )}
                            {apiKeysErrorMap[u.id] && (
                              <Alert severity="error" sx={{ mb: 1 }}>{apiKeysErrorMap[u.id]}</Alert>
                            )}
                            {!apiKeysLoadingMap[u.id] && apiKeysMap[u.id] && (
                              apiKeysMap[u.id].length === 0 ? (
                                <Typography variant="body2" color="text.secondary">
                                  No API keys.
                                </Typography>
                              ) : (
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
                                    {apiKeysMap[u.id].map((key) => (
                                      <TableRow key={key.id}>
                                        <TableCell>{key.name}</TableCell>
                                        <TableCell>
                                          <Typography sx={{ fontFamily: 'monospace', fontSize: 12 }}>
                                            {key.prefix}...
                                          </Typography>
                                        </TableCell>
                                        <TableCell>{new Date(key.created_at).toLocaleDateString()}</TableCell>
                                        <TableCell>
                                          {key.last_used_at
                                            ? new Date(key.last_used_at).toLocaleDateString()
                                            : '—'}
                                        </TableCell>
                                        <TableCell>
                                          {key.expires_at
                                            ? new Date(key.expires_at).toLocaleDateString()
                                            : 'Never'}
                                        </TableCell>
                                        <TableCell>
                                          <IconButton
                                            size="small"
                                            color="error"
                                            onClick={() => setRevokeKeyTarget({ userId: u.id, key })}
                                            aria-label={`Revoke key ${key.name}`}
                                          >
                                            <DeleteIcon fontSize="small" />
                                          </IconButton>
                                        </TableCell>
                                      </TableRow>
                                    ))}
                                  </TableBody>
                                </Table>
                              )
                            )}
                          </Box>
                        </Collapse>
                      </TableCell>
                    </TableRow>
                  </Fragment>
                ))
              )}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* ── Create User Dialog ─────────────────────────────────────── */}
      <Dialog open={createUserOpen} onClose={() => setCreateUserOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Add User</DialogTitle>
        <DialogContent>
          {createUserError && <Alert severity="error" sx={{ mb: 2, mt: 1 }}>{createUserError}</Alert>}
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
            <TextField
              label="Username"
              value={createUserForm.username}
              onChange={(e) => setCreateUserForm((prev) => ({ ...prev, username: e.target.value }))}
              required
              fullWidth
              size="small"
              autoFocus
            />
            <TextField
              label="Password"
              type="password"
              value={createUserForm.password}
              onChange={(e) => setCreateUserForm((prev) => ({ ...prev, password: e.target.value }))}
              required
              fullWidth
              size="small"
            />
            <TextField
              label="Display Name"
              value={createUserForm.display_name}
              onChange={(e) => setCreateUserForm((prev) => ({ ...prev, display_name: e.target.value }))}
              fullWidth
              size="small"
            />
            <TextField
              label="Role"
              value={createUserForm.role}
              onChange={(e) => setCreateUserForm((prev) => ({ ...prev, role: e.target.value }))}
              select
              fullWidth
              size="small"
            >
              <MenuItem value="user">user</MenuItem>
              <MenuItem value="devops">devops</MenuItem>
              <MenuItem value="admin">admin</MenuItem>
            </TextField>
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCreateUserOpen(false)}>Cancel</Button>
          <Button variant="contained" onClick={handleCreateUser} disabled={createUserLoading}>
            {createUserLoading ? <CircularProgress size={20} /> : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* ── Delete User Confirm ────────────────────────────────────── */}
      <Dialog open={Boolean(deleteUserTarget)} onClose={() => setDeleteUserTarget(null)}>
        <DialogTitle>Delete User</DialogTitle>
        <DialogContent>
          <Typography>
            Are you sure you want to delete user <strong>{deleteUserTarget?.username}</strong>?
            This action cannot be undone.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteUserTarget(null)}>Cancel</Button>
          <Button variant="contained" color="error" onClick={handleDeleteUser}>
            Delete
          </Button>
        </DialogActions>
      </Dialog>

      {/* ── Generate API Key Dialog ────────────────────────────────── */}
      <Dialog
        open={Boolean(generateKeyUserId)}
        onClose={() => setGenerateKeyUserId(null)}
        maxWidth="xs"
        fullWidth
      >
        <DialogTitle>Generate API Key</DialogTitle>
        <DialogContent>
          {generateKeyError && <Alert severity="error" sx={{ mb: 2, mt: 1 }}>{generateKeyError}</Alert>}
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
              label="Expires In (days)"
              type="number"
              value={generateKeyForm.expires_in_days ?? 90}
              onChange={(e) => {
                const parsed = parseInt(e.target.value, 10);
                const days = Number.isFinite(parsed) ? Math.max(1, parsed) : 90;
                setGenerateKeyForm((prev) => ({ ...prev, expires_in_days: days, expires_at: undefined }));
              }}
              required
              fullWidth
              size="small"
              helperText="Default: 90 days"
              slotProps={{ htmlInput: { min: 1 } }}
            />
          </Box>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setGenerateKeyUserId(null)}>Cancel</Button>
          <Button variant="contained" onClick={handleGenerateKey} disabled={generateKeyLoading}>
            {generateKeyLoading ? <CircularProgress size={20} /> : 'Generate'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* ── Raw Key Modal ──────────────────────────────────────────── */}
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

      {/* ── Reset Password Dialog ────────────────────────────────── */}
      <Dialog
        open={Boolean(resetPasswordTarget)}
        onClose={() => setResetPasswordTarget(null)}
        maxWidth="xs"
        fullWidth
      >
        <DialogTitle>Reset Password</DialogTitle>
        <DialogContent>
          {resetPasswordError && <Alert severity="error" sx={{ mb: 2, mt: 1 }}>{resetPasswordError}</Alert>}
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2, mt: 1 }}>
            Set a new password for <strong>{resetPasswordTarget?.username}</strong>.
            Existing sessions will be revoked.
          </Typography>
          <TextField
            label="New Password"
            type="password"
            value={resetPasswordValue}
            onChange={(e) => setResetPasswordValue(e.target.value)}
            required
            fullWidth
            size="small"
            helperText="Minimum 8 characters"
            error={resetPasswordValue.length > 0 && resetPasswordValue.length < 8}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setResetPasswordTarget(null)}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            onClick={handleResetPassword}
            disabled={resetPasswordLoading || resetPasswordValue.length < 8}
          >
            {resetPasswordLoading ? <CircularProgress size={20} /> : 'Reset Password'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* ── Revoke API Key Confirm ─────────────────────────────────── */}
      <Dialog open={Boolean(revokeKeyTarget)} onClose={() => setRevokeKeyTarget(null)}>
        <DialogTitle>Revoke API Key</DialogTitle>
        <DialogContent>
          <Typography>
            Revoke key <strong>{revokeKeyTarget?.key.name}</strong>? It will stop working immediately.
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

export default AdminUsers;
