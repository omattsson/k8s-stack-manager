// Default cluster for the load tests. Instance create needs a default cluster.
import http from 'k6/http';

// Name of the cluster that ensureDefaultCluster creates.
export const LOADTEST_CLUSTER = 'k6-loadtest';

/**
 * Make sure that a default cluster exists, as ensureDefaultCluster in
 * frontend/e2e/helpers.ts does. An existing default cluster stays the default.
 * A new cluster uses the in-cluster config: outside a pod, the backend has no
 * cluster access with it.
 * @param {string} baseUrl - Backend URL
 * @param {object} headers - Headers of an admin request
 * @returns {{id: string, name: string}} The default cluster
 */
export function ensureDefaultCluster(baseUrl, headers) {
  for (let attempt = 0; attempt < 3; attempt++) {
    const listRes = http.get(`${baseUrl}/api/v1/clusters`, { headers, tags: { name: 'GET /clusters (setup)' } });
    if (listRes.status !== 200) {
      throw new Error(`setup: GET /clusters failed (status ${listRes.status})`);
    }
    const clusters = listRes.json() || [];
    const current = clusters.find((c) => c.is_default);
    if (current) return { id: current.id, name: current.name };

    const own = clusters.find((c) => c.name === LOADTEST_CLUSTER);
    if (own) {
      const res = http.post(`${baseUrl}/api/v1/clusters/${own.id}/default`, null, { headers });
      if (res.status !== 200) {
        throw new Error(`setup: could not make ${LOADTEST_CLUSTER} the default cluster (status ${res.status})`);
      }
      return { id: own.id, name: own.name };
    }

    const createRes = http.post(
      `${baseUrl}/api/v1/clusters`,
      JSON.stringify({
        name: LOADTEST_CLUSTER,
        description: 'Created by the k6 load tests. No cluster access outside a pod.',
        api_server_url: 'https://kubernetes.default.svc',
        use_in_cluster: true,
        is_default: true,
      }),
      { headers },
    );
    if (createRes.status === 201 || createRes.status === 200) {
      const created = createRes.json();
      return { id: created.id, name: created.name };
    }
    // 409: another run created it at the same time. Look again.
    if (createRes.status !== 409) {
      throw new Error(`setup: could not create the cluster ${LOADTEST_CLUSTER} (status ${createRes.status}): ${createRes.body}`);
    }
  }
  throw new Error('setup: no default cluster after 3 attempts');
}
