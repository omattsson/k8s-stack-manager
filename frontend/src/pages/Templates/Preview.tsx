import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Typography,
  Paper,
  Chip,
  Button,
  CircularProgress,
  Alert,
  Divider,
} from '@mui/material';
import { templateService } from '../../api/client';
import { useAuth } from '../../context/AuthContext';
import type { StackTemplate } from '../../types';

const Preview = () => {
  const { id } = useParams<{ id: string }>();
  const [template, setTemplate] = useState<StackTemplate | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { user } = useAuth();
  const navigate = useNavigate();

  const isDevOps = user?.role === 'devops' || user?.role === 'admin';

  useEffect(() => {
    if (!id) return;
    const fetchTemplate = async () => {
      try {
        const data = await templateService.get(id);
        setTemplate(data);
      } catch {
        setError('Failed to load template');
      } finally {
        setLoading(false);
      }
    };
    fetchTemplate();
  }, [id]);

  const handleClone = async () => {
    if (!id) return;
    try {
      const cloned = await templateService.clone(id);
      navigate(`/templates/${cloned.id}/edit`);
    } catch {
      setError('Failed to clone template');
    }
  };

  if (loading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
        <CircularProgress />
      </Box>
    );
  }

  if (error || !template) {
    return <Alert severity="error">{error || 'Template not found'}</Alert>;
  }

  return (
    <Box>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', mb: 3 }}>
        <Box>
          <Typography variant="h4" component="h1">
            {template.name}
          </Typography>
          <Box sx={{ display: 'flex', gap: 1, mt: 1 }}>
            <Chip label={template.is_published ? 'Published' : 'Draft'} color={template.is_published ? 'success' : 'default'} size="small" />
            {template.category && <Chip label={template.category} variant="outlined" size="small" />}
            {template.version && <Chip label={`v${template.version}`} variant="outlined" size="small" />}
          </Box>
        </Box>
        <Box sx={{ display: 'flex', gap: 1 }}>
          {template.is_published && (
            <Button variant="contained" onClick={() => navigate(`/templates/${id}/use`)}>
              Use Template
            </Button>
          )}
          {isDevOps && (
            <>
              <Button variant="outlined" onClick={handleClone}>
                Clone as Template
              </Button>
              {template.owner_id === user?.id && (
                <Button variant="outlined" onClick={() => navigate(`/templates/${id}/edit`)}>
                  Edit
                </Button>
              )}
            </>
          )}
        </Box>
      </Box>

      {template.description && (
        <Typography variant="body1" sx={{ mb: 3 }} color="text.secondary">
          {template.description}
        </Typography>
      )}

      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        Default Branch: {template.default_branch}
      </Typography>

      <Typography variant="h5" gutterBottom>
        Charts ({template.charts?.length || 0})
      </Typography>

      {(!template.charts || template.charts.length === 0) ? (
        <Typography color="text.secondary">No charts configured.</Typography>
      ) : (
        template.charts.map((chart) => (
          <Paper key={chart.id} sx={{ p: 3, mb: 2 }}>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
              <Typography variant="h6">{chart.chart_name}</Typography>
              <Box sx={{ display: 'flex', gap: 1 }}>
                {chart.required && <Chip label="Required" color="primary" size="small" />}
                <Chip label={`Order: ${chart.deploy_order}`} variant="outlined" size="small" />
              </Box>
            </Box>
            {chart.repository_url && (
              <Typography variant="body2" color="text.secondary">
                Repo: {chart.repository_url}
              </Typography>
            )}
            {chart.chart_path && (
              <Typography variant="body2" color="text.secondary">
                Path: {chart.chart_path} {chart.chart_version && `(v${chart.chart_version})`}
              </Typography>
            )}
            {chart.default_values && (
              <>
                <Divider sx={{ my: 2 }} />
                <Typography variant="subtitle2" gutterBottom>Default Values</Typography>
                <Paper variant="outlined" sx={{ p: 2, bgcolor: 'grey.50' }}>
                  <Typography variant="body2" component="pre" sx={{ fontFamily: 'monospace', fontSize: 13, whiteSpace: 'pre-wrap', m: 0 }}>
                    {chart.default_values}
                  </Typography>
                </Paper>
              </>
            )}
            {chart.locked_values && (
              <>
                <Divider sx={{ my: 2 }} />
                <Typography variant="subtitle2" gutterBottom>
                  Locked Values
                  <Chip label="Read-only" size="small" color="warning" sx={{ ml: 1 }} />
                </Typography>
                <Paper variant="outlined" sx={{ p: 2, bgcolor: 'warning.50', borderColor: 'warning.main' }}>
                  <Typography variant="body2" component="pre" sx={{ fontFamily: 'monospace', fontSize: 13, whiteSpace: 'pre-wrap', m: 0 }}>
                    {chart.locked_values}
                  </Typography>
                </Paper>
              </>
            )}
          </Paper>
        ))
      )}

      <Button variant="outlined" onClick={() => navigate('/templates')} sx={{ mt: 2 }}>
        Back to Gallery
      </Button>
    </Box>
  );
};

export default Preview;
