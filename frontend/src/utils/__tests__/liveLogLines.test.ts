import { describe, it, expect } from 'vitest';
import { liveLogLines } from '../liveLogLines';

describe('liveLogLines', () => {
  it.each([
    ['no output and no lines', '', [], []],
    ['undefined input', undefined, undefined, []],
    ['only stored output (page loaded, no new line yet)', 'a\nb\n', [], ['a', 'b']],
    ['only streamed lines (output not written yet)', '', ['a', 'b'], ['a', 'b']],
    ['page open from the start: output is a prefix of the stream', 'a\nb\n', ['a', 'b', 'c'], ['a', 'b', 'c']],
    ['page loaded during the run, overlap of one line', 'a\nb\nc\n', ['c', 'd'], ['a', 'b', 'c', 'd']],
    ['page loaded during the run, no overlap (gap)', 'a\nb\n', ['d', 'e'], ['a', 'b', 'd', 'e']],
    ['stream equals the output', 'a\nb\n', ['a', 'b'], ['a', 'b']],
    ['output without a final newline', 'a\nb', ['b', 'c'], ['a', 'b', 'c']],
    ['repeated lines use the longest overlap', 'x\nx\n', ['x', 'x', 'y'], ['x', 'x', 'y']],
  ])('%s', (_name, output, streamed, expected) => {
    expect(liveLogLines(output, streamed)).toEqual(expected);
  });
});
