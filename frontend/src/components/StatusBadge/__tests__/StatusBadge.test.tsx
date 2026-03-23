import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import StatusBadge from '../index';

describe('StatusBadge', () => {
  it('renders the status text', () => {
    render(<StatusBadge status="running" />);
    expect(screen.getByText('running')).toBeInTheDocument();
  });

  it.each([
    ['draft', 'default'],
    ['deploying', 'info'],
    ['running', 'success'],
    ['stopped', 'warning'],
    ['stopping', 'warning'],
    ['cleaning', 'warning'],
    ['error', 'error'],
  ])('renders "%s" status with "%s" color chip', (status, color) => {
    render(<StatusBadge status={status} />);
    const chip = screen.getByText(status);
    expect(chip.closest('.MuiChip-root')).toHaveClass(`MuiChip-color${color.charAt(0).toUpperCase() + color.slice(1)}`);
  });

  it('renders with default color for unknown status', () => {
    render(<StatusBadge status="unknown" />);
    const chip = screen.getByText('unknown');
    expect(chip.closest('.MuiChip-root')).toHaveClass('MuiChip-colorDefault');
  });

  it('renders as a small chip', () => {
    render(<StatusBadge status="running" />);
    const chip = screen.getByText('running').closest('.MuiChip-root');
    expect(chip).toHaveClass('MuiChip-sizeSmall');
  });
});
