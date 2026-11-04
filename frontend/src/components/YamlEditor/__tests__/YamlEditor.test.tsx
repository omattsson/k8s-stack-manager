import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import YamlEditor from '../index';

const { mockUseThemeMode } = vi.hoisted(() => ({
  mockUseThemeMode: vi.fn().mockReturnValue({ mode: 'dark', toggleMode: vi.fn() }),
}));

vi.mock('../../../context/ThemeContext', () => ({
  useThemeMode: () => mockUseThemeMode(),
}));

// Mock the Monaco Editor since it requires a browser environment
vi.mock('@monaco-editor/react', () => ({
  default: ({
    value,
    onChange,
    options,
    theme,
  }: {
    value: string;
    onChange?: (value: string | undefined) => void;
    options?: { readOnly?: boolean; autoIndent?: string };
    theme?: string;
  }) => (
    <div data-testid="monaco-editor" data-theme={theme} data-autoindent={options?.autoIndent}>
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

  it('keeps the indentation of the current line on Enter', () => {
    render(<YamlEditor {...defaultProps} />);
    expect(screen.getByTestId('monaco-editor')).toHaveAttribute('data-autoindent', 'keep');
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

  describe('validation (#753)', () => {
    it('reports a blocking error for a value that the parent loaded', () => {
      const onValidationChange = vi.fn();
      render(<YamlEditor value="tag: {{.Branch}}" onChange={vi.fn()} onValidationChange={onValidationChange} />);

      expect(onValidationChange).toHaveBeenCalledTimes(1);
      expect(onValidationChange.mock.calls[0][0]).toMatch(/complex keys/);
      expect(screen.getByText(/complex keys/)).toBeInTheDocument();
    });

    it('reports null for a valid value', () => {
      const onValidationChange = vi.fn();
      render(<YamlEditor value={'tag: "{{.Branch}}"'} onChange={vi.fn()} onValidationChange={onValidationChange} />);

      expect(onValidationChange).toHaveBeenCalledWith(null);
    });

    it('reports the change when the value becomes valid', () => {
      const onValidationChange = vi.fn();
      const { rerender } = render(<YamlEditor value="a: b: c" onChange={vi.fn()} onValidationChange={onValidationChange} />);
      expect(onValidationChange).toHaveBeenLastCalledWith(expect.stringMatching(/bad indentation/));

      rerender(<YamlEditor value="a: b" onChange={vi.fn()} onValidationChange={onValidationChange} />);
      expect(onValidationChange).toHaveBeenLastCalledWith(null);
    });

    it('does not report again on a render without a change, also with a new callback', () => {
      const first = vi.fn();
      const second = vi.fn();
      const { rerender } = render(<YamlEditor value="a: b" onChange={vi.fn()} onValidationChange={first} />);
      rerender(<YamlEditor value="a: b" onChange={vi.fn()} onValidationChange={second} />);

      expect(first).toHaveBeenCalledTimes(1);
      expect(second).not.toHaveBeenCalled();
    });

    it('shows a hint and reports null for a value that only js-yaml refuses', () => {
      const onValidationChange = vi.fn();
      render(<YamlEditor value={'a: 1\na: 2'} onChange={vi.fn()} onValidationChange={onValidationChange} />);

      expect(onValidationChange).toHaveBeenCalledWith(null);
      expect(screen.getByTestId('yaml-hint')).toHaveTextContent(/^Possible problem: /);
    });

    it('does not check or report a read-only value', () => {
      const onValidationChange = vi.fn();
      render(<YamlEditor value="tag: {{.Branch}}" onChange={vi.fn()} readOnly onValidationChange={onValidationChange} />);

      expect(onValidationChange).not.toHaveBeenCalled();
      expect(screen.queryByText(/complex keys/)).not.toBeInTheDocument();
    });

    it('prefers an external error to the hint', () => {
      render(<YamlEditor value={'a: 1\na: 2'} onChange={vi.fn()} error="YAML is required" />);

      expect(screen.getByText('YAML is required')).toBeInTheDocument();
      expect(screen.queryByTestId('yaml-hint')).not.toBeInTheDocument();
    });
  });

  it('renders the editor container', () => {
    render(<YamlEditor {...defaultProps} />);
    expect(screen.getByTestId('monaco-editor')).toBeInTheDocument();
  });

  it('uses vs-dark theme in dark mode', () => {
    mockUseThemeMode.mockReturnValue({ mode: 'dark', toggleMode: vi.fn() });
    render(<YamlEditor {...defaultProps} />);
    expect(screen.getByTestId('monaco-editor')).toHaveAttribute('data-theme', 'vs-dark');
  });

  it('uses vs theme in light mode', () => {
    mockUseThemeMode.mockReturnValue({ mode: 'light', toggleMode: vi.fn() });
    render(<YamlEditor {...defaultProps} />);
    expect(screen.getByTestId('monaco-editor')).toHaveAttribute('data-theme', 'vs');
  });
});
