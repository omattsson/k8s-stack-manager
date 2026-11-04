import { load as loadYaml } from 'js-yaml';

/** A problem that the browser check found in override values. */
export interface YamlProblem {
  message: string;
  /**
   * True when the deploy parser refuses such a value as well, so a save can be
   * blocked. False when the parsers can differ: the API decides on save.
   */
  blocking: boolean;
}

/** A document marker at the start of a line: another document follows, or the first one ends. */
const DOCUMENT_MARKER = /^(?:---|\.\.\.)(?:\s|$)/m;
/** Blank lines, comments, directives and the `---` before the first document. */
const FIRST_DOCUMENT_START = /^(?:[ \t]*(?:#.*)?\r?\n|%.*\r?\n)*(?:---(?=\s|$))?/;

/** js-yaml 5 refuses a text without a document (only comments). For the deploy that is no values. */
const NO_DOCUMENT_REASON = /^expected a document/;

/**
 * js-yaml errors that may block a save. This is an allow-list, because the
 * parsers differ in many other places: `yaml.v3` accepts a multi-line quoted
 * string at column 0 ("deficient indentation"), `"it\'s"` ("unknown escape
 * sequence"), both `~:` and `null:` as keys ("duplicated mapping key"), unknown
 * tags and text after a closed root map ("end of the stream"). When js-yaml
 * changes its wording, a reason stops blocking and the API still decides.
 *
 * The list holds the reasons that yaml.v3 refused in practically every case of
 * a differential run over 500,000 mutated values. It still blocked 4 of the
 * 169,641 that yaml.v3 accepts, all nonsense: a null key with a template as its
 * value (`~: {{.Branch}}`, the deploy drops that key) and `z: ...w:}` in a flow
 * map. `backend/internal/helm/testdata/override_values_cases.json` holds the
 * realistic cases, and both test suites read it.
 */
const BLOCKING_REASONS = [
  /^object-based map does not support complex keys/, // tag: {{.Branch}}
  /^bad indentation of a mapping entry/,
  /^bad indentation of a sequence entry/,
  /^can not read a block mapping entry/,
  /^expected ':' after a mapping key/,
  /^missed comma between flow collection entries/,
  /^a whitespace character is expected after the key-value separator/,
  /^unexpected end of the stream within/,
  /^unidentified alias/,
  /^tab characters must not be used in indentation/,
  /^expected hexadecimal character/,
];

/**
 * Forms that yaml.v3 accepts and that give a blocking reason in js-yaml: a
 * comment without a space after a closing quote (`a: "x"# text`), a lone dash
 * as a flow value (`a: {b: -}`), and a colon right after an alias (`*x: bar`).
 */
const ACCEPTED_BY_DEPLOY_PARSER = /"[^"\n]*"#|'[^'\n]*'#|[{[,:\s]-\s*[}\],]|\*[^\s:]+:/;

/** A template at the start of a plain value or key. `helm.ValidateValues` has the same rule. */
const UNQUOTED_TEMPLATE = /(?:^\s*|:\s+|-\s+|[[{,]\s*)\{\{/m;

/** The text that holds no template to quote: quoted strings and comments. `helm.ValidateValues` has the same rule. */
const QUOTED_OR_COMMENT = /"(?:[^"\\\n]|\\.)*"|'(?:[^'\n]|'')*'|(?:^|\s)#.*/gm;

/** The same advice as `helm.ValidateValues`. */
const TEMPLATE_QUOTE_HINT = 'Put a template value in quotes, for example tag: "{{.Branch}}"';

/**
 * Check override values in the browser. The API (`helm.ValidateValues`) is the
 * authority, and `js-yaml` is not the parser of the deploy. So only a problem
 * with `blocking: true` may stop a save. A value with any other problem goes to
 * the API.
 * @param value - YAML text of the editor
 * @returns The problem, or null when the check found none
 */
export function checkYamlValues(value: string): YamlProblem | null {
  // The deploy parser trims the value first, so the first line loses its indentation.
  const text = value.trim();
  if (!text) return null;
  // The deploy reads only the first document. js-yaml reads all of them.
  if (DOCUMENT_MARKER.test(text.replace(FIRST_DOCUMENT_START, ''))) {
    return { message: 'The deploy uses only the first YAML document in these values.', blocking: false };
  }

  let parsed: unknown;
  try {
    parsed = loadYaml(text);
  } catch (e: unknown) {
    const rawReason = (e as { reason?: unknown } | null)?.reason;
    const reason = typeof rawReason === 'string' ? rawReason : '';
    if (NO_DOCUMENT_REASON.test(reason)) return null;
    // The first line: the reason and the position. The other lines are a code
    // snippet that needs a fixed-width block.
    const first = e instanceof Error ? e.message.split('\n')[0] : 'Invalid YAML';
    return {
      message: UNQUOTED_TEMPLATE.test(text.replace(QUOTED_OR_COMMENT, '')) ? `${first}. ${TEMPLATE_QUOTE_HINT}` : first,
      blocking: BLOCKING_REASONS.some((r) => r.test(reason)) && !ACCEPTED_BY_DEPLOY_PARSER.test(text),
    };
  }
  // A root that is not a map: `yaml.v3` cannot decode it into the values map.
  // The empty string is the bare tag `!`, which it reads as no value.
  const isScalarOrList = Array.isArray(parsed) || (typeof parsed !== 'object' && parsed !== undefined && parsed !== '');
  return isScalarOrList
    ? { message: 'The values must be a map of keys and values, not a scalar or a list.', blocking: true }
    : null;
}
