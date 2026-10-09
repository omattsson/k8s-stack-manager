import { describe, it, expect } from 'vitest';
import {
  compareVersions,
  isNoPublishedVersionError,
  nextPatchVersion,
  suggestPublishVersion,
} from '../templateVersion';
import { getApiErrorInfo } from '../apiError';

describe('nextPatchVersion', () => {
  it.each([
    ['1.2.3', '1.2.4'],
    ['v1.2.3', '1.2.4'],
    ['1.2', '1.2.1'],
    ['2', '2.0.1'],
    ['1.0.0-beta', ''],
    ['latest', ''],
  ])('%s gives %s', (input, expected) => {
    expect(nextPatchVersion(input)).toBe(expected);
  });
});

describe('compareVersions', () => {
  it('compares numerically', () => {
    expect(compareVersions('1.10.0', '1.9.0')).toBeGreaterThan(0);
    expect(compareVersions('1.0', '1.0.0')).toBe(0);
    expect(compareVersions('1.0.0', '1.0.1')).toBeLessThan(0);
    expect(compareVersions('abc', '1.0.0')).toBeNull();
  });
});

describe('suggestPublishVersion', () => {
  it('uses the working copy version without a release', () => {
    expect(suggestPublishVersion('1.0.0', null)).toBe('1.0.0');
    expect(suggestPublishVersion('', undefined)).toBe('1.0.0');
  });

  it('uses the working copy version when it is newer than the release', () => {
    expect(suggestPublishVersion('1.2.0', '1.1.0')).toBe('1.2.0');
  });

  it('uses the next patch of the release otherwise', () => {
    expect(suggestPublishVersion('1.1.0', '1.1.0')).toBe('1.1.1');
    expect(suggestPublishVersion('1.0.0', '1.1.0')).toBe('1.1.1');
  });

  it('uses the released version when there are no unpublished changes', () => {
    expect(suggestPublishVersion('1.0.0', '1.0.0', false)).toBe('1.0.0');
    expect(suggestPublishVersion('1.0.0', '1.0.0', true)).toBe('1.0.1');
  });

  it('falls back to the working copy version for a non-numeric release', () => {
    expect(suggestPublishVersion('2.0.0', 'latest')).toBe('2.0.0');
  });
});

describe('isNoPublishedVersionError', () => {
  it('detects the 409 no-published-version response', () => {
    expect(isNoPublishedVersionError({ response: { status: 409, data: { error: 'Template has no published version' } } })).toBe(true);
  });

  it('ignores other errors', () => {
    expect(isNoPublishedVersionError({ response: { status: 409, data: { error: 'Name already taken' } } })).toBe(false);
    expect(isNoPublishedVersionError({ response: { status: 500, data: { error: 'Template has no published version' } } })).toBe(false);
    expect(isNoPublishedVersionError(new Error('network'))).toBe(false);
  });
});

describe('getApiErrorInfo', () => {
  it('reads status and message', () => {
    expect(getApiErrorInfo({ response: { status: 409, data: { error: 'x' } } })).toEqual({ status: 409, message: 'x' });
    expect(getApiErrorInfo({ response: { status: 400, data: { message: 'y' } } })).toEqual({ status: 400, message: 'y' });
    expect(getApiErrorInfo({ response: { status: 500, data: 'text' } })).toEqual({ status: 500, message: undefined });
    expect(getApiErrorInfo(null)).toEqual({});
  });
});
