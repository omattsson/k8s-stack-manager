import { createTheme, type Theme } from '@mui/material';
import { lightPalette, darkPalette } from './palette';
import { typography } from './typography';
import { getComponentOverrides } from './components';

export const DRAWER_WIDTH = 260;
export const DRAWER_COLLAPSED_WIDTH = 64;

export function createAppTheme(mode: 'light' | 'dark'): Theme {
  return createTheme({
    palette: mode === 'light' ? lightPalette : darkPalette,
    typography,
    components: getComponentOverrides(mode),
  });
}
