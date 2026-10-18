import { useEffect, useState } from 'react';
import {
  Box,
  Typography,
  Button,
  Alert,
  CircularProgress,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Chip,
  List,
  ListItem,
  ListItemIcon,
  ListItemText,
} from '@mui/material';
import AddCircleOutlinedIcon from '@mui/icons-material/AddCircleOutlined';
import RemoveCircleOutlinedIcon from '@mui/icons-material/RemoveCircleOutlined';
import EditIcon from '@mui/icons-material/Edit';
import CheckCircleOutlinedIcon from '@mui/icons-material/CheckCircleOutlined';
import ArrowForwardIcon from '@mui/icons-material/ArrowForward';
import { definitionService } from '../../api/client';
import { useNotification } from '../../context/NotificationContext';
import ChartDiffList from '../../components/ChartDiffList';
import { isNoPublishedVersionError, NO_PUBLISHED_VERSION_MESSAGE } from '../../utils/templateVersion';
import type { UpgradeCheckResponse } from '../../types';

interface UpgradeDialogProps {
  definitionId: string;
  open: boolean;
  onClose: () => void;
  onUpgraded?: () => void;
}

const UpgradeDialog = ({ definitionId, open, onClose, onUpgraded }: UpgradeDialogProps) => {
  const [checkResult, setCheckResult] = useState<UpgradeCheckResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [upgrading, setUpgrading] = useState(false);
  const { showSuccess, showError } = useNotification();

  useEffect(() => {
    if (!open) {
      setCheckResult(null);
      setError(null);
      return;
    }

    const checkForUpgrade = async () => {
      setLoading(true);
      setError(null);
      try {
        const result = await definitionService.checkUpgrade(definitionId);
        setCheckResult(result);
      } catch {
        setError('Failed to check for upgrades');
      } finally {
        setLoading(false);
      }
    };
    checkForUpgrade();
  }, [open, definitionId]);

  const valueDiffs = checkResult?.chart_diffs ?? [];
  const hasValueChanges = valueDiffs.some((d) => d.has_differences);

  const handleUpgrade = async () => {
    setUpgrading(true);
    try {
      await definitionService.applyUpgrade(definitionId);
      showSuccess('Definition upgraded successfully');
      onUpgraded?.();
      onClose();
    } catch (err) {
      showError(isNoPublishedVersionError(err) ? NO_PUBLISHED_VERSION_MESSAGE : 'Failed to apply upgrade');
    } finally {
      setUpgrading(false);
    }
  };

  const changes = checkResult?.changes;

  return (
    <Dialog open={open} onClose={onClose} maxWidth={hasValueChanges ? 'lg' : 'sm'} fullWidth>
      <DialogTitle>Template Upgrade</DialogTitle>
      <DialogContent>
        {loading && (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress role="progressbar" />
          </Box>
        )}

        {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

        {checkResult && !checkResult.upgrade_available && (
          <Alert severity="success">
            You are on the latest version. No upgrade available.
          </Alert>
        )}

        {checkResult?.upgrade_available && (
          <Box>
            <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 2, mb: 3, py: 2 }}>
              <Chip label={`v${checkResult.current_version}`} variant="outlined" />
              <ArrowForwardIcon color="primary" />
              <Chip label={`v${checkResult.latest_version}`} color="primary" />
            </Box>

            {changes && (
              <Box>
                <Typography variant="subtitle2" gutterBottom>
                  Changes Summary
                </Typography>

                <List dense disablePadding>
                  {changes.charts_added.map((name) => (
                    <ListItem key={`added-${name}`}>
                      <ListItemIcon sx={{ minWidth: 36 }}>
                        <AddCircleOutlinedIcon color="success" fontSize="small" />
                      </ListItemIcon>
                      <ListItemText
                        primary={name}
                        secondary="New chart will be added"
                      />
                      <Chip label="Added" size="small" color="success" />
                    </ListItem>
                  ))}

                  {changes.charts_removed.map((name) => (
                    <ListItem key={`removed-${name}`}>
                      <ListItemIcon sx={{ minWidth: 36 }}>
                        <RemoveCircleOutlinedIcon color="warning" fontSize="small" />
                      </ListItemIcon>
                      <ListItemText
                        primary={name}
                        secondary="Not in the new template version. The upgrade keeps this chart."
                      />
                      <Chip label="Not in template" size="small" color="warning" variant="outlined" />
                    </ListItem>
                  ))}

                  {changes.charts_modified.map((name) => (
                    <ListItem key={`modified-${name}`}>
                      <ListItemIcon sx={{ minWidth: 36 }}>
                        <EditIcon color="info" fontSize="small" />
                      </ListItemIcon>
                      <ListItemText
                        primary={name}
                        secondary="Chart configuration will be updated"
                      />
                      <Chip label="Modified" size="small" color="info" />
                    </ListItem>
                  ))}

                  {changes.charts_unchanged.map((name) => (
                    <ListItem key={`unchanged-${name}`}>
                      <ListItemIcon sx={{ minWidth: 36 }}>
                        <CheckCircleOutlinedIcon color="disabled" fontSize="small" />
                      </ListItemIcon>
                      <ListItemText
                        primary={name}
                        secondary="No changes"
                        sx={{ color: 'text.secondary' }}
                      />
                    </ListItem>
                  ))}
                </List>

                {(changes.charts_removed.length > 0) && (
                  <Alert severity="info" sx={{ mt: 2 }}>
                    Charts that are not in the new template version stay in your definition. Remove them yourself if you do not need them.
                  </Alert>
                )}
              </Box>
            )}

            <Box sx={{ mt: 3 }}>
              <Typography variant="subtitle2" component="h2" gutterBottom>
                Value Changes
              </Typography>
              <ChartDiffList
                chartDiffs={valueDiffs}
                leftTitle="Current definition"
                rightTitle={`v${checkResult.latest_version}`}
                hideUnchanged
                removedIsKept
                emptyMessage="No chart value changes."
              />
            </Box>
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={upgrading}>
          Cancel
        </Button>
        {checkResult?.upgrade_available && (
          <Button
            variant="contained"
            onClick={handleUpgrade}
            disabled={upgrading}
          >
            {upgrading ? 'Upgrading...' : 'Upgrade'}
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
};

export default UpgradeDialog;
