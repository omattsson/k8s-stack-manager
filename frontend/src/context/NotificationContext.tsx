import { createContext, useContext, useState, useCallback, useMemo, useRef } from 'react';
import type { ReactNode } from 'react';
import { Snackbar, Alert, Stack } from '@mui/material';
import type { AlertColor } from '@mui/material';

interface ToastItem {
  id: number;
  message: string;
  severity: AlertColor;
  autoHideDuration: number;
}

interface NotificationContextType {
  showSuccess: (message: string) => void;
  showError: (message: string) => void;
  showWarning: (message: string) => void;
  showInfo: (message: string) => void;
}

const MAX_VISIBLE = 4;

/**
 * Top offset of the toasts in px below the md breakpoint. The Layout shows a
 * fixed app bar there. Its height is 56 px to 64 px (theme.mixins.toolbar),
 * and the main content starts at 64 px. The toasts start 8 px below that.
 */
const TOAST_TOP_BELOW_APP_BAR = 72;

/** Top offset of the toasts in px from the md breakpoint. The Layout shows no app bar there. */
const TOAST_TOP_NO_APP_BAR = 16;

const NotificationContext = createContext<NotificationContextType | undefined>(undefined);

export const NotificationProvider = ({ children }: { children: ReactNode }) => {
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const idCounter = useRef(0);

  const addToast = useCallback((message: string, severity: AlertColor, autoHideDuration: number) => {
    const id = ++idCounter.current;
    setToasts((prev) => {
      const next = [...prev, { id, message, severity, autoHideDuration }];
      // Keep only the most recent MAX_VISIBLE toasts
      return next.length > MAX_VISIBLE ? next.slice(-MAX_VISIBLE) : next;
    });
  }, []);

  const removeToast = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const showSuccess = useCallback((message: string) => {
    addToast(message, 'success', 4000);
  }, [addToast]);

  const showError = useCallback((message: string) => {
    addToast(message, 'error', 8000);
  }, [addToast]);

  const showWarning = useCallback((message: string) => {
    addToast(message, 'warning', 6000);
  }, [addToast]);

  const showInfo = useCallback((message: string) => {
    addToast(message, 'info', 4000);
  }, [addToast]);

  const value = useMemo(
    () => ({ showSuccess, showError, showWarning, showInfo }),
    [showSuccess, showError, showWarning, showInfo],
  );

  return (
    <NotificationContext.Provider value={value}>
      {children}
      {/*
        The toasts show at the top center of the screen, so they do not cover
        the action buttons of a dialog (#630). The snackbar layer of the theme
        is above the modal layer, so the toasts stay visible over a dialog.
        Only the toasts take clicks: the full-width container ignores them.
      */}
      <Stack
        data-testid="toast-stack"
        spacing={1}
        sx={(theme) => ({
          position: 'fixed',
          top: `calc(${TOAST_TOP_BELOW_APP_BAR}px + env(safe-area-inset-top, 0px))`,
          [theme.breakpoints.up('md')]: {
            top: `calc(${TOAST_TOP_NO_APP_BAR}px + env(safe-area-inset-top, 0px))`,
          },
          left: 0,
          right: 0,
          px: 2,
          alignItems: 'center',
          zIndex: theme.zIndex.snackbar,
          pointerEvents: 'none',
        })}
      >
        {toasts.map((toast) => (
          <Snackbar
            key={toast.id}
            open
            autoHideDuration={toast.autoHideDuration}
            onClose={(_event, reason) => {
              if (reason !== 'clickaway') removeToast(toast.id);
            }}
            // Keep the default anchorOrigin. The Stack sets the position. A
            // center anchor adds a translateX(-50%) that moves a static toast.
            sx={{ position: 'static', pointerEvents: 'auto' }}
          >
            <Alert
              onClose={() => removeToast(toast.id)}
              severity={toast.severity}
              variant="filled"
              sx={{ width: '100%', minWidth: 'min(300px, calc(100vw - 32px))' }}
            >
              {toast.message}
            </Alert>
          </Snackbar>
        ))}
      </Stack>
    </NotificationContext.Provider>
  );
};

export const useNotification = (): NotificationContextType => {
  const context = useContext(NotificationContext);
  if (context === undefined) {
    throw new Error('useNotification must be used within a NotificationProvider');
  }
  return context;
};
