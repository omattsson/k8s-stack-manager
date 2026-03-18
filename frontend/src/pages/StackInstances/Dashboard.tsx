import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Typography,
  Grid,
  Card,
  CardContent,
  CardActions,
  Button,
  CircularProgress,
  Alert,
  TextField,
  MenuItem,
  InputAdornment,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import AddIcon from '@mui/icons-material/Add';
import StatusBadge from '../../components/StatusBadge';
import { instanceService } from '../../api/client';
import type { StackInstance } from '../../types';

const STATUSES = ['All', 'draft', 'deploying', 'running', 'stopped', 'error'];

const Dashboard = () => {
  const [instances, setInstances] = useState<StackInstance[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState('All');
  const navigate = useNavigate();

  useEffect(() => {
    const fetchInstances = async () => {
      try {
        const data = await instanceService.list();
        setInstances(data || []);
      } catch {
        setError('Failed to load stack instances');
      } finally {
        setLoading(false);
      }
    };
    fetchInstances();
  }, []);

  const filtered = instances.filter((inst) => {
    if (statusFilter !== 'All' && inst.status !== statusFilter) return false;
    if (search && !inst.name.toLowerCase().includes(search.toLowerCase())) return false;
    return true;
  });

  if (loading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">{error}</Alert>;
  }

  return (
    <Box>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Stack Instances
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/stack-instances/new')}>
          Create Instance
        </Button>
      </Box>

      <Box sx={{ display: 'flex', gap: 2, mb: 3 }}>
        <TextField
          size="small"
          placeholder="Search instances..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          slotProps={{
            input: {
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon />
                </InputAdornment>
              ),
            },
          }}
          sx={{ minWidth: 250 }}
        />
        <TextField
          size="small"
          select
          label="Status"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          sx={{ minWidth: 150 }}
        >
          {STATUSES.map((s) => (
            <MenuItem key={s} value={s}>{s === 'All' ? 'All Statuses' : s}</MenuItem>
          ))}
        </TextField>
      </Box>

      {filtered.length === 0 ? (
        <Box sx={{ textAlign: 'center', mt: 4 }}>
          <Typography color="text.secondary" gutterBottom>
            No stack instances found.
          </Typography>
          <Button variant="outlined" onClick={() => navigate('/stack-instances/new')}>
            Create your first instance
          </Button>
        </Box>
      ) : (
        <Grid container spacing={3}>
          {filtered.map((instance) => (
            <Grid key={instance.id} size={{ xs: 12, sm: 6, md: 4 }}>
              <Card sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
                <CardContent sx={{ flex: 1 }}>
                  <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
                    <Typography variant="h6" component="h2" noWrap>
                      {instance.name}
                    </Typography>
                    <StatusBadge status={instance.status} />
                  </Box>
                  <Typography variant="body2" color="text.secondary">
                    Branch: {instance.branch}
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    Namespace: {instance.namespace}
                  </Typography>
                  {instance.definition && (
                    <Typography variant="body2" color="text.secondary">
                      Definition: {instance.definition.name}
                    </Typography>
                  )}
                </CardContent>
                <CardActions>
                  <Button size="small" onClick={() => navigate(`/stack-instances/${instance.id}`)}>
                    Details
                  </Button>
                </CardActions>
              </Card>
            </Grid>
          ))}
        </Grid>
      )}
    </Box>
  );
};

export default Dashboard;
