import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import EmptyState from '../index';
import { Button } from '@mui/material';
import InboxIcon from '@mui/icons-material/Inbox';

describe('EmptyState', () => {
  it('renders the title', () => {
    render(<EmptyState title="No items found" />);
    expect(screen.getByText('No items found')).toBeInTheDocument();
  });

  it('renders the description when provided', () => {
    render(<EmptyState title="No items" description="Create your first item to get started." />);
    expect(screen.getByText('Create your first item to get started.')).toBeInTheDocument();
  });

  it('does not render description when not provided', () => {
    render(<EmptyState title="No items" />);
    // Only the title should be present
    expect(screen.getByText('No items')).toBeInTheDocument();
    expect(screen.queryByText('Create your first item to get started.')).not.toBeInTheDocument();
  });

  it('renders the action button when provided', () => {
    render(
      <EmptyState
        title="No items"
        action={<Button>Create Item</Button>}
      />,
    );
    expect(screen.getByRole('button', { name: 'Create Item' })).toBeInTheDocument();
  });

  it('does not render action when not provided', () => {
    render(<EmptyState title="No items" />);
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('renders the icon when provided', () => {
    render(
      <EmptyState
        title="No items"
        icon={<InboxIcon data-testid="empty-icon" />}
      />,
    );
    expect(screen.getByTestId('empty-icon')).toBeInTheDocument();
  });

  it('does not render icon container when icon is not provided', () => {
    const { container } = render(<EmptyState title="No items" />);
    expect(container.querySelector('.MuiSvgIcon-root')).not.toBeInTheDocument();
  });
});
