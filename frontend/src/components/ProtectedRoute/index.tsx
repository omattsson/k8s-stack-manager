import { Navigate } from 'react-router-dom';
import { Box, CircularProgress, Alert } from '@mui/material';
import { useAuth } from '../../context/AuthContext';
import type { ReactNode } from 'react';

const ROLE_RANK: Record<string, number> = { user: 1, devops: 2, admin: 3 };

interface ProtectedRouteProps {
  children: ReactNode;
  requiredRole?: 'user' | 'devops' | 'admin';
}

const ProtectedRoute = ({ children, requiredRole }: ProtectedRouteProps) => {
  const { user, isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
        <CircularProgress />
      </Box>
    );
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }

  if (requiredRole && user) {
    const userRank = ROLE_RANK[user.role] ?? 0;
    const required = ROLE_RANK[requiredRole] ?? 999;
    if (userRank < required) {
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
