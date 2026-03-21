import type { ReactNode } from 'react';
import { Box, AppBar, Toolbar, Typography, Container, Button, Chip } from '@mui/material';
import { Link as RouterLink } from 'react-router-dom';
import { useAuth } from '../../context/AuthContext';

interface LayoutProps {
  children: ReactNode;
}

const Layout = ({ children }: LayoutProps) => {
  const { user, isAuthenticated, logout } = useAuth();

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', minHeight: '100vh' }}>
      <AppBar position="static">
        <Toolbar>
          <Typography
            variant="h6"
            component={RouterLink}
            to="/"
            sx={{ color: 'inherit', textDecoration: 'none', mr: 3 }}
          >
            K8s Stack Manager
          </Typography>

          {isAuthenticated && (
            <Box sx={{ display: 'flex', gap: 1, flexGrow: 1 }}>
              <Button color="inherit" component={RouterLink} to="/">
                Dashboard
              </Button>
              <Button color="inherit" component={RouterLink} to="/templates">
                Templates
              </Button>
              <Button color="inherit" component={RouterLink} to="/stack-definitions">
                Definitions
              </Button>
              <Button color="inherit" component={RouterLink} to="/audit-log">
                Audit Log
              </Button>
              {user?.role === 'admin' && (
                <>
                  <Button color="inherit" component={RouterLink} to="/admin/users">
                    Users
                  </Button>
                  <Button color="inherit" component={RouterLink} to="/admin/orphaned-namespaces">
                    Orphaned NS
                  </Button>
                  <Button color="inherit" component={RouterLink} to="/admin/clusters">
                    Clusters
                  </Button>
                  <Button color="inherit" component={RouterLink} to="/admin/cluster-health">
                    Cluster Health
                  </Button>
                </>
              )}
            </Box>
          )}

          {!isAuthenticated && <Box sx={{ flexGrow: 1 }} />}

          {isAuthenticated && user && (
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
              <Button color="inherit" component={RouterLink} to="/profile" size="small">
                {user.username}
              </Button>
              <Chip
                label={user.role}
                size="small"
                sx={{ color: 'inherit', borderColor: 'rgba(255,255,255,0.5)' }}
                variant="outlined"
              />
              <Button color="inherit" onClick={logout} size="small">
                Logout
              </Button>
            </Box>
          )}
        </Toolbar>
      </AppBar>
      <Container component="main" maxWidth="lg" sx={{ mt: 4, mb: 4, flex: '1 0 auto' }}>
        {children}
      </Container>
    </Box>
  );
};

export default Layout;
