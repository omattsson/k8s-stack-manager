import Editor, { type OnMount } from '@monaco-editor/react';
import { Box, Typography, useMediaQuery, useTheme } from '@mui/material';
import { useEffect, useMemo, useRef } from 'react';
import { useThemeMode } from '../../context/ThemeContext';
import { checkYamlValues } from '../../utils/yamlValidation';

type EditorInstance = Parameters<OnMount>[0];

interface YamlEditorProps {
  value: string;
  onChange: (value: string) => void;
  label?: string;
  height?: string | number;
  readOnly?: boolean;
  error?: string;
  /**
   * Called with the error that must block a save (null when there is none), on
   * mount and whenever it changes, also for a value that the parent loaded. A
   * value that only js-yaml refuses gives null: the API decides. Not called for
   * a read-only editor.
   */
  onValidationChange?: (error: string | null) => void;
}

const YamlEditor = ({ value, onChange, label, height = '300px', readOnly = false, error, onValidationChange }: YamlEditorProps) => {
  const { mode } = useThemeMode();
  // iOS Safari zooms on focus below 16px: use 16px on phones.
  const theme = useTheme();
  const isPhone = useMediaQuery(theme.breakpoints.down('sm'));
  const editorRef = useRef<EditorInstance | null>(null);

  // Read-only values are not the user's to fix, so they show no problem.
  const problem = useMemo(() => (readOnly ? null : checkYamlValues(value)), [value, readOnly]);
  const blockingError = problem?.blocking ? problem.message : null;

  // The latest callback, so an inline parent function does not fire the report on every render.
  const onValidationChangeRef = useRef(onValidationChange);
  useEffect(() => {
    onValidationChangeRef.current = onValidationChange;
  }, [onValidationChange]);
  useEffect(() => {
    if (!readOnly) onValidationChangeRef.current?.(blockingError);
  }, [blockingError, readOnly]);

  const handleEditorMount: OnMount = (editor) => {
    editorRef.current = editor;
  };

  return (
    <Box>
      {label && (
        <Typography variant="subtitle2" component="div" sx={{ mb: 0.5 }}>
          {label}
        </Typography>
      )}
      <Box
        sx={{
          border: 1,
          borderColor: error || blockingError ? 'error.main' : 'divider',
          borderRadius: 1,
          overflow: 'hidden',
        }}
      >
        <Editor
          height={height}
          defaultLanguage="yaml"
          value={value}
          onChange={(newValue = '') => onChange(newValue)}
          onMount={handleEditorMount}
          theme={mode === 'dark' ? 'vs-dark' : 'vs'}
          options={{
            readOnly,
            minimap: { enabled: false },
            lineNumbers: 'on',
            scrollBeyondLastLine: false,
            wordWrap: 'on',
            fontSize: isPhone ? 16 : 13,
            tabSize: 2,
            // On Enter, keep the indentation of the current line. The default
            // ('advanced') also adds an indent after a line that ends with a
            // colon, so typed YAML gets more indentation than the user typed.
            autoIndent: 'keep',
          }}
        />
      </Box>
      {(error || blockingError) ? (
        <Typography variant="caption" color="error" sx={{ mt: 0.5 }}>
          {error || blockingError}
        </Typography>
      ) : problem && (
        <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5 }} data-testid="yaml-hint">
          Possible problem: {problem.message}
        </Typography>
      )}
    </Box>
  );
};

export default YamlEditor;
