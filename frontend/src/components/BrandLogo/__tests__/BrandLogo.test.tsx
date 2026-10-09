import { describe, it, expect } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import BrandLogo from '../index';
import { StaticBrandingProvider } from '../../../context/BrandingContext';

describe('BrandLogo', () => {
  it('uses the built-in logo outside a provider', () => {
    render(<BrandLogo />);
    expect(screen.getByTestId('brand-logo')).toHaveAttribute('src', '/logo.svg');
  });

  it('falls back to the built-in logo when the configured image fails to load', () => {
    render(
      <StaticBrandingProvider value={{ title: 'X', logoUrl: '/branding/missing.svg', faviconUrl: '/favicon.svg' }}>
        <BrandLogo size={32} />
      </StaticBrandingProvider>,
    );
    const img = screen.getByTestId('brand-logo');
    expect(img).toHaveAttribute('src', '/branding/missing.svg');
    expect(img).toHaveAttribute('alt', '');
    fireEvent.error(img);
    expect(screen.getByTestId('brand-logo')).toHaveAttribute('src', '/logo.svg');
  });
});
