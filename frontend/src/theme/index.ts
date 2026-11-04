import { createTheme, type Theme } from '@mui/material';
import { lightPalette, darkPalette } from './palette';
import { typography } from './typography';
import { getComponentOverrides } from './components';

export const DRAWER_WIDTH = 260;
export const DRAWER_COLLAPSED_WIDTH = 64;

export function createAppTheme(mode: 'light' | 'dark'): Theme {
  return createTheme({
    // Guard for colours added later and for getContrastText. It changes no text
    // colour today and ignores the alpha of the black text; contrast.test.ts checks the ratios.
    palette: { ...(mode === 'light' ? lightPalette : darkPalette), contrastThreshold: 4.5 },
    typography,
    components: getComponentOverrides(mode),
  });
}
