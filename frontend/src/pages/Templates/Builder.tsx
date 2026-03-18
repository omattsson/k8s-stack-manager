import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Typography,
  TextField,
  Button,
  Paper,
  MenuItem,
  IconButton,
  Switch,
  FormControlLabel,
  Alert,
  CircularProgress,
  Divider,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import DeleteIcon from '@mui/icons-material/Delete';
import { templateService } from '../../api/client';
import type { StackTemplate, TemplateChartConfig } from '../../types';
import YamlEditor from '../../components/YamlEditor';

const CATEGORIES = ['Web', 'API', 'Data', 'Infrastructure', 'Other'];

interface ChartFormData {
  id?: string;
  chart_name: string;
  repository_url: string;
  source_repo_url: string;
  chart_path: string;
  chart_version: string;
  default_values: string;
  locked_values: string;
  deploy_order: number;
  required: boolean;
}

const emptyChart = (): ChartFormData => ({
  chart_name: '',
  repository_url: '',
  source_repo_url: '',
  chart_path: '',
  chart_version: '',
  default_values: '',
  locked_values: '',
  deploy_order: 0,
  required: false,
});

const Builder = () => {
  const { id } = useParams<{ id: string }>();
  const isEdit = Boolean(id);
  const navigate = useNavigate();

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [categoryVal, setCategoryVal] = useState('');
  const [version, setVersion] = useState('');
  const [defaultBranch, setDefaultBranch] = useState('master');
  const [isPublished, setIsPublished] = useState(false);
  const [charts, setCharts] = useState<ChartFormData[]>([]);
  const [loading, setLoading] = useState(isEdit);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    const fetchTemplate = async () => {
      try {
        const data = await templateService.get(id);
        setName(data.name);
        setDescription(data.description);
        setCategoryVal(data.category);
        setVersion(data.version);
        setDefaultBranch(data.default_branch);
        setIsPublished(data.is_published);
        if (data.charts) {
          setCharts(data.charts.map((c: TemplateChartConfig) => ({
            id: c.id,
            chart_name: c.chart_name,
            repository_url: c.repository_url,
            source_repo_url: c.source_repo_url,
            chart_path: c.chart_path,
            chart_version: c.chart_version,
            default_values: c.default_values,
            locked_values: c.locked_values,
            deploy_order: c.deploy_order,
            required: c.required,
          })));
        }
      } catch {
        setError('Failed to load template');
      } finally {
        setLoading(false);
      }
    };
    fetchTemplate();
  }, [id]);

  const addChart = () => {
    setCharts([...charts, emptyChart()]);
  };

  const removeChart = (index: number) => {
    setCharts(charts.filter((_c, i) => i !== index));
  };

  const updateChart = (index: number, field: keyof ChartFormData, value: string | number | boolean) => {
    setCharts(charts.map((c, i) => i === index ? { ...c, [field]: value } : c));
  };

  const handleSave = async () => {
    setError(null);
    setSaving(true);
    try {
      const templateData: Partial<StackTemplate> = {
        name,
        description,
        category: categoryVal,
        version,
        default_branch: defaultBranch,
        is_published: isPublished,
      };

      let savedTemplate: StackTemplate;
      if (isEdit && id) {
        savedTemplate = await templateService.update(id, templateData);
      } else {
        savedTemplate = await templateService.create(templateData);
      }

      // Save charts
      for (const chart of charts) {
        if (chart.id) {
          await templateService.updateChart(savedTemplate.id, chart.id, {
            chart_name: chart.chart_name,
            repository_url: chart.repository_url,
            source_repo_url: chart.source_repo_url,
            chart_path: chart.chart_path,
            chart_version: chart.chart_version,
            default_values: chart.default_values,
            locked_values: chart.locked_values,
            deploy_order: chart.deploy_order,
            required: chart.required,
          });
        } else {
          await templateService.addChart(savedTemplate.id, {
            chart_name: chart.chart_name,
            repository_url: chart.repository_url,
            source_repo_url: chart.source_repo_url,
            chart_path: chart.chart_path,
            chart_version: chart.chart_version,
            default_values: chart.default_values,
            locked_values: chart.locked_values,
            deploy_order: chart.deploy_order,
            required: chart.required,
          });
        }
      }

      navigate(`/templates/${savedTemplate.id}`);
    } catch {
      setError('Failed to save template');
    } finally {
      setSaving(false);
    }
  };

  const handleTogglePublish = async () => {
    if (!id) return;
    try {
      if (isPublished) {
        await templateService.unpublish(id);
        setIsPublished(false);
      } else {
        await templateService.publish(id);
        setIsPublished(true);
      }
    } catch {
      setError('Failed to update publish status');
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
        {isEdit ? 'Edit Template' : 'Create Template'}
      </Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>Template Details</Typography>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} required fullWidth />
          <TextField label="Description" value={description} onChange={(e) => setDescription(e.target.value)} fullWidth multiline rows={2} />
          <Box sx={{ display: 'flex', gap: 2 }}>
            <TextField
              label="Category"
              value={categoryVal}
              onChange={(e) => setCategoryVal(e.target.value)}
              select
              sx={{ minWidth: 200 }}
            >
              {CATEGORIES.map((c) => (
                <MenuItem key={c} value={c}>{c}</MenuItem>
              ))}
            </TextField>
            <TextField label="Version" value={version} onChange={(e) => setVersion(e.target.value)} sx={{ minWidth: 150 }} />
            <TextField label="Default Branch" value={defaultBranch} onChange={(e) => setDefaultBranch(e.target.value)} sx={{ minWidth: 150 }} />
          </Box>
          {isEdit && (
            <Box sx={{ display: 'flex', gap: 2, alignItems: 'center' }}>
              <FormControlLabel
                control={<Switch checked={isPublished} onChange={handleTogglePublish} />}
                label={isPublished ? 'Published' : 'Draft'}
              />
            </Box>
          )}
        </Box>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
          <Typography variant="h6">Charts</Typography>
          <Button startIcon={<AddIcon />} onClick={addChart}>Add Chart</Button>
        </Box>

        {charts.length === 0 && (
          <Typography color="text.secondary">No charts added yet. Click "Add Chart" to get started.</Typography>
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
              <FormControlLabel
                control={<Switch checked={chart.required} onChange={(e) => updateChart(index, 'required', e.target.checked)} />}
                label="Required"
              />
              <Divider />
              <YamlEditor
                label="Default Values (YAML)"
                value={chart.default_values}
                onChange={(val) => updateChart(index, 'default_values', val)}
                height="200px"
              />
              <YamlEditor
                label="Locked Values (YAML)"
                value={chart.locked_values}
                onChange={(val) => updateChart(index, 'locked_values', val)}
                height="150px"
              />
            </Box>
          </Box>
        ))}
      </Paper>

      <Box sx={{ display: 'flex', gap: 2 }}>
        <Button variant="contained" onClick={handleSave} disabled={saving || !name}>
          {saving ? 'Saving...' : 'Save Template'}
        </Button>
        <Button variant="outlined" onClick={() => navigate('/templates')}>
          Cancel
        </Button>
      </Box>
    </Box>
  );
};

export default Builder;
