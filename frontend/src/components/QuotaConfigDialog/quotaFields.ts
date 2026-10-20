import { formatCpuCores, formatMemoryQuantity, parseCpuMillicores, parseMemoryBytes, parseQuantity } from '../../utils/quantity';

/** Quantity fields of a cluster quota and of an instance quota override (pod_limit is a number). */
export type QuantityField =
  | 'cpu_request'
  | 'cpu_limit'
  | 'memory_request'
  | 'memory_limit'
  | 'storage_limit'
  | 'ephemeral_storage_limit'
  | 'container_cpu_request'
  | 'container_cpu_limit'
  | 'container_cpu_max'
  | 'container_memory_request'
  | 'container_memory_limit'
  | 'container_memory_max'
  | 'container_ephemeral_storage_request'
  | 'container_ephemeral_storage_limit';

export type QuantityValues = Record<QuantityField, string>;
export type QuantityErrors = Partial<Record<QuantityField, string>>;

export const emptyQuantities: QuantityValues = {
  cpu_request: '',
  cpu_limit: '',
  memory_request: '',
  memory_limit: '',
  storage_limit: '',
  ephemeral_storage_limit: '',
  container_cpu_request: '',
  container_cpu_limit: '',
  container_cpu_max: '',
  container_memory_request: '',
  container_memory_limit: '',
  container_memory_max: '',
  container_ephemeral_storage_request: '',
  container_ephemeral_storage_limit: '',
};

export const quantityFields = Object.keys(emptyQuantities) as QuantityField[];

type ResourceKind = 'cpu' | 'memory' | 'storage';

const fieldKind: Record<QuantityField, ResourceKind> = {
  cpu_request: 'cpu',
  cpu_limit: 'cpu',
  memory_request: 'memory',
  memory_limit: 'memory',
  storage_limit: 'storage',
  ephemeral_storage_limit: 'storage',
  container_cpu_request: 'cpu',
  container_cpu_limit: 'cpu',
  container_cpu_max: 'cpu',
  container_memory_request: 'memory',
  container_memory_limit: 'memory',
  container_memory_max: 'memory',
  container_ephemeral_storage_request: 'storage',
  container_ephemeral_storage_limit: 'storage',
};

const kindHint: Record<ResourceKind, string> = {
  cpu: 'Use a CPU quantity such as 500m, 1 or 1.5',
  memory: 'Use a memory quantity such as 256Mi, 1Gi or 1G',
  storage: 'Use a storage quantity such as 10Gi or 50G',
};

const kindParser: Record<ResourceKind, (v: string) => number | null> = {
  cpu: parseCpuMillicores,
  memory: parseMemoryBytes,
  storage: parseMemoryBytes,
};

/**
 * Order rules: the first field must not be higher than the second when both
 * are set. The same rules as the backend (models.quotaOrderRules). The error
 * shows under the first field.
 */
const orderRules: Array<[QuantityField, QuantityField, string]> = [
  ['cpu_request', 'cpu_limit', 'CPU request must not be higher than the CPU limit'],
  ['memory_request', 'memory_limit', 'Memory request must not be higher than the memory limit'],
  ['container_cpu_request', 'container_cpu_limit', 'Must not be higher than the container CPU limit'],
  ['container_cpu_limit', 'container_cpu_max', 'Must not be higher than the container CPU max'],
  ['container_cpu_request', 'container_cpu_max', 'Must not be higher than the container CPU max'],
  ['container_memory_request', 'container_memory_limit', 'Must not be higher than the container memory limit'],
  ['container_memory_limit', 'container_memory_max', 'Must not be higher than the container memory max'],
  ['container_memory_request', 'container_memory_max', 'Must not be higher than the container memory max'],
  ['container_ephemeral_storage_request', 'container_ephemeral_storage_limit', 'Must not be higher than the container ephemeral storage limit'],
  ['container_cpu_request', 'cpu_request', 'Must not be higher than the namespace CPU request'],
  ['container_cpu_request', 'cpu_limit', 'Must not be higher than the namespace CPU limit'],
  ['container_cpu_limit', 'cpu_limit', 'Must not be higher than the namespace CPU limit'],
  ['container_memory_request', 'memory_request', 'Must not be higher than the namespace memory request'],
  ['container_memory_request', 'memory_limit', 'Must not be higher than the namespace memory limit'],
  ['container_memory_limit', 'memory_limit', 'Must not be higher than the namespace memory limit'],
  ['container_ephemeral_storage_request', 'ephemeral_storage_limit', 'Must not be higher than the namespace ephemeral storage limit'],
  ['container_ephemeral_storage_limit', 'ephemeral_storage_limit', 'Must not be higher than the namespace ephemeral storage limit'],
];

/** Fields that share an order rule with each field (the error shows when either is touched). */
export const relatedFields: Partial<Record<QuantityField, QuantityField[]>> = {};
for (const [low, high] of orderRules) {
  relatedFields[low] = [...(relatedFields[low] ?? []), high];
}

export interface FieldSpec {
  field: QuantityField;
  label: string;
  help: string;
  placeholder: string;
}

