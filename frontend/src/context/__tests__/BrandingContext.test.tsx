import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, renderHook, waitFor, act } from '@testing-library/react';
import {
  BrandingProvider,
  useBranding,
  brandingFromConfig,
  isSafeImageUrl,
  BRANDING_DEFAULTS,
  UI_CONFIG_STORAGE_KEY,
  loadCachedBranding,
} from '../BrandingContext';

vi.mock('../../api/client', () => ({
  uiConfigService: { get: vi.fn() },
}));

import { uiConfigService } from '../../api/client';

const mockGet = uiConfigService.get as ReturnType<typeof vi.fn>;

const Probe = () => {
  const { title, logoUrl, faviconUrl } = useBranding();
  return (
    <div>
      <span data-testid="title">{title}</span>
      <span data-testid="logo">{logoUrl}</span>
      <span data-testid="favicon">{faviconUrl}</span>
    </div>
  );
};

const faviconHref = () => document.querySelector<HTMLLinkElement>('link[rel~="icon"]')?.getAttribute('href');

describe('BrandingContext', () => {
  beforeEach(() => {
    document.head.innerHTML = '<link rel="icon" type="image/svg+xml" href="/favicon.svg" />';
    document.title = 'initial';
    localStorage.clear();
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('returns the built-in defaults outside a provider', () => {
    const { result } = renderHook(() => useBranding());
    expect(result.current).toEqual(BRANDING_DEFAULTS);
  });

  it('renders the defaults at once while the config loads (no spinner)', () => {
    mockGet.mockReturnValue(new Promise(() => {}));
    render(
      <BrandingProvider>
        <Probe />
      </BrandingProvider>,
    );
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    expect(screen.getByTestId('title')).toHaveTextContent(BRANDING_DEFAULTS.title);
    expect(screen.getByTestId('logo')).toHaveTextContent(BRANDING_DEFAULTS.logoUrl);
    expect(document.title).toBe(BRANDING_DEFAULTS.title);
  });

  it('swaps in the config when it arrives', async () => {
    let resolve: (v: unknown) => void = () => {};
    mockGet.mockReturnValue(new Promise((r) => { resolve = r; }));
    render(
      <BrandingProvider>
        <Probe />
      </BrandingProvider>,
    );
    expect(screen.getByTestId('title')).toHaveTextContent(BRANDING_DEFAULTS.title);
    await act(async () => {
      resolve({ title: 'Platform Portal', logo_url: '/branding/logo.svg', favicon_url: '/branding/f.svg' });
    });
    expect(screen.getByTestId('title')).toHaveTextContent('Platform Portal');
    expect(document.title).toBe('Platform Portal');
    expect(faviconHref()).toBe('/branding/f.svg');
  });

  it('applies the configured title, logo and favicon', async () => {
    mockGet.mockResolvedValue({
      title: 'Platform Portal',
      logo_url: '/branding/logo.svg',
      favicon_url: 'https://cdn.example.com/favicon.png',
    });
    render(
      <BrandingProvider>
        <Probe />
      </BrandingProvider>,
    );
    await waitFor(() => expect(screen.getByTestId('title')).toHaveTextContent('Platform Portal'));
    expect(screen.getByTestId('logo')).toHaveTextContent('/branding/logo.svg');
    expect(document.title).toBe('Platform Portal');
    expect(faviconHref()).toBe('https://cdn.example.com/favicon.png');
    // Not an SVG: the type attribute of index.html must go.
    expect(document.querySelector('link[rel~="icon"]')?.hasAttribute('type')).toBe(false);
    expect(mockGet).toHaveBeenCalledTimes(1);
  });

  it('falls back to the built-in defaults when the call fails', async () => {
    mockGet.mockRejectedValue(new Error('timeout of 5000ms exceeded'));
    render(
      <BrandingProvider>
        <Probe />
      </BrandingProvider>,
    );
    await waitFor(() => expect(mockGet).toHaveBeenCalledTimes(1));
    expect(screen.getByTestId('title')).toHaveTextContent(BRANDING_DEFAULTS.title);
    expect(screen.getByTestId('logo')).toHaveTextContent(BRANDING_DEFAULTS.logoUrl);
    expect(document.title).toBe(BRANDING_DEFAULTS.title);
    expect(faviconHref()).toBe(BRANDING_DEFAULTS.faviconUrl);
  });

  it('creates the favicon link when the page has none', async () => {
    document.head.innerHTML = '';
    mockGet.mockResolvedValue({ title: 'X', logo_url: '', favicon_url: '/branding/icon.svg' });
    render(
      <BrandingProvider>
        <Probe />
      </BrandingProvider>,
    );
    await waitFor(() => expect(faviconHref()).toBe('/branding/icon.svg'));
    expect(document.querySelector('link[rel~="icon"]')?.getAttribute('type')).toBe('image/svg+xml');
  });

  describe('localStorage cache', () => {
    it('uses the cached config as the initial value, then refreshes it from the server', async () => {
      localStorage.setItem(UI_CONFIG_STORAGE_KEY, JSON.stringify({ title: 'Cached Portal', logo_url: '/branding/old.svg', favicon_url: '/f.svg' }));
      let resolve: (v: unknown) => void = () => {};
      mockGet.mockReturnValue(new Promise((r) => { resolve = r; }));
      render(
        <BrandingProvider>
          <Probe />
        </BrandingProvider>,
      );
      expect(screen.getByTestId('title')).toHaveTextContent('Cached Portal');
      expect(screen.getByTestId('logo')).toHaveTextContent('/branding/old.svg');
      expect(document.title).toBe('Cached Portal');

      await act(async () => {
        resolve({ title: 'New Portal', logo_url: '/branding/new.svg', favicon_url: '/f2.svg', extra: 'dropped' });
      });
      expect(screen.getByTestId('title')).toHaveTextContent('New Portal');
      expect(JSON.parse(localStorage.getItem(UI_CONFIG_STORAGE_KEY) ?? '{}')).toEqual({
        title: 'New Portal', logo_url: '/branding/new.svg', favicon_url: '/f2.svg',
      });
    });

    it('does not cache stream_api_keys_allowed but uses the server value', async () => {
      mockGet.mockResolvedValue({ title: 'P', logo_url: '', favicon_url: '/f.svg', exec_enabled: true, stream_api_keys_allowed: true });
      render(
        <BrandingProvider>
          <Probe />
        </BrandingProvider>,
      );
      await waitFor(() => expect(mockGet).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(localStorage.getItem(UI_CONFIG_STORAGE_KEY)).not.toBeNull());
      const cached = JSON.parse(localStorage.getItem(UI_CONFIG_STORAGE_KEY) ?? '{}');
      expect(cached.exec_enabled).toBe(true);
      expect(cached).not.toHaveProperty('stream_api_keys_allowed');
      expect(loadCachedBranding().streamApiKeysAllowed).toBe(false);
    });

    it('caches the TTL default and uses it at the next start', async () => {
      mockGet.mockResolvedValue({ title: 'P', logo_url: '', favicon_url: '/f.svg', default_instance_ttl_minutes: 240 });
      render(
        <BrandingProvider>
          <Probe />
        </BrandingProvider>,
      );
      await waitFor(() => expect(mockGet).toHaveBeenCalledTimes(1));
      expect(loadCachedBranding().defaultTtlMinutes).toBe(240);
    });

    it('keeps the cached config when the server call fails', async () => {
      localStorage.setItem(UI_CONFIG_STORAGE_KEY, JSON.stringify({ title: 'Cached Portal', logo_url: '', favicon_url: '/f.svg' }));
      mockGet.mockRejectedValue(new Error('Network Error'));
      render(
        <BrandingProvider>
          <Probe />
        </BrandingProvider>,
      );
      await waitFor(() => expect(mockGet).toHaveBeenCalledTimes(1));
      expect(screen.getByTestId('title')).toHaveTextContent('Cached Portal');
    });

    it.each([
      ['invalid JSON', '{not json'],
      ['unsafe URLs', JSON.stringify({ title: 'X', logo_url: 'javascript:alert(1)', favicon_url: 'http://a.example/f.ico' })],
    ])('ignores a cache with %s', (_name, raw) => {
      localStorage.setItem(UI_CONFIG_STORAGE_KEY, raw);
      const b = loadCachedBranding();
      expect(b.logoUrl).toBe(BRANDING_DEFAULTS.logoUrl);
      expect(b.faviconUrl).toBe(BRANDING_DEFAULTS.faviconUrl);
    });

    it('uses the defaults when localStorage throws', () => {
      const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
        throw new Error('SecurityError');
      });
      const original = globalThis.localStorage.getItem;
      globalThis.localStorage.getItem = () => {
        throw new Error('SecurityError');
      };
      try {
        expect(loadCachedBranding()).toEqual(BRANDING_DEFAULTS);
      } finally {
        globalThis.localStorage.getItem = original;
        spy.mockRestore();
      }
    });
  });

  describe('brandingFromConfig', () => {
    it.each([
      ['empty logo uses the built-in logo', { title: 'A', logo_url: '', favicon_url: '/f.svg' }, { title: 'A', logoUrl: '/logo.svg', faviconUrl: '/f.svg', defaultTtlMinutes: 0, maxTtlDays: 30, portForwardEnabled: false, execEnabled: false, streamApiKeysAllowed: false }],
      ['blank title uses the default', { title: '  ', logo_url: '/l.svg', favicon_url: '/f.svg' }, { title: BRANDING_DEFAULTS.title, logoUrl: '/l.svg', faviconUrl: '/f.svg', defaultTtlMinutes: 0, maxTtlDays: 30, portForwardEnabled: false, execEnabled: false, streamApiKeysAllowed: false }],
      ['javascript URL is ignored', { title: 'A', logo_url: 'javascript:alert(1)', favicon_url: 'data:image/svg+xml,<svg/>' }, { title: 'A', logoUrl: '/logo.svg', faviconUrl: '/favicon.svg', defaultTtlMinutes: 0, maxTtlDays: 30, portForwardEnabled: false, execEnabled: false, streamApiKeysAllowed: false }],
      ['non-object body gives the defaults', 'not json', BRANDING_DEFAULTS],
    ])('%s', (_name, input, expected) => {
      expect(brandingFromConfig(input)).toEqual(expected);
    });

    it.each([
      ['a valid TTL default', 480, 480],
      ['the maximum TTL', 43200, 43200],
      ['0 (no expiry)', 0, 0],
      ['a negative value', -5, 0],
      ['a value above the maximum', 43201, 0],
      ['a fraction', 1.5, 0],
      ['a string', '480', 0],
      ['a missing value', undefined, 0],
    ])('TTL default: %s gives %s', (_name, value, expected) => {
      expect(brandingFromConfig({ title: 'A', default_instance_ttl_minutes: value }).defaultTtlMinutes).toBe(expected);
    });

    it.each([
      ['true', true, true],
      ['false', false, false],
      ['a string', 'true', false],
      ['a missing value', undefined, false],
    ])('port-forward flag: %s gives %s', (_name, value, expected) => {
      expect(brandingFromConfig({ title: 'A', port_forward_enabled: value }).portForwardEnabled).toBe(expected);
    });

    it.each([
      ['true', true, true],
      ['false', false, false],
      ['a string', 'true', false],
      ['a missing value', undefined, false],
    ])('exec flag: %s gives %s', (_name, value, expected) => {
      expect(brandingFromConfig({ title: 'A', exec_enabled: value }).execEnabled).toBe(expected);
    });

    it.each([
      ['a valid limit', 7, 7],
      ['one year', 365, 365],
      ['0', 0, 30],
      ['above one year', 366, 30],
      ['a fraction', 1.5, 30],
      ['a string', '7', 30],
      ['a missing value', undefined, 30],
    ])('TTL limit: %s gives %s days', (_name, value, expected) => {
      expect(brandingFromConfig({ title: 'A', max_instance_ttl_days: value }).maxTtlDays).toBe(expected);
    });

    it('checks the TTL default against the configured limit', () => {
      expect(brandingFromConfig({ default_instance_ttl_minutes: 7 * 1440, max_instance_ttl_days: 7 }).defaultTtlMinutes).toBe(7 * 1440);
      expect(brandingFromConfig({ default_instance_ttl_minutes: 7 * 1440 + 1, max_instance_ttl_days: 7 }).defaultTtlMinutes).toBe(0);
      expect(brandingFromConfig({ default_instance_ttl_minutes: 50000, max_instance_ttl_days: 90 }).defaultTtlMinutes).toBe(50000);
    });
  });

  describe('isSafeImageUrl', () => {
    it.each([
      ['/branding/logo.svg', true],
      ['https://cdn.example.com/x.png', true],
      ['http://intranet.example.com/x.png', false],
      ['/branding/logo .svg', false],
      ['/branding/logo.svg\n', false],
      ['https://cdn.example.com/x\t.png', false],
      ['/a\u0000b', false],
      ['/a\u00a0b', false],
      ['/a\u2028b', false],
      [' /leading-space.svg', false],
      ['//evil.example.com/x.svg', false],
      ['/\\evil.example.com', false],
      ['javascript:alert(1)', false],
      ['data:image/png;base64,AAAA', false],
      ['relative/logo.svg', false],
      ['', false],
    ])('%s -> %s', (url, expected) => {
      expect(isSafeImageUrl(url)).toBe(expected);
    });
  });
});

describe('brandingFromConfig stream_api_keys_allowed', () => {
  it('is true only for a boolean true', () => {
    expect(brandingFromConfig({ title: 'A', stream_api_keys_allowed: true }).streamApiKeysAllowed).toBe(true);
    expect(brandingFromConfig({ title: 'A', stream_api_keys_allowed: 'true' }).streamApiKeysAllowed).toBe(false);
    expect(brandingFromConfig({ title: 'A' }).streamApiKeysAllowed).toBe(false);
  });
});
