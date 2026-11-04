import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider, createTheme, decomposeColor, getContrastRatio, recomposeColor } from '@mui/material/styles';
import StatusBadge from '../index';
import { darkPalette, lightPalette } from '../../../theme/palette';

/** The opaque colour of a colour with alpha on top of an opaque background. */
const blendOver = (color: string, background: string): string => {
  const top = decomposeColor(color);
  const bottom = decomposeColor(background);
  const alpha = top.values[3] ?? 1;
  const values = [0, 1, 2].map((i) => Math.round(top.values[i] * alpha + bottom.values[i] * (1 - alpha)));
  return recomposeColor({ type: 'rgb', values: values as [number, number, number] });
};

describe('StatusBadge', () => {
  it('renders the status text', () => {
    render(<StatusBadge status="running" />);
    expect(screen.getByText('running')).toBeInTheDocument();
  });

  it.each([
    ['draft', 'default'],
    ['deploying', 'info'],
    ['running', 'success'],
    ['partial', 'warning'],
    ['stopped', 'default'],
    ['stopping', 'info'],
    ['cleaning', 'info'],
    ['error', 'error'],
  ])('renders "%s" status with "%s" color chip', (status, color) => {
    render(<StatusBadge status={status} />);
    const chip = screen.getByText(status);
    expect(chip.closest('.MuiChip-root')).toHaveClass(`MuiChip-color${color.charAt(0).toUpperCase() + color.slice(1)}`);
  });

  // Contrast of the neutral chip, with the colours that the chip gets from the
  // application theme, on the page background (WCAG AA for normal text: 4.5).
  it.each([
    ['light', lightPalette],
    ['dark', darkPalette],
  ] as const)('gives the stopped chip enough contrast in %s mode', (_mode, palette) => {
    const theme = createTheme({ palette });
    render(
      <ThemeProvider theme={theme}>
        <StatusBadge status="stopped" />
      </ThemeProvider>,
    );
    const style = window.getComputedStyle(screen.getByText('stopped').closest('.MuiChip-root') as HTMLElement);
    const background = blendOver(style.backgroundColor, theme.palette.background.paper);
    const foreground = blendOver(style.color, background);

    expect(getContrastRatio(foreground, background)).toBeGreaterThanOrEqual(4.5);
  });

  it('renders with default color for unknown status', () => {
    render(<StatusBadge status="unknown" />);
    const chip = screen.getByText('unknown');
    expect(chip.closest('.MuiChip-root')).toHaveClass('MuiChip-colorDefault');
  });

  it('renders as a small chip', () => {
    render(<StatusBadge status="running" />);
    const chip = screen.getByText('running').closest('.MuiChip-root');
    expect(chip).toHaveClass('MuiChip-sizeSmall');
  });
});
