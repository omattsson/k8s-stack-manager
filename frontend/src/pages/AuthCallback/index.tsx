import { useEffect, useState } from 'react';
import { useSearchParams, useNavigate, Link as RouterLink } from 'react-router-dom';
import { Box, Typography, CircularProgress, Alert, Button, Paper } from '@mui/material';
import { useAuth } from '../../context/AuthContext';

const ERROR_MESSAGES: Record<string, string> = {
  invalid_state: 'Your sign-in session expired. Please try again.',
  auth_failed: 'Authentication failed. Please try again.',
  no_account: 'No account found. Contact your administrator.',
};

/** Parse a URL hash fragment (e.g. "#token=abc&redirect=/foo") into URLSearchParams. */
function parseFragment(hash: string): URLSearchParams {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash;
  return new URLSearchParams(raw);
}

/** Return true when `path` is a safe relative redirect (starts with `/`, not `//`). */
function isSafeRedirect(path: string): boolean {
  return path.startsWith('/') && !path.startsWith('//');
}

const AuthCallback = () => {
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const { handleOIDCCallback } = useAuth();

  useEffect(() => {
    // IdP errors still arrive via query string
    const errorParam = searchParams.get('error');
    if (errorParam) {
      setError(ERROR_MESSAGES[errorParam] || 'Something went wrong. Please try again.');
      return;
    }

    // Token and redirect come from the URL fragment
    const fragment = parseFragment(window.location.hash);
    const token = fragment.get('token');
    const redirect = fragment.get('redirect');

    // Clear the fragment from the URL immediately
    window.history.replaceState(null, '', window.location.pathname + window.location.search);

    if (!token) {
      setError('Something went wrong. Please try again.');
      return;
    }

    handleOIDCCallback(token);

    const target = redirect && isSafeRedirect(redirect) ? redirect : '/';
    navigate(target, { replace: true });
  }, [searchParams, navigate, handleOIDCCallback]);

  if (error) {
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
        <Paper sx={{ p: 4, maxWidth: 440, width: '100%', textAlign: 'center' }}>
          <Alert severity="error" sx={{ mb: 3, textAlign: 'left' }}>
            {error}
          </Alert>
          <Button variant="contained" component={RouterLink} to="/login">
            Back to Login
          </Button>
        </Paper>
      </Box>
    );
  }

  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'center',
        alignItems: 'center',
        minHeight: '60vh',
        gap: 2,
      }}
    >
      <CircularProgress />
      <Typography variant="body1" color="text.secondary">
        Completing sign-in…
      </Typography>
    </Box>
  );
};

export default AuthCallback;
