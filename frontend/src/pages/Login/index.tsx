import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  TextField,
  Button,
  Typography,
  Paper,
  Alert,
  Divider,
  CircularProgress,
} from '@mui/material';
import SecurityOutlinedIcon from '@mui/icons-material/SecurityOutlined';
import { useAuth } from '../../context/AuthContext';

const Login = () => {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [ssoLoading, setSsoLoading] = useState(false);
  const { login, isAuthenticated, oidcConfig, oidcLoading, loginWithOIDC } = useAuth();
  const navigate = useNavigate();

  useEffect(() => {
    if (isAuthenticated) {
      navigate('/', { replace: true });
    }
  }, [isAuthenticated, navigate]);

  const handleSsoLogin = async () => {
    setSsoLoading(true);
    setError(null);
    try {
      await loginWithOIDC();
    } catch {
      setError('Failed to initiate SSO login. Please try again.');
      setSsoLoading(false);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await login(username, password);
    } catch (err: unknown) {
      const status = (err as { response?: { status?: number } })?.response?.status;
      const message = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      if (status === 403 && message) {
        setError(message);
      } else {
        setError('Invalid username or password');
      }
    } finally {
      setLoading(false);
    }
  };

  const showSso = oidcConfig?.enabled === true;
  const showLocalAuth = !oidcConfig?.enabled || oidcConfig.local_auth_enabled !== false;

  if (oidcLoading) {
    return (
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'center',
          alignItems: 'center',
          minHeight: '60vh',
        }}
      >
        <CircularProgress />
      </Box>
    );
  }

  return (
    <Box
      sx={{
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'center',
        minHeight: '60vh',
        px: 2,
      }}
    >
      <Paper sx={{ p: 4, maxWidth: 400, width: '100%' }}>
        <Typography variant="h5" component="h1" gutterBottom sx={{ textAlign: 'center' }}>
          Sign In
        </Typography>

        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}

        {showSso && (
          <Box>
            {!showLocalAuth && (
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{ textAlign: 'center', mb: 2 }}
              >
                Sign in with your organization account
              </Typography>
            )}
            <Button
              variant="contained"
              fullWidth
              size="large"
              startIcon={ssoLoading ? undefined : <SecurityOutlinedIcon />}
              onClick={handleSsoLogin}
              disabled={ssoLoading}
            >
              {ssoLoading ? (
                <CircularProgress size={24} color="inherit" />
              ) : (
                `Sign in with ${oidcConfig?.provider_name || 'SSO'}`
              )}
            </Button>
          </Box>
        )}

        {showSso && showLocalAuth && (
          <Divider sx={{ my: 3 }}>
            <Typography variant="body2" color="text.secondary">
              or sign in with credentials
            </Typography>
          </Divider>
        )}

        {showLocalAuth && (
          <Box component="form" onSubmit={handleSubmit}>
            <TextField
              label="Username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              fullWidth
              required
              sx={{ mb: 2 }}
              autoFocus={!showSso}
            />
            <TextField
              label="Password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              fullWidth
              required
              sx={{ mb: 3 }}
            />
            <Button
              type="submit"
              variant={showSso ? 'outlined' : 'contained'}
              fullWidth
              disabled={loading}
              size="large"
            >
              {loading ? 'Signing in...' : 'Sign In'}
            </Button>
          </Box>
        )}
      </Paper>
    </Box>
  );
};

export default Login;
