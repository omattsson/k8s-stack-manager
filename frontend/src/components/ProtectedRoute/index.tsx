import { Navigate } from 'react-router-dom';
import { Box, Alert } from '@mui/material';
import LoadingState from '../LoadingState';
import { useAuth } from '../../context/AuthContext';
import { hasAtLeastRole } from '../../utils/roles';
import type { ReactNode } from 'react';

interface ProtectedRouteProps {
  children: ReactNode;
  requiredRole?: 'user' | 'devops' | 'admin';
}

const ProtectedRoute = ({ children, requiredRole }: ProtectedRouteProps) => {
  const { user, isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return <LoadingState />;
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }

  if (requiredRole && user) {
    if (!hasAtLeastRole(user.role, requiredRole)) {
      return (
        <Box sx={{ mt: 4 }}>
          <Alert severity="error">You do not have permission to access this page.</Alert>
        </Box>
      );
    }
  }

  return <>{children}</>;
};

export default ProtectedRoute;
