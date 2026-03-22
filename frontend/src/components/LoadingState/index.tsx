import { Box, CircularProgress, Typography } from '@mui/material';

interface LoadingStateProps {
  label?: string;
}

const LoadingState = ({ label }: LoadingStateProps) => (
  <Box
    display="flex"
    flexDirection="column"
    alignItems="center"
    justifyContent="center"
    minHeight="200px"
    aria-live="polite"
    role="status"
  >
    <CircularProgress />
    {label && (
      <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>
        {label}
      </Typography>
    )}
  </Box>
);

export default LoadingState;
