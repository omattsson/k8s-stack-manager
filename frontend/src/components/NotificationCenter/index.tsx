import { useState, useEffect, useCallback, useRef } from 'react';
import {
  Badge,
  Box,
  Button,
  Divider,
  IconButton,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Popover,
  Tooltip,
  Typography,
  CircularProgress,
} from '@mui/material';
import NotificationsIcon from '@mui/icons-material/Notifications';
import CheckCircleOutline from '@mui/icons-material/CheckCircleOutline';
import ErrorOutline from '@mui/icons-material/ErrorOutline';
import StopCircleOutlined from '@mui/icons-material/StopCircleOutlined';
import DeleteOutline from '@mui/icons-material/DeleteOutline';
import InfoOutlined from '@mui/icons-material/InfoOutlined';
import DoneAllIcon from '@mui/icons-material/DoneAll';
import { useNavigate } from 'react-router-dom';
import { notificationService } from '../../api/client';
import { useWebSocket } from '../../hooks/useWebSocket';
import { useNotification } from '../../context/NotificationContext';
import type { Notification } from '../../types';

const POLL_INTERVAL_MS = 30_000;
const POPOVER_LIMIT = 10;

/**
 * Returns a human-readable relative time string for a given ISO date.
 */
function timeAgo(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  const diffSeconds = Math.floor((now - then) / 1000);

  if (diffSeconds < 60) return 'just now';
  const diffMinutes = Math.floor(diffSeconds / 60);
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 30) return `${diffDays}d ago`;
  return new Date(dateStr).toLocaleDateString();
}

/**
 * Returns an MUI icon element appropriate for the notification type.
 */
function notificationIcon(type: string) {
  if (type.includes('success')) return <CheckCircleOutline color="success" fontSize="small" />;
  if (type.includes('error')) return <ErrorOutline color="error" fontSize="small" />;
  if (type.includes('stopped')) return <StopCircleOutlined color="warning" fontSize="small" />;
  if (type.includes('deleted')) return <DeleteOutline color="action" fontSize="small" />;
  return <InfoOutlined color="info" fontSize="small" />;
}

/**
 * Maps a notification entity_type to a base route path for navigation.
 */
function entityRoute(entityType?: string): string {
  switch (entityType) {
    case 'stack_instance':
      return '/stack-instances';
    case 'stack_definition':
      return '/stack-definitions';
    case 'stack_template':
      return '/templates';
    default:
      return '';
  }
}

