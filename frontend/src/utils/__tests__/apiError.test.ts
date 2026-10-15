import { describe, it, expect } from 'vitest';
import { describeApiError, describeNameConflict } from '../apiError';

describe('describeApiError', () => {
  it('returns the fallback when there is no HTTP response', async () => {
    expect(await describeApiError(new Error('Network Error'), 'Failed')).toBe('Failed');
    expect(await describeApiError(null, 'Failed')).toBe('Failed');
  });

  it('includes the status and the server error from a JSON body', async () => {
    const err = { response: { status: 400, statusText: 'Bad Request', data: { error: 'memory_limit is invalid' } } };
    expect(await describeApiError(err, 'Failed to save')).toBe('Failed to save (HTTP 400: memory_limit is invalid)');
  });

  it('reads the server error from a Blob body', async () => {
    const data = new Blob([JSON.stringify({ error: 'Instance not found' })], { type: 'application/json' });
    const err = { response: { status: 404, statusText: 'Not Found', data } };
    expect(await describeApiError(err, 'Failed to export values')).toBe('Failed to export values (HTTP 404: Instance not found)');
  });

  it('reads a JSON string body', async () => {
    const err = { response: { status: 403, data: '{"error":"Forbidden"}' } };
    expect(await describeApiError(err, 'Failed')).toBe('Failed (HTTP 403: Forbidden)');
  });

  it('uses a short plain text body', async () => {
    const err = { response: { status: 502, data: 'Bad gateway' } };
    expect(await describeApiError(err, 'Failed')).toBe('Failed (HTTP 502: Bad gateway)');
  });

  it('falls back to the status text when the body has no message', async () => {
    const err = { response: { status: 500, statusText: 'Internal Server Error', data: new Blob(['']) } };
    expect(await describeApiError(err, 'Failed')).toBe('Failed (HTTP 500: Internal Server Error)');
  });

  it('shows only the status when nothing else is known', async () => {
    const err = { response: { status: 418, data: {} } };
    expect(await describeApiError(err, 'Failed')).toBe('Failed (HTTP 418)');
  });
});

describe('describeNameConflict', () => {
  const conflict = (data: unknown, status = 409) => ({ response: { status, data } });

  it('joins the message and the free names', () => {
    const err = conflict({ error: 'instance name already exists', message: 'A stack named "demo" already exists on this cluster (owner: carol)', suggestions: ['demo-2', 'demo-3'] });
    expect(describeNameConflict(err)).toBe('A stack named "demo" already exists on this cluster (owner: carol). Free names: demo-2, demo-3.');
  });

  it('returns the message alone when no name is free', () => {
    expect(describeNameConflict(conflict({ message: 'Namespace "stack-a-bob" is already in use', suggestions: [] }))).toBe('Namespace "stack-a-bob" is already in use.');
  });

  it('returns null for other errors', () => {
    expect(describeNameConflict(conflict({ error: 'Template has no published version' }))).toBeNull();
    expect(describeNameConflict(conflict({ message: 'x', suggestions: ['a'] }, 400))).toBeNull();
    expect(describeNameConflict(new Error('Network Error'))).toBeNull();
    expect(describeNameConflict(null)).toBeNull();
  });
});
