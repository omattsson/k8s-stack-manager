import { describe, it, expect } from 'vitest';
import { mergeWatcherStatus, execPodSetChanged } from '../mergeWatcherStatus';
import type { NamespaceStatus, ChartStatus } from '../../../types';

const chart = (release: string, extra: Partial<ChartStatus> = {}): ChartStatus => ({
  release_name: release,
  chart_name: release,
  status: 'healthy',
  deployments: [],
  pods: [],
  services: [],
  ...extra,
});

const ns = (charts: ChartStatus[], extra: Partial<NamespaceStatus> = {}): NamespaceStatus => ({
  namespace: 'stack-x',
  status: 'healthy',
  charts,
  last_checked: '2025-01-01T00:00:00Z',
  ...extra,
});

describe('mergeWatcherStatus', () => {
  it('returns the incoming status without a previous one', () => {
    const incoming = ns([chart('a')]);
    expect(mergeWatcherStatus(null, incoming)).toBe(incoming);
  });

  it('keeps exec_hints and the exec fields of charts with the same release', () => {
    const prev = ns(
      [chart('a', { exec_containers: ['app', 'side'], exec_default_container: 'app' }), chart('b')],
      { exec_hints: true },
    );
    const incoming = ns([chart('a', { status: 'degraded' }), chart('b'), chart('c')]);
    const merged = mergeWatcherStatus(prev, incoming);
    expect(merged.exec_hints).toBe(true);
    expect(merged.charts[0].status).toBe('degraded');
    expect(merged.charts[0].exec_containers).toEqual(['app', 'side']);
    expect(merged.charts[0].exec_default_container).toBe('app');
    expect(merged.charts[1].exec_containers).toBeUndefined();
    expect(merged.charts[2].exec_containers).toBeUndefined();
  });

  it('keeps events from the previous status', () => {
    const events = [{ type: 'Warning', reason: 'r', message: 'm', object: 'o', count: 1, last_seen: 't' }] as unknown as NamespaceStatus['events'];
    const merged = mergeWatcherStatus(ns([chart('a')], { events }), ns([chart('a')]));
    expect(merged.events).toBe(events);
    expect(merged.exec_hints).toBeUndefined();
  });
});

describe('mergeWatcherStatus with null charts', () => {
  it('does not throw when prev.charts is null', () => {
    const prev = { ...ns([]), charts: null } as unknown as NamespaceStatus;
    const merged = mergeWatcherStatus(prev, ns([chart('a')]));
    expect(merged.charts).toHaveLength(1);
  });

  it('keeps a null incoming charts list', () => {
    const incoming = { ...ns([]), charts: null } as unknown as NamespaceStatus;
    const merged = mergeWatcherStatus(ns([chart('a')], { exec_hints: true }), incoming);
    expect(merged.charts).toBeNull();
    expect(merged.exec_hints).toBe(true);
  });
});

describe('mergeWatcherStatus with execHintsSupported', () => {
  it('sets exec_hints without lists when prev is null', () => {
    const merged = mergeWatcherStatus(null, ns([chart('a')]), { execHintsSupported: true });
    expect(merged.exec_hints).toBe(true);
    expect(merged.charts[0].exec_containers).toBeUndefined();
  });

  it('sets exec_hints when prev has none', () => {
    const merged = mergeWatcherStatus(ns([chart('a')]), ns([chart('a')]), { execHintsSupported: true });
    expect(merged.exec_hints).toBe(true);
  });

  it('keeps the old behaviour without the option', () => {
    expect(mergeWatcherStatus(null, ns([chart('a')])).exec_hints).toBeUndefined();
    expect(mergeWatcherStatus(ns([chart('a')]), ns([chart('a')])).exec_hints).toBeUndefined();
  });
});

describe('execPodSetChanged', () => {
  const pod = (name: string, ready: boolean) => ({ name, ready }) as unknown as ChartStatus['pods'][number];

  it('is true without a previous status', () => {
    expect(execPodSetChanged(null, ns([chart('a')]))).toBe(true);
  });

  it('is false for the same ready pods in another order and for unready changes', () => {
    const prev = ns([chart('a', { pods: [pod('p1', true), pod('p2', true), pod('p3', false)] })]);
    const next = ns([chart('a', { pods: [pod('p2', true), pod('p1', true), pod('p3', false)] , status: 'degraded' })]);
    expect(execPodSetChanged(prev, next)).toBe(false);
    const next2 = ns([chart('a', { pods: [pod('p2', true), pod('p1', true), pod('p4', false)] })]);
    expect(execPodSetChanged(prev, next2)).toBe(false);
  });

  it('is true when a pod becomes ready or is replaced', () => {
    const prev = ns([chart('a', { pods: [pod('p1', false)] })]);
    expect(execPodSetChanged(prev, ns([chart('a', { pods: [pod('p1', true)] })]))).toBe(true);
    const r1 = ns([chart('a', { pods: [pod('p1', true)] })]);
    expect(execPodSetChanged(r1, ns([chart('a', { pods: [pod('p2', true)] })]))).toBe(true);
  });

  it('is true when a chart is added or removed', () => {
    expect(execPodSetChanged(ns([chart('a')]), ns([chart('a'), chart('b')]))).toBe(true);
    expect(execPodSetChanged(ns([chart('a'), chart('b')]), ns([chart('a')]))).toBe(true);
  });

  it('handles null charts', () => {
    const nul = { ...ns([]), charts: null } as unknown as NamespaceStatus;
    expect(execPodSetChanged(nul, nul)).toBe(false);
    expect(execPodSetChanged(nul, ns([chart('a')]))).toBe(true);
  });
});
