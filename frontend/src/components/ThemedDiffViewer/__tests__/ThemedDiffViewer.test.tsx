import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider } from '@mui/material';
import { createAppTheme } from '../../../theme';
import ThemedDiffViewer from '../index';

interface ViewerProps {
  useDarkTheme?: boolean;
  styles?: { gutter?: { pre?: { opacity?: number } } };
}

const captured: ViewerProps[] = [];

vi.mock('react-diff-viewer-continued', () => ({
  default: (props: ViewerProps) => {
    captured.push(props);
    return <div data-testid="diff" />;
  },
}));

const renderIn = (mode: 'light' | 'dark') => {
  captured.length = 0;
  render(
    <ThemeProvider theme={createAppTheme(mode)}>
      <ThemedDiffViewer oldValue="a" newValue="b" />
    </ThemeProvider>,
  );
  expect(screen.getByTestId('diff')).toBeInTheDocument();
  return captured[captured.length - 1];
};

describe('ThemedDiffViewer', () => {
  it.each([['light', false], ['dark', true]] as const)('follows the %s colour mode', (mode, dark) => {
    expect(renderIn(mode).useDarkTheme).toBe(dark);
  });

  it.each(['light', 'dark'] as const)('draws line numbers at full opacity in %s mode', (mode) => {
    expect(renderIn(mode).styles?.gutter?.pre?.opacity).toBe(1);
  });
});
