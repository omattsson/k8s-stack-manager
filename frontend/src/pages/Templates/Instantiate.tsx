import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Typography,
  TextField,
  Button,
  Paper,
  CircularProgress,
  Alert,
  Chip,
  Switch,
  FormControlLabel,
  Divider,
} from '@mui/material';
import { templateService } from '../../api/client';
import type { StackTemplate, TemplateChartConfig } from '../../types';
import YamlEditor from '../../components/YamlEditor';

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

  useEffect(() => {
    if (!id) return;
    const fetchTemplate = async () => {
      try {
        const data = await templateService.get(id);
        setTemplate(data);
        setDefName(`${data.name} - My Stack`);
        setDefDescription(data.description);
        if (data.charts) {
          setChartOverrides(
            data.charts.map((chart) => ({
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
            overridesMap[co.chart.id] = co.values;
          }
        });

      const definition = await templateService.instantiate(id, {
        name: defName,
        description: defDescription,
        chart_overrides: Object.keys(overridesMap).length > 0 ? overridesMap : undefined,
      });
      navigate(`/stack-definitions/${definition.id}/edit`);
    } catch {
      setError('Failed to instantiate template');
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

  if (error && !template) {
    return <Alert severity="error">{error}</Alert>;
  }

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        Use Template: {template?.name}
      </Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>Stack Definition Details</Typography>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <TextField
            label="Definition Name"
            value={defName}
            onChange={(e) => setDefName(e.target.value)}
            required
            fullWidth
          />
          <TextField
            label="Description"
            value={defDescription}
            onChange={(e) => setDefDescription(e.target.value)}
            fullWidth
            multiline
            rows={2}
          />
        </Box>
      </Paper>

      <Typography variant="h5" gutterBottom>
        Charts
      </Typography>

      {chartOverrides.map((co, index) => (
        <Paper key={co.chart.id} sx={{ p: 3, mb: 2, opacity: co.enabled ? 1 : 0.5 }}>
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

      <Box sx={{ display: 'flex', gap: 2, mt: 2 }}>
        <Button variant="contained" onClick={handleInstantiate} disabled={saving || !defName}>
          {saving ? 'Creating...' : 'Create Stack Definition'}
        </Button>
        <Button variant="outlined" onClick={() => navigate(`/templates/${id}`)}>
          Cancel
        </Button>
      </Box>
    </Box>
  );
};

export default Instantiate;
