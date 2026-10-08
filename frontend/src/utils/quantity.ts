/**
 * Kubernetes resource quantity parser.
 *
 * Follows the grammar of `k8s.io/apimachinery/pkg/api/resource.Quantity`:
 *
 *   <quantity>        ::= <signedNumber><suffix>
 *   <number>          ::= <digits> | <digits>.<digits> | <digits>. | .<digits>
 *   <suffix>          ::= <binarySI> | <decimalExponent> | <decimalSI>
 *   <binarySI>        ::= Ki | Mi | Gi | Ti | Pi | Ei
 *   <decimalSI>       ::= n | u | m | "" | k | M | G | T | P | E
 *   <decimalExponent> ::= "e" <signedNumber> | "E" <signedNumber>
 *
 * Values are returned as plain numbers in the base unit (cores for CPU,
 * bytes for memory and storage). Precision is that of a JavaScript number,
 * which is enough for validation and display.
 */

const QUANTITY_PATTERN = /^([+-]?)(\d+(?:\.\d*)?|\.\d+)([eE][+-]?\d+|Ki|Mi|Gi|Ti|Pi|Ei|n|u|m|k|M|G|T|P|E)?$/;

const SUFFIX_MULTIPLIERS: Record<string, number> = {
  '': 1,
  n: 1e-9,
  u: 1e-6,
  m: 1e-3,
  k: 1e3,
  M: 1e6,
  G: 1e9,
  T: 1e12,
  P: 1e15,
  E: 1e18,
  Ki: 2 ** 10,
  Mi: 2 ** 20,
  Gi: 2 ** 30,
  Ti: 2 ** 40,
  Pi: 2 ** 50,
  Ei: 2 ** 60,
};

/**
 * Check whether a string is a syntactically valid Kubernetes quantity.
 * @param input - Quantity string, for example `500m`, `1.5`, `256Mi`, `1e3`
 * @returns True when the string matches the Kubernetes quantity grammar
 */
export function isValidQuantity(input: string): boolean {
  return QUANTITY_PATTERN.test(input.trim());
}

/**
 * Parse a Kubernetes quantity into its value in the base unit.
 * @param input - Quantity string, for example `500m`, `2`, `1Gi`, `1G`, `1e3`
 * @returns The value in the base unit (cores or bytes), or null when the string is not a valid quantity
 */
export function parseQuantity(input: string): number | null {
  const match = QUANTITY_PATTERN.exec(input.trim());
  if (!match) return null;
  const [, sign, digits, suffix = ''] = match;
  const base = Number.parseFloat(digits);
  let multiplier: number;
  if (/^[eE][+-]?\d+$/.test(suffix)) {
    multiplier = 10 ** Number.parseInt(suffix.slice(1), 10);
  } else {
    multiplier = SUFFIX_MULTIPLIERS[suffix];
  }
  const value = base * multiplier;
  return sign === '-' ? -value : value;
}

/**
 * Parse a CPU quantity into millicores.
 * @param input - CPU quantity, for example `500m`, `1`, `1.5`
 * @returns Millicores (rounded to an integer), or null when the string is not a valid quantity
 */
export function parseCpuMillicores(input: string): number | null {
  const cores = parseQuantity(input);
  return cores === null ? null : Math.round(cores * 1000);
}

/**
 * Parse a memory or storage quantity into bytes.
 * @param input - Memory quantity, for example `256Mi`, `1Gi`, `1G`, `1e9`
 * @returns Bytes, or null when the string is not a valid quantity
 */
export function parseMemoryBytes(input: string): number | null {
  return parseQuantity(input);
}
