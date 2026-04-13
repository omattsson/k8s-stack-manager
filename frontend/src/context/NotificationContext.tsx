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
      <Stack
        spacing={1}
        sx={{
          position: 'fixed',
          bottom: 16,
          left: 16,
          zIndex: 2000,
          pointerEvents: 'none',
        }}
      >
        {toasts.map((toast) => (
          <Snackbar
            key={toast.id}
            open
            autoHideDuration={toast.autoHideDuration}
            onClose={(_event, reason) => {
              if (reason !== 'clickaway') removeToast(toast.id);
            }}
            sx={{ position: 'static', pointerEvents: 'auto' }}
          >
            <Alert
              onClose={() => removeToast(toast.id)}
              severity={toast.severity}
              variant="filled"
              sx={{ width: '100%', minWidth: 300 }}
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
