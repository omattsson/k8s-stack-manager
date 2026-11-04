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

  // Known existing exceptions, deliberately not fixed here: dark success (4.16),
  // info (4.28) and secondary (4.14) with black text on their `dark` shade.
  const hoverCases: Array<[Mode, ColorName]> = [
    ...COLORS.map((c): [Mode, ColorName] => ['light', c]),
    ['dark', 'primary'],
    ['dark', 'warning'],
    ['dark', 'error'],
  ];
  const allCases = modes.flatMap((m) => COLORS.map((c): [Mode, ColorName] => [m, c]));

  it.each(allCases)('%s %s: contrastText on main', (mode, color) => {
    const p = themes[mode].palette[color];
    expect(contrast(p.contrastText, p.main)).toBeGreaterThanOrEqual(AA);
  });

  it.each(hoverCases)('%s %s: contrastText on dark (hover)', (mode, color) => {
    const p = themes[mode].palette[color];
    expect(contrast(p.contrastText, p.dark)).toBeGreaterThanOrEqual(AA);
  });

  it.each([
    ['dark', 'error', 'dark'],
    ['dark', 'warning', 'dark'],
    ['light', 'error', 'main'],
    ['light', 'warning', 'main'],
    ['light', 'info', 'main'],
    ['light', 'success', 'main'],
  ] as Array<[Mode, ColorName, 'main' | 'dark']>)(
    '%s %s: filled Alert text on %s',
    (mode, color, shade) => {
      const { palette } = themes[mode];
      const background = palette[color][shade];
      expect(contrast(palette.getContrastText(palette[color].main), background)).toBeGreaterThanOrEqual(AA);
    },
  );

  it.each(allCases)('%s %s: main as text on the page and paper backgrounds', (mode, color) => {
    const { palette } = themes[mode];
    expect(contrast(palette[color].main, palette.background.default)).toBeGreaterThanOrEqual(AA);
    expect(contrast(palette[color].main, palette.background.paper)).toBeGreaterThanOrEqual(AA);
  });

  it.each([
    ['elevation 1 Paper', '#292929'],
    ['elevation 8 menu', '#383838'],
    ['elevation 8 menu (rounded up)', '#393939'],
  ])('dark error main as text on %s', (_name, background) => {
    expect(contrast(themes.dark.palette.error.main, background)).toBeGreaterThanOrEqual(AA);
  });
});