const NotificationCenter = () => {
  const navigate = useNavigate();
  const { showInfo } = useNotification();

  const [anchorEl, setAnchorEl] = useState<HTMLButtonElement | null>(null);
  const [unreadCount, setUnreadCount] = useState(0);
  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [loading, setLoading] = useState(false);

  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchUnreadCount = useCallback(async () => {
    try {
      const data = await notificationService.countUnread();
      setUnreadCount(data.unread_count);
    } catch {
      // Silently ignore polling errors
    }
  }, []);

  const fetchNotifications = useCallback(async () => {
    setLoading(true);
    try {
      const data = await notificationService.list(false, POPOVER_LIMIT, 0);
      setNotifications(data.notifications || []);
      setUnreadCount(data.unread_count);
    } catch {
      // Error is non-blocking for the popover
    } finally {
      setLoading(false);
    }
  }, []);

  // Poll unread count every 30 seconds
  useEffect(() => {
    fetchUnreadCount();
    pollRef.current = setInterval(fetchUnreadCount, POLL_INTERVAL_MS);
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [fetchUnreadCount]);

  // Listen for real-time notification WebSocket messages
  useWebSocket(useCallback((msg) => {
    if (msg.type === 'notification.new') {
      const notification = msg.payload as unknown as Notification;
      setUnreadCount((prev) => prev + 1);
      setNotifications((prev) => [notification, ...prev].slice(0, POPOVER_LIMIT));
      showInfo(notification.title);
    }
  }, [showInfo]));

  const handleOpen = (event: React.MouseEvent<HTMLButtonElement>) => {
    setAnchorEl(event.currentTarget);
    fetchNotifications();
  };

  const handleClose = () => {
    setAnchorEl(null);
  };

  const handleClickNotification = async (notification: Notification) => {
    if (!notification.is_read) {
      try {
        await notificationService.markAsRead(notification.id);
        setNotifications((prev) =>
          prev.map((n) => (n.id === notification.id ? { ...n, is_read: true } : n)),
        );
        setUnreadCount((prev) => Math.max(0, prev - 1));
      } catch {
        // Non-blocking
      }
    }

    const basePath = entityRoute(notification.entity_type);
    if (basePath && notification.entity_id) {
      handleClose();
      navigate(`${basePath}/${notification.entity_id}`);
    }
  };

  const handleMarkAllRead = async () => {
    try {
      await notificationService.markAllAsRead();
      setNotifications((prev) => prev.map((n) => ({ ...n, is_read: true })));
      setUnreadCount(0);
    } catch {
      // Non-blocking
    }
  };

  const handleViewAll = () => {
    handleClose();
    navigate('/notifications');
  };

  const open = Boolean(anchorEl);

  return (
    <>
      <Tooltip title="Notifications">
        <IconButton
          color="inherit"
          onClick={handleOpen}
          aria-label="Open notifications"
          size="small"
        >
          <Badge badgeContent={unreadCount} color="error" max={99}>
            <NotificationsIcon />
          </Badge>
        </IconButton>
      </Tooltip>

      <Popover
        open={open}
        anchorEl={anchorEl}
        onClose={handleClose}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
        transformOrigin={{ vertical: 'top', horizontal: 'right' }}
        slotProps={{
          paper: {
            sx: { width: 380, maxHeight: 480 },
          },
        }}
      >
        {/* Header */}
        <Box
          sx={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            px: 2,
            py: 1.5,
          }}
        >
          <Typography variant="h6" component="h2">
            Notifications
          </Typography>
          {unreadCount > 0 && (
            <Button
              size="small"
              startIcon={<DoneAllIcon />}
              onClick={handleMarkAllRead}
            >
              Mark all read
            </Button>
          )}
        </Box>
        <Divider />

        {/* Notification list */}
        {loading ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
            <CircularProgress size={28} />
          </Box>
        ) : notifications.length === 0 ? (
          <Box sx={{ py: 4, textAlign: 'center' }}>
            <Typography color="text.secondary" variant="body2">
              No notifications yet
            </Typography>
          </Box>
        ) : (
          <List disablePadding sx={{ overflow: 'auto', maxHeight: 340 }}>
            {notifications.map((notification) => (
              <ListItem key={notification.id} disablePadding>
                <ListItemButton
                  onClick={() => handleClickNotification(notification)}
                  sx={{
                    bgcolor: notification.is_read ? 'transparent' : 'action.hover',
                    py: 1.5,
                    px: 2,
                  }}
                >
                  <ListItemIcon sx={{ minWidth: 36 }}>
                    {notificationIcon(notification.type)}
                  </ListItemIcon>
                  <ListItemText
                    primary={notification.title}
                    secondary={
                      <Box component="span" sx={{ display: 'flex', flexDirection: 'column', gap: 0.25 }}>
                        <Typography
                          variant="body2"
                          color="text.secondary"
                          component="span"
                          sx={{
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                          }}
                        >
                          {notification.message}
                        </Typography>
                        <Typography variant="caption" color="text.disabled" component="span">
                          {timeAgo(notification.created_at)}
                        </Typography>
                      </Box>
                    }
                    primaryTypographyProps={{
                      variant: 'body2',
                      fontWeight: notification.is_read ? 400 : 600,
                      noWrap: true,
                    }}
                  />
                </ListItemButton>
              </ListItem>
            ))}
          </List>
        )}

        {/* Footer */}
        <Divider />
        <Box sx={{ p: 1, textAlign: 'center' }}>
          <Button size="small" onClick={handleViewAll}>
            View all notifications
          </Button>
        </Box>
      </Popover>
    </>
  );
};

export default NotificationCenter;
