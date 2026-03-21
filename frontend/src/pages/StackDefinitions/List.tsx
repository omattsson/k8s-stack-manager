import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Typography,
  Button,
  CircularProgress,
  Alert,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  Chip,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import { definitionService, templateService } from '../../api/client';
import FavoriteButton from '../../components/FavoriteButton';
import type { StackDefinition, StackTemplate } from '../../types';

const List = () => {
  const [definitions, setDefinitions] = useState<StackDefinition[]>([]);
  const [templates, setTemplates] = useState<Record<string, StackTemplate>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    const fetchDefinitions = async () => {
      try {
        const data = await definitionService.list();
        setDefinitions(data || []);
        // Fetch templates to check for updates
        try {
          const tmplList = await templateService.list();
          const tmplMap: Record<string, StackTemplate> = {};
          for (const t of tmplList) {
            tmplMap[t.id] = t;
          }
          setTemplates(tmplMap);
        } catch {
          // Best-effort: don't block page if templates fail
        }
      } catch {
        setError('Failed to load stack definitions');
      } finally {
        setLoading(false);
      }
    };
    fetchDefinitions();
  }, []);

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
          Stack Definitions
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/stack-definitions/new')}>
          Create Definition
        </Button>
      </Box>

      {definitions.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <Typography color="text.secondary" gutterBottom>
            No stack definitions yet.
          </Typography>
          <Button variant="outlined" onClick={() => navigate('/templates')}>
            Browse Templates
          </Button>
        </Paper>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Description</TableCell>
                <TableCell>Default Branch</TableCell>
                <TableCell>Charts</TableCell>
                <TableCell>Source Template</TableCell>
                <TableCell>Created</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {definitions.map((def) => (
                <TableRow
                  key={def.id}
                  hover
                  sx={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/stack-definitions/${def.id}/edit`)}
                >
                  <TableCell>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                      <FavoriteButton entityType="definition" entityId={def.id} size="small" />
                      <Typography variant="body2" fontWeight="bold">
                        {def.name}
                      </Typography>
                    </Box>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary" noWrap sx={{ maxWidth: 300 }}>
                      {def.description || '—'}
                    </Typography>
                  </TableCell>
                  <TableCell>{def.default_branch}</TableCell>
                  <TableCell>{def.charts ? def.charts.length : '—'}</TableCell>
                  <TableCell>
                    {def.source_template_id ? (
                      <Box sx={{ display: 'flex', gap: 0.5, alignItems: 'center', flexWrap: 'wrap' }}>
                        <Chip
                          label={`Template${def.source_template_version ? ` v${def.source_template_version}` : ''}`}
                          size="small"
                          variant="outlined"
                          onClick={(e) => {
                            e.stopPropagation();
                            navigate(`/templates/${def.source_template_id}`);
                          }}
                        />
                        {templates[def.source_template_id] &&
                          def.source_template_version &&
                          templates[def.source_template_id].version !== def.source_template_version && (
                          <Chip
                            label="Template update available"
                            size="small"
                            color="info"
                          />
                        )}
                      </Box>
                    ) : (
                      '—'
                    )}
                  </TableCell>
                  <TableCell>
                    {new Date(def.created_at).toLocaleDateString()}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
};

export default List;
