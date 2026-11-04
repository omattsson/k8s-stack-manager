import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider, ToggleButton } from '@mui/material';
import { createAppTheme } from '../index';

type Mode = 'light' | 'dark';
const modes: Mode[] = ['light', 'dark'];

function resolveColor(color: string): string {
  const el = document.createElement('div');
  el.style.color = color;
  return el.style.color;
}

function renderToggle(mode: Mode, props: { selected?: boolean; color?: 'primary' }) {
  const theme = createAppTheme(mode);
  render(
    <ThemeProvider theme={theme}>
      <ToggleButton value="a" {...props}>
        Option
      </ToggleButton>
    </ThemeProvider>,
  );
  return { theme, button: screen.getByRole('button', { name: 'Option' }) };
}

describe('MuiToggleButton override', () => {
  it.each(modes)('colours an unselected ToggleButton with text.secondary in %s mode', (mode) => {
    const { theme, button } = renderToggle(mode, {});
    const color = getComputedStyle(button).color;
    expect(color).not.toBe('');
    expect(color).toBe(resolveColor(theme.palette.text.secondary));
  });

  it.each(modes)('keeps text.primary on a selected ToggleButton in %s mode', (mode) => {
    const { theme, button } = renderToggle(mode, { selected: true });
    const color = getComputedStyle(button).color;
    expect(color).toBe(resolveColor(theme.palette.text.primary));
    expect(color).not.toBe(resolveColor(theme.palette.text.secondary));
  });

  it.each(modes)('keeps primary.main on a selected primary ToggleButton in %s mode', (mode) => {
    const { theme, button } = renderToggle(mode, { selected: true, color: 'primary' });
    expect(getComputedStyle(button).color).toBe(resolveColor(theme.palette.primary.main));
  });
});
