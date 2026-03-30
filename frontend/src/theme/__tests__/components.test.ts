import { describe, it, expect } from 'vitest';
import { getComponentOverrides } from '../components';

describe('getComponentOverrides', () => {
  it('returns component overrides for light mode', () => {
    const overrides = getComponentOverrides('light');
    expect(overrides).toBeDefined();
    expect(overrides?.MuiButton?.styleOverrides?.root).toEqual(
      expect.objectContaining({ borderRadius: 8, textTransform: 'none' }),
    );
    expect(overrides?.MuiButton?.defaultProps?.disableElevation).toBe(true);
    expect(overrides?.MuiCard?.defaultProps?.elevation).toBe(0);
    expect(overrides?.MuiTextField?.defaultProps?.variant).toBe('outlined');
    expect(overrides?.MuiTextField?.defaultProps?.size).toBe('small');
  });

  it('returns component overrides for dark mode', () => {
    const overrides = getComponentOverrides('dark');
    expect(overrides).toBeDefined();
    expect(overrides?.MuiButton).toBeDefined();
    expect(overrides?.MuiCard).toBeDefined();
  });

  it('uses light divider color in light mode', () => {
    const overrides = getComponentOverrides('light');
    const cardRoot = overrides?.MuiCard?.styleOverrides?.root as Record<string, string>;
    expect(cardRoot.border).toContain('rgba(0, 0, 0, 0.12)');
  });

  it('uses dark divider color in dark mode', () => {
    const overrides = getComponentOverrides('dark');
    const cardRoot = overrides?.MuiCard?.styleOverrides?.root as Record<string, string>;
    expect(cardRoot.border).toContain('rgba(255, 255, 255, 0.12)');
  });

  it('includes all expected component overrides', () => {
    const overrides = getComponentOverrides('light');
    expect(overrides?.MuiPaper).toBeDefined();
    expect(overrides?.MuiChip).toBeDefined();
    expect(overrides?.MuiDialog).toBeDefined();
    expect(overrides?.MuiButtonBase).toBeDefined();
    expect(overrides?.MuiListItemButton).toBeDefined();
    expect(overrides?.MuiTooltip).toBeDefined();
  });
});
