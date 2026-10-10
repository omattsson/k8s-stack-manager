import { useState, useEffect } from 'react';
import type { KeyboardEvent } from 'react';
import { Autocomplete, TextField } from '@mui/material';
import { gitService } from '../../api/client';

interface BranchSelectorProps {
  /**
   * Repository URL, or a list of repository URLs. For a list the options are
   * the branches of all repositories (each branch once, in list order).
   * Empty URLs are ignored.
   */
  repoUrl: string | readonly string[];
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
 * Unique, non-empty repository URLs in input order.
 * @param repoUrl - One URL or a list of URLs
 * @returns The URLs to load branches from
 */
export const normalizeRepoUrls = (repoUrl: string | readonly string[]): string[] => {
  const list = typeof repoUrl === 'string' ? [repoUrl] : repoUrl;
  const out: string[] = [];
  for (const url of list) {
    const trimmed = (url ?? '').trim();
    if (trimmed && !out.includes(trimmed)) out.push(trimmed);
  }
  return out;
};

/**
 * Merge branch lists: each branch once, in the order of first appearance.
 * @param lists - Branch lists, one per repository
 * @returns The merged list
 */
export const mergeBranchLists = (lists: readonly (readonly string[])[]): string[] => {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const list of lists) {
    for (const b of list ?? []) {
      if (!seen.has(b)) {
        seen.add(b);
        out.push(b);
      }
    }
  }
  return out;
};

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
  /** Repositories whose branches did not load, and all repositories (when some loaded). */
  const [partial, setPartial] = useState<{ failed: number; total: number } | null>(null);
  const [inputValue, setInputValue] = useState(value);
  const [focused, setFocused] = useState(false);

  // A stable key, so a new array with the same URLs does not load again.
  const urlsKey = JSON.stringify(normalizeRepoUrls(repoUrl));

  // Load the branches of all repositories. A load for older URLs that ends
  // after a newer load started is ignored.
  useEffect(() => {
    const urls = JSON.parse(urlsKey) as string[];
    if (urls.length === 0) {
      setBranches([]);
      setError(false);
      setPartial(null);
      setLoading(false);
      return undefined;
    }
    let active = true;
    setLoading(true);
    setError(false);
    setPartial(null);
    void Promise.allSettled(urls.map((url) => gitService.branches(url))).then((results) => {
      if (!active) return;
      const loaded = results.filter((r): r is PromiseFulfilledResult<string[]> => r.status === 'fulfilled');
      if (loaded.length === 0) {
        setError(true);
        setBranches([]);
      } else {
        setBranches(mergeBranchLists(loaded.map((r) => r.value)));
        const failed = results.length - loaded.length;
        setPartial(failed > 0 ? { failed, total: results.length } : null);
      }
      setLoading(false);
    });
    return () => {
      active = false;
    };
  }, [urlsKey]);

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
  const partialText = partial
    ? `Branches of ${partial.failed} of ${partial.total} repositories did not load.`
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
          helperText={warning ?? hint ?? partialText}
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
