/** Delay before the object URL is revoked. */
const REVOKE_DELAY_MS = 1000;

/**
 * Save a Blob as a file through a temporary object URL and anchor element.
 * The Blob is passed to the browser unchanged (no text conversion).
 * @param blob - File content
 * @param filename - Suggested download file name
 */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  anchor.style.display = 'none';
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
    // Revoke later: some browsers cancel the download when the URL is
    // revoked too soon after the click.
    setTimeout(() => URL.revokeObjectURL(url), REVOKE_DELAY_MS);
  }
}
