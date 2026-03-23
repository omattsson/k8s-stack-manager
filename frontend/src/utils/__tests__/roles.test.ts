import { describe, it, expect } from 'vitest';
import { ROLE_RANK, hasAtLeastRole } from '../roles';

describe('ROLE_RANK', () => {
  it('defines user < devops < admin', () => {
    expect(ROLE_RANK['user']).toBeLessThan(ROLE_RANK['devops']);
    expect(ROLE_RANK['devops']).toBeLessThan(ROLE_RANK['admin']);
  });
});

describe('hasAtLeastRole', () => {
  describe('when user role meets or exceeds required role', () => {
    it('returns true for admin >= admin', () => {
      expect(hasAtLeastRole('admin', 'admin')).toBe(true);
    });

    it('returns true for admin >= devops', () => {
      expect(hasAtLeastRole('admin', 'devops')).toBe(true);
    });

    it('returns true for admin >= user', () => {
      expect(hasAtLeastRole('admin', 'user')).toBe(true);
    });

    it('returns true for devops >= devops', () => {
      expect(hasAtLeastRole('devops', 'devops')).toBe(true);
    });

    it('returns true for devops >= user', () => {
      expect(hasAtLeastRole('devops', 'user')).toBe(true);
    });

    it('returns true for user >= user', () => {
      expect(hasAtLeastRole('user', 'user')).toBe(true);
    });
  });

  describe('when user role is below required role', () => {
    it('returns false for user < devops', () => {
      expect(hasAtLeastRole('user', 'devops')).toBe(false);
    });

    it('returns false for user < admin', () => {
      expect(hasAtLeastRole('user', 'admin')).toBe(false);
    });

    it('returns false for devops < admin', () => {
      expect(hasAtLeastRole('devops', 'admin')).toBe(false);
    });
  });

  describe('edge cases', () => {
    it('returns false for undefined role', () => {
      expect(hasAtLeastRole(undefined, 'user')).toBe(false);
    });

    it('returns false for empty string role', () => {
      expect(hasAtLeastRole('', 'user')).toBe(false);
    });

    it('returns false for unknown role string', () => {
      expect(hasAtLeastRole('superuser', 'user')).toBe(false);
    });

    it('returns false for unknown required role', () => {
      expect(hasAtLeastRole('admin', 'superadmin')).toBe(false);
    });

    it('returns false when both roles are unknown (0 >= 999 is false)', () => {
      // unknown user role gets rank 0, unknown required role gets rank 999
      expect(hasAtLeastRole('foo', 'bar')).toBe(false);
    });
  });
});
