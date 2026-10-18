import { getApiErrorInfo } from './apiError';

/** Message for Use Template and Quick Deploy when the template has no release. */
export const NO_PUBLISHED_VERSION_MESSAGE =
  'This template has no published version. Ask the template owner to publish a version first.';

/** Message for Use Template when the template has releases but is unpublished. */
export const UNPUBLISHED_TEMPLATE_MESSAGE =
  'This template is unpublished. Ask the template owner to publish it again.';

/** Message after a save of a template that users already get through a release. */
export const SAVED_AS_DRAFT_MESSAGE = 'Saved as draft. Publish a new version to release the changes.';

const SEMVER_RE = /^v?(\d+)\.(\d+)\.(\d+)(.*)$/;
const SHORT_VERSION_RE = /^v?(\d+)(?:\.(\d+))?$/;

/** Parse "1", "1.2" or "1.2.3" into numeric parts. Returns null for other strings. */
function parseVersion(version: string): [number, number, number] | null {
  const v = version.trim();
  const full = SEMVER_RE.exec(v);
  if (full && !full[4]) return [Number(full[1]), Number(full[2]), Number(full[3])];
  const short = SHORT_VERSION_RE.exec(v);
  if (short) return [Number(short[1]), Number(short[2] ?? 0), 0];
  return null;
}

/**
 * Return the next patch version, for example "1.2.3" gives "1.2.4" and "1.2" gives "1.2.1".
 * Returns an empty string when the version is not numeric.
 * @param version - Current version string
 * @returns The next patch version, or '' when the input cannot be parsed
 */
export function nextPatchVersion(version: string): string {
  const parts = parseVersion(version);
  if (!parts) return '';
  return `${parts[0]}.${parts[1]}.${parts[2] + 1}`;
}

/**
 * Compare two version strings numerically.
 * @returns A negative number when a < b, 0 when equal, a positive number when a > b, or null when one is not numeric
 */
export function compareVersions(a: string, b: string): number | null {
  const pa = parseVersion(a);
  const pb = parseVersion(b);
  if (!pa || !pb) return null;
  for (let i = 0; i < 3; i += 1) {
    if (pa[i] !== pb[i]) return pa[i] - pb[i];
  }
  return 0;
}

/**
 * Suggest the version for the next release.
 * Without a release, use the working copy version.
 * Without unpublished changes, use the released version: publish is then a no-op
 * (the API compares the version too, so a new version string would create a snapshot).
 * Else use the working copy version when it is newer, or the next patch of the release.
 * @param workingVersion - Version string of the working copy
 * @param publishedVersion - Version string of the latest release, if any
 * @param hasUnpublishedChanges - Whether the working copy differs from the release (undefined: unknown)
 * @returns The suggested version string
 */
export function suggestPublishVersion(
  workingVersion: string,
  publishedVersion?: string | null,
  hasUnpublishedChanges?: boolean,
): string {
  if (!publishedVersion) return workingVersion || '1.0.0';
  if (hasUnpublishedChanges === false) return publishedVersion;
  const cmp = workingVersion ? compareVersions(workingVersion, publishedVersion) : null;
  if (cmp !== null && cmp > 0) return workingVersion;
  return nextPatchVersion(publishedVersion) || workingVersion;
}

/**
 * Return true when the error is the HTTP 409 "Template has no published version" response.
 * @param error - The thrown error (usually an AxiosError)
 */
export function isNoPublishedVersionError(error: unknown): boolean {
  const { status, message } = getApiErrorInfo(error);
  return status === 409 && /no published version/i.test(message ?? '');
}
