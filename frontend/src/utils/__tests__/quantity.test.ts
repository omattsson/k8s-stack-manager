import { describe, it, expect } from 'vitest';
import {
  isValidQuantity,
  parseQuantity,
  parseCpuMillicores,
  parseMemoryBytes,
  formatCpuCores,
  formatMemoryQuantity,
  formatBytes,
  quantityPercent,
} from '../quantity';

describe('isValidQuantity', () => {
  it.each([
    '1', '0', '1.5', '.5', '1.', '500m', '100u', '500n', '2000m', '+1', '-1',
    '256Mi', '1Gi', '1Ki', '1Ti', '1Pi', '1Ei',
    '1k', '1M', '1G', '1T', '1P', '1E',
    '1e3', '1E3', '1e-3', '1.5e+2', ' 1Gi ',
  ])('accepts %s', (input) => {
    expect(isValidQuantity(input)).toBe(true);
  });

  it.each([
    '', 'abc', '1 Gi', '1gi', '1GB', '1Kb', '1K', 'Mi', '1.2.3', '1e', '1e1.5', '--1', 'm', '1mi', '0x10', '1U', '1N', '1ni',
  ])('rejects %s', (input) => {
    expect(isValidQuantity(input)).toBe(false);
  });
});

describe('parseQuantity', () => {
  it.each<[string, number]>([
    ['1', 1],
    ['1.5', 1.5],
    ['.5', 0.5],
    ['500m', 0.5],
    ['100u', 1e-4],
    ['500n', 5e-7],
    ['1k', 1000],
    ['1M', 1e6],
    ['1G', 1e9],
    ['1T', 1e12],
    ['1P', 1e15],
    ['1E', 1e18],
    ['1Ki', 1024],
    ['256Mi', 256 * 1024 * 1024],
    ['1Gi', 1024 ** 3],
    ['1Ti', 1024 ** 4],
    ['1Pi', 1024 ** 5],
    ['1Ei', 1024 ** 6],
    ['1e3', 1000],
    ['1E3', 1000],
    ['2e-3', 0.002],
    ['1.5e+2', 150],
    ['-2Gi', -2 * 1024 ** 3],
  ])('parses %s', (input, expected) => {
    expect(parseQuantity(input)).toBeCloseTo(expected, 9);
  });

  it('returns null for an invalid quantity', () => {
    expect(parseQuantity('abc')).toBeNull();
    expect(parseQuantity('')).toBeNull();
  });
});

describe('parseCpuMillicores', () => {
  it.each<[string, number]>([
    ['500m', 500],
    ['1', 1000],
    ['1.5', 1500],
    ['2000m', 2000],
    ['0.1', 100],
    ['1e-3', 1],
    ['250000u', 250],
    ['1500000000n', 1500],
  ])('converts %s', (input, expected) => {
    expect(parseCpuMillicores(input)).toBe(expected);
  });

  it('returns null for an invalid quantity', () => {
    expect(parseCpuMillicores('two')).toBeNull();
  });
});

describe('parseMemoryBytes', () => {
  it('converts binary and decimal suffixes to bytes', () => {
    expect(parseMemoryBytes('1Gi')).toBe(1073741824);
    expect(parseMemoryBytes('1G')).toBe(1e9);
    expect(parseMemoryBytes('128974848')).toBe(128974848);
    expect(parseMemoryBytes('129e6')).toBe(129e6);
  });

  it('orders binary and decimal units correctly', () => {
    expect(parseMemoryBytes('1Gi')!).toBeGreaterThan(parseMemoryBytes('1G')!);
    expect(parseMemoryBytes('1024Mi')).toBe(parseMemoryBytes('1Gi'));
  });

  it('returns null for an invalid quantity', () => {
    expect(parseMemoryBytes('1 GB')).toBeNull();
  });
});

describe('display helpers', () => {
  it.each([
    ['1620m', '1.62'],
    ['16', '16'],
    ['0.5', '0.5'],
    ['500m', '0.5'],
    ['192000m', '192'],
    ['1e3', '1000'],
    ['bogus', 'bogus'],
  ])('formatCpuCores(%s) = %s', (input, expected) => {
    expect(formatCpuCores(input)).toBe(expected);
  });

  it.each([
    ['259033492Ki', '247 GiB'],
    ['24Gi', '24 GiB'],
    ['256Mi', '256 MiB'],
    ['1Gi', '1 GiB'],
    ['2560Mi', '2.5 GiB'],
    ['1G', '954 MiB'],
    ['512', '512 B'],
    ['bogus', 'bogus'],
  ])('formatMemoryQuantity(%s) = %s', (input, expected) => {
    expect(formatMemoryQuantity(input)).toBe(expected);
  });

  it.each([
    ['1620m', '16', 10],
    ['8', '16', 50],
    ['500m', '2', 25],
    ['2560Mi', '24Gi', 10],
    ['1Gi', '1Gi', 100],
    ['1', '0', 0],
    ['', '16', 0],
    ['bogus', '16', 0],
  ])('quantityPercent(%s, %s) = %d', (used, total, expected) => {
    expect(quantityPercent(used, total)).toBe(expected);
  });

  it('formats a negative byte count', () => {
    expect(formatBytes(-2048)).toBe('-2 KiB');
  });
});
