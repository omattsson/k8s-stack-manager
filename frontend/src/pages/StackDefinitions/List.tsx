import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Typography,
  Button,
  Alert,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  Chip,
  IconButton,
  Tooltip,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import FileUploadIcon from '@mui/icons-material/FileUpload';
import SystemUpdateAltIcon from '@mui/icons-material/SystemUpdateAlt';
import { definitionService, templateService } from '../../api/client';
import FavoriteButton from '../../components/FavoriteButton';
import type { StackDefinition, StackTemplate } from '../../types';
import LoadingState from '../../components/LoadingState';
import EmptyState from '../../components/EmptyState';
import ImportDefinitionDialog from './ImportDefinitionDialog';
import UpgradeDialog from './UpgradeDialog';
import { useNotification } from '../../context/NotificationContext';

const List = () => {
  const [definitions, setDefinitions] = useState<StackDefinition[]>([]);
  const [templates, setTemplates] = useState<Record<string, StackTemplate>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [importDialogOpen, setImportDialogOpen] = useState(false);
  const [upgradeDialogDefId, setUpgradeDialogDefId] = useState<string | null>(null);
  const navigate = useNavigate();
  const { showSuccess } = useNotification();

  const fetchDefinitions = useCallback(async () => {
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
  }, []);

  useEffect(() => {
    fetchDefinitions();
  }, [fetchDefinitions]);

  if (loading) {
    return <LoadingState label="Loading definitions..." />;
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
        <Box sx={{ display: 'flex', gap: 1 }}>
          <Button variant="outlined" startIcon={<FileUploadIcon />} onClick={() => setImportDialogOpen(true)}>
            Import
          </Button>
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/stack-definitions/new')}>
            Create Definition
          </Button>
        </Box>
      </Box>

      {definitions.length === 0 ? (
        <EmptyState
          title="No stack definitions yet"
          description="Create a definition from a template to get started."
          action={
            <Button variant="outlined" onClick={() => navigate('/templates')}>
              Browse Templates
            </Button>
          }
        />
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
                <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>Created</TableCell>
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
                            label="Update available"
                            size="small"
                            color="info"
                          />
                        )}
                        <Tooltip title="Check for template upgrades">
                          <IconButton
                            size="small"
                            onClick={(e) => {
                              e.stopPropagation();
                              setUpgradeDialogDefId(def.id);
                            }}
                            aria-label={`Check upgrades for ${def.name}`}
                          >
                            <SystemUpdateAltIcon fontSize="small" />
                          </IconButton>
                        </Tooltip>
                      </Box>
                    ) : (
                      '—'
                    )}
                  </TableCell>
                  <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>
                    {new Date(def.created_at).toLocaleDateString()}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <ImportDefinitionDialog
        open={importDialogOpen}
        onClose={() => setImportDialogOpen(false)}
        onImported={(created) => {
          showSuccess(`Definition "${created.name}" imported successfully`);
          navigate(`/stack-definitions/${created.id}/edit`);
        }}
      />

      {upgradeDialogDefId && (
        <UpgradeDialog
          definitionId={upgradeDialogDefId}
          open={Boolean(upgradeDialogDefId)}
          onClose={() => setUpgradeDialogDefId(null)}
          onUpgraded={() => fetchDefinitions()}
        />
      )}
    </Box>
  );
};

export default List;
