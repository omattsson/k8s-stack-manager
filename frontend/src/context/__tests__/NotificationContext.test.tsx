import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { NotificationProvider, useNotification } from '../NotificationContext';

const wrapper = ({ children }: { children: ReactNode }) => (
  <NotificationProvider>{children}</NotificationProvider>
);

describe('NotificationContext', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  describe('useNotification outside provider', () => {
    it('throws when used outside NotificationProvider', () => {
      const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
      expect(() => renderHook(() => useNotification())).toThrow(
        'useNotification must be used within a NotificationProvider'
      );
      spy.mockRestore();
    });
  });

  describe('showSuccess', () => {
    it('renders a success alert with the given message', async () => {
      const TestComponent = () => {
        const { showSuccess } = useNotification();
        return <button onClick={() => showSuccess('Operation succeeded')}>Trigger</button>;
      };

      render(
        <NotificationProvider>
          <TestComponent />
        </NotificationProvider>
      );

      await userEvent.click(screen.getByText('Trigger'));

      expect(await screen.findByText('Operation succeeded')).toBeInTheDocument();
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  describe('showError', () => {
    it('renders an error alert with the given message', async () => {
      const TestComponent = () => {
        const { showError } = useNotification();
        return <button onClick={() => showError('Something failed')}>Trigger</button>;
      };

      render(
        <NotificationProvider>
          <TestComponent />
        </NotificationProvider>
      );

      await userEvent.click(screen.getByText('Trigger'));

      expect(await screen.findByText('Something failed')).toBeInTheDocument();
    });
  });

  describe('showWarning', () => {
    it('renders a warning alert with the given message', async () => {
      const TestComponent = () => {
        const { showWarning } = useNotification();
        return <button onClick={() => showWarning('Be careful')}>Trigger</button>;
      };

      render(
        <NotificationProvider>
          <TestComponent />
        </NotificationProvider>
      );

      await userEvent.click(screen.getByText('Trigger'));

      expect(await screen.findByText('Be careful')).toBeInTheDocument();
    });
  });

  describe('showInfo', () => {
    it('renders an info alert with the given message', async () => {
      const TestComponent = () => {
        const { showInfo } = useNotification();
        return <button onClick={() => showInfo('FYI update')}>Trigger</button>;
      };

      render(
        <NotificationProvider>
          <TestComponent />
        </NotificationProvider>
      );

      await userEvent.click(screen.getByText('Trigger'));

      expect(await screen.findByText('FYI update')).toBeInTheDocument();
    });
  });

  describe('auto-dismiss', () => {
    it('hides success notification after autoHideDuration', async () => {
      const TestComponent = () => {
        const { showSuccess } = useNotification();
        return <button onClick={() => showSuccess('Will disappear')}>Trigger</button>;
      };

      render(
        <NotificationProvider>
          <TestComponent />
        </NotificationProvider>
      );

      await userEvent.click(screen.getByText('Trigger'));
      expect(await screen.findByText('Will disappear')).toBeInTheDocument();

      // Advance past the 4000ms autoHideDuration for success
      act(() => {
        vi.advanceTimersByTime(5000);
      });

      await waitFor(() => {
        expect(screen.queryByText('Will disappear')).not.toBeInTheDocument();
      });
    });
  });

  describe('close button', () => {
    it('dismisses notification when close button is clicked', async () => {
      const TestComponent = () => {
        const { showSuccess } = useNotification();
        return <button onClick={() => showSuccess('Close me')}>Trigger</button>;
      };

      render(
        <NotificationProvider>
          <TestComponent />
        </NotificationProvider>
      );

      await userEvent.click(screen.getByText('Trigger'));
      expect(await screen.findByText('Close me')).toBeInTheDocument();

      // The Alert has a close button
      const closeButton = screen.getByRole('button', { name: /close/i });
      await userEvent.click(closeButton);

      await waitFor(() => {
        expect(screen.queryByText('Close me')).not.toBeInTheDocument();
      });
    });
  });

  describe('queuing', () => {
    it('queues a second notification when one is already showing', async () => {
      let callCount = 0;
      const TestComponent = () => {
        const { showSuccess, showError } = useNotification();
        return (
          <>
            <button onClick={() => {
              callCount++;
              if (callCount === 1) showSuccess('First');
              else showError('Second');
            }}>
              Trigger
            </button>
          </>
        );
      };

      render(
        <NotificationProvider>
          <TestComponent />
        </NotificationProvider>
      );

      // Show first notification
      await userEvent.click(screen.getByText('Trigger'));
      expect(await screen.findByText('First')).toBeInTheDocument();

      // Queue second while first is showing
      await userEvent.click(screen.getByText('Trigger'));

      // First should still be visible (second is queued)
      expect(screen.getByText('First')).toBeInTheDocument();
    });
  });

  describe('hook return value', () => {
    it('provides all four notification methods', () => {
      const { result } = renderHook(() => useNotification(), { wrapper });

      expect(typeof result.current.showSuccess).toBe('function');
      expect(typeof result.current.showError).toBe('function');
      expect(typeof result.current.showWarning).toBe('function');
      expect(typeof result.current.showInfo).toBe('function');
    });
  });
});
