import { describe, it, expect } from 'vitest';
import { validateInstanceName, suggestCloneName, INSTANCE_NAME_MAX_LENGTH } from '../instanceName';

describe('validateInstanceName', () => {
  it.each(['a', 'my-app', 'app1', '1app', 'a-b-c-9', 'x'.repeat(INSTANCE_NAME_MAX_LENGTH)])(
    'accepts %s',
    (name) => {
      expect(validateInstanceName(name)).toBeNull();
    },
  );

  it.each([
    ['', 'Instance name is required'],
    ['My-App', 'Use lowercase letters only'],
    ['my app', 'Use only lowercase letters (a-z), digits (0-9) and hyphens (-)'],
    ['my_app', 'Use only lowercase letters (a-z), digits (0-9) and hyphens (-)'],
    ['app (Copy)', 'Use lowercase letters only'],
    ['my.app', 'Use only lowercase letters (a-z), digits (0-9) and hyphens (-)'],
    ['-app', 'Start and end with a letter or a digit'],
    ['app-', 'Start and end with a letter or a digit'],
    ['x'.repeat(INSTANCE_NAME_MAX_LENGTH + 1), `Use ${INSTANCE_NAME_MAX_LENGTH} characters or fewer`],
  ])('rejects %j', (name, message) => {
    expect(validateInstanceName(name)).toBe(message);
  });
});

describe('suggestCloneName', () => {
  it('appends -copy', () => {
    expect(suggestCloneName('my-app')).toBe('my-app-copy');
  });

  it('makes a legacy name valid', () => {
    const suggestion = suggestCloneName('My App (Copy)');
    expect(suggestion).toBe('my-app-copy-copy');
    expect(validateInstanceName(suggestion)).toBeNull();
  });

  it('stays within the maximum length', () => {
    const suggestion = suggestCloneName('a'.repeat(INSTANCE_NAME_MAX_LENGTH));
    expect(suggestion.length).toBeLessThanOrEqual(INSTANCE_NAME_MAX_LENGTH);
    expect(validateInstanceName(suggestion)).toBeNull();
  });
});
