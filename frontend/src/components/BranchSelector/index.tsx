import { useState, useEffect } from 'react';
import type { KeyboardEvent } from 'react';
import CheckCircleOutlinedIcon from '@mui/icons-material/CheckCircleOutlined';
import ErrorOutlinedIcon from '@mui/icons-material/ErrorOutlined';
import WarningAmberOutlinedIcon from '@mui/icons-material/WarningAmberOutlined';
import { Autocomplete, Box, CircularProgress, TextField } from '@mui/material';
import { gitService, type ProviderStatus } from '../../api/client';

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

type ProviderType = 'azure_devops' | 'github' | 'gitlab';
type ProviderAvailability = 'loading' | 'available' | 'unavailable' | 'error';

const providerLabels: Record<ProviderType, string> = {
  azure_devops: 'Azure DevOps',
  github: 'GitHub',
  gitlab: 'GitLab',
};

/**
 * Lowercase hostname of a repository URL. Like the backend, an scp-style SSH
 * URL (`git@host:path`) is read as `ssh://git@host/path`, and a URL without a
 * scheme as `https://...`. Only the authority is parsed, so an odd path still
 * gives a hostname.
 * @param repoUrl - Repository URL
 * @returns The hostname, or null for an invalid URL or a scheme other than
 *   http, https or ssh
 */
