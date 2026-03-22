import { ReactNode } from 'react';
import { Box, Typography } from '@mui/material';

interface EmptyStateProps {
  icon?: ReactNode;
  title: string;
  description?: string;
  action?: ReactNode;
}

const EmptyState = ({ icon, title, description, action }: EmptyStateProps) => (
  <Box
    display="flex"
    flexDirection="column"
    alignItems="center"
    justifyContent="center"
    minHeight="200px"
    sx={{ py: 4 }}
  >
    {icon && (
      <Box sx={{ mb: 2, color: 'text.secondary', '& .MuiSvgIcon-root': { fontSize: 48 } }}>
        {icon}
      </Box>
    )}
    <Typography variant="h6" gutterBottom>
      {title}
    </Typography>
    {description && (
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2, textAlign: 'center', maxWidth: 400 }}>
        {description}
      </Typography>
    )}
    {action && <Box sx={{ mt: 1 }}>{action}</Box>}
  </Box>
);

export default EmptyState;
