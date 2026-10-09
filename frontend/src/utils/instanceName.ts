/** Maximum length of a stack instance name. */
export const INSTANCE_NAME_MAX_LENGTH = 50;

/** Short description of the instance name rule, for helper texts. */
export const INSTANCE_NAME_RULE =
  'Use lowercase letters (a-z), digits (0-9) and hyphens (-). Start and end with a letter or a digit.';

/** RFC 1123 label: lowercase alphanumerics and '-', starts and ends alphanumeric. */
const RFC1123_LABEL = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;

/**
 * Validates a stack instance name as an RFC 1123 label (the rule of the API).
 * Many definitions build host names from the instance name, so the name must be a valid DNS label.
 *
 * @param name - The instance name to check.
 * @returns An error message, or null when the name is valid.
 */
export function validateInstanceName(name: string): string | null {
  if (name === '') return 'Instance name is required';
  if (name.length > INSTANCE_NAME_MAX_LENGTH) {
    return `Use ${INSTANCE_NAME_MAX_LENGTH} characters or fewer`;
  }
  if (/[A-Z]/.test(name)) return 'Use lowercase letters only';
  if (!RFC1123_LABEL.test(name)) {
    if (/^-|-$/.test(name)) return 'Start and end with a letter or a digit';
    return 'Use only lowercase letters (a-z), digits (0-9) and hyphens (-)';
  }
  return null;
}

/**
 * Suggests a valid clone name from a source name: `<name>-copy`, shortened to the maximum length.
 *
 * @param sourceName - Name of the source instance.
 * @returns A suggested name for the clone.
 */
export function suggestCloneName(sourceName: string): string {
  const suffix = '-copy';
  const base = sourceName
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, '-')
    .slice(0, INSTANCE_NAME_MAX_LENGTH - suffix.length)
    .replace(/^-+|-+$/g, '');
  return `${base || 'stack'}${suffix}`;
}
