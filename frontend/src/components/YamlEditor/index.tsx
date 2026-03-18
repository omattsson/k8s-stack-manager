import Editor, { type OnMount } from '@monaco-editor/react';
import { Box, Typography } from '@mui/material';
import { useState, useCallback, useRef } from 'react';
import yaml from 'js-yaml';

type EditorInstance = Parameters<OnMount>[0];

interface YamlEditorProps {
  value: string;
  onChange: (value: string) => void;
  label?: string;
  height?: string | number;
  readOnly?: boolean;
  error?: string;
}

const YamlEditor = ({ value, onChange, label, height = '300px', readOnly = false, error }: YamlEditorProps) => {
  const [validationError, setValidationError] = useState<string | null>(null);
  const editorRef = useRef<EditorInstance | null>(null);

  const handleEditorMount: OnMount = (editor) => {
    editorRef.current = editor;
  };

  const handleChange = useCallback(
    (newValue: string | undefined) => {
      const val = newValue ?? '';
      // Validate YAML
      try {
        if (val.trim()) {
          yaml.load(val);
        }
        setValidationError(null);
      } catch (e: unknown) {
        const message = e instanceof Error ? e.message : 'Invalid YAML';
        setValidationError(message);
      }
      onChange(val);
    },
    [onChange],
  );

  return (
    <Box>
      {label && (
        <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
          {label}
        </Typography>
      )}
      <Box
        sx={{
          border: 1,
          borderColor: error || validationError ? 'error.main' : 'divider',
          borderRadius: 1,
          overflow: 'hidden',
        }}
      >
        <Editor
          height={height}
          defaultLanguage="yaml"
          value={value}
          onChange={handleChange}
          onMount={handleEditorMount}
          options={{
            readOnly,
            minimap: { enabled: false },
            lineNumbers: 'on',
            scrollBeyondLastLine: false,
            wordWrap: 'on',
            fontSize: 13,
            tabSize: 2,
          }}
        />
      </Box>
      {(error || validationError) && (
        <Typography variant="caption" color="error" sx={{ mt: 0.5 }}>
          {error || validationError}
        </Typography>
      )}
    </Box>
  );
};

export default YamlEditor;
