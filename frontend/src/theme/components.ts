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
    MuiDialog: {
      styleOverrides: {
        paper: {
          borderRadius: 12,
        },
      },
    },
    MuiButtonBase: {
      styleOverrides: {
        root: {
          '&:focus-visible': {
            outline: '2px solid',
            outlineColor: 'primary.main',
            outlineOffset: 2,
          },
        },
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
    MuiTooltip: {
      styleOverrides: {
        tooltip: {
          fontSize: '0.8125rem',
        },
      },
    },
  };
}
