import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { uiConfigService } from '../api/client';

/** Built-in logo of the build (`public/logo.svg`). */
export const DEFAULT_LOGO_URL = '/logo.svg';

/** Built-in favicon of the build (`public/favicon.svg`). */
export const DEFAULT_FAVICON_URL = '/favicon.svg';

/** Built-in product name. */
export const DEFAULT_APP_TITLE = 'K8s Stack Manager';

/** Display values of the web UI. */
export interface Branding {
  /** Product name. */
  title: string;
  /** Logo image URL (the built-in logo when the server sends none). */
  logoUrl: string;
  /** Browser tab icon URL. */
  faviconUrl: string;
}

/** Values used when the UI config cannot be loaded, and outside a BrandingProvider. */
export const BRANDING_DEFAULTS: Branding = {
  title: DEFAULT_APP_TITLE,
  logoUrl: DEFAULT_LOGO_URL,
  faviconUrl: DEFAULT_FAVICON_URL,
};

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
  return {
    title,
    logoUrl: isSafeImageUrl(cfg.logo_url) ? cfg.logo_url : DEFAULT_LOGO_URL,
    faviconUrl: isSafeImageUrl(cfg.favicon_url) ? cfg.favicon_url : DEFAULT_FAVICON_URL,
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
 * Store the UI config (only the three display fields) for the next start.
 * @param data - Response body of GET /api/v1/ui-config
 */
const cacheUIConfig = (data: unknown) => {
  try {
    const cfg = (data && typeof data === 'object' ? data : {}) as Record<string, unknown>;
    localStorage.setItem(
      UI_CONFIG_STORAGE_KEY,
      JSON.stringify({ title: cfg.title, logo_url: cfg.logo_url, favicon_url: cfg.favicon_url }),
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
 * @returns Title, logo URL and favicon URL
 */
export const useBranding = (): Branding => useContext(BrandingContext);
