import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import YamlEditor from '../index';

vi.mock('../../../context/ThemeContext', () => ({
  useThemeMode: () => ({ mode: 'dark', toggleMode: vi.fn() }),
}));

// Mock the Monaco Editor since it requires a browser environment
vi.mock('@monaco-editor/react', () => ({
  default: ({
    value,
    onChange,
    options,
  }: {
    value: string;
    onChange?: (value: string | undefined) => void;
    options?: { readOnly?: boolean };
  }) => (
    <div data-testid="monaco-editor">
      <textarea
        data-testid="monaco-textarea"
        value={value}
        readOnly={options?.readOnly}
        onChange={(e) => onChange?.(e.target.value)}
      />
    </div>
  ),
}));

describe('YamlEditor', () => {
  const defaultProps = {
    value: 'key: value',
    onChange: vi.fn(),
  };

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders with initial value', () => {
    render(<YamlEditor {...defaultProps} />);
    const textarea = screen.getByTestId('monaco-textarea');
    expect(textarea).toHaveValue('key: value');
  });

  it('renders the label when provided', () => {
    render(<YamlEditor {...defaultProps} label="YAML Values" />);
    expect(screen.getByText('YAML Values')).toBeInTheDocument();
  });

  it('does not render a label when not provided', () => {
    render(<YamlEditor {...defaultProps} />);
    expect(screen.queryByText('YAML Values')).not.toBeInTheDocument();
  });

  it('calls onChange when content is edited', async () => {
    const { default: userEvent } = await import('@testing-library/user-event');
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<YamlEditor value="" onChange={onChange} />);

    const textarea = screen.getByTestId('monaco-textarea');
    await user.type(textarea, 'a');
    expect(onChange).toHaveBeenCalled();
  });

  it('displays an external error message', () => {
    render(<YamlEditor {...defaultProps} error="YAML is required" />);
    expect(screen.getByText('YAML is required')).toBeInTheDocument();
  });

  it('renders the editor container', () => {
    render(<YamlEditor {...defaultProps} />);
    expect(screen.getByTestId('monaco-editor')).toBeInTheDocument();
  });
});
