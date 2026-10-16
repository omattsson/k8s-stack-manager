import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { uiConfigService } from '../api/client';

/** Built-in logo of the build (`public/logo.svg`). */
export const DEFAULT_LOGO_URL = '/logo.svg';

/** Built-in favicon of the build (`public/favicon.svg`). */
export const DEFAULT_FAVICON_URL = '/favicon.svg';

/** Built-in product name. */
export const DEFAULT_APP_TITLE = 'K8s Stack Manager';

/** Highest TTL in minutes that the server accepts by default (30 days). */
export const MAX_TTL_MINUTES = 43200;

/** Default and highest values of MAX_INSTANCE_TTL_DAYS. */
const DEFAULT_MAX_TTL_DAYS = 30;
const MAX_TTL_DAYS_LIMIT = 365;

/** Display values and form defaults of the web UI (GET /api/v1/ui-config). */
export interface Branding {
  /** Product name. */
  title: string;
  /** Logo image URL (the built-in logo when the server sends none). */
  logoUrl: string;
  /** Browser tab icon URL. */
  faviconUrl: string;
  /** Start value of the TTL field in the create forms, in minutes. 0: no expiry. */
  defaultTtlMinutes: number;
  /** Maximum TTL and stop-time distance from now, in days (MAX_INSTANCE_TTL_DAYS). */
  maxTtlDays: number;
}

/** Values used when the UI config cannot be loaded, and outside a BrandingProvider. */
export const BRANDING_DEFAULTS: Branding = {
  title: DEFAULT_APP_TITLE,
  logoUrl: DEFAULT_LOGO_URL,
  faviconUrl: DEFAULT_FAVICON_URL,
  defaultTtlMinutes: 0,
  maxTtlDays: DEFAULT_MAX_TTL_DAYS,
};

/**
 * Accept a whole number of days from 1 to 365.
 * @param value - MAX_INSTANCE_TTL_DAYS from the server
 * @returns The value, or the default (30) when it is missing or invalid
 */
const maxTtlDaysFromConfig = (value: unknown): number =>
  typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= MAX_TTL_DAYS_LIMIT
    ? value
    : DEFAULT_MAX_TTL_DAYS;

/**
 * Accept a whole number of minutes from 0 to the limit.
 * @param value - TTL default from the server
 * @param maxDays - The limit in days
 * @returns The value, or 0 when it is not a valid TTL
 */
const ttlFromConfig = (value: unknown, maxDays: number): number =>
  typeof value === 'number' && Number.isInteger(value) && value > 0 && value <= maxDays * 24 * 60 ? value : 0;

const BrandingContext = createContext<Branding>(BRANDING_DEFAULTS);

// Whitespace (including Unicode spaces) or a control character anywhere in the URL.
// eslint-disable-next-line no-control-regex
const UNSAFE_URL_CHARS = /[\s\u0000-\u001f\u007f-\u009f]/;

/**
 * Accept a same-origin path ("/x") or an https URL, without whitespace or
 * control characters. The backend validates the values at startup; this
 * check is defence in depth.
 * @param value - URL from the server
 * @returns true when the URL is safe to use as an image source
 */
export const isSafeImageUrl = (value: unknown): value is string => {
  if (typeof value !== 'string' || value === '') return false;
  if (UNSAFE_URL_CHARS.test(value) || value.includes('\\')) return false;
  if (value.startsWith('/')) return !value.startsWith('//');
  try {
    return new URL(value).protocol === 'https:';
  } catch {
    return false;
  }
};

/**
 * Build the branding from a server response. Missing or unsafe values get
 * the built-in defaults.
 * @param data - Response body of GET /api/v1/ui-config
 * @returns The branding to show
 */
