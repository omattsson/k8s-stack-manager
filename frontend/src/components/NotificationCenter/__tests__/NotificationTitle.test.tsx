import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import NotificationTitle from '../NotificationTitle';

describe('NotificationTitle', () => {
  it('says that a row is unread, in words', () => {
    render(<NotificationTitle notification={{ title: 'Deploy failed', is_read: false }} />);
    expect(screen.getByText(/Deploy failed/)).toBeInTheDocument();
    expect(screen.getByText(', unread')).toBeInTheDocument();
  });

  it('adds nothing to a read title', () => {
    render(<NotificationTitle notification={{ title: 'Deploy done', is_read: true }} />);
    expect(screen.getByText('Deploy done')).toBeInTheDocument();
    expect(screen.queryByText(', unread')).not.toBeInTheDocument();
  });
});
