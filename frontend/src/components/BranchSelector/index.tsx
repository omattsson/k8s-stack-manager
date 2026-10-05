import { useState, useEffect } from 'react';
import CheckCircleOutlinedIcon from '@mui/icons-material/CheckCircleOutlined';
import ErrorOutlinedIcon from '@mui/icons-material/ErrorOutlined';
import WarningAmberOutlinedIcon from '@mui/icons-material/WarningAmberOutlined';
import { Autocomplete, Box, CircularProgress, TextField } from '@mui/material';
import { gitService, type ProviderStatus } from '../../api/client';

interface BranchSelectorProps {
  repoUrl: string;
  value: string;
  onChange: (branch: string) => void;
  label?: string;
}

type ProviderType = 'azure_devops' | 'github' | 'gitlab';
type ProviderAvailability = 'idle' | 'loading' | 'available' | 'unavailable' | 'error';

const providerLabels: Record<ProviderType, string> = {
  azure_devops: 'Azure DevOps',
  github: 'GitHub',
  gitlab: 'GitLab',
};

const detectProvider = (repoUrl: string): ProviderType | null => {
  let url: URL;
  try {
    url = new URL(repoUrl);
  } catch {
    return null;
  }

  if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;

  const hostname = url.hostname.toLowerCase();
  if (hostname === 'github.com') return 'github';
  if (hostname === 'dev.azure.com' || hostname.endsWith('.visualstudio.com')) {
    return 'azure_devops';
  }
  return 'gitlab';
};

let providerStatusesRequest: Promise<ProviderStatus[]> | null = null;

const getProviderStatuses = (): Promise<ProviderStatus[]> => {
  if (!providerStatusesRequest) {
    providerStatusesRequest = gitService.providers().finally(() => {
      providerStatusesRequest = null;
    });
  }
  return providerStatusesRequest;
};

const BranchSelector = ({ repoUrl, value, onChange, label = 'Branch' }: BranchSelectorProps) => {
  const [branches, setBranches] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  const providerType = detectProvider(repoUrl);
  const [providerAvailability, setProviderAvailability] = useState<ProviderAvailability>('idle');

  useEffect(() => {
    let active = true;

    setBranches([]);
    setError(false);

    if (!repoUrl) {
      setLoading(false);
      return () => {
        active = false;
      };
    }

    const controller = new AbortController();
    setLoading(true);
    gitService.branches(repoUrl, { signal: controller.signal })
      .then((data) => {
        if (active) setBranches(data);
      })
      .catch(() => {
        if (!active) return;
        setError(true);
        setBranches([]);
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
      controller.abort();
    };
  }, [repoUrl]);

  useEffect(() => {
    let active = true;

    if (!providerType) {
      setProviderAvailability('idle');
      return () => {
        active = false;
      };
    }

    setProviderAvailability('loading');
    getProviderStatuses()
      .then((statuses: ProviderStatus[]) => {
        if (!active) return;
        const status = statuses.find(({ type }) => type === providerType);
        setProviderAvailability(status ? (status.available ? 'available' : 'unavailable') : 'error');
      })
      .catch(() => {
        if (active) setProviderAvailability('error');
      });

    return () => {
      active = false;
    };
  }, [providerType]);

  const providerHint = providerType && providerAvailability !== 'idle' ? (
    <Box
      component="span"
      role="status"
      aria-live="polite"
      sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}
    >
      {providerAvailability === 'loading' && <CircularProgress size={14} aria-hidden="true" />}
      {providerAvailability === 'available' && (
        <CheckCircleOutlinedIcon color="success" fontSize="inherit" aria-hidden="true" />
      )}
      {providerAvailability === 'unavailable' && (
        <WarningAmberOutlinedIcon color="warning" fontSize="inherit" aria-hidden="true" />
      )}
      {providerAvailability === 'error' && (
        <ErrorOutlinedIcon color="warning" fontSize="inherit" aria-hidden="true" />
      )}
      {providerAvailability === 'loading' && `Checking ${providerLabels[providerType]}...`}
      {providerAvailability === 'available' && `${providerLabels[providerType]} available.`}
      {providerAvailability === 'unavailable' && (
        `${providerLabels[providerType]} unavailable. Enter a branch name manually.`
      )}
      {providerAvailability === 'error' && (
        `Could not check ${providerLabels[providerType]}. Enter a branch name manually.`
      )}
    </Box>
  ) : undefined;

  if (error) {
    return (
      <TextField
        label={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        fullWidth
        size="small"
        helperText={(
          <Box component="span" role="status" aria-live="polite">
            Could not load branches. Enter a branch name manually.
          </Box>
        )}
      />
    );
  }

  return (
    <Autocomplete
      options={branches}
      value={value || null}
      onChange={(_e, newValue) => onChange(newValue || '')}
      loading={loading}
      freeSolo
      onInputChange={(_e, newValue, reason) => {
        if (reason === 'input') onChange(newValue);
      }}
      renderInput={(params) => (
        <TextField {...params} label={label} size="small" fullWidth helperText={providerHint} />
      )}
    />
  );
};

export default BranchSelector;
