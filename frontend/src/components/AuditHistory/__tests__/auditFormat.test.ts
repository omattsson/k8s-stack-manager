import { describe, it, expect } from 'vitest';
import { detailRows } from '../auditFormat';
import type { AuditLog } from '../../../types';

const base: AuditLog = {
  id: 'a1', user_id: 'u1', username: 'alice', action: 'update', entity_type: 'cluster',
  entity_id: 'c1', details: '', timestamp: '2026-11-13T10:00:00Z',
};

describe('detailRows', () => {
  it('lists the context fields that are set', () => {
    const rows = detailRows({ ...base, client_ip: '198.51.100.7', request_id: 'r1', auth_method: 'api_key', api_key_id: 'k1' });
    expect(rows).toEqual([
      { label: 'Client IP', value: '198.51.100.7' },
      { label: 'Request ID', value: 'r1' },
      { label: 'Auth method', value: 'api_key' },
      { label: 'API key ID', value: 'k1' },
    ]);
  });

  it('gives no row for absent context fields', () => {
    expect(detailRows(base)).toEqual([]);
  });

  it('formats changes', () => {
    const details = JSON.stringify({
      changes: [
        { field: 'allow_exec', old: false, new: true },
        { field: 'replicas' },
        { field: 'kubeconfig', redacted: true },
        { field: 'ttl_minutes', new: 60 },
      ],
      changes_truncated: true,
    });
    expect(detailRows({ ...base, details })).toEqual([
      { label: 'allow_exec', value: 'false to true' },
      { label: 'replicas', value: 'changed' },
      { label: 'kubeconfig', value: 'changed (hidden)' },
      { label: 'ttl_minutes', value: '(none) to 60' },
      { label: 'changes_truncated', value: 'true' },
    ]);
  });

  it('lists other detail keys', () => {
    expect(detailRows({ ...base, details: '{"reason":"password_mismatch","port":80,"bulk":true}' })).toEqual([
      { label: 'reason', value: 'password_mismatch' },
      { label: 'port', value: '80' },
      { label: 'bulk', value: 'true' },
    ]);
  });

  it('keeps details that are not JSON as one row', () => {
    expect(detailRows({ ...base, details: 'Instance expired after TTL' })).toEqual([
      { label: 'Details', value: 'Instance expired after TTL' },
    ]);
  });
});
