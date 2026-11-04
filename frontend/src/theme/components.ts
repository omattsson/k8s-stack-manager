import type { ThemeOptions } from '@mui/material';

export function getComponentOverrides(mode: 'light' | 'dark'): ThemeOptions['components'] {
  const dividerColor = mode === 'light' ? 'rgba(0, 0, 0, 0.12)' : 'rgba(255, 255, 255, 0.12)';

  return {
    MuiButton: {
      styleOverrides: {
        root: {
          borderRadius: 8,
          textTransform: 'none' as const,
        },
      },
      defaultProps: {
        disableElevation: true,
      },
    },
    MuiCard: {
      styleOverrides: {
        root: {
          borderRadius: 12,
          border: `1px solid ${dividerColor}`,
        },
      },
      defaultProps: {
        elevation: 0,
      },
    },
    MuiPaper: {
      styleOverrides: {
        root: {
          borderRadius: 8,
        },
      },
    },
    MuiInputBase: {
      styleOverrides: {
        // iOS Safari zooms the page when a field with a font size under
        // 16px gets focus. Use 16px on phones (body1 is 14px).
        input: ({ theme }) => ({
          [theme.breakpoints.down('sm')]: {
            fontSize: '1rem',
          },
        }),
      },
    },
    MuiTextField: {
      defaultProps: {
        variant: 'outlined' as const,
        size: 'small' as const,
      },
    },
    MuiChip: {
      styleOverrides: {
        root: {
          borderRadius: 6,
        },
      },
    },
    // Dark mode lightens a Paper by its elevation, up to #434343 on a dialog and
    // #393939 on a menu. Status colours as text then fall below 4.5:1 (the error
    // text of a dialog 3.94:1, a hovered Delete menu item 3.59:1). Dialogs and
    // menus stay on background.paper: a hovered or focused menu item is then at
    // most #393939. The backdrop or the border sets them apart.
    MuiDialog: {
      styleOverrides: {
        paper: {
          borderRadius: 12,
          backgroundImage: 'none',
        },
      },
    },
    MuiMenu: {
      styleOverrides: {
        paper: {
          backgroundImage: 'none',
          ...(mode === 'dark' && { border: `1px solid ${dividerColor}` }),
        },
      },
    },
    MuiButtonBase: {
      styleOverrides: {
        root: ({ theme }) => ({
          '&:focus-visible': {
            outline: '2px solid',
            outlineColor: theme.palette.primary.main,
            outlineOffset: 2,
          },
        }),
      },
    },
    MuiListItemButton: {
      styleOverrides: {
        root: ({ theme }) => ({
          '&.Mui-selected': {
            backgroundColor: theme.palette.primary.main + '14',
            '&:hover': {
              backgroundColor: theme.palette.primary.main + '1F',
            },
          },
        }),
      },
    },
    MuiToggleButton: {
      styleOverrides: {
        // The default action.active renders as #717171 on #f5f5f5 (4.47:1).
        root: ({ theme }) => ({
          color: theme.palette.text.secondary,
        }),
      },
    },
    MuiTooltip: {
      styleOverrides: {
        tooltip: {
          fontSize: '0.8125rem',
        },
      },
    },
  };
}
