import type { JwtPayload } from '../types';

/**
 * Decode the payload of a JWT without verifying the signature.
 * @param token - Encoded JWT (header.payload.signature)
 * @returns The decoded payload, or null when the token is malformed
 */
export function decodeJwtPayload(token: string): JwtPayload | null {
  try {
    const base64Url = token.split('.')[1];
    const base64 = base64Url.replaceAll('-', '+').replaceAll('_', '/');
    const padded = base64.padEnd(base64.length + (4 - (base64.length % 4)) % 4, '=');
    const json = atob(padded);
    return JSON.parse(json);
  } catch {
    return null;
  }
}

/**
 * Check whether a decoded JWT payload is past its `exp` claim.
 * @param payload - Decoded JWT payload
 * @returns True when the token is expired
 */
export function isTokenExpired(payload: JwtPayload): boolean {
  return Date.now() >= payload.exp * 1000;
}

/**
 * Check whether an encoded JWT decodes and is not expired.
 * @param token - Encoded JWT, or null
 * @returns True when the token is usable for API calls
 */
export function isTokenUsable(token: string | null): token is string {
  if (!token) return false;
  const payload = decodeJwtPayload(token);
  return payload !== null && typeof payload.exp === 'number' && !isTokenExpired(payload);
}
