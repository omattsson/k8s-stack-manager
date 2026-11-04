import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect } from 'vitest';
import { checkYamlValues } from '../yamlValidation';

/**
 * The fixture is shared with `backend/internal/helm/validate_values_test.go`.
 * That test checks `api` against the parser of the deploy, this test checks
 * `browser`: `ok` (no problem), `hint` (shown, does not block, the API decides)
 * or `block`. The rule: when the API accepts a value, the browser must not block it.
 */
type Result = 'ok' | 'hint' | 'block';
interface OverrideValuesCase { name: string; input: string; api: boolean; browser: Result }
const CASES = JSON.parse(readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../../../../backend/internal/helm/testdata/override_values_cases.json'),
  'utf8',
)) as OverrideValuesCase[];

const resultOf = (input: string): Result => {
  const problem = checkYamlValues(input);
  if (!problem) return 'ok';
  return problem.blocking ? 'block' : 'hint';
};

describe('checkYamlValues', () => {
  it('reads the shared cases', () => {
    expect(CASES.length).toBeGreaterThan(50);
    expect(CASES.some((c) => c.name === 'unquoted template' && !c.api)).toBe(true);
  });

  it.each(CASES)('$name: $browser', ({ input, browser }) => {
    expect(resultOf(input), JSON.stringify(checkYamlValues(input))).toBe(browser);
  });

  it('never blocks a value that the API accepts', () => {
    expect(CASES.filter((c) => c.api && resultOf(c.input) === 'block').map((c) => c.name)).toEqual([]);
  });

  it('blocks the examples of the issue', () => {
    expect(resultOf('foo: [unclosed\n  bar: : baz')).toBe('block');
    expect(resultOf('tag: {{.Branch}}')).toBe('block');
    expect(resultOf('just-a-string')).toBe('block');
  });

  it('tells the user to quote an unquoted template value, in one line', () => {
    const message = checkYamlValues('tag: {{.Branch}}')?.message ?? '';
    expect(message).toContain('Put a template value in quotes, for example tag: "{{.Branch}}"');
    expect(message).not.toContain('\n');
    expect(checkYamlValues('a: b: c')?.message).not.toContain('\n');
  });

  it.each([
    ['no template', 'a: b: c'],
    ['a quoted template beside another error', 'a: "{{.Branch}}"\na: 2'],
    ['a template in a quoted string beside another error', 'a: "x: {{.Branch}}"\na: 2'],
    ['a template in a comment beside another error', '# tag: {{.Branch}}\na: b: c'],
    ['a template inside a plain scalar beside another error', 'image: repo:{{.Branch}}\nb: c: d'],
  ])('does not give the quote advice for %s', (_name, value) => {
    expect(checkYamlValues(value)?.message).not.toContain('quotes');
  });

  it('explains a root that is not a map', () => {
    expect(checkYamlValues('just-a-string')?.message).toMatch(/must be a map/);
    expect(checkYamlValues('- a')?.message).toMatch(/must be a map/);
  });

  it('says that the deploy uses only the first document', () => {
    expect(checkYamlValues('a: 1\n---\nb: 2')?.message).toMatch(/only the first YAML document/);
    expect(checkYamlValues('--- # c\na: 1')).toBeNull();
  });

  it('still checks the first document after a leading marker or comment', () => {
    expect(resultOf('---\na: b: c')).toBe('block');
    expect(resultOf('# comment\n---\na: b: c')).toBe('block');
  });
});
