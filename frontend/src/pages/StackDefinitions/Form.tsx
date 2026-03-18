import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Typography,
  TextField,
  Button,
  Paper,
  IconButton,
  Alert,
  CircularProgress,
  Chip,
  Divider,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import DeleteIcon from '@mui/icons-material/Delete';
import { definitionService } from '../../api/client';
import type { StackDefinition, ChartConfig } from '../../types';

interface ChartFormData {
  id?: string;
  chart_name: string;
  repository_url: string;
  source_repo_url: string;
  chart_path: string;
  chart_version: string;
  default_values: string;
  deploy_order: number;
}

const emptyChart = (): ChartFormData => ({
  chart_name: '',
  repository_url: '',
  source_repo_url: '',
  chart_path: '',
  chart_version: '',
  default_values: '',
  deploy_order: 0,
});

const Form = () => {
  const { id } = useParams<{ id: string }>();
  const isEdit = Boolean(id);
  const navigate = useNavigate();

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [defaultBranch, setDefaultBranch] = useState('master');
  const [sourceTemplateId, setSourceTemplateId] = useState<string | undefined>();
  const [sourceTemplateVersion, setSourceTemplateVersion] = useState<string | undefined>();
  const [charts, setCharts] = useState<ChartFormData[]>([]);
  const [loading, setLoading] = useState(isEdit);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    const fetchDefinition = async () => {
      try {
        const data = await definitionService.get(id);
        setName(data.name);
        setDescription(data.description);
        setDefaultBranch(data.default_branch);
        setSourceTemplateId(data.source_template_id);
        setSourceTemplateVersion(data.source_template_version);
        if (data.charts) {
          setCharts(data.charts.map((c: ChartConfig) => ({
            id: c.id,
            chart_name: c.chart_name,
            repository_url: c.repository_url,
            source_repo_url: c.source_repo_url,
            chart_path: c.chart_path,
            chart_version: c.chart_version,
            default_values: c.default_values,
            deploy_order: c.deploy_order,
          })));
        }
      } catch {
        setError('Failed to load definition');
      } finally {
        setLoading(false);
      }
    };
    fetchDefinition();
  }, [id]);

  const addChart = () => {
    setCharts([...charts, emptyChart()]);
  };

  const removeChart = (index: number) => {
    setCharts(charts.filter((_c, i) => i !== index));
  };

  const updateChart = (index: number, field: keyof ChartFormData, value: string | number) => {
    setCharts(charts.map((c, i) => (i === index ? { ...c, [field]: value } : c)));
  };

  const handleSave = async () => {
    setError(null);
    setSaving(true);
    try {
      const defData: Partial<StackDefinition> = {
        name,
        description,
        default_branch: defaultBranch,
      };

      let saved: StackDefinition;
      if (isEdit && id) {
        saved = await definitionService.update(id, defData);
      } else {
        saved = await definitionService.create(defData);
      }

      for (const chart of charts) {
        if (chart.id) {
          await definitionService.updateChart(saved.id, chart.id, {
            chart_name: chart.chart_name,
            repository_url: chart.repository_url,
            source_repo_url: chart.source_repo_url,
            chart_path: chart.chart_path,
            chart_version: chart.chart_version,
            default_values: chart.default_values,
            deploy_order: chart.deploy_order,
          });
        } else {
          await definitionService.addChart(saved.id, {
            chart_name: chart.chart_name,
            repository_url: chart.repository_url,
            source_repo_url: chart.source_repo_url,
            chart_path: chart.chart_path,
            chart_version: chart.chart_version,
            default_values: chart.default_values,
            deploy_order: chart.deploy_order,
          });
        }
      }

      navigate('/stack-definitions');
    } catch {
      setError('Failed to save definition');
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
        <CircularProgress />
      </Box>
    );
  }

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        {isEdit ? 'Edit Stack Definition' : 'Create Stack Definition'}
      </Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {sourceTemplateId && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Derived from template
          {sourceTemplateVersion && ` (v${sourceTemplateVersion})`}.{' '}
          <Chip
            label="View Template"
            size="small"
            onClick={() => navigate(`/templates/${sourceTemplateId}`)}
            sx={{ cursor: 'pointer' }}
          />
        </Alert>
      )}

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>Definition Details</Typography>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} required fullWidth />
          <TextField label="Description" value={description} onChange={(e) => setDescription(e.target.value)} fullWidth multiline rows={2} />
          <TextField label="Default Branch" value={defaultBranch} onChange={(e) => setDefaultBranch(e.target.value)} sx={{ maxWidth: 300 }} />
        </Box>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
          <Typography variant="h6">Charts</Typography>
          <Button startIcon={<AddIcon />} onClick={addChart}>Add Chart</Button>
        </Box>

        {charts.length === 0 && (
          <Typography color="text.secondary">No charts added yet.</Typography>
        )}

        {charts.map((chart, index) => (
          <Box key={index} sx={{ mb: 3, p: 2, border: 1, borderColor: 'divider', borderRadius: 1 }}>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
              <Typography variant="subtitle1">Chart #{index + 1}</Typography>
              <IconButton onClick={() => removeChart(index)} size="small" color="error">
                <DeleteIcon />
              </IconButton>
            </Box>
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <Box sx={{ display: 'flex', gap: 2 }}>
                <TextField label="Chart Name" value={chart.chart_name} onChange={(e) => updateChart(index, 'chart_name', e.target.value)} fullWidth required size="small" />
                <TextField label="Deploy Order" type="number" value={chart.deploy_order} onChange={(e) => updateChart(index, 'deploy_order', parseInt(e.target.value) || 0)} sx={{ width: 120 }} size="small" />
              </Box>
              <TextField label="Repository URL" value={chart.repository_url} onChange={(e) => updateChart(index, 'repository_url', e.target.value)} fullWidth size="small" />
              <TextField label="Source Repo URL" value={chart.source_repo_url} onChange={(e) => updateChart(index, 'source_repo_url', e.target.value)} fullWidth size="small" />
              <Box sx={{ display: 'flex', gap: 2 }}>
                <TextField label="Chart Path" value={chart.chart_path} onChange={(e) => updateChart(index, 'chart_path', e.target.value)} fullWidth size="small" />
                <TextField label="Chart Version" value={chart.chart_version} onChange={(e) => updateChart(index, 'chart_version', e.target.value)} sx={{ width: 150 }} size="small" />
              </Box>
              <Divider />
              <TextField
                label="Default Values (YAML)"
                value={chart.default_values}
                onChange={(e) => updateChart(index, 'default_values', e.target.value)}
                multiline
                rows={6}
                fullWidth
                size="small"
                slotProps={{ input: { sx: { fontFamily: 'monospace', fontSize: 13 } } }}
              />
            </Box>
          </Box>
        ))}
      </Paper>

      <Box sx={{ display: 'flex', gap: 2 }}>
        <Button variant="contained" onClick={handleSave} disabled={saving || !name}>
          {saving ? 'Saving...' : 'Save Definition'}
        </Button>
        <Button variant="outlined" onClick={() => navigate('/stack-definitions')}>
          Cancel
        </Button>
      </Box>
    </Box>
  );
};

export default Form;
