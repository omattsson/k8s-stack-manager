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

/** Scheme, lowercase hostname and port of a repository URL. */
interface RepositoryOrigin {
  scheme: 'http' | 'https' | 'ssh';
  host: string;
  /** Explicit port; empty when absent (or the scheme default for http(s)). */
  port: string;
}

/**
 * Origin of a repository URL. Like the backend, an scp-style SSH URL
 * (`git@host:path`) is read as `ssh://git@host/path`, and a URL without a
 * scheme as `https://...`. Only the authority is parsed, so an odd path still
 * gives a hostname.
 * @param repoUrl - Repository URL
 * @returns The origin, or null for an invalid URL, a scheme other than http,
 *   https or ssh, or a hostname with an empty label (such as a trailing dot)
 */
const parseRepositoryOrigin = (repoUrl: string): RepositoryOrigin | null => {
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
  const scheme = url.protocol.slice(0, -1);
  if (scheme !== 'http' && scheme !== 'https' && scheme !== 'ssh') return null;
  // ssh:// hosts are opaque in the URL standard and keep their case.
  const host = url.hostname.toLowerCase();
  if (!host || host.split('.').includes('')) return null;
  return { scheme, host, port: url.port };
};

/**
 * Effective port of an origin, as on the backend: the explicit port in
 * canonical decimal form (`0443` is `443`), else 80 or 443 from an http(s)
 * scheme.
 * @param origin - Scheme and explicit port
 * @returns The port, or an empty string for an invalid port or an SSH URL
 *   without a port
 */
const effectivePort = (origin: Pick<RepositoryOrigin, 'scheme' | 'port'>): string => {
  if (origin.port !== '') {
    const port = Number(origin.port);
    return Number.isInteger(port) && port > 0 && port <= 65535 ? String(port) : '';
  }
  if (origin.scheme === 'http') return '80';
  if (origin.scheme === 'https') return '443';
  return '';
};

/**
 * Host and effective port of a custom GitLab origin (the `origin` of the
 * gitlab entry of GET /api/v1/git/providers).
 * @param gitlabOrigin - The GitLab origin, for example `https://git.example.com:8443`
 * @returns The custom origin, or null when the origin is missing, malformed or
 *   on gitlab.com (then the default rule applies)
 */
const parseCustomGitLabOrigin = (gitlabOrigin?: string): { host: string; port: string } | null => {
  if (!gitlabOrigin) return null;
  let url: URL;
  try {
    url = new URL(gitlabOrigin);
  } catch {
    return null;
  }
  const scheme = url.protocol.slice(0, -1);
  if (scheme !== 'http' && scheme !== 'https') return null;
  const host = url.hostname.toLowerCase();
  if (!host || host.split('.').includes('') || host === 'gitlab.com') return null;
  const port = effectivePort({ scheme, port: url.port });
  return port ? { host, port } : null;
};

/**
 * Git provider of a repository URL, by exact hostname (case-insensitive), with
 * the routing rules of the backend. Accepts https, http, ssh:// and scp-style
 * (`git@host:path`) URLs.
 *
 * - GitHub: github.com.
 * - Azure DevOps: dev.azure.com, ssh.dev.azure.com and *.visualstudio.com
 *   (includes vs-ssh.visualstudio.com).
 * - GitLab, default origin (gitlab.com, or no origin known): gitlab.com.
 * - GitLab, custom origin (GITLAB_BASE_URL): only the host of that origin.
 *   An http(s) URL must also have the same effective port (explicit, or
 *   80/443 from the scheme; the scheme itself is not compared). An ssh:// or
 *   scp-style URL matches on the host only. gitlab.com is then not GitLab.
 *
 * Any other host is unknown: no provider status is shown and manual entry
 * stays. A hostname with a trailing dot (as on the backend) or another empty
 * label is never a provider host.
 * @param repoUrl - Repository URL
 * @param gitlabOrigin - The GitLab origin from GET /api/v1/git/providers;
 *   omit it while the origin is not known (gitlab.com is then GitLab)
 * @returns The provider, or null for an unknown host or an invalid URL
 */
