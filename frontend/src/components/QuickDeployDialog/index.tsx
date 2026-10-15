import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  TextField,
  Typography,
} from '@mui/material';
import RocketLaunchIcon from '@mui/icons-material/RocketLaunch';
import { templateService, clusterService } from '../../api/client';
import TtlSelector from '../TtlSelector';
import { useBranding } from '../../context/BrandingContext';
import type { Cluster, StackTemplate } from '../../types';
import { trackRecentTemplate } from '../../utils/recentTemplates';
import { isNoPublishedVersionError, NO_PUBLISHED_VERSION_MESSAGE } from '../../utils/templateVersion';
import { INSTANCE_NAME_RULE, validateInstanceName } from '../../utils/instanceName';
import { describeNameConflict } from '../../utils/apiError';

interface QuickDeployDialogProps {
  open: boolean;
  onClose: () => void;
  template: StackTemplate | null;
}

const QuickDeployDialog = ({ open, onClose, template }: QuickDeployDialogProps) => {
  const navigate = useNavigate();
  const [instanceName, setInstanceName] = useState('');
  const [description, setDescription] = useState('');
  const [branch, setBranch] = useState('');
  // null: the user has not changed the TTL field. The field shows the server
  // default and the request leaves out ttl_minutes, so the server applies its
  // own default (DEFAULT_INSTANCE_TTL_MINUTES).
  const { defaultTtlMinutes } = useBranding();
  const [ttlChoice, setTtlChoice] = useState<number | null>(null);
  const ttlMinutes = ttlChoice ?? defaultTtlMinutes;
  const [clusterId, setClusterId] = useState('');
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [deploying, setDeploying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setInstanceName('');
      setDescription('');
      setBranch(template?.default_branch || 'master');
      setTtlChoice(null);
      setClusterId('');
      setError(null);
      setNameError(null);
      setDeploying(false);

      clusterService.list().then((data) => {
        setClusters(data || []);
        if (data?.length === 1) {
          setClusterId(data[0].id);
        }
      }).catch(() => {
        setClusters([]);
      });
    }
  }, [open, template]);

  const handleDeploy = async () => {
    if (!template) return;
    const invalid = validateInstanceName(instanceName.trim());
    if (invalid) {
      setNameError(invalid);
      return;
    }
    setNameError(null);
    setError(null);
    setDeploying(true);

    try {
      const result = await templateService.quickDeploy(template.id, {
        instance_name: instanceName.trim(),
        instance_description: description.trim() || undefined,
        branch: branch.trim() || undefined,
        cluster_id: clusterId || undefined,
        ...(ttlChoice !== null ? { ttl_minutes: ttlChoice } : {}),
      });
      // Track in recently used templates
      trackRecentTemplate({ id: template.id, name: template.name });
      onClose();
      navigate(`/stack-instances/${result.instance.id}`);
    } catch (err: unknown) {
      const message = isNoPublishedVersionError(err)
        ? NO_PUBLISHED_VERSION_MESSAGE
        : describeNameConflict(err) ||
          (err as { response?: { data?: { error?: string } } })?.response?.data?.error ||
          'Failed to deploy. Please try again.';
      setError(message);
    } finally {
      setDeploying(false);
    }
  };

  const deployButtonLabel = deploying ? 'Deploying...' : (error ? 'Retry' : 'Deploy');

  return (
    <Dialog open={open} onClose={deploying ? undefined : onClose} maxWidth="sm" fullWidth>
      <DialogTitle>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <RocketLaunchIcon color="primary" />
          <Typography variant="h6" component="span">
            Quick Deploy: {template?.name}
          </Typography>
        </Box>
      </DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, mt: 1 }}>
          {error && <Alert severity="error">{error}</Alert>}
          <TextField
            label="Instance Name"
            value={instanceName}
            onChange={(e) => {
              const value = e.target.value;
              setInstanceName(value);
              // Validate live once the user types; an empty field is reported on submit.
              setNameError(value.trim() ? validateInstanceName(value.trim()) : null);
            }}
            required
            error={!!nameError}
            helperText={nameError ?? INSTANCE_NAME_RULE}
            size="small"
            autoFocus
          />
          <TextField
            label="Description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            size="small"
            multiline
            rows={2}
          />
          <TextField
            label="Branch"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            size="small"
            helperText="Git branch to deploy"
          />
          <TtlSelector value={ttlMinutes} onChange={setTtlChoice} disabled={deploying} />
          {clusters.length > 1 && (
            <TextField
              select
              label="Cluster"
              value={clusterId}
              onChange={(e) => setClusterId(e.target.value)}
              size="small"
            >
              <MenuItem value="">Auto (default cluster)</MenuItem>
              {clusters.map((c) => (
                <MenuItem key={c.id} value={c.id}>
                  {c.name}{c.is_default ? ' (default)' : ''}
                </MenuItem>
              ))}
            </TextField>
          )}
        </Box>
      </DialogContent>
      <DialogActions sx={{ px: 3, pb: 2 }}>
        <Button onClick={onClose} disabled={deploying}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={handleDeploy}
          disabled={deploying}
          startIcon={deploying ? <CircularProgress size={18} /> : <RocketLaunchIcon />}
        >
          {deployButtonLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default QuickDeployDialog;
