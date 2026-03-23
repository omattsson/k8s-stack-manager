import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import LoadingState from '../index';

describe('LoadingState', () => {
  it('renders a loading spinner', () => {
    render(<LoadingState />);
    expect(screen.getByRole('status')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('does not render a label by default', () => {
    render(<LoadingState />);
    // Only the spinner should be present, no text
    expect(screen.queryByText(/.+/)).not.toBeInTheDocument();
  });

  it('renders a custom label when provided', () => {
    render(<LoadingState label="Loading items..." />);
    expect(screen.getByText('Loading items...')).toBeInTheDocument();
  });

  it('has aria-live="polite" for accessibility', () => {
    render(<LoadingState />);
    expect(screen.getByRole('status')).toHaveAttribute('aria-live', 'polite');
  });
});
