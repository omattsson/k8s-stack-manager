import { describe, it, expect } from 'vitest';
import { emptyQuantities, formatQuotaQuantity, validateQuantities } from '../quotaFields';

describe('formatQuotaQuantity', () => {
  it.each([
    ['cpu_limit', '2', '2 cores'],
    ['cpu_request', '1', '1 core'],
    ['cpu_limit', '1500m', '1.5 cores (1500m)'],
    ['container_cpu_request', '100m', '0.1 cores (100m)'],
    ['memory_limit', '2Gi', '2 GiB'],
    ['container_memory_limit', '512Mi', '512 MiB'],
    ['memory_limit', '1536Mi', '1.5 GiB (1536Mi)'],
    ['storage_limit', '1G', '954 MiB (1G)'],
    ['memory_limit', 'abc', 'abc'],
  ] as const)('formats %s %s as %s', (field, raw, want) => {
    expect(formatQuotaQuantity(field, raw)).toBe(want);
  });
});

describe('validateQuantities', () => {
  it('accepts empty and valid values', () => {
    expect(validateQuantities({ ...emptyQuantities, cpu_limit: ' 2 ', memory_limit: '1Gi' })).toEqual({});
  });

  it('reports invalid quantities and order rules', () => {
    const errors = validateQuantities({ ...emptyQuantities, cpu_request: '3', cpu_limit: '2', memory_limit: 'two' });
    expect(errors.cpu_request).toBe('CPU request must not be higher than the CPU limit');
    expect(errors.memory_limit).toMatch(/^Invalid quantity/);
  });
});