export const brandingFromConfig = (data: unknown): Branding => {
  const cfg = (data && typeof data === 'object' ? data : {}) as Record<string, unknown>;
  const title = typeof cfg.title === 'string' && cfg.title.trim() !== '' ? cfg.title.trim() : DEFAULT_APP_TITLE;
  const maxTtlDays = maxTtlDaysFromConfig(cfg.max_instance_ttl_days);
  return {
    title,
    logoUrl: isSafeImageUrl(cfg.logo_url) ? cfg.logo_url : DEFAULT_LOGO_URL,
    faviconUrl: isSafeImageUrl(cfg.favicon_url) ? cfg.favicon_url : DEFAULT_FAVICON_URL,
    defaultTtlMinutes: ttlFromConfig(cfg.default_instance_ttl_minutes, maxTtlDays),
    maxTtlDays,
  };
};

/** localStorage key of the last UI config from the server. */
export const UI_CONFIG_STORAGE_KEY = 'ui-config';

/**
 * Branding from the last UI config in localStorage, or the built-in
 * defaults. The stored value passes the same checks as a server response.
 * @returns The initial branding
 */
export const loadCachedBranding = (): Branding => {
  try {
    const raw = localStorage.getItem(UI_CONFIG_STORAGE_KEY);
    return raw ? brandingFromConfig(JSON.parse(raw)) : BRANDING_DEFAULTS;
  } catch {
    return BRANDING_DEFAULTS;
  }
};

/**
 * Store the UI config (only the known fields) for the next start.
 * @param data - Response body of GET /api/v1/ui-config
 */
const cacheUIConfig = (data: unknown) => {
  try {
    const cfg = (data && typeof data === 'object' ? data : {}) as Record<string, unknown>;
    localStorage.setItem(
      UI_CONFIG_STORAGE_KEY,
      JSON.stringify({
        title: cfg.title,
        logo_url: cfg.logo_url,
        favicon_url: cfg.favicon_url,
        default_instance_ttl_minutes: cfg.default_instance_ttl_minutes,
        max_instance_ttl_days: cfg.max_instance_ttl_days,
      }),
    );
  } catch {
    // Storage full or blocked: the next start uses the defaults.
  }
};

/**
 * Set the favicon `<link rel="icon">` of the document. Creates the element
 * when the page has none.
 * @param href - Favicon URL
 */
const applyFavicon = (href: string) => {
  let link = document.querySelector<HTMLLinkElement>('link[rel~="icon"]');
  if (!link) {
    link = document.createElement('link');
    link.rel = 'icon';
    document.head.appendChild(link);
  }
  // The type attribute of index.html says SVG; let the browser detect other formats.
  if (/\.svg(\?|#|$)/i.test(href)) {
    link.type = 'image/svg+xml';
  } else {
    link.removeAttribute('type');
  }
  link.href = href;
};

/**
 * Renders the children at once with the last cached UI config (or the
 * built-in defaults), loads the UI config once at startup and swaps in its
 * values when they arrive; the response is cached for the next start. On a
 * failure or a timeout (see uiConfigService.get) the initial values stay.
 * Sets the document title and the favicon.
 */
export function BrandingProvider({ children }: { children: ReactNode }) {
  const [branding, setBranding] = useState<Branding>(loadCachedBranding);

  useEffect(() => {
    let cancelled = false;
    uiConfigService
      .get()
      .then((data) => {
        cacheUIConfig(data);
        if (!cancelled) setBranding(brandingFromConfig(data));
      })
      .catch(() => {
        // Keep the built-in defaults.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    document.title = branding.title;
    applyFavicon(branding.faviconUrl);
  }, [branding]);

  return <BrandingContext.Provider value={branding}>{children}</BrandingContext.Provider>;
}

/**
 * Provides fixed branding values without loading the UI config (tests).
 */
export function StaticBrandingProvider({ value, children }: { value: Branding; children: ReactNode }) {
  return <BrandingContext.Provider value={value}>{children}</BrandingContext.Provider>;
}

/**
 * Branding of the web UI. Outside a BrandingProvider it returns the built-in
 * defaults.
 * @returns Title, logo URL, favicon URL and the TTL default of the create forms
 */
export const useBranding = (): Branding => useContext(BrandingContext);
