import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Typography,
  TextField,
  Button,
  Paper,
  Alert,
  Chip,
  Switch,
  FormControlLabel,
  Divider,
} from '@mui/material';
import { templateService } from '../../api/client';
import type { StackTemplate, TemplateChartConfig } from '../../types';
import YamlEditor from '../../components/YamlEditor';
import LoadingState from '../../components/LoadingState';
import { trackRecentTemplate } from '../../utils/recentTemplates';
import {
  isNoPublishedVersionError,
  NO_PUBLISHED_VERSION_MESSAGE,
  UNPUBLISHED_TEMPLATE_MESSAGE,
} from '../../utils/templateVersion';

interface ChartOverride {
  chart: TemplateChartConfig;
  values: string;
  enabled: boolean;
}

const Instantiate = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const [template, setTemplate] = useState<StackTemplate | null>(null);
  const [defName, setDefName] = useState('');
  const [defDescription, setDefDescription] = useState('');
  const [chartOverrides, setChartOverrides] = useState<ChartOverride[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [touched, setTouched] = useState<Record<string, boolean>>({});

  useEffect(() => {
    if (!id) return;
    const fetchTemplate = async () => {
      try {
        const data = await templateService.get(id);
        setTemplate(data);
        setDefName(`${data.name} - My Stack`);
        setDefDescription(data.description);
        // Show what users get: the release charts. Fall back to the working
        // copy only when there is no release (the API then rejects the submit).
        const charts = Array.isArray(data.published_charts) ? data.published_charts : data.charts;
        if (charts) {
          setChartOverrides(
            charts.map((chart) => ({
              chart,
              values: chart.default_values,
              enabled: chart.required || true,
            }))
          );
        }
      } catch {
        setError('Failed to load template');
      } finally {
        setLoading(false);
      }
    };
    fetchTemplate();
  }, [id]);

  const updateOverride = (index: number, values: string) => {
    setChartOverrides((prev) =>
      prev.map((co, i) => (i === index ? { ...co, values } : co))
    );
  };

  const toggleChart = (index: number) => {
    setChartOverrides((prev) =>
      prev.map((co, i) => {
        if (i !== index) return co;
        if (co.chart.required) return co;
        return { ...co, enabled: !co.enabled };
      })
    );
  };

  const handleInstantiate = async () => {
    if (!id) return;
    setError(null);
    setSaving(true);
    try {
      const overridesMap: Record<string, string> = {};
      chartOverrides
        .filter((co) => co.enabled)
        .forEach((co) => {
          if (co.values !== co.chart.default_values) {
            // Key by chart name: the API accepts names, and legacy release
            // charts can lack IDs.
            overridesMap[co.chart.chart_name] = co.values;
          }
        });

      const definition = await templateService.instantiate(id, {
        name: defName,
        description: defDescription,
        chart_overrides: Object.keys(overridesMap).length > 0 ? overridesMap : undefined,
      });
      // Track in recently used templates
      if (template) {
        trackRecentTemplate({ id: template.id, name: template.name });
      }
      navigate(`/stack-definitions/${definition.id}/edit`);
    } catch (err) {
      setError(isNoPublishedVersionError(err) ? NO_PUBLISHED_VERSION_MESSAGE : 'Failed to instantiate template');
    } finally {
      setSaving(false);
    }
  };

  /** True when the API reports that the template has no release. */
  const noRelease = Boolean(
    template
    && (template.is_published === false || template.published_charts === null || template.published_version === null),
  );

  if (loading) {
    return <LoadingState label="Loading template..." />;
  }

  if (error && !template) {
    return <Alert severity="error">{error}</Alert>;
  }

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        Use Template: {template?.name}
      </Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {noRelease && !error && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          {template?.is_published === false && template.published_version
            ? UNPUBLISHED_TEMPLATE_MESSAGE
            : NO_PUBLISHED_VERSION_MESSAGE}
        </Alert>
      )}
      {template?.published_version && (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Creates a definition from version {template.published_version}.
        </Typography>
      )}

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>Stack Definition Details</Typography>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <TextField
            label="Definition Name"
            value={defName}
            onChange={(e) => setDefName(e.target.value)}
            onBlur={() => setTouched((prev) => ({ ...prev, defName: true }))}
            required
            fullWidth
            error={touched.defName === true && !defName.trim()}
            helperText={touched.defName === true && !defName.trim() ? 'Definition name is required' : undefined}
          />
          <TextField
            label="Description"
            value={defDescription}
            onChange={(e) => setDefDescription(e.target.value)}
            fullWidth
            multiline
            rows={2}
            helperText={`${defDescription.length}/200`}
            error={defDescription.length > 200}
            slotProps={{ htmlInput: { maxLength: 200 } }}
          />
        </Box>
      </Paper>

      <Typography variant="h5" gutterBottom>
        Charts
      </Typography>

      {chartOverrides.map((co, index) => (
        <Paper key={co.chart.id || co.chart.chart_name} sx={{ p: 3, mb: 2, opacity: co.enabled ? 1 : 0.5 }}>
          <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
              <Typography variant="h6">{co.chart.chart_name}</Typography>
              {co.chart.required && <Chip label="Required" color="primary" size="small" />}
            </Box>
            {!co.chart.required && (
              <FormControlLabel
                control={<Switch checked={co.enabled} onChange={() => toggleChart(index)} />}
                label="Include"
              />
            )}
          </Box>

          {co.enabled && (
            <>
              <YamlEditor
                label="Values (YAML)"
                value={co.values}
                onChange={(val) => updateOverride(index, val)}
                height="250px"
              />

              {co.chart.locked_values && (
                <>
                  <Divider sx={{ my: 2 }} />
                  <YamlEditor
                    label="Locked Values"
                    value={co.chart.locked_values}
                    onChange={() => {}}
                    readOnly
                    height="150px"
                  />
                </>
              )}
            </>
          )}
        </Paper>
      ))}

      <Box sx={{ display: 'flex', gap: 2, mt: 2, justifyContent: 'flex-end' }}>
        <Button variant="outlined" onClick={() => navigate(`/templates/${id}`)}>
          Cancel
        </Button>
        <Button variant="contained" onClick={handleInstantiate} disabled={saving || !defName.trim() || noRelease}>
          {saving ? 'Creating...' : 'Create Stack Definition'}
        </Button>
      </Box>
    </Box>
  );
};

export default Instantiate;
