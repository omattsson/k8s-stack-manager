import { describe, it, expect } from 'vitest';
import { mergeInstanceUpdate } from '../instanceState';
import type { StackInstance } from '../../types';

const base: StackInstance = {
  id: 'i1',
  stack_definition_id: 'd1',
  name: 'demo',
  namespace: 'stack-demo-owner',
  owner_id: 'owner',
  branch: 'master',
  status: 'running',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
};

describe('mergeInstanceUpdate', () => {
  it('returns the response when there is no previous state', () => {
    const updated = { ...base, expires_at: '2026-10-02T00:00:00Z' };
    expect(mergeInstanceUpdate(null, updated)).toBe(updated);
  });

  it('keeps only the follow state when the response does not have it', () => {
    const prev = {
      ...base,
      following: true,
      follower_count: 3,
      owner_username: 'alice',
      cluster_name: 'dev',
    };
    const updated = { ...base, expires_at: '2026-10-02T00:00:00Z', owner_username: 'alice' };
    const merged = mergeInstanceUpdate(prev, updated);
    expect(merged.expires_at).toBe('2026-10-02T00:00:00Z');
    expect(merged.following).toBe(true);
    expect(merged.follower_count).toBe(3);
    expect(merged.owner_username).toBe('alice');
    // An absent name means empty: it is not kept.
    expect(merged.cluster_name).toBeUndefined();
  });

  it('clears values_drift when the response does not have it', () => {
    const prev = { ...base, values_drift: true };
    const updated = { ...base, expires_at: '2026-10-02T00:00:00Z' };
    expect(mergeInstanceUpdate(prev, updated).values_drift).toBeUndefined();
  });

  it('uses the computed fields of the response when it has them', () => {
    const prev = { ...base, following: true, follower_count: 3 };
    const updated = { ...base, following: false, follower_count: 2 };
    const merged = mergeInstanceUpdate(prev, updated);
    expect(merged.following).toBe(false);
    expect(merged.follower_count).toBe(2);
  });

  it('keeps a cleared stored field cleared', () => {
    const prev = { ...base, ttl_minutes: 60, expires_at: '2026-10-02T00:00:00Z' };
    const updated = { ...base, ttl_minutes: 0 };
    expect(mergeInstanceUpdate(prev, updated).expires_at).toBeUndefined();
  });

  it('does not merge a different instance', () => {
    const prev = { ...base, following: true };
    const updated = { ...base, id: 'i2' };
    expect(mergeInstanceUpdate(prev, updated).following).toBeUndefined();
  });
});
