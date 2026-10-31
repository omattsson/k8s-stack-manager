import type { NamespaceStatus } from '../../types';

/** Options of {@link mergeWatcherStatus}. */
export interface MergeWatcherOptions {
  /**
   * The server sent `exec_hints: true` for this instance before. A merged status
   * without hints then gets `exec_hints: true` with no per-chart lists (no exec
   * lines until the next getPods() call), not the fallback of older servers.
   */
  execHintsSupported?: boolean;
}

/**
 * Merge a namespace status from a WebSocket `instance.status` message into the
 * status from the last getPods() call. The watcher broadcasts to all users, so
 * its status has no events and never the exec fields (`exec_hints`,
 * `exec_containers`, `exec_default_container`). Keep those from `prev`
 * (charts match by release name); the next getPods() call refreshes them.
 * `charts` can be null (a namespace without labelled resources).
 * @param prev - Status held by the page, or null
 * @param incoming - Status of the WebSocket message
 * @param options - See {@link MergeWatcherOptions}
 * @returns The status to show
 */
export const mergeWatcherStatus = (
  prev: NamespaceStatus | null,
  incoming: NamespaceStatus,
  options: MergeWatcherOptions = {},
): NamespaceStatus => {
  if (!prev) {
    return options.execHintsSupported ? { ...incoming, exec_hints: true } : incoming;
  }
  const prevCharts = new Map((prev.charts ?? []).map((c) => [c.release_name, c]));
  const merged: NamespaceStatus = {
    ...incoming,
    events: incoming.events?.length ? incoming.events : prev.events,
    charts: incoming.charts
      ? incoming.charts.map((chart) => {
          const old = prevCharts.get(chart.release_name);
          if (!old) return chart;
          return {
            ...chart,
            ...(old.exec_containers ? { exec_containers: old.exec_containers } : {}),
            ...(old.exec_default_container ? { exec_default_container: old.exec_default_container } : {}),
          };
        })
      : incoming.charts,
  };
  if (prev.exec_hints !== undefined) merged.exec_hints = prev.exec_hints;
  else if (options.execHintsSupported) merged.exec_hints = true;
  return merged;
};

/**
 * Describe the part of a status that exec hints depend on: the charts and, per
 * chart, the names of the ready pods.
 */
const execPodSignature = (status: NamespaceStatus): string =>
  (status.charts ?? [])
    .map((c) => `${c.release_name}:${(c.pods ?? []).filter((p) => p.ready).map((p) => p.name).sort().join(',')}`)
    .sort()
    .join('|');

/**
 * Tell whether the exec-relevant pod set differs between two statuses: the set
 * of charts (release names) or the ready pod names of a chart. Then the exec
 * hints of the page can be stale and getPods() should run again.
 * @param prev - Status held by the page, or null
 * @param incoming - Status of the WebSocket message
 * @returns True when the charts or their ready pods differ (also for a null prev)
 */
export const execPodSetChanged = (prev: NamespaceStatus | null, incoming: NamespaceStatus): boolean =>
  !prev || execPodSignature(prev) !== execPodSignature(incoming);
