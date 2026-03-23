import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Button,
  Alert,
  Paper,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  TablePagination,
  ToggleButtonGroup,
  ToggleButton,
} from '@mui/material';
import CheckCircleOutline from '@mui/icons-material/CheckCircleOutline';
import ErrorOutline from '@mui/icons-material/ErrorOutline';
import StopCircleOutlined from '@mui/icons-material/StopCircleOutlined';
import DeleteOutline from '@mui/icons-material/DeleteOutline';
import InfoOutlined from '@mui/icons-material/InfoOutlined';
import DoneAllIcon from '@mui/icons-material/DoneAll';
import { useNavigate } from 'react-router-dom';
import { notificationService } from '../../api/client';
import LoadingState from '../../components/LoadingState';
import type { Notification } from '../../types';

const PAGE_SIZE_OPTIONS = [10, 25, 50];

function timeAgo(dateStr: string): string {
  const now = Date.now();
  const then = new Date(dateStr).getTime();
  if (!Number.isFinite(then)) return dateStr || 'unknown';
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

function notificationIcon(type: string) {
  if (type.includes('success')) return <CheckCircleOutline color="success" fontSize="small" />;
  if (type.includes('error')) return <ErrorOutline color="error" fontSize="small" />;
  if (type.includes('stopped')) return <StopCircleOutlined color="warning" fontSize="small" />;
  if (type.includes('deleted')) return <DeleteOutline color="action" fontSize="small" />;
  return <InfoOutlined color="info" fontSize="small" />;
}

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

const Notifications = () => {
  const navigate = useNavigate();

  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [total, setTotal] = useState(0);
  const [unreadCount, setUnreadCount] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<'all' | 'unread'>('all');
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);

  const fetchNotifications = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await notificationService.list(
        filter === 'unread',
        rowsPerPage,
        page * rowsPerPage,
      );
      setNotifications(data.notifications || []);
      setTotal(data.total);
      setUnreadCount(data.unread_count);
    } catch {
      setError('Failed to load notifications');
    } finally {
      setLoading(false);
    }
  }, [filter, page, rowsPerPage]);

  useEffect(() => {
    fetchNotifications();
  }, [fetchNotifications]);

  const handleFilterChange = (_: React.MouseEvent<HTMLElement>, value: 'all' | 'unread' | null) => {
    if (value !== null) {
      setFilter(value);
      setPage(0);
    }
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
      navigate(`${basePath}/${notification.entity_id}`);
    }
  };

  const handleMarkAllRead = async () => {
    try {
      await notificationService.markAllAsRead();
      setNotifications((prev) => prev.map((n) => ({ ...n, is_read: true })));
      setUnreadCount(0);
    } catch {
      setError('Failed to mark all as read');
    }
  };

  return (
    <Box>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Notifications
        </Typography>
        <Box sx={{ display: 'flex', gap: 2, alignItems: 'center' }}>
          <ToggleButtonGroup
            value={filter}
            exclusive
            onChange={handleFilterChange}
            size="small"
          >
            <ToggleButton value="all">All</ToggleButton>
            <ToggleButton value="unread">
              Unread{unreadCount > 0 ? ` (${unreadCount})` : ''}
            </ToggleButton>
          </ToggleButtonGroup>
          {unreadCount > 0 && (
            <Button
              variant="outlined"
              size="small"
              startIcon={<DoneAllIcon />}
              onClick={handleMarkAllRead}
            >
              Mark all read
            </Button>
          )}
        </Box>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <LoadingState label="Loading notifications..." />
      ) : notifications.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <Typography color="text.secondary">
            {filter === 'unread' ? 'No unread notifications' : 'No notifications yet'}
          </Typography>
        </Paper>
      ) : (
        <Paper>
          <List disablePadding>
            {notifications.map((notification, index) => (
              <ListItem key={notification.id} disablePadding divider={index < notifications.length - 1}>
                <ListItemButton
                  onClick={() => handleClickNotification(notification)}
                  sx={{
                    bgcolor: notification.is_read ? 'transparent' : 'action.hover',
                    py: 2,
                    px: 2.5,
                  }}
                >
                  <ListItemIcon sx={{ minWidth: 40 }}>
                    {notificationIcon(notification.type)}
                  </ListItemIcon>
                  <ListItemText
                    primary={notification.title}
                    secondary={
                      <Box component="span" sx={{ display: 'flex', flexDirection: 'column', gap: 0.25 }}>
                        <Typography variant="body2" color="text.secondary" component="span">
                          {notification.message}
                        </Typography>
                        <Typography variant="caption" color="text.disabled" component="span">
                          {timeAgo(notification.created_at)}
                        </Typography>
                      </Box>
                    }
                    primaryTypographyProps={{
                      fontWeight: notification.is_read ? 400 : 600,
                    }}
                  />
                </ListItemButton>
              </ListItem>
            ))}
          </List>
          <TablePagination
            component="div"
            count={total}
            page={page}
            onPageChange={(_, newPage) => setPage(newPage)}
            rowsPerPage={rowsPerPage}
            onRowsPerPageChange={(e) => {
              setRowsPerPage(parseInt(e.target.value, 10));
              setPage(0);
            }}
            rowsPerPageOptions={PAGE_SIZE_OPTIONS}
          />
        </Paper>
      )}
    </Box>
  );
};

export default Notifications;
