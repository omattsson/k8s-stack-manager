import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Dialog, Menu, MenuItem, Paper, ThemeProvider } from '@mui/material';
import { createAppTheme } from '../index';

// contrast.test.ts checks the status colours on the paper background for dialogs
// and menus. That only holds if these surfaces do not draw the lighter elevation
// overlay of dark mode.

const dark = createAppTheme('dark');

const renderDark = (ui: React.ReactNode) => render(<ThemeProvider theme={dark}>{ui}</ThemeProvider>);

describe('dark surfaces without an elevation overlay', () => {
  it('still draws the overlay on a plain Paper, so the checks below can fail', () => {
    renderDark(<Paper elevation={8} data-testid="paper" />);
    expect(getComputedStyle(screen.getByTestId('paper')).backgroundImage).toContain('--Paper-overlay');
  });

  it('draws no overlay on a Dialog', () => {
    renderDark(<Dialog open>content</Dialog>);
    const paper = screen.getByRole('dialog');
    expect(getComputedStyle(paper).backgroundImage).toBe('none');
    expect(getComputedStyle(paper).backgroundColor).not.toBe('');
  });

  it('draws no overlay on a Menu and keeps it apart from a dialog behind it', () => {
    renderDark(
      <Menu open anchorEl={document.body}>
        <MenuItem>Item</MenuItem>
      </Menu>,
    );
    const paper = screen.getByRole('menu').closest('.MuiPaper-root') as HTMLElement;
    const style = getComputedStyle(paper);
    expect(style.backgroundImage).toBe('none');
    expect(style.borderStyle).toBe('solid');
  });
});
