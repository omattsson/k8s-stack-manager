import { useTheme } from '@mui/material';
import ReactDiffViewer, { type ReactDiffViewerProps } from 'react-diff-viewer-continued';

// The library draws line numbers at half opacity: 2.9:1 to 3.2:1 on their gutters.
const styles: ReactDiffViewerProps['styles'] = {
  gutter: { pre: { opacity: 1 } },
};

/** The diff viewer in the colour mode of the app, with readable line numbers. */
const ThemedDiffViewer = (props: Omit<ReactDiffViewerProps, 'styles' | 'useDarkTheme'>) => {
  const { palette } = useTheme();
  return <ReactDiffViewer {...props} useDarkTheme={palette.mode === 'dark'} styles={styles} />;
};

export default ThemedDiffViewer;
