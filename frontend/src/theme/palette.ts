import type { PaletteOptions } from '@mui/material';

export const lightPalette: PaletteOptions = {
  mode: 'light',
  primary: {
    main: '#1565c0',
  },
  secondary: {
    main: '#7b1fa2',
  },
  // A text or outlined button tints itself on hover, which costs it about 0.25:1
  // on #f5f5f5. success, warning and info are dark enough to keep 4.5:1 there.
  success: {
    main: '#2a7a2e',
  },
  warning: {
    // Darker than MUI's #ed6c02 (3.11:1 as text on white).
    main: '#b04e00',
  },
  error: {
    // Darker than the others: Delete and Clean are error text on menu items, and
    // a focused item is #e0e0e0 (4.58:1).
    main: '#c02020',
  },
  info: {
    // Darker than MUI's #0288d1.
    main: '#0270b0',
  },
  background: {
    default: '#f5f5f5',
    paper: '#ffffff',
  },
};

// `dark` is the hover of a contained button and the background of a filled Alert
// (a toast). The text on it is near-black, and the automatic shade (30% darker)
// is too dark for that: 4.15:1 to 4.3:1 on success, info and secondary.
export const darkPalette: PaletteOptions = {
  mode: 'dark',
  primary: {
    main: '#90caf9',
  },
  secondary: {
    main: '#ce93d8',
    dark: '#ba68c8',
  },
  success: {
    main: '#66bb6a',
    dark: '#43a047',
  },
  warning: {
    main: '#ffa726',
  },
  error: {
    // Lighter main: #f44336 as text is 3.18:1 on a focused menu item (#393939);
    // this is 4.6:1. Menus and dialogs have no overlay: see components.ts.
    main: '#ff7d66',
    dark: '#f44336',
  },
  info: {
    main: '#29b6f6',
    dark: '#039be5',
  },
  background: {
    default: '#121212',
    paper: '#1e1e1e',
  },
};
