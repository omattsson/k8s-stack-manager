import { Routes, Route } from 'react-router-dom';
import { Typography } from '@mui/material';

const AppRoutes = () => {
  return (
    <Routes>
      <Route path="/" element={<Typography>Welcome to K8s Stack Manager</Typography>} />
    </Routes>
  );
};

export default AppRoutes;
