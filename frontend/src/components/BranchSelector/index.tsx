import { useState, useEffect, useCallback } from 'react';
import { Autocomplete, TextField } from '@mui/material';
import { gitService } from '../../api/client';

interface BranchSelectorProps {
  repoUrl: string;
  value: string;
  onChange: (branch: string) => void;
  label?: string;
}

const BranchSelector = ({ repoUrl, value, onChange, label = 'Branch' }: BranchSelectorProps) => {
  const [branches, setBranches] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);

  const fetchBranches = useCallback(async () => {
    if (!repoUrl) return;
    setLoading(true);
    setError(false);
    try {
      const data = await gitService.branches(repoUrl);
      setBranches(data);
    } catch {
      setError(true);
      setBranches([]);
    } finally {
      setLoading(false);
    }
  }, [repoUrl]);

  useEffect(() => {
    fetchBranches();
  }, [fetchBranches]);

  if (error) {
    return (
      <TextField
        label={label}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        fullWidth
        size="small"
        helperText="Could not load branches. Enter branch name manually."
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
        <TextField {...params} label={label} size="small" fullWidth />
      )}
    />
  );
};

export default BranchSelector;
