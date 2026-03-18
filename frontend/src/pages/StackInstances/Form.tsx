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
} from '@mui/material';
import { instanceService, definitionService } from '../../api/client';
import type { StackDefinition } from '../../types';

const Form = () => {
  const navigate = useNavigate();

  const [definitions, setDefinitions] = useState<StackDefinition[]>([]);
  const [selectedDefId, setSelectedDefId] = useState('');
  const [name, setName] = useState('');
  const [branch, setBranch] = useState('master');
  const [namespace, setNamespace] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const fetchDefinitions = async () => {
      try {
        const data = await definitionService.list();
        setDefinitions(data || []);
      } catch {
        setError('Failed to load definitions');
      } finally {
        setLoading(false);
      }
    };
    fetchDefinitions();
  }, []);

  useEffect(() => {
    if (name) {
      const sanitized = name.toLowerCase().replace(/[^a-z0-9-]/g, '-');
      setNamespace(`stack-${sanitized}`);
    } else {
      setNamespace('');
    }
  }, [name]);

  useEffect(() => {
    const def = definitions.find((d) => d.id === selectedDefId);
    if (def) {
      setBranch(def.default_branch || 'master');
    }
  }, [selectedDefId, definitions]);

  const handleCreate = async () => {
    setError(null);
    setSaving(true);
    try {
      const instance = await instanceService.create({
        stack_definition_id: selectedDefId,
        name,
        branch,
      });
      navigate(`/stack-instances/${instance.id}`);
    } catch {
      setError('Failed to create instance');
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
        Create Stack Instance
      </Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

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
          />

          <TextField
            label="Branch"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            fullWidth
          />

          <TextField
            label="Namespace (auto-generated)"
            value={namespace}
            fullWidth
            slotProps={{ input: { readOnly: true } }}
            helperText="Namespace is auto-generated from the instance name"
          />
        </Box>

        <Box sx={{ display: 'flex', gap: 2, mt: 3 }}>
          <Button
            variant="contained"
            onClick={handleCreate}
            disabled={saving || !name || !selectedDefId}
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