export const namespaceFields: FieldSpec[] = [
  { field: 'cpu_request', label: 'CPU Request', help: 'Default CPU request per namespace (e.g., 500m, 1, 2000m)', placeholder: '500m' },
  { field: 'cpu_limit', label: 'CPU Limit', help: 'Maximum CPU per namespace (e.g., 2000m, 4)', placeholder: '2000m' },
  { field: 'memory_request', label: 'Memory Request', help: 'Default memory request per namespace (e.g., 256Mi, 1Gi)', placeholder: '256Mi' },
  { field: 'memory_limit', label: 'Memory Limit', help: 'Maximum memory per namespace (e.g., 1Gi, 2Gi)', placeholder: '1Gi' },
  { field: 'storage_limit', label: 'Storage Limit', help: 'Maximum storage per namespace (e.g., 10Gi, 50Gi)', placeholder: '10Gi' },
  {
    field: 'ephemeral_storage_limit',
    label: 'Ephemeral Storage Limit',
    help: 'Maximum ephemeral storage (container logs, emptyDir) per namespace, for requests and limits (e.g., 20Gi)',
    placeholder: '20Gi',
  },
];

export const containerFields: FieldSpec[] = [
  { field: 'container_cpu_request', label: 'Container CPU Request', help: 'Default CPU request of a container without its own (e.g., 100m)', placeholder: '100m' },
  { field: 'container_cpu_limit', label: 'Container CPU Limit', help: 'Default CPU limit of a container without its own (e.g., 500m)', placeholder: '500m' },
  { field: 'container_cpu_max', label: 'Container CPU Max', help: 'Highest CPU limit one container may set (e.g., 2)', placeholder: '2' },
  { field: 'container_memory_request', label: 'Container Memory Request', help: 'Default memory request of a container without its own (e.g., 128Mi)', placeholder: '128Mi' },
  { field: 'container_memory_limit', label: 'Container Memory Limit', help: 'Default memory limit of a container without its own (e.g., 512Mi)', placeholder: '512Mi' },
  { field: 'container_memory_max', label: 'Container Memory Max', help: 'Highest memory limit one container may set (e.g., 4Gi)', placeholder: '4Gi' },
  {
    field: 'container_ephemeral_storage_request',
    label: 'Container Ephemeral Storage Request',
    help: 'Default ephemeral storage request of a container (e.g., 128Mi)',
    placeholder: '128Mi',
  },
  {
    field: 'container_ephemeral_storage_limit',
    label: 'Container Ephemeral Storage Limit',
    help: 'Default ephemeral storage limit of a container (e.g., 1Gi)',
    placeholder: '1Gi',
  },
];

export const POD_LIMIT_LABEL = 'Pod Limit';

/**
 * Return a copy of the values with surrounding whitespace removed from every quantity field.
 * @param form - Form values (other keys are copied unchanged)
 * @returns The trimmed copy
 */
export function trimQuantities<T extends QuantityValues>(form: T): T {
  const trimmed = { ...form };
  for (const field of quantityFields) {
    trimmed[field] = form[field].trim() as T[QuantityField];
  }
  return trimmed;
}

/**
 * Validate the quantity fields: the quantity syntax, not negative, and the order rules.
 * An empty quantity means "not set" and is valid.
 * @param input - Form values
 * @returns The error message of each invalid field
 */
export function validateQuantities(input: QuantityValues): QuantityErrors {
  const form = trimQuantities(input);
  const errors: QuantityErrors = {};
  for (const field of quantityFields) {
    const raw = form[field];
    if (!raw) continue;
    const value = parseQuantity(raw);
    if (value === null) {
      errors[field] = `Invalid quantity. ${kindHint[fieldKind[field]]}`;
    } else if (value < 0) {
      errors[field] = 'Must not be negative';
    }
  }
  for (const [lowField, highField, message] of orderRules) {
    if (errors[lowField] || errors[highField]) continue;
    const parse = kindParser[fieldKind[lowField]];
    const low = form[lowField] ? parse(form[lowField]) : null;
    const high = form[highField] ? parse(form[highField]) : null;
    if (low !== null && high !== null && low > high) {
      errors[lowField] = message;
    }
  }
  return errors;
}

/**
 * Format a quota quantity for display: CPU in cores, memory and storage in binary units.
 * The raw quantity follows in parentheses when the readable text does not show it.
 * @param field - Quantity field
 * @param raw - Kubernetes quantity, for example `1500m` or `2Gi`
 * @returns For example `1.5 cores (1500m)`, `2 cores`, `2 GiB` or `954 MiB (1G)`
 */
export function formatQuotaQuantity(field: QuantityField, raw: string): string {
  if (parseQuantity(raw) === null) return raw;
  if (fieldKind[field] === 'cpu') {
    const cores = formatCpuCores(raw);
    const text = `${cores} ${cores === '1' ? 'core' : 'cores'}`;
    return cores === raw ? text : `${text} (${raw})`;
  }
  const text = formatMemoryQuantity(raw);
  return text === raw.replace(/^(\d+)(Ki|Mi|Gi|Ti|Pi|Ei)$/, '$1 $2B') ? text : `${text} (${raw})`;
}
