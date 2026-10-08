import { describe, it, expect } from 'vitest';
import { ROLE_RANK, hasAtLeastRole, canModifyInstance } from '../roles';

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

describe('canModifyInstance', () => {
  const instance = { owner_id: 'u-alice' };

  it('returns true for the owner with role user', () => {
    expect(canModifyInstance({ id: 'u-alice', role: 'user' }, instance)).toBe(true);
  });

  it('returns true for an admin who is not the owner', () => {
    expect(canModifyInstance({ id: 'u-bob', role: 'admin' }, instance)).toBe(true);
  });

  it('returns true for a devops user who is not the owner', () => {
    expect(canModifyInstance({ id: 'u-bob', role: 'devops' }, instance)).toBe(true);
  });

  it('returns false for another user with role user', () => {
    expect(canModifyInstance({ id: 'u-bob', role: 'user' }, instance)).toBe(false);
  });

  it('returns false for an unknown role that is not the owner', () => {
    expect(canModifyInstance({ id: 'u-bob', role: 'superuser' }, instance)).toBe(false);
  });

  it('returns false when there is no user', () => {
    expect(canModifyInstance(null, instance)).toBe(false);
    expect(canModifyInstance(undefined, instance)).toBe(false);
  });

  it('returns false when there is no instance', () => {
    expect(canModifyInstance({ id: 'u-alice', role: 'user' }, null)).toBe(false);
  });

  it('returns false for an empty user id against an empty owner id', () => {
    expect(canModifyInstance({ id: '', role: 'user' }, { owner_id: '' })).toBe(false);
  });
});
