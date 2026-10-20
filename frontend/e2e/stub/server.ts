import fs from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { STUB_HTTP_PORT, STUB_HTTP_URL, STUB_HTTPS_PORT, type StubRecord } from './client';

/**
 * Test stub of the e2e specs. global-setup.ts starts it once for each
 * Playwright run, and its teardown stops it.
 *
 * HTTP on 127.0.0.1:18090: the subscriber of the custom
 * actions in e2e/fixtures/hooks-e2e.json (#588).
 *   POST /actions/echo      200 with the parameters of the request; 409
 *                           {"message": "Refused by the e2e stub"} when the
 *                           parameter target is "refuse" (#640)
 *   POST /actions/job       202 with a job_id; GET /jobs/{job_id}/log serves
 *                           its log: two polls "running", then "succeeded"
 *   POST /actions/redirect  302 to a URL with a token, HTML body
 *
 * HTTPS on 127.0.0.1:18443: a webhook receiver for the notification
 * channels (#592). POST /webhook/<key> records the request and answers 200. The backend
 * accepts only https:// webhooks and checks the certificate, so the receiver
 * needs the certificate of `make e2e-prepare` (backend/tmp/e2e-stub). Without
 * it, only the HTTP part starts.
 *
 * Control (HTTP): GET /__health ({"https": bool}) and GET /__records?prefix=
 * (the requests whose path starts with the prefix, oldest first).
 */

/** Requests that the stub received. Specs filter them by a unique path or instance ID. */
const records: StubRecord[] = [];
/** The stub keeps the newest records only. */
const MAX_RECORDS = 5000;

/** Jobs of /actions/job: the instance of the job and the number of log polls. */
const jobs = new Map<string, { instanceId: string; polls: number }>();

/** Log lines of a job; poll n shows the first n lines. */
const jobLines = (id: string) => [`Job ${id} started`, 'Step 1 of 2 done', 'Step 2 of 2 done'];

const MAX_BODY_BYTES = 1024 * 1024;

