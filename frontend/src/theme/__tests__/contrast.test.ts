import { describe, it, expect } from 'vitest';
import type { Theme } from '@mui/material';
import { createAppTheme } from '../index';

type Mode = 'light' | 'dark';
type Rgba = { r: number; g: number; b: number; a: number };

const COLORS = ['primary', 'secondary', 'success', 'warning', 'error', 'info'] as const;
type ColorName = (typeof COLORS)[number];

function parseColor(value: string): Rgba {
  const v = value.trim();
  if (v.startsWith('#')) {
    const hex = v.length === 4 ? v.slice(1).replace(/./g, (c) => c + c) : v.slice(1);
    return {
      r: parseInt(hex.slice(0, 2), 16),
      g: parseInt(hex.slice(2, 4), 16),
      b: parseInt(hex.slice(4, 6), 16),
      a: 1,
    };
  }
  const m = /^rgba?\(([^)]+)\)$/.exec(v);
  if (!m) throw new Error(`unsupported colour: ${value}`);
  const parts = m[1].split(/[\s,/]+/).filter(Boolean).map(Number);
  return { r: parts[0], g: parts[1], b: parts[2], a: parts[3] ?? 1 };
}

function luminance({ r, g, b }: Pick<Rgba, 'r' | 'g' | 'b'>): number {
  const [lr, lg, lb] = [r, g, b].map((c) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * lr + 0.7152 * lg + 0.0722 * lb;
}

/**
 * WCAG contrast ratio. A translucent foreground is composited over the
 * opaque background first (MUI's getContrastRatio ignores alpha).
 */
function contrast(foreground: string, background: string): number {
  const bg = parseColor(background);
  const fg = parseColor(foreground);
  const blended = {
    r: fg.r * fg.a + bg.r * (1 - fg.a),
    g: fg.g * fg.a + bg.g * (1 - fg.a),
    b: fg.b * fg.a + bg.b * (1 - fg.a),
  };
  const l1 = luminance(blended);
  const l2 = luminance(bg);
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
}

/** The opaque colour of a translucent `top` over an opaque `under`. */
function over(top: string, under: string): string {
  const t = parseColor(top);
  const u = parseColor(under);
  const mix = (a: number, b: number) => a * t.a + b * (1 - t.a);
  return `rgb(${mix(t.r, u.r)}, ${mix(t.g, u.g)}, ${mix(t.b, u.b)})`;
}

/** A palette colour at the opacity of a hover tint (text and outlined buttons). */
function tint(color: string, opacity: number): string {
  const { r, g, b } = parseColor(color);
  return `rgba(${r}, ${g}, ${b}, ${opacity})`;
}

const AA = 4.5;
const themes: Record<Mode, Theme> = { light: createAppTheme('light'), dark: createAppTheme('dark') };
const modes: Mode[] = ['light', 'dark'];

describe('contrast helper', () => {
  it('computes known ratios', () => {
    expect(contrast('#000000', '#ffffff')).toBeCloseTo(21, 1);
    expect(contrast('#ffffff', '#ffffff')).toBeCloseTo(1, 5);
    // Close to the limit: #767676 on white is about 4.54.
    expect(contrast('#767676', '#ffffff')).toBeCloseTo(4.54, 1);
    // Alpha is composited: black at 87% on white is lighter than pure black.
    expect(contrast('rgba(0, 0, 0, 0.87)', '#ffffff')).toBeLessThan(21);
    expect(contrast('rgba(0, 0, 0, 0.87)', '#ffffff')).toBeGreaterThan(15);
  });
});

describe('theme contrast (WCAG AA, 4.5:1)', () => {
  it.each(modes)('keeps contrastThreshold at 4.5 in %s mode', (mode) => {
    expect(themes[mode].palette.contrastThreshold).toBe(4.5);
  });

  const allCases = modes.flatMap((m) => COLORS.map((c): [Mode, ColorName] => [m, c]));

  it.each(allCases)('%s %s: contrastText on main', (mode, color) => {
    const p = themes[mode].palette[color];
    expect(contrast(p.contrastText, p.main)).toBeGreaterThanOrEqual(AA);
  });

  it.each(allCases)('%s %s: contrastText on dark (contained button on hover)', (mode, color) => {
    const p = themes[mode].palette[color];
    expect(contrast(p.contrastText, p.dark)).toBeGreaterThanOrEqual(AA);
  });

  it.each(allCases)('%s %s: the hover of a contained button changes the colour', (mode, color) => {
    const p = themes[mode].palette[color];
    expect(p.dark.toLowerCase()).not.toBe(p.main.toLowerCase());
  });

  // A filled Alert (a toast) has the `dark` shade in dark mode and `main` in light mode.
  it.each(allCases)('%s %s: filled Alert text', (mode, color) => {
    const { palette } = themes[mode];
    const background = mode === 'dark' ? palette[color].dark : palette[color].main;
    expect(contrast(palette.getContrastText(palette[color].main), background)).toBeGreaterThanOrEqual(AA);
  });

  it.each(allCases)('%s %s: main as text on the page and paper backgrounds', (mode, color) => {
    const { palette } = themes[mode];
    expect(contrast(palette[color].main, palette.background.default)).toBeGreaterThanOrEqual(AA);
    expect(contrast(palette[color].main, palette.background.paper)).toBeGreaterThanOrEqual(AA);
  });

  // A text or outlined button tints itself on hover, which takes contrast from its
  // own text. Dialogs use the paper background: see surfaces.test.tsx for the overlay.
  it.each(allCases)('%s %s: text or outlined button on hover, on page and paper', (mode, color) => {
    const { palette } = themes[mode];
    const main = palette[color].main;
    const hover = tint(main, palette.action.hoverOpacity);
    expect(contrast(main, over(hover, palette.background.default))).toBeGreaterThanOrEqual(AA);
    expect(contrast(main, over(hover, palette.background.paper))).toBeGreaterThanOrEqual(AA);
  });

  // MUI colours a menu item with the action colours, and focus is the stronger one.
  // Menus use the paper background: see surfaces.test.tsx for the overlay.
  it.each(modes)('%s error main: Delete and Clean menu items at rest, on hover and on focus', (mode) => {
    const { palette } = themes[mode];
    const surface = palette.background.paper;
    expect(contrast(palette.error.main, surface)).toBeGreaterThanOrEqual(AA);
    expect(contrast(palette.error.main, over(palette.action.hover, surface))).toBeGreaterThanOrEqual(AA);
    expect(contrast(palette.error.main, over(palette.action.focus, surface))).toBeGreaterThanOrEqual(AA);
  });

  // The selection toolbars of the dashboard and the template gallery.
  it.each(modes)('%s selection toolbar: contrastText on primary.dark', (mode) => {
    const { primary } = themes[mode].palette;
    expect(contrast(primary.contrastText, primary.dark)).toBeGreaterThanOrEqual(AA);
  });

  it.each([
    ['elevation 1 Paper', '#292929'],
    ['elevation 8 menu', '#383838'],
    ['elevation 8 menu (rounded up)', '#393939'],
  ])('dark error main as text on %s', (_name, background) => {
    expect(contrast(themes.dark.palette.error.main, background)).toBeGreaterThanOrEqual(AA);
  });
});
