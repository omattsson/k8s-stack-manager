import type { PaletteOptions } from '@mui/material';

export const lightPalette: PaletteOptions = {
  mode: 'light',
  primary: {
    main: '#1565c0',
  },
  secondary: {
    main: '#7b1fa2',
  },
  success: {
    main: '#2e7d32',
  },
  warning: {
    // Darker than MUI's #ed6c02 (3.11:1 as text on white): 4.95:1 as text on
    // white and as the background of white button text.
    main: '#b85200',
  },
  error: {
    main: '#d32f2f',
  },
  info: {
    // Darker than MUI's #0288d1: 5.01:1 with white text, 4.59:1 as text on #f5f5f5.
    main: '#0274b8',
  },
  background: {
    default: '#f5f5f5',
    paper: '#ffffff',
  },
};

export const darkPalette: PaletteOptions = {
  mode: 'dark',
  primary: {
    main: '#90caf9',
  },
  secondary: {
    main: '#ce93d8',
  },
  success: {
    main: '#66bb6a',
  },
  warning: {
    main: '#ffa726',
  },
  error: {
    // Lighter main: #f44336 as text is 3.18:1 on menus (#383838); this is 4.61:1
    // on #393939. `dark` is the hover and filled Alert background; the auto
    // shade is too dark for black text.
    main: '#ff7d66',
    dark: '#f44336',
  },
  info: {
    main: '#29b6f6',
  },
  background: {
    default: '#121212',
    paper: '#1e1e1e',
  },
};
