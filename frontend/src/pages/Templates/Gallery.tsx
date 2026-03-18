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
  TextField,
  Chip,
  CircularProgress,
  Alert,
  Tabs,
  Tab,
  InputAdornment,
} from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';
import AddIcon from '@mui/icons-material/Add';
import { templateService } from '../../api/client';
import { useAuth } from '../../context/AuthContext';
import type { StackTemplate } from '../../types';

const CATEGORIES = ['All', 'Web', 'API', 'Data', 'Infrastructure', 'Other'];

const Gallery = () => {
  const [templates, setTemplates] = useState<StackTemplate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [category, setCategory] = useState('All');
  const [tab, setTab] = useState(0);
  const { user } = useAuth();
  const navigate = useNavigate();

  const isDevOps = user?.role === 'devops' || user?.role === 'admin';

  useEffect(() => {
    const fetchTemplates = async () => {
      try {
        const data = await templateService.list();
        setTemplates(data || []);
      } catch {
        setError('Failed to load templates');
      } finally {
        setLoading(false);
      }
    };
    fetchTemplates();
  }, []);

  const filtered = templates.filter((t) => {
    if (tab === 1 && t.owner_id !== user?.id) return false;
    if (tab === 0 && !t.is_published) return false;
    if (category !== 'All' && t.category !== category) return false;
    if (search && !t.name.toLowerCase().includes(search.toLowerCase()) &&
        !t.description.toLowerCase().includes(search.toLowerCase())) return false;
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
          Template Gallery
        </Typography>
        {isDevOps && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/templates/new')}>
            Create Template
          </Button>
        )}
      </Box>

      {isDevOps && (
        <Tabs value={tab} onChange={(_e, v: number) => setTab(v)} sx={{ mb: 2 }}>
          <Tab label="Published" />
          <Tab label="My Templates" />
        </Tabs>
      )}

      <Box sx={{ display: 'flex', gap: 2, mb: 3, flexWrap: 'wrap', alignItems: 'center' }}>
        <TextField
          size="small"
          placeholder="Search templates..."
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
        <Box sx={{ display: 'flex', gap: 0.5 }}>
          {CATEGORIES.map((cat) => (
            <Chip
              key={cat}
              label={cat}
              onClick={() => setCategory(cat)}
              color={category === cat ? 'primary' : 'default'}
              variant={category === cat ? 'filled' : 'outlined'}
            />
          ))}
        </Box>
      </Box>

      {filtered.length === 0 ? (
        <Typography color="text.secondary" sx={{ textAlign: 'center', mt: 4 }}>
          No templates found.
        </Typography>
      ) : (
        <Grid container spacing={3}>
          {filtered.map((template) => (
            <Grid key={template.id} size={{ xs: 12, sm: 6, md: 4 }}>
              <Card sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
                <CardContent sx={{ flex: 1 }}>
                  <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 1 }}>
                    <Typography variant="h6" component="h2">
                      {template.name}
                    </Typography>
                    {!template.is_published && <Chip label="Draft" size="small" />}
                  </Box>
                  <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                    {template.description || 'No description'}
                  </Typography>
                  <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
                    {template.category && (
                      <Chip label={template.category} size="small" variant="outlined" />
                    )}
                    {template.version && (
                      <Chip label={`v${template.version}`} size="small" variant="outlined" />
                    )}
                    {template.charts && (
                      <Chip label={`${template.charts.length} charts`} size="small" variant="outlined" />
                    )}
                  </Box>
                </CardContent>
                <CardActions>
                  <Button size="small" onClick={() => navigate(`/templates/${template.id}`)}>
                    View
                  </Button>
                  {template.is_published && (
                    <Button size="small" color="primary" onClick={() => navigate(`/templates/${template.id}/use`)}>
                      Use Template
                    </Button>
                  )}
                  {isDevOps && template.owner_id === user?.id && (
                    <Button size="small" onClick={() => navigate(`/templates/${template.id}/edit`)}>
                      Edit
                    </Button>
                  )}
                </CardActions>
              </Card>
            </Grid>
          ))}
        </Grid>
      )}
    </Box>
  );
};

export default Gallery;
