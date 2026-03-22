import { createContext, useContext, useState, useCallback, useMemo } from 'react';
import type { ReactNode } from 'react';
import { Snackbar, Alert } from '@mui/material';
import type { AlertColor } from '@mui/material';

interface Notification {
  message: string;
  severity: AlertColor;
  autoHideDuration: number | null;
}

interface NotificationContextType {
  showSuccess: (message: string) => void;
  showError: (message: string) => void;
  showWarning: (message: string) => void;
  showInfo: (message: string) => void;
}

const NotificationContext = createContext<NotificationContextType | undefined>(undefined);

export const NotificationProvider = ({ children }: { children: ReactNode }) => {
  const [queue, setQueue] = useState<Notification[]>([]);
  const [current, setCurrent] = useState<Notification | null>(null);
  const [open, setOpen] = useState(false);

  const showNext = useCallback((notifications: Notification[]) => {
    if (notifications.length > 0) {
      setCurrent(notifications[0]);
      setQueue(notifications.slice(1));
      setOpen(true);
    }
  }, []);

  const enqueue = useCallback((notification: Notification) => {
    if (!current) {
      setCurrent(notification);
      setOpen(true);
    } else {
      setQueue((prev) => [...prev, notification]);
    }
  }, [current]);

  const handleClose = useCallback((_event?: React.SyntheticEvent | Event, reason?: string) => {
    if (reason === 'clickaway') return;
    setOpen(false);
  }, []);

  const handleExited = useCallback(() => {
    setCurrent(null);
    showNext(queue);
  }, [queue, showNext]);

  const showSuccess = useCallback((message: string) => {
    enqueue({ message, severity: 'success', autoHideDuration: 4000 });
  }, [enqueue]);

  const showError = useCallback((message: string) => {
    enqueue({ message, severity: 'error', autoHideDuration: null });
  }, [enqueue]);

  const showWarning = useCallback((message: string) => {
    enqueue({ message, severity: 'warning', autoHideDuration: 6000 });
  }, [enqueue]);

  const showInfo = useCallback((message: string) => {
    enqueue({ message, severity: 'info', autoHideDuration: 4000 });
  }, [enqueue]);

  const value = useMemo(
    () => ({ showSuccess, showError, showWarning, showInfo }),
    [showSuccess, showError, showWarning, showInfo],
  );

  return (
    <NotificationContext.Provider value={value}>
      {children}
      <Snackbar
        open={open}
        autoHideDuration={current?.autoHideDuration}
        onClose={handleClose}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
        TransitionProps={{ onExited: handleExited }}
      >
        {current ? (
          <Alert
            onClose={handleClose}
            severity={current.severity}
            variant="filled"
            sx={{ width: '100%' }}
          >
            {current.message}
          </Alert>
        ) : undefined}
      </Snackbar>
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
