import { useState } from 'react';
import { Box } from '@mui/material';
import { DEFAULT_LOGO_URL, useBranding } from '../../context/BrandingContext';

interface BrandLogoProps {
  /** Height and maximum width of the logo in pixels. */
  size?: number;
}

/**
 * Logo of the web UI (APP_LOGO_URL, or the built-in logo). Falls back to the
 * built-in logo when the image does not load. Decorative: the product name
 * or the surrounding control carries the accessible name.
 */
const BrandLogo = ({ size = 28 }: BrandLogoProps) => {
  const { logoUrl } = useBranding();
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  const src = failedUrl === logoUrl ? DEFAULT_LOGO_URL : logoUrl;

  return (
    <Box
      component="img"
      src={src}
      alt=""
      data-testid="brand-logo"
      onError={() => {
        if (src !== DEFAULT_LOGO_URL) setFailedUrl(logoUrl);
      }}
      sx={{ height: size, maxWidth: size * 4, objectFit: 'contain', display: 'block', flexShrink: 0 }}
    />
  );
};

export default BrandLogo;
