import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Typography,
  TextField,
  Button,
  Paper,
  MenuItem,
  CircularProgress,
  Alert,
  Chip,
} from '@mui/material';
import axios from 'axios';
import { instanceService, definitionService, clusterService } from '../../api/client';
import type { StackDefinition, Cluster } from '../../types';

interface ConflictResponse {
  error: string;
  message: string;
  suggestions: string[];
}

const Form = () => {
  const navigate = useNavigate();

  const [definitions, setDefinitions] = useState<StackDefinition[]>([]);
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [selectedDefId, setSelectedDefId] = useState('');
  const [selectedClusterId, setSelectedClusterId] = useState('');
  const [name, setName] = useState('');
  const [branch, setBranch] = useState('master');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [suggestions, setSuggestions] = useState<string[]>([]);

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [defs, cls] = await Promise.all([definitionService.list(), clusterService.list()]);
        setDefinitions(defs || []);
        setClusters(cls || []);
        const defaultCluster = (cls || []).find((c) => c.is_default);
        if (defaultCluster) {
          setSelectedClusterId(defaultCluster.id);
        }
      } catch {
        setError('Failed to load definitions');
      } finally {
        setLoading(false);
      }
    };
    fetchData();
  }, []);

  useEffect(() => {
    const def = definitions.find((d) => d.id === selectedDefId);
    if (def) {
      setBranch(def.default_branch || 'master');
    }
  }, [selectedDefId, definitions]);

  const handleCreate = async () => {
    setError(null);
    setSuggestions([]);
    setSaving(true);
    try {
      const instance = await instanceService.create({
        stack_definition_id: selectedDefId,
        name,
        branch,
        ...(selectedClusterId ? { cluster_id: selectedClusterId } : {}),
      });
      navigate(`/stack-instances/${instance.id}`);
    } catch (err) {
      if (axios.isAxiosError(err) && err.response?.status === 409) {
        const data = err.response.data as ConflictResponse;
        setError(data.message || data.error || 'Name already taken');
        if (data.suggestions && data.suggestions.length > 0) {
          setSuggestions(data.suggestions);
        }
      } else {
        setError('Failed to create instance');
      }
    } finally {
      setSaving(false);
    }
  };

  const handleSuggestionClick = (suggestion: string) => {
    setName(suggestion);
    setSuggestions([]);
    setError(null);
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
        Create Stack Instance
      </Typography>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
          {suggestions.length > 0 && (
            <Box sx={{ mt: 1, display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
              <Typography variant="body2">Try:</Typography>
              {suggestions.map((s) => (
                <Chip
                  key={s}
                  label={s}
                  size="small"
                  onClick={() => handleSuggestionClick(s)}
                  clickable
                  color="primary"
                  variant="outlined"
                />
              ))}
            </Box>
          )}
        </Alert>
      )}

      <Paper sx={{ p: 3 }}>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
          <TextField
            label="Stack Definition"
            value={selectedDefId}
            onChange={(e) => setSelectedDefId(e.target.value)}
            select
            required
            fullWidth
          >
            {definitions.length === 0 ? (
              <MenuItem disabled value="">No definitions available</MenuItem>
            ) : (
              definitions.map((def) => (
                <MenuItem key={def.id} value={def.id}>
                  {def.name} {def.description && `— ${def.description}`}
                </MenuItem>
              ))
            )}
          </TextField>

          <TextField
            label="Instance Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            fullWidth
            helperText={`${name.length}/50 characters — namespace will be auto-generated from your name and owner`}
            error={name.length > 50}
            slotProps={{ htmlInput: { maxLength: 50 } }}
          />

          <TextField
            label="Branch"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            fullWidth
          />

          {clusters.length > 0 && (
            <TextField
              label="Cluster"
              value={selectedClusterId}
              onChange={(e) => setSelectedClusterId(e.target.value)}
              select
              fullWidth
              helperText="Leave empty to use the default cluster"
            >
              <MenuItem value="">
                <em>Default cluster</em>
              </MenuItem>
              {clusters.map((c) => (
                <MenuItem key={c.id} value={c.id}>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                    {c.name}
                    {c.region && (
                      <Chip label={c.region} size="small" variant="outlined" />
                    )}
                    <Chip
                      label={c.health_status}
                      size="small"
                      color={c.health_status === 'healthy' ? 'success' : c.health_status === 'degraded' ? 'warning' : 'error'}
                    />
                    {c.is_default && (
                      <Chip label="Default" size="small" variant="outlined" color="primary" />
                    )}
                  </Box>
                </MenuItem>
              ))}
            </TextField>
          )}
        </Box>

        <Box sx={{ display: 'flex', gap: 2, mt: 3 }}>
          <Button
            variant="contained"
            onClick={handleCreate}
            disabled={saving || !name || !selectedDefId || name.length > 50}
          >
            {saving ? 'Creating...' : 'Create Instance'}
          </Button>
          <Button variant="outlined" onClick={() => navigate('/')}>
            Cancel
          </Button>
        </Box>
      </Paper>
    </Box>
  );
};

export default Form;
