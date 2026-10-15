import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import CloneDialog from '../CloneDialog';
import { instanceService } from '../../../api/client';
import type { StackInstance } from '../../../types';

vi.mock('../../../api/client', () => ({
  instanceService: {
    clone: vi.fn(),
  },
}));

const source = {
  id: 'inst-1',
  name: 'demo',
  branch: 'main',
  ttl_minutes: 0,
} as StackInstance;

describe('CloneDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders the suggested clone name', () => {
    render(<CloneDialog open={true} source={source} onClose={vi.fn()} onCloned={vi.fn()} />);
    expect(screen.getByLabelText(/name/i, { selector: 'input' })).toHaveValue('demo-copy');
  });

  it('shows the owner and the free names of a 409 name conflict', async () => {
    const user = userEvent.setup();
    (instanceService.clone as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: {
        status: 409,
        data: {
          error: 'instance name already exists',
          message: 'A stack named "demo-copy" already exists on this cluster (owner: carol)',
          suggestions: ['demo-copy-3'],
        },
      },
    });

    render(<CloneDialog open={true} source={source} onClose={vi.fn()} onCloned={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /create clone/i }));

    await waitFor(() => {
      expect(
        screen.getByText('A stack named "demo-copy" already exists on this cluster (owner: carol). Free names: demo-copy-3.'),
      ).toBeInTheDocument();
    });
  });
});
