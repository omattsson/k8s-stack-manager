import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ImportDefinitionDialog from '../ImportDefinitionDialog';

vi.mock('../../../api/client', () => ({
  definitionService: {
    importDefinition: vi.fn(),
  },
}));

import { definitionService } from '../../../api/client';

const validBundle = JSON.stringify({
  schema_version: '1.0',
  exported_at: '2024-01-01T00:00:00Z',
  definition: {
    name: 'Test Definition',
    description: 'A test',
    default_branch: 'main',
    repository_url: 'https://example.com/repo',
  },
  charts: [
    { chart_name: 'chart-1', repository_url: 'https://example.com', default_values: '', sort_order: 0 },
  ],
});

describe('ImportDefinitionDialog', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders dialog when open', () => {
    render(
      <ImportDefinitionDialog open={true} onClose={vi.fn()} onImported={vi.fn()} />
    );
    expect(screen.getByText('Import Stack Definition')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /select file/i })).toBeInTheDocument();
  });

  it('does not render dialog when closed', () => {
    render(
      <ImportDefinitionDialog open={false} onClose={vi.fn()} onImported={vi.fn()} />
    );
    expect(screen.queryByText('Import Stack Definition')).not.toBeInTheDocument();
  });

  it('shows preview after selecting a valid file', async () => {
    const user = userEvent.setup();
    render(
      <ImportDefinitionDialog open={true} onClose={vi.fn()} onImported={vi.fn()} />
    );

    const file = new File([validBundle], 'test-export.json', { type: 'application/json' });
    const input = screen.getByLabelText('Select definition file');
    await user.upload(input, file);

    await waitFor(() => {
      expect(screen.getByText('Preview')).toBeInTheDocument();
      expect(screen.getByText('Test Definition')).toBeInTheDocument();
      expect(screen.getByText('1')).toBeInTheDocument();
    });
  });

  it('shows error for invalid JSON file', async () => {
    const user = userEvent.setup();
    render(
      <ImportDefinitionDialog open={true} onClose={vi.fn()} onImported={vi.fn()} />
    );

    const file = new File(['not valid json'], 'bad.json', { type: 'application/json' });
    const input = screen.getByLabelText('Select definition file');
    await user.upload(input, file);

    await waitFor(() => {
      expect(screen.getByText(/invalid json file/i)).toBeInTheDocument();
    });
  });

  it('shows error for bundle missing schema_version', async () => {
    const user = userEvent.setup();
    render(
      <ImportDefinitionDialog open={true} onClose={vi.fn()} onImported={vi.fn()} />
    );

    const file = new File([JSON.stringify({ definition: { name: 'Test' }, charts: [] })], 'bad.json', { type: 'application/json' });
    const input = screen.getByLabelText('Select definition file');
    await user.upload(input, file);

    await waitFor(() => {
      expect(screen.getByText(/missing schema_version/i)).toBeInTheDocument();
    });
  });

  it('calls importDefinition and onImported on confirm', async () => {
    const user = userEvent.setup();
    const onImported = vi.fn();
    const created = { id: 'new-id', name: 'Test Definition' };
    (definitionService.importDefinition as ReturnType<typeof vi.fn>).mockResolvedValue(created);

    render(
      <ImportDefinitionDialog open={true} onClose={vi.fn()} onImported={onImported} />
    );

    const file = new File([validBundle], 'test-export.json', { type: 'application/json' });
    const input = screen.getByLabelText('Select definition file');
    await user.upload(input, file);

    await waitFor(() => {
      expect(screen.getByText('Preview')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /^import$/i }));

    await waitFor(() => {
      expect(definitionService.importDefinition).toHaveBeenCalledTimes(1);
      expect(onImported).toHaveBeenCalledWith(created);
    });
  });

  it('shows error when import API call fails', async () => {
    const user = userEvent.setup();
    (definitionService.importDefinition as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('server error'));

    render(
      <ImportDefinitionDialog open={true} onClose={vi.fn()} onImported={vi.fn()} />
    );

    const file = new File([validBundle], 'test-export.json', { type: 'application/json' });
    const input = screen.getByLabelText('Select definition file');
    await user.upload(input, file);

    await waitFor(() => {
      expect(screen.getByText('Preview')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /^import$/i }));

    await waitFor(() => {
      expect(screen.getByText(/failed to import definition/i)).toBeInTheDocument();
    });
  });

  it('calls onClose when Cancel is clicked', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(
      <ImportDefinitionDialog open={true} onClose={onClose} onImported={vi.fn()} />
    );

    await user.click(screen.getByRole('button', { name: /cancel/i }));
    expect(onClose).toHaveBeenCalled();
  });
});
