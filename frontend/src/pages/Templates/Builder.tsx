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
  Divider,
} from '@mui/material';
import AddIcon from '@mui/icons-material/Add';
import DeleteIcon from '@mui/icons-material/Delete';
import { templateService } from '../../api/client';
import { useNotification } from '../../context/NotificationContext';
import type { PublishTemplateResult, StackTemplate, TemplateChartConfig } from '../../types';
import YamlEditor from '../../components/YamlEditor';
import LoadingState from '../../components/LoadingState';
import { SAVED_AS_DRAFT_MESSAGE } from '../../utils/templateVersion';
import PublishDialog from './PublishDialog';

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
  const { showInfo, showSuccess } = useNotification();

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [categoryVal, setCategoryVal] = useState('');
  const [version, setVersion] = useState('');
  const [defaultBranch, setDefaultBranch] = useState('master');
  const [isPublished, setIsPublished] = useState(false);
  const [publishedVersion, setPublishedVersion] = useState<string | null>(null);
  const [publishTarget, setPublishTarget] = useState<StackTemplate | null>(null);
  /** IDs of saved charts the user removed; deleted on the server at save. */
  const [removedChartIds, setRemovedChartIds] = useState<string[]>([]);
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
        setPublishedVersion(data.published_version ?? null);
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
    const removed = charts[index];
    if (removed?.id) {
      const removedId = removed.id;
      setRemovedChartIds((prev) => (prev.includes(removedId) ? prev : [...prev, removedId]));
    }
    setCharts(charts.filter((_c, i) => i !== index));
  };

  const updateChart = (index: number, field: keyof ChartFormData, value: string | number | boolean) => {
    setCharts(charts.map((c, i) => i === index ? { ...c, [field]: value } : c));
  };

  /** Save the template details and charts. Returns the saved template, or null on failure. */
  const persist = async (): Promise<StackTemplate | null> => {
    setError(null);
    setSaving(true);
    try {
      // Publish state is not part of the working copy: use the Publish flow instead.
      const templateData: Partial<StackTemplate> = {
        name,
        description,
        category: categoryVal,
        version,
        default_branch: defaultBranch,
      };

      let savedTemplate: StackTemplate;
      if (isEdit && id) {
        savedTemplate = await templateService.update(id, templateData);
      } else {
        savedTemplate = await templateService.create(templateData);
      }

      // Delete removed charts first, so that a following publish cannot
      // release a chart the user removed.
      for (const chartId of removedChartIds) {
        await templateService.deleteChart(savedTemplate.id, chartId);
        setRemovedChartIds((prev) => prev.filter((cid) => cid !== chartId));
      }

      // Save charts
      for (const chart of charts) {
        const chartData = {
          chart_name: chart.chart_name,
          repository_url: chart.repository_url,
          source_repo_url: chart.source_repo_url,
          chart_path: chart.chart_path,
          chart_version: chart.chart_version,
          default_values: chart.default_values,
          locked_values: chart.locked_values,
          deploy_order: chart.deploy_order,
          required: chart.required,
        };
        if (chart.id) {
          await templateService.updateChart(savedTemplate.id, chart.id, chartData);
        } else {
          await templateService.addChart(savedTemplate.id, chartData);
        }
      }
      return savedTemplate;
    } catch {
      setError('Failed to save template');
      return null;
    } finally {
      setSaving(false);
    }
  };

  /** True when users already get a release of this template, so a save only changes the draft. */
  const hasRelease = isEdit && (isPublished || Boolean(publishedVersion));

  const handleSave = async () => {
    const savedTemplate = await persist();
    if (!savedTemplate) return;
    if (hasRelease) {
      showInfo(SAVED_AS_DRAFT_MESSAGE);
    }
    navigate(`/templates/${savedTemplate.id}`);
  };

  const handleSaveAndPublish = async () => {
    const savedTemplate = await persist();
    if (!savedTemplate) return;
    try {
      setPublishTarget(await templateService.get(savedTemplate.id));
    } catch {
      setPublishTarget(savedTemplate);
    }
  };

  const handlePublishClosed = () => {
    if (!publishTarget) return;
    const targetId = publishTarget.id;
    setPublishTarget(null);
    if (hasRelease) {
      showInfo(SAVED_AS_DRAFT_MESSAGE);
    }
    navigate(`/templates/${targetId}`);
  };

  const handlePublished = (result: PublishTemplateResult, requestedVersion: string) => {
    if (!publishTarget) return;
    const targetId = publishTarget.id;
    const releasedVersion = result.template.published_version || requestedVersion;
    setPublishTarget(null);
    if (result.snapshotCreated) {
      showSuccess(`Published version ${releasedVersion}.`);
    } else {
      showInfo(`No changes since version ${releasedVersion}. No new version was created.`);
    }
    navigate(`/templates/${targetId}`);
  };

  if (loading) {
    return <LoadingState label="Loading template..." />;
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
          {hasRelease && (
            <Alert severity="info">
              {publishedVersion
                ? `Users get version ${publishedVersion}. Saving changes the draft only. Publish a new version to release the changes.`
                : 'Saving changes the draft only. Publish a new version to release the changes.'}
            </Alert>
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
          <Box key={chart.chart_name || `new-chart-${index}`} sx={{ mb: 3, p: 2, border: 1, borderColor: 'divider', borderRadius: 1 }}>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
              <Typography variant="subtitle1">Chart #{index + 1}</Typography>
              <IconButton onClick={() => removeChart(index)} size="small" color="error" aria-label={`Remove chart ${index + 1}`}>
                <DeleteIcon />
              </IconButton>
            </Box>
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <Box sx={{ display: 'flex', gap: 2 }}>
                <TextField label="Chart Name" value={chart.chart_name} onChange={(e) => updateChart(index, 'chart_name', e.target.value)} fullWidth required size="small" />
                <TextField label="Deploy Order" type="number" value={chart.deploy_order} onChange={(e) => updateChart(index, 'deploy_order', Number.parseInt(e.target.value) || 0)} sx={{ width: 120 }} size="small" />
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
        <Button variant="outlined" onClick={handleSaveAndPublish} disabled={saving || !name}>
          Save and Publish
        </Button>
        <Button variant="outlined" onClick={() => navigate('/templates')}>
          Cancel
        </Button>
      </Box>

      {publishTarget && (
        <PublishDialog
          open
          template={publishTarget}
          onClose={handlePublishClosed}
          onPublished={handlePublished}
        />
      )}
    </Box>
  );
};

export default Builder;
