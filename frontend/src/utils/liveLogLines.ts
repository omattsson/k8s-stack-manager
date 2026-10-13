/**
 * Lines of a running deploy log for display: the stored output (written by
 * the server every few seconds during the operation) followed by the lines
 * that arrived over the WebSocket.
 *
 * The two can overlap: a page that was open from the start has every line in
 * `streamed`, and a page that loaded the log during the operation has the
 * older lines only in `output`. The longest end of `output` that is also the
 * start of `streamed` is shown once.
 *
 * @param output - Stored output of the log (lines separated by "\n")
 * @param streamed - Lines received over the WebSocket since the page loaded
 * @returns The lines to show, without the overlap
 */
export const liveLogLines = (output: string | undefined, streamed: string[] | undefined): string[] => {
  const stored = output ? output.replace(/\n$/, '').split('\n') : [];
  const live = streamed ?? [];
  if (stored.length === 0) return live;
  if (live.length === 0) return stored;

  for (let overlap = Math.min(stored.length, live.length); overlap > 0; overlap--) {
    let match = true;
    for (let i = 0; i < overlap; i++) {
      if (stored[stored.length - overlap + i] !== live[i]) {
        match = false;
        break;
      }
    }
    if (match) return [...stored, ...live.slice(overlap)];
  }
  return [...stored, ...live];
};
