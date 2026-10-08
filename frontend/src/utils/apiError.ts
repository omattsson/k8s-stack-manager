/** Shape of an HTTP error as thrown by axios (checked structurally). */
interface HttpErrorLike {
  response?: {
    status?: number;
    statusText?: string;
    data?: unknown;
  };
}

/** Read the server error message from a response body (JSON object, JSON text, plain text or Blob). */
async function readServerMessage(data: unknown): Promise<string | null> {
  let body: unknown = data;
  if (typeof Blob !== 'undefined' && body instanceof Blob) {
    try {
      body = await body.text();
    } catch {
      return null;
    }
  }
  if (typeof body === 'string') {
    const text = body.trim();
    if (!text) return null;
    try {
      body = JSON.parse(text);
    } catch {
      // Plain text body (not JSON). Use it only when it is short.
      return text.length <= 200 ? text : null;
    }
  }
  if (body && typeof body === 'object') {
    const obj = body as { error?: unknown; message?: unknown };
    if (typeof obj.error === 'string' && obj.error) return obj.error;
    if (typeof obj.message === 'string' && obj.message) return obj.message;
  }
  return null;
}

/**
 * Build a user-facing error message that includes the HTTP status and the
 * server error message. Reads Blob bodies (from `responseType: 'blob'`
 * requests) as text first.
 * @param error - The thrown error (usually an AxiosError)
 * @param fallback - Message prefix, for example "Failed to export values"
 * @returns For example "Failed to export values (HTTP 404: Instance not found)", or the fallback when there is no HTTP response
 */
export async function describeApiError(error: unknown, fallback: string): Promise<string> {
  const response = (error as HttpErrorLike | null)?.response;
  if (!response || typeof response.status !== 'number') return fallback;
  const serverMessage = await readServerMessage(response.data);
  const reason = serverMessage ?? response.statusText;
  return reason
    ? `${fallback} (HTTP ${response.status}: ${reason})`
    : `${fallback} (HTTP ${response.status})`;
}
