import { useState, useEffect, useCallback } from 'react';
import type { KeyboardEvent } from 'react';
import { Autocomplete, TextField } from '@mui/material';
import { gitService } from '../../api/client';

interface BranchSelectorProps {
  repoUrl: string;
  /** The selected branch. */
  value: string;
  /**
   * Called with the new branch when the user commits a branch: an option is
   * selected, or the user presses Enter or leaves the field with text that
   * differs from `value`. Not called while the user types, and not called with
   * an empty string.
   */
  onChange: (branch: string) => void;
  label?: string;
  disabled?: boolean;
}

/**
 * Git branch picker with autocomplete. The typed text stays local until the
 * user commits it, so the user can clear the field and type a search without
 * a change of the selected branch. Empty text on blur restores the selected
 * branch.
 */
const BranchSelector = ({ repoUrl, value, onChange, label = 'Branch', disabled = false }: BranchSelectorProps) => {
  const [branches, setBranches] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  const [inputValue, setInputValue] = useState(value);
  const [focused, setFocused] = useState(false);

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

  // Show a new selected branch (for example after a reset by the parent).
  useEffect(() => {
    setInputValue(value);
  }, [value]);

  /** Commit the text as the branch. Empty text restores the selected branch. */
  const commit = (text: string) => {
    const branch = text.trim();
    if (!branch) {
      setInputValue(value);
      return;
    }
    setInputValue(branch);
    if (branch !== value) onChange(branch);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') setInputValue(value);
  };

  const typed = inputValue.trim();
  const lowerTyped = typed.toLowerCase();
  // While the user types, warn only when no branch matches the text. Else warn
  // when the text is not a branch of the repository.
  const unknownBranch = !loading && branches.length > 0 && typed !== ''
    && !branches.includes(typed)
    && (!focused || typed === value || !branches.some((b) => b.toLowerCase().includes(lowerTyped)));
  // The git provider can cap the branch list, so the text says "loaded list".
  const warning = unknownBranch ? `Branch "${typed}" is not in the loaded branch list.` : undefined;
  const hint = focused && typed !== value
    ? 'Press Enter to use typed text. Escape restores the current branch.'
    : undefined;

  if (error) {
    return (
      <TextField
        label={label}
        value={inputValue}
        onChange={(e) => setInputValue(e.target.value)}
        onBlur={() => commit(inputValue)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit(inputValue);
          handleKeyDown(e);
        }}
        fullWidth
        size="small"
        disabled={disabled}
        helperText="Could not load branches. Enter the branch name and press Enter."
      />
    );
  }

  return (
    <Autocomplete
      options={branches}
      value={value}
      inputValue={inputValue}
      onInputChange={(_e, newValue) => setInputValue(newValue)}
      onChange={(_e, newValue) => {
        // Option select or Enter with free text. Clearing is not possible
        // (disableClearable), so newValue is a string.
        if (typeof newValue === 'string') commit(newValue);
      }}
      loading={loading}
      disabled={disabled}
      freeSolo
      disableClearable
      renderInput={(params) => (
        <TextField
          {...params}
          label={label}
          size="small"
          fullWidth
          onFocus={() => setFocused(true)}
          onBlur={() => {
            setFocused(false);
            commit(inputValue);
          }}
          onKeyDown={handleKeyDown}
          helperText={warning ?? hint}
          slotProps={{
            ...params.slotProps,
            formHelperText: warning ? { sx: { color: 'warning.main' } } : undefined,
          }}
        />
      )}
    />
  );
};

export default BranchSelector;