function readBody(req: http.IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    req.on('data', (c: Buffer) => {
      size += c.length;
      if (size > MAX_BODY_BYTES) {
        reject(new Error('body too large'));
        req.destroy();
        return;
      }
      chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

async function record(req: http.IncomingMessage, url: URL): Promise<StubRecord> {
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(req.headers)) {
    if (v !== undefined) headers[k] = Array.isArray(v) ? v.join(', ') : v;
  }
  const rec: StubRecord = {
    at: Date.now(),
    method: req.method ?? '',
    url: req.url ?? '',
    path: url.pathname,
    query: Object.fromEntries(url.searchParams.entries()),
    headers,
    body: await readBody(req),
  };
  records.push(rec);
  if (records.length > MAX_RECORDS) records.splice(0, records.length - MAX_RECORDS);
  return rec;
}

function send(res: http.ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) {
  const text = typeof body === 'string' ? body : JSON.stringify(body);
  res.writeHead(status, {
    'Content-Type': typeof body === 'string' ? 'text/plain; charset=utf-8' : 'application/json',
    ...headers,
  });
  res.end(text);
}

function parseJSON(text: string): Record<string, unknown> {
  try {
    const v: unknown = JSON.parse(text);
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

/** Instance ID of an action request envelope. */
function instanceIdOf(envelope: Record<string, unknown>): string {
  const inst = envelope.instance as Record<string, unknown> | undefined;
  return typeof inst?.id === 'string' ? inst.id : '';
}

let httpsUp = false;

async function handleHttp(req: http.IncomingMessage, res: http.ServerResponse) {
  const url = new URL(req.url ?? '/', STUB_HTTP_URL);

  if (req.method === 'GET' && url.pathname === '/__health') {
    send(res, 200, { ok: true, https: httpsUp });
    return;
  }
  if (req.method === 'GET' && url.pathname === '/__records') {
    const prefix = url.searchParams.get('prefix') ?? '/';
    send(res, 200, records.filter((r) => r.path.startsWith(prefix)));
    return;
  }

  const rec = await record(req, url);

  if (req.method === 'POST' && url.pathname === '/actions/echo') {
    const envelope = parseJSON(rec.body);
    const params = (envelope.parameters ?? {}) as Record<string, unknown>;
    if (params.target === 'refuse') {
      send(res, 409, { message: 'Refused by the e2e stub' });
      return;
    }
    send(res, 200, { ok: true, echo: params, request_id: envelope.request_id ?? '' });
    return;
  }
  if (req.method === 'POST' && url.pathname === '/actions/job') {
    const id = `job-${randomUUID().slice(0, 8)}`;
    jobs.set(id, { instanceId: instanceIdOf(parseJSON(rec.body)), polls: 0 });
    send(res, 202, { job_id: id, status: 'started' });
    return;
  }
  if (req.method === 'POST' && url.pathname === '/actions/redirect') {
    // A 3xx with a non-JSON body: the backend answers 502 and never shows this URL.
    res.writeHead(302, { Location: `${STUB_HTTP_URL}/elsewhere?token=stub-redirect-secret`, 'Content-Type': 'text/html' });
    res.end('<html><body>Moved to the stub redirect target</body></html>');
    return;
  }
  const jobMatch = /^\/jobs\/([^/]+)\/log$/.exec(url.pathname);
  if (req.method === 'GET' && jobMatch) {
    const id = jobMatch[1];
    const job = jobs.get(id);
    // The contract: a job of another instance is not found.
    if (!job || job.instanceId !== (url.searchParams.get('instance_id') ?? '')) {
      send(res, 404, 'job not found');
      return;
    }
    job.polls += 1;
    const lines = jobLines(id);
    const text = lines.slice(0, Math.min(job.polls, lines.length)).join('\n') + '\n';
    const offset = Math.max(0, Number(url.searchParams.get('offset') ?? '0') || 0);
    const status = job.polls >= lines.length ? 'succeeded' : 'running';
    send(res, 200, text.slice(offset), { 'X-Job-Status': status, 'X-Log-Offset': String(text.length) });
    return;
  }
  send(res, 404, 'not found');
}

async function handleHttps(req: http.IncomingMessage, res: http.ServerResponse) {
  const url = new URL(req.url ?? '/', `https://127.0.0.1:${STUB_HTTPS_PORT}`);
  await record(req, url);
  if (req.method === 'POST' && url.pathname.startsWith('/webhook/')) {
    send(res, 200, { ok: true });
    return;
  }
  send(res, 404, 'not found');
}

function listen(server: http.Server, port: number, name: string): Promise<boolean> {
  return new Promise((resolve) => {
    const onError = (err: NodeJS.ErrnoException) => {
      console.warn(`\nWARNING: the e2e stub (${name}) cannot listen on 127.0.0.1:${port}: ${err.code ?? err.message}. ` +
        'The specs that need it skip.\n');
      resolve(false);
    };
    server.once('error', onError);
    server.listen(port, '127.0.0.1', () => {
      server.off('error', onError);
      resolve(true);
    });
  });
}

const wrap = (fn: (req: http.IncomingMessage, res: http.ServerResponse) => Promise<void>) =>
  (req: http.IncomingMessage, res: http.ServerResponse) => {
    fn(req, res).catch((err: unknown) => {
      if (!res.headersSent) send(res, 500, `stub error: ${String(err)}`);
      else res.destroy();
    });
  };

/** Directory with stub.key and trust/stub.pem (`make e2e-prepare`). */
function certDir(): string {
  return process.env.E2E_STUB_CERT_DIR || path.resolve(process.cwd(), '../backend/tmp/e2e-stub');
}

/** Start the stub. Returns a function that stops it. */
export async function startStub(): Promise<() => Promise<void>> {
  const servers: http.Server[] = [];
  const plain = http.createServer(wrap(handleHttp));
  if (await listen(plain, STUB_HTTP_PORT, 'HTTP')) servers.push(plain);

  const dir = certDir();
  const keyFile = path.join(dir, 'stub.key');
  const certFile = path.join(dir, 'trust', 'stub.pem');
  if (fs.existsSync(keyFile) && fs.existsSync(certFile)) {
    const tls = https.createServer({ key: fs.readFileSync(keyFile), cert: fs.readFileSync(certFile) }, wrap(handleHttps));
    httpsUp = await listen(tls, STUB_HTTPS_PORT, 'HTTPS');
    if (httpsUp) servers.push(tls);
  } else {
    console.warn(`\nNOTE: no stub certificate in ${dir} (run make e2e-prepare). The HTTPS webhook receiver does not start.\n`);
  }

  return async () => {
    await Promise.all(servers.map((s) => new Promise<void>((resolve) => {
      s.closeAllConnections();
      s.close(() => resolve());
    })));
  };
}
