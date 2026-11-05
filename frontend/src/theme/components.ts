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
    MuiCircularProgress: {
      defaultProps: {
        'aria-label': 'Loading',
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
        // MUI draws a placeholder at 42% or 50% opacity: about 2.4:1 on white.
        input: ({ theme }) => ({
          [theme.breakpoints.down('sm')]: {
            fontSize: '1rem',
          },
          '&::placeholder': {
            color: theme.palette.text.secondary,
            opacity: 1,
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
    // MUI tints the track from the bar colour: the light warning bar is 2.99:1 on it.
    // A neutral track keeps every bar colour at 3:1 or more in both modes (#760).
    MuiLinearProgress: {
      styleOverrides: {
        root: ({ theme }) => ({
          backgroundColor: theme.palette.action.disabledBackground,
        }),
      },
    },
    // A control in a cell that a sideways scroll cuts off: focus must scroll it fully
    // into view, with room for its focus ring (#760). Chromium leaves a part-visible control as it is.
    MuiTableContainer: {
      styleOverrides: {
        root: {
          scrollPaddingInline: '48px',
        },
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
    // The browser default ring is 2.4:1 to 2.8:1 on dark surfaces.
    MuiCssBaseline: {
      styleOverrides: (theme) => ({
        // Room for the ring when focus scrolls a link into view in a scrolling table.
        a: {
          scrollMargin: 4,
        },
        // Whatever else takes focus without a ring of its own: a link, an element with
        // a tabindex, or a scroll container, which Chromium makes a Tab stop when nothing
        // inside it can take focus. :where has no specificity, so a component that sets
        // its own outline still wins. The browser default depends on the platform:
        // 3.13:1 on macOS, 1.02:1 on Linux, on the same dark page.
        ':where(:focus-visible)': {
          outline: '2px solid',
          outlineColor: theme.palette.primary.main,
          outlineOffset: 2,
        },
      }),
    },
    MuiLink: {
      styleOverrides: {
        root: ({ theme }) => ({
          '&.Mui-focusVisible, &:focus-visible': {
            outline: '2px solid',
            outlineColor: theme.palette.primary.main,
            outlineOffset: 2,
          },
        }),
      },
    },
    // Inside the element: the menu list, the tab scroller and the card clip what is outside.
    MuiCardActionArea: {
      styleOverrides: {
        root: {
          '&.Mui-focusVisible, &:focus-visible': {
            outlineOffset: -2,
          },
        },
      },
    },
    MuiMenuItem: {
      styleOverrides: {
        root: {
          '&.Mui-focusVisible, &:focus-visible': {
            outlineOffset: -2,
          },
        },
      },
    },
    MuiTab: {
      styleOverrides: {
        root: {
          '&.Mui-focusVisible, &:focus-visible': {
            outlineOffset: -2,
          },
        },
      },
    },
    MuiSwitch: {
      styleOverrides: {
        switchBase: {
          '&.Mui-focusVisible': {
            outlineOffset: -2,
          },
        },
      },
    },
    // The primary ring has the colour of the app bar on a phone: 1:1.
    MuiAppBar: {
      styleOverrides: {
        root: {
          '& a:focus-visible, & .MuiButtonBase-root:focus-visible, & .MuiButtonBase-root.Mui-focusVisible': {
            outlineColor: 'currentColor',
          },
        },
      },
    },
    // This select has no border, so focus had no visible indicator.
    MuiTablePagination: {
      styleOverrides: {
        // Wider than a phone: the next-page button and its ring would be cut off.
        toolbar: ({ theme }) => ({
          [theme.breakpoints.down('sm')]: {
            flexWrap: 'wrap',
          },
        }),
        // At the edge of a container that scrolls, which clips what is outside.
        actions: {
          '& .MuiButtonBase-root.Mui-focusVisible': {
            outlineOffset: -2,
          },
        },
        select: ({ theme }) => ({
          '&:focus-visible': {
            outline: '2px solid',
            outlineColor: theme.palette.primary.main,
            outlineOffset: 1,
          },
        }),
      },
    },
    MuiButtonBase: {
      styleOverrides: {
        // Mui-focusVisible as well: a checkbox, radio or switch has its focus on an
        // inner input, so :focus-visible never matches its root.
        root: ({ theme }) => ({
          scrollMargin: 4,
          '&:focus-visible, &.Mui-focusVisible': {
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
          // The bar marks the current page by shape, not by the tint alone.
          '&.Mui-selected': {
            backgroundColor: theme.palette.primary.main + '14',
            boxShadow: `inset 3px 0 0 ${theme.palette.primary.main}`,
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
    // An inactive step number is white on the disabled grey by default: 2.7:1.
    MuiStepIcon: {
      styleOverrides: {
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
