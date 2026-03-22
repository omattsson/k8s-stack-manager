import { Box, Typography, Button } from '@mui/material';
import { Link as RouterLink } from 'react-router-dom';

const NotFound = () => {
  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        minHeight: '60vh',
        textAlign: 'center',
      }}
    >
      <Typography variant="h1" sx={{ fontSize: '8rem', fontWeight: 700, color: 'text.secondary' }}>
        404
      </Typography>
      <Typography variant="h5" sx={{ mb: 1 }}>
        Page not found
      </Typography>
      <Typography variant="body1" color="text.secondary" sx={{ mb: 4 }}>
        The page you are looking for does not exist or has been moved.
      </Typography>
      <Button variant="contained" component={RouterLink} to="/">
        Back to Dashboard
      </Button>
    </Box>
  );
};

export default NotFound;
