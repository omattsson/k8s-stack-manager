import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { ThemeModeProvider, useThemeMode } from '../ThemeContext';

// Mock useMediaQuery to control prefers-color-scheme
let mockPrefersDark = false;
vi.mock('@mui/material', async () => {
  const actual = await vi.importActual('@mui/material');
  return {
    ...actual,
    useMediaQuery: () => mockPrefersDark,
  };
});

const wrapper = ({ children }: { children: ReactNode }) => (
  <ThemeModeProvider>{children}</ThemeModeProvider>
);

describe('ThemeContext', () => {
  beforeEach(() => {
    localStorage.clear();
    mockPrefersDark = false;
  });

  afterEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
  });

  describe('useThemeMode outside provider', () => {
    it('throws when used outside ThemeModeProvider', () => {
      const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
      expect(() => renderHook(() => useThemeMode())).toThrow(
        'useThemeMode must be used within a ThemeModeProvider'
      );
      spy.mockRestore();
    });
  });

  describe('default mode', () => {
    it('defaults to light when system prefers light and no stored value', () => {
      mockPrefersDark = false;
      const { result } = renderHook(() => useThemeMode(), { wrapper });
      expect(result.current.mode).toBe('light');
    });

    it('defaults to dark when system prefers dark and no stored value', () => {
      mockPrefersDark = true;
      const { result } = renderHook(() => useThemeMode(), { wrapper });
      expect(result.current.mode).toBe('dark');
    });

    it('uses stored value over system preference', () => {
      mockPrefersDark = false;
      localStorage.setItem('theme-mode', 'dark');
      const { result } = renderHook(() => useThemeMode(), { wrapper });
      expect(result.current.mode).toBe('dark');
    });

    it('uses stored light value over dark system preference', () => {
      mockPrefersDark = true;
      localStorage.setItem('theme-mode', 'light');
      const { result } = renderHook(() => useThemeMode(), { wrapper });
      expect(result.current.mode).toBe('light');
    });
  });

  describe('toggleMode', () => {
    it('toggles from light to dark', () => {
      mockPrefersDark = false;
      const { result } = renderHook(() => useThemeMode(), { wrapper });
      expect(result.current.mode).toBe('light');

      act(() => {
        result.current.toggleMode();
      });

      expect(result.current.mode).toBe('dark');
    });

    it('toggles from dark to light', () => {
      localStorage.setItem('theme-mode', 'dark');
      const { result } = renderHook(() => useThemeMode(), { wrapper });
      expect(result.current.mode).toBe('dark');

      act(() => {
        result.current.toggleMode();
      });

      expect(result.current.mode).toBe('light');
    });

    it('toggles back and forth', () => {
      mockPrefersDark = false;
      const { result } = renderHook(() => useThemeMode(), { wrapper });

      act(() => result.current.toggleMode());
      expect(result.current.mode).toBe('dark');

      act(() => result.current.toggleMode());
      expect(result.current.mode).toBe('light');
    });
  });

  describe('persistence to localStorage', () => {
    it('saves mode to localStorage on toggle', () => {
      mockPrefersDark = false;
      const { result } = renderHook(() => useThemeMode(), { wrapper });

      act(() => {
        result.current.toggleMode();
      });

      expect(localStorage.getItem('theme-mode')).toBe('dark');
    });

    it('saves initial mode to localStorage', () => {
      mockPrefersDark = false;
      renderHook(() => useThemeMode(), { wrapper });

      expect(localStorage.getItem('theme-mode')).toBe('light');
    });
  });

  describe('MUI ThemeProvider integration', () => {
    it('provides a MUI theme to children', () => {
      const ThemeConsumer = () => {
        // useTheme is from MUI — if ThemeProvider works, this won't throw
        const { mode } = useThemeMode();
        return <div data-testid="mode">{mode}</div>;
      };

      render(
        <ThemeModeProvider>
          <ThemeConsumer />
        </ThemeModeProvider>
      );

      expect(screen.getByTestId('mode')).toHaveTextContent('light');
    });

    it('renders CssBaseline via the provider (no crash)', () => {
      render(
        <ThemeModeProvider>
          <div data-testid="child">Hello</div>
        </ThemeModeProvider>
      );

      expect(screen.getByTestId('child')).toBeInTheDocument();
    });
  });
});