const repositoryHostname = (repoUrl: string): string | null => {
  let raw = repoUrl.trim();
  if (!raw) return null;
  if (!raw.includes('://')) {
    const colon = raw.indexOf(':');
    raw = colon > 0 && raw.slice(0, colon).includes('@')
      ? `ssh://${raw.slice(0, colon)}/${raw.slice(colon + 1)}`
      : `https://${raw}`;
  }
  const schemeEnd = raw.indexOf('://');
  const rest = raw.slice(schemeEnd + 3);
  const separator = rest.search(/[/?#]/);
  const authority = separator >= 0 ? raw.slice(0, schemeEnd + 3 + separator) : raw;

  let url: URL;
  try {
    url = new URL(authority);
  } catch {
    return null;
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:' && url.protocol !== 'ssh:') return null;
  // ssh:// hosts are opaque in the URL standard and keep their case.
  return url.hostname.toLowerCase();
};

/**
 * Git provider of a repository URL, by exact hostname (case-insensitive).
 * Accepts https, http, ssh:// and scp-style (`git@host:path`) URLs.
 *
 * - GitHub: github.com.
 * - Azure DevOps: dev.azure.com, ssh.dev.azure.com and *.visualstudio.com
 *   (includes vs-ssh.visualstudio.com).
 * - GitLab: gitlab.com only.
 *
 * The backend also routes a self-hosted GitLab (its configured GITLAB_BASE_URL
 * origin) to GitLab, and then no longer serves gitlab.com. GET
 * /api/v1/git/providers does not expose that origin, so any other host is
 * unknown here: no provider status is shown and manual entry stays. A
 * hostname with a trailing dot (as on the backend) or another empty label is
 * never a provider host.
 * @param repoUrl - Repository URL
 * @returns The provider, or null for an unknown host or an invalid URL
 */
export const detectProvider = (repoUrl: string): ProviderType | null => {
  const hostname = repositoryHostname(repoUrl);
  if (!hostname || hostname.split('.').includes('')) return null;

  if (hostname === 'github.com') return 'github';
  if (
    hostname === 'dev.azure.com'
    || hostname === 'ssh.dev.azure.com'
    || hostname.endsWith('.visualstudio.com')
  ) {
    return 'azure_devops';
  }
  if (hostname === 'gitlab.com') return 'gitlab';
  return null;
};

let providerStatusesRequest: Promise<ProviderStatus[]> | null = null;

/** Provider statuses; selectors that ask at the same time share one request. */
const getProviderStatuses = (): Promise<ProviderStatus[]> => {
  if (!providerStatusesRequest) {
    providerStatusesRequest = gitService.providers().finally(() => {
      providerStatusesRequest = null;
    });
  }
  return providerStatusesRequest;
};

/**
 * Status text for the providers of the repositories, for example
 * "GitHub available." or "GitLab unavailable. Enter a branch name manually."
 * @param types - Providers in display order
 * @param states - Availability of each provider
 * @returns The text
 */
const providerStatusText = (
  types: readonly ProviderType[],
  states: Partial<Record<ProviderType, ProviderAvailability>>,
): string => {
  const names = (state: ProviderAvailability) => types
    .filter((t) => states[t] === state)
    .map((t) => providerLabels[t])
    .join(', ');
  const loading = names('loading');
  if (loading) return `Checking ${loading}...`;
  const available = names('available');
  const unavailable = names('unavailable');
  const failed = names('error');
  const parts: string[] = [];
  if (available) parts.push(`${available} available.`);
  if (unavailable) parts.push(`${unavailable} unavailable.`);
  if (failed) parts.push(`Could not check ${failed}.`);
  if (unavailable || failed) parts.push('Enter a branch name manually.');
  return parts.join(' ');
};

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
 * branch. Below the field it shows whether the Git provider of the
 * repositories is available; manual entry works in every case.
 */
const BranchSelector = ({ repoUrl, value, onChange, label = 'Branch', disabled = false }: BranchSelectorProps) => {
  const [branches, setBranches] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  /** Repositories whose branches did not load, and all repositories (when some loaded). */
  const [partial, setPartial] = useState<{ failed: number; total: number } | null>(null);
  const [inputValue, setInputValue] = useState(value);
  const [focused, setFocused] = useState(false);
  const [providerStates, setProviderStates] = useState<Partial<Record<ProviderType, ProviderAvailability>>>({});

  const urls = normalizeRepoUrls(repoUrl);
  // A stable key, so a new array with the same URLs does not load again.
  const urlsKey = JSON.stringify(urls);
  const providerTypes: ProviderType[] = [];
  for (const url of urls) {
    const type = detectProvider(url);
    if (type && !providerTypes.includes(type)) providerTypes.push(type);
  }
  const providersKey = providerTypes.join(',');

  // Load the branches of all repositories. A new URL list clears the old
  // options and error at once, and aborts the requests of the old list; a
  // load for older URLs that ends after a newer load started is ignored.
  useEffect(() => {
    const keyUrls = JSON.parse(urlsKey) as string[];
    setBranches([]);
    setError(false);
    setPartial(null);
    if (keyUrls.length === 0) {
      setLoading(false);
      return undefined;
    }
    let active = true;
    const controller = new AbortController();
    setLoading(true);
    void Promise.allSettled(
      keyUrls.map((url) => gitService.branches(url, { signal: controller.signal })),
    ).then((results) => {
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
      controller.abort();
    };
  }, [urlsKey]);

  // Check whether the Git providers of the repositories are available.
  useEffect(() => {
    const types = (providersKey ? providersKey.split(',') : []) as ProviderType[];
    const all = (state: ProviderAvailability) => Object.fromEntries(types.map((t) => [t, state]));
    let active = true;

    setProviderStates(all('loading'));
    if (types.length === 0) {
      return () => {
        active = false;
      };
    }

    getProviderStatuses()
      .then((statuses: ProviderStatus[]) => {
        if (!active) return;
        setProviderStates(Object.fromEntries(types.map((t) => {
          const status = statuses.find(({ type }) => type === t);
          return [t, status ? (status.available ? 'available' : 'unavailable') : 'error'];
        })));
      })
      .catch(() => {
        if (active) setProviderStates(all('error'));
      });

    return () => {
      active = false;
    };
  }, [providersKey]);

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
  const fieldText = warning ?? hint ?? partialText;

  // Only providers of the current URLs (a state of old URLs can remain for
  // one render after the URLs change).
  const shownProviderStates = providerTypes.map((t) => providerStates[t]);
  const providerHint = providerTypes.length > 0 && shownProviderStates.every(Boolean) ? (
    <Box
      component="span"
      role="status"
      aria-live="polite"
      sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}
    >
      {shownProviderStates.includes('loading') ? (
        <CircularProgress size={14} aria-hidden="true" />
      ) : shownProviderStates.includes('error') ? (
        <ErrorOutlinedIcon color="warning" fontSize="inherit" aria-hidden="true" />
      ) : shownProviderStates.includes('unavailable') ? (
        <WarningAmberOutlinedIcon color="warning" fontSize="inherit" aria-hidden="true" />
      ) : (
        <CheckCircleOutlinedIcon color="success" fontSize="inherit" aria-hidden="true" />
      )}
      {providerStatusText(providerTypes, providerStates)}
    </Box>
  ) : undefined;

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
        helperText={(
          <Box component="span" role="status" aria-live="polite">
            Could not load branches. Enter the branch name and press Enter.
          </Box>
        )}
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
          helperText={fieldText || providerHint ? (
            <>
              {fieldText && (
                <Box component="span" sx={{ display: 'block', color: warning ? 'warning.main' : undefined }}>
                  {fieldText}
                </Box>
              )}
              {providerHint}
            </>
          ) : undefined}
        />
      )}
    />
  );
};

export default BranchSelector;
