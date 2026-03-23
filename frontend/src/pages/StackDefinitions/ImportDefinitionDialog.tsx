import { useState, useRef, type ChangeEvent } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Typography,
  Box,
  Alert,
  CircularProgress,
  Paper,
} from '@mui/material';
import FileUploadIcon from '@mui/icons-material/FileUpload';
import type { DefinitionExportBundle, StackDefinition } from '../../types';
import { definitionService } from '../../api/client';

interface ImportDefinitionDialogProps {
  open: boolean;
  onClose: () => void;
  onImported: (definition: StackDefinition) => void;
}

const ImportDefinitionDialog = ({ open, onClose, onImported }: ImportDefinitionDialogProps) => {
  const [bundle, setBundle] = useState<DefinitionExportBundle | null>(null);
  const [fileName, setFileName] = useState<string | null>(null);
  const [parseError, setParseError] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const resetState = () => {
    setBundle(null);
    setFileName(null);
    setParseError(null);
    setImporting(false);
    setImportError(null);
  };

  const handleClose = () => {
    resetState();
    onClose();
  };

  const handleFileSelect = (event: ChangeEvent<HTMLInputElement>) => {
    setParseError(null);
    setImportError(null);
    setBundle(null);
    setFileName(null);

    const file = event.target.files?.[0];
    if (!file) return;

    setFileName(file.name);

    const reader = new FileReader();
    reader.onload = (e) => {
      try {
        const parsed = JSON.parse(e.target?.result as string) as DefinitionExportBundle;
        if (!parsed.schema_version) {
          setParseError('Invalid bundle: missing schema_version field');
          return;
        }
        if (!parsed.definition) {
          setParseError('Invalid bundle: missing definition field');
          return;
        }
        setBundle(parsed);
      } catch {
        setParseError('Invalid JSON file. Please select a valid definition export file.');
      }
    };
    reader.onerror = () => {
      setParseError('Failed to read file');
    };
    reader.readAsText(file);

    // Reset input so the same file can be re-selected
    event.target.value = '';
  };

  const handleImport = async () => {
    if (!bundle) return;
    setImporting(true);
    setImportError(null);
    try {
      const created = await definitionService.importDefinition(bundle);
      onImported(created);
      handleClose();
    } catch {
      setImportError('Failed to import definition. The server may have rejected the bundle.');
    } finally {
      setImporting(false);
    }
  };

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>Import Stack Definition</DialogTitle>
      <DialogContent>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Select a JSON file previously exported from a stack definition.
        </Typography>

        <input
          ref={fileInputRef}
          type="file"
          accept=".json"
          onChange={handleFileSelect}
          style={{ display: 'none' }}
          aria-label="Select definition file"
        />

        <Box sx={{ display: 'flex', justifyContent: 'center', mb: 2 }}>
          <Button
            variant="outlined"
            startIcon={<FileUploadIcon />}
            onClick={() => fileInputRef.current?.click()}
            disabled={importing}
          >
            {fileName ? 'Choose Different File' : 'Select File'}
          </Button>
        </Box>

        {fileName && !parseError && !bundle && (
          <Typography variant="body2" color="text.secondary" sx={{ textAlign: 'center', mb: 1 }}>
            Reading {fileName}...
          </Typography>
        )}

        {parseError && (
          <Alert severity="error" sx={{ mb: 2 }}>{parseError}</Alert>
        )}

        {importError && (
          <Alert severity="error" sx={{ mb: 2 }}>{importError}</Alert>
        )}

        {bundle && (
          <Paper variant="outlined" sx={{ p: 2 }}>
            <Typography variant="subtitle2" gutterBottom>Preview</Typography>
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
              <Typography variant="body2">
                <strong>Name:</strong> {bundle.definition.name}
              </Typography>
              {bundle.definition.description && (
                <Typography variant="body2">
                  <strong>Description:</strong> {bundle.definition.description}
                </Typography>
              )}
              <Typography variant="body2">
                <strong>Default Branch:</strong> {bundle.definition.default_branch || 'master'}
              </Typography>
              <Typography variant="body2">
                <strong>Charts:</strong> {bundle.charts?.length ?? 0}
              </Typography>
              <Typography variant="body2" color="text.secondary">
                Schema version: {bundle.schema_version}
              </Typography>
            </Box>
          </Paper>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={handleClose} disabled={importing}>Cancel</Button>
        <Button
          variant="contained"
          onClick={handleImport}
          disabled={!bundle || importing}
          startIcon={importing ? <CircularProgress size={16} /> : undefined}
        >
          {importing ? 'Importing...' : 'Import'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default ImportDefinitionDialog;