export const detectProvider = (repoUrl: string, gitlabOrigin?: string): ProviderType | null => {
  const origin = parseRepositoryOrigin(repoUrl);
  if (!origin) return null;
  const { host } = origin;

  if (host === 'github.com') return 'github';
  if (host === 'dev.azure.com' || host === 'ssh.dev.azure.com' || host.endsWith('.visualstudio.com')) {
    return 'azure_devops';
  }

  const custom = parseCustomGitLabOrigin(gitlabOrigin);
  if (custom) {
    if (host !== custom.host) return null;
    if (origin.scheme === 'ssh') return 'gitlab';
    return effectivePort(origin) === custom.port ? 'gitlab' : null;
  }
  return host === 'gitlab.com' ? 'gitlab' : null;
};

/**
 * Whether a repository URL can belong to a Git provider once the GitLab origin
 * is known (any http, https or ssh URL with a valid hostname).
 * @param repoUrl - Repository URL
 * @returns True when the provider statuses are needed for this URL
 */
const mayHaveProvider = (repoUrl: string): boolean => parseRepositoryOrigin(repoUrl) !== null;

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
 * repositories is available; manual entry works in every case. Which hosts
 * are GitLab depends on the GitLab origin in the provider statuses, so a
 * GitLab status shows only after the statuses load; a host that no provider
 * serves shows no status.
 */
const BranchSelector = ({ repoUrl, value, onChange, label = 'Branch', disabled = false }: BranchSelectorProps) => {
  const [branches, setBranches] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  /** Repositories whose branches did not load, and all repositories (when some loaded). */
  const [partial, setPartial] = useState<{ failed: number; total: number } | null>(null);
  const [inputValue, setInputValue] = useState(value);
  const [focused, setFocused] = useState(false);
  /**
   * Provider statuses (null when the request failed), with the key of the
   * repositories they were loaded for.
   */
  const [statusResult, setStatusResult] = useState<{ key: string; statuses: ProviderStatus[] | null } | null>(null);

  const urls = normalizeRepoUrls(repoUrl);
  // A stable key, so a new array with the same URLs does not load again.
  const urlsKey = JSON.stringify(urls);
  // The URLs that can belong to a provider; the statuses are loaded for them.
  const statusKey = JSON.stringify(urls.filter(mayHaveProvider));

  // Statuses of the current URLs only: a result of old URLs can remain for
  // one render after the URLs change, and then counts as loading.
  const statusesLoaded = statusResult?.key === statusKey;
  const statuses = statusesLoaded ? statusResult.statuses : null;
  // The GitLab origin decides which hosts are GitLab. Without it (request in
  // flight or failed) the default rule applies: GitLab is gitlab.com.
  const gitlabOrigin = statuses?.find(({ type }) => type === 'gitlab')?.origin;
  const providerTypes: ProviderType[] = [];
  for (const url of urls) {
    const type = detectProvider(url, gitlabOrigin);
    // While the statuses load, the GitLab origin is unknown: show no GitLab
    // status yet instead of a guess for gitlab.com.
    if (!type || (type === 'gitlab' && !statusesLoaded)) continue;
    if (!providerTypes.includes(type)) providerTypes.push(type);
  }
  const providerStates: Partial<Record<ProviderType, ProviderAvailability>> = {};
  for (const type of providerTypes) {
    if (!statusesLoaded) {
      providerStates[type] = 'loading';
    } else {
      const status = statuses?.find((s) => s.type === type);
      providerStates[type] = status ? (status.available ? 'available' : 'unavailable') : 'error';
    }
  }

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

  // Load the provider statuses (availability and the GitLab origin) for the
  // repositories that can belong to a provider. A response for older URLs
  // that ends after the URLs changed is ignored.
  useEffect(() => {
    if (statusKey === '[]') return undefined;
    let active = true;
    getProviderStatuses()
      .then((loaded: ProviderStatus[]) => {
        if (active) setStatusResult({ key: statusKey, statuses: loaded });
      })
      .catch(() => {
        if (active) setStatusResult({ key: statusKey, statuses: null });
      });
    return () => {
      active = false;
    };
  }, [statusKey]);

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

  const shownProviderStates = providerTypes.map((t) => providerStates[t]);
  const providerHint = providerTypes.length > 0 ? (
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
