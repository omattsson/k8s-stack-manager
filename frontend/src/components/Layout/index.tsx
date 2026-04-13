import { type ReactNode, useState, useEffect } from 'react';
import {
  Box,
  AppBar,
  Toolbar,
  Typography,
  Container,
  Chip,
  Drawer,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  ListSubheader,
  Divider,
  IconButton,
  Tooltip,
  useMediaQuery,
  useTheme,
} from '@mui/material';
import { Link as RouterLink, useLocation } from 'react-router-dom';
import { useAuth } from '../../context/AuthContext';
import { useThemeMode } from '../../context/ThemeContext';
import { hasAtLeastRole } from '../../utils/roles';
import { DRAWER_WIDTH, DRAWER_COLLAPSED_WIDTH } from '../../theme';

import MenuIcon from '@mui/icons-material/Menu';
import ChevronLeftIcon from '@mui/icons-material/ChevronLeft';
import DarkModeOutlined from '@mui/icons-material/DarkModeOutlined';
import LightModeOutlined from '@mui/icons-material/LightModeOutlined';
import DashboardOutlined from '@mui/icons-material/DashboardOutlined';
import ViewModuleOutlined from '@mui/icons-material/ViewModuleOutlined';
import DescriptionOutlined from '@mui/icons-material/DescriptionOutlined';
import HistoryOutlined from '@mui/icons-material/HistoryOutlined';
import MonitorHeartOutlined from '@mui/icons-material/MonitorHeartOutlined';
import BarChartOutlined from '@mui/icons-material/BarChartOutlined';
import PeopleOutlined from '@mui/icons-material/PeopleOutlined';
import DeleteSweepOutlined from '@mui/icons-material/DeleteSweepOutlined';
import CloudOutlined from '@mui/icons-material/CloudOutlined';
import TuneOutlined from '@mui/icons-material/TuneOutlined';
import CleaningServicesOutlined from '@mui/icons-material/CleaningServicesOutlined';
import AccountCircleOutlined from '@mui/icons-material/AccountCircleOutlined';
import LogoutIcon from '@mui/icons-material/Logout';
import HubOutlined from '@mui/icons-material/HubOutlined';
import NotificationCenter from '../NotificationCenter';

const DRAWER_MINI_WIDTH = DRAWER_COLLAPSED_WIDTH;
const STORAGE_KEY = 'sidebar-open';

interface LayoutProps {
  children: ReactNode;
}

interface NavItem {
  label: string;
  path: string;
  icon: ReactNode;
}

const mainNav: NavItem[] = [
  { label: 'Dashboard', path: '/', icon: <DashboardOutlined /> },
  { label: 'Templates', path: '/templates', icon: <ViewModuleOutlined /> },
  { label: 'Definitions', path: '/stack-definitions', icon: <DescriptionOutlined /> },
  { label: 'Audit Log', path: '/audit-log', icon: <HistoryOutlined /> },
];

const operationsNav: NavItem[] = [
  { label: 'Cluster Health', path: '/admin/cluster-health', icon: <MonitorHeartOutlined /> },
  { label: 'Analytics', path: '/admin/analytics', icon: <BarChartOutlined /> },
];

const adminNav: NavItem[] = [
  { label: 'Users', path: '/admin/users', icon: <PeopleOutlined /> },
  { label: 'Orphaned NS', path: '/admin/orphaned-namespaces', icon: <DeleteSweepOutlined /> },
  { label: 'Clusters', path: '/admin/clusters', icon: <CloudOutlined /> },
  { label: 'Shared Values', path: '/admin/shared-values', icon: <TuneOutlined /> },
  { label: 'Cleanup Policies', path: '/admin/cleanup-policies', icon: <CleaningServicesOutlined /> },
];

const isRouteActive = (pathname: string, itemPath: string): boolean => {
  if (itemPath === '/') return pathname === '/';
  return pathname === itemPath || pathname.startsWith(itemPath + '/');
};

const Layout = ({ children }: LayoutProps) => {
  const { user, isAuthenticated, logout } = useAuth();
  const { mode, toggleMode } = useThemeMode();
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down('md'));
  const location = useLocation();

  const [desktopOpen, setDesktopOpen] = useState(() => {
    const stored = localStorage.getItem(STORAGE_KEY);
    return stored === null ? true : stored === 'true';
  });
  const [mobileOpen, setMobileOpen] = useState(false);

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, String(desktopOpen));
  }, [desktopOpen]);

  // Close mobile drawer on navigation
  useEffect(() => {
    setMobileOpen(false);
  }, [location.pathname]);

  if (!isAuthenticated) {
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', minHeight: '100vh' }}>
        <Container component="main" maxWidth="lg" sx={{ mt: 4, mb: 4, flex: '1 0 auto' }}>
          {children}
        </Container>
      </Box>
    );
  }

  const currentWidth = desktopOpen ? DRAWER_WIDTH : DRAWER_MINI_WIDTH;

  const renderNavSection = (
    items: NavItem[],
    sectionLabel: string,
    open: boolean,
  ) => (
    <>
      {open ? (
        <ListSubheader
          sx={{
            fontSize: '0.675rem',
            fontWeight: 700,
            letterSpacing: '0.08em',
            textTransform: 'uppercase',
            lineHeight: '36px',
            color: 'text.secondary',
            bgcolor: 'transparent',
          }}
        >
          {sectionLabel}
        </ListSubheader>
      ) : (
        <Divider sx={{ my: 1 }} />
      )}
      {items.map((item) => {
        const active = isRouteActive(location.pathname, item.path);
        return (
          <ListItemButton
            key={item.path}
            component={RouterLink}
            to={item.path}
            selected={active}
            sx={{
              minHeight: 44,
              px: 2.5,
              justifyContent: open ? 'initial' : 'center',
              borderRadius: 1,
              mx: 1,
              mb: 0.25,
            }}
          >
            <Tooltip title={open ? '' : item.label} placement="right" arrow>
              <ListItemIcon
                sx={{
                  minWidth: 0,
                  mr: open ? 2 : 'auto',
                  justifyContent: 'center',
                  color: active ? 'primary.main' : 'text.secondary',
                }}
              >
                {item.icon}
              </ListItemIcon>
            </Tooltip>
            {open && <ListItemText primary={item.label} />}
          </ListItemButton>
        );
      })}
    </>
  );

  const renderDrawerHeader = (open: boolean) => (
    <Toolbar
      sx={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: open ? 'space-between' : 'center',
        px: open ? 2 : 0,
      }}
    >
      {open ? (
        <Typography
          variant="h6"
          noWrap
          component={RouterLink}
          to="/"
          sx={{ color: 'inherit', textDecoration: 'none', fontWeight: 700 }}
        >
          K8s Stack Manager
        </Typography>
      ) : (
        <Tooltip title="K8s Stack Manager" placement="right" arrow>
          <IconButton component={RouterLink} to="/" size="small" aria-label="Home">
            <HubOutlined color="primary" />
          </IconButton>
        </Tooltip>
      )}
      {!isMobile && (
        <IconButton
          onClick={() => setDesktopOpen((prev) => !prev)}
          size="small"
          aria-label={open ? 'Collapse sidebar' : 'Expand sidebar'}
        >
          <ChevronLeftIcon
            sx={{
              transition: 'transform 0.2s',
              transform: open ? 'rotate(0deg)' : 'rotate(180deg)',
            }}
          />
        </IconButton>
      )}
    </Toolbar>
  );

  const renderDarkModeToggle = (open: boolean) => (
    <Box sx={{ px: open ? 2 : 1, py: 1, display: 'flex', alignItems: 'center', justifyContent: open ? 'flex-start' : 'center' }}>
      {open ? (
        <ListItemButton
          onClick={toggleMode}
          sx={{ borderRadius: 1, py: 0.5 }}
        >
          <ListItemIcon sx={{ minWidth: 0, mr: 2, justifyContent: 'center', color: 'text.secondary' }}>
            {mode === 'dark' ? <LightModeOutlined /> : <DarkModeOutlined />}
          </ListItemIcon>
          <ListItemText
            primary={mode === 'dark' ? 'Light mode' : 'Dark mode'}
            slotProps={{ primary: { variant: 'body2' } }}
          />
        </ListItemButton>
      ) : (
        <Tooltip title={mode === 'dark' ? 'Light mode' : 'Dark mode'} placement="right" arrow>
          <IconButton onClick={toggleMode} size="small" aria-label="Toggle dark mode">
            {mode === 'dark' ? <LightModeOutlined /> : <DarkModeOutlined />}
          </IconButton>
        </Tooltip>
      )}
    </Box>
  );

  const roleChipColor = user?.role === 'admin' ? 'error' as const : user?.role === 'devops' ? 'warning' as const : 'default' as const;

  const renderUserSectionExpanded = () => (
    <>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
        <AccountCircleOutlined sx={{ color: 'text.secondary' }} />
        <Box sx={{ overflow: 'hidden' }}>
          <Typography variant="body2" sx={{ fontWeight: 600 }} noWrap>
            {user?.username}
          </Typography>
          <Chip
            label={user?.role}
            size="small"
            color={roleChipColor}
            sx={{ height: 20, fontSize: '0.7rem' }}
          />
        </Box>
      </Box>
      <Box sx={{ display: 'flex', gap: 1 }}>
        <ListItemButton
          component={RouterLink}
          to="/profile"
          selected={location.pathname === '/profile'}
          sx={{ borderRadius: 1, py: 0.5, flex: 1, justifyContent: 'center' }}
        >
          <ListItemText
            primary="Profile"
            slotProps={{ primary: { variant: 'body2', sx: { textAlign: 'center' } } }}
          />
        </ListItemButton>
        <ListItemButton
          onClick={logout}
          sx={{ borderRadius: 1, py: 0.5, flex: 1, justifyContent: 'center' }}
        >
          <ListItemText
            primary="Logout"
            slotProps={{ primary: { variant: 'body2', sx: { textAlign: 'center' } } }}
          />
        </ListItemButton>
      </Box>
    </>
  );

  const renderUserSectionCollapsed = () => (
    <>
      <Tooltip title="Profile" placement="right" arrow>
        <IconButton
          component={RouterLink}
          to="/profile"
          size="small"
          aria-label="Profile"
          color={location.pathname === '/profile' ? 'primary' : 'default'}
        >
          <AccountCircleOutlined />
        </IconButton>
      </Tooltip>
      <Tooltip title="Logout" placement="right" arrow>
        <IconButton onClick={logout} size="small" aria-label="Logout">
          <LogoutIcon />
        </IconButton>
      </Tooltip>
    </>
  );

  const drawerContent = (open: boolean) => (
    <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      {renderDrawerHeader(open)}
      <Divider />

      <List component="nav" aria-label="Main navigation" sx={{ flex: 1, overflow: 'auto', pt: 1 }}>
        {renderNavSection(mainNav, 'Main', open)}
        {hasAtLeastRole(user?.role, 'devops') &&
          renderNavSection(operationsNav, 'Operations', open)}
        {user?.role === 'admin' &&
          renderNavSection(adminNav, 'Administration', open)}
      </List>

      <Divider />
      {renderDarkModeToggle(open)}

      <Divider />
      <Box sx={{ p: open ? 2 : 1, display: 'flex', flexDirection: 'column', alignItems: open ? 'stretch' : 'center', gap: 1 }}>
        {!isMobile && (
          <Box sx={{ display: 'flex', justifyContent: open ? 'flex-start' : 'center', pl: open ? 0.5 : 0 }}>
            <NotificationCenter />
          </Box>
        )}
        {open ? renderUserSectionExpanded() : renderUserSectionCollapsed()}
      </Box>
    </Box>
  );

  return (
    <Box sx={{ display: 'flex', minHeight: '100vh' }}>
      {/* Skip to content link */}
      <Box
        component="a"
        href="#main-content"
        sx={{
          position: 'absolute',
          left: '-9999px',
          top: 'auto',
          width: '1px',
          height: '1px',
          overflow: 'hidden',
          '&:focus': {
            position: 'fixed',
            top: 8,
            left: 8,
            width: 'auto',
            height: 'auto',
            zIndex: 9999,
            bgcolor: 'background.paper',
            p: 1,
            borderRadius: 1,
            boxShadow: 3,
          },
        }}
      >
        Skip to main content
      </Box>

      {/* Mobile AppBar */}
      {isMobile && (
        <AppBar
          position="fixed"
          sx={{ zIndex: theme.zIndex.drawer + 1 }}
        >
          <Toolbar>
            <IconButton
              color="inherit"
              edge="start"
              onClick={() => setMobileOpen(true)}
              aria-label="Open navigation"
              sx={{ mr: 2 }}
            >
              <MenuIcon />
            </IconButton>
            <Typography variant="h6" noWrap sx={{ fontWeight: 700, flexGrow: 1 }}>
              K8s Stack Manager
            </Typography>
            <NotificationCenter />
          </Toolbar>
        </AppBar>
      )}

      {/* Mobile drawer (temporary overlay) */}
      {isMobile && (
        <Drawer
          variant="temporary"
          open={mobileOpen}
          onClose={() => setMobileOpen(false)}
          ModalProps={{ keepMounted: true }}
          sx={{
            '& .MuiDrawer-paper': {
              width: DRAWER_WIDTH,
              boxSizing: 'border-box',
            },
          }}
        >
          {drawerContent(true)}
        </Drawer>
      )}

      {/* Desktop drawer (persistent) */}
      {!isMobile && (
        <Drawer
          variant="permanent"
          open={desktopOpen}
          sx={{
            width: currentWidth,
            flexShrink: 0,
            '& .MuiDrawer-paper': {
              width: currentWidth,
              boxSizing: 'border-box',
              overflowX: 'hidden',
              transition: theme.transitions.create('width', {
                easing: theme.transitions.easing.sharp,
                duration: desktopOpen
                  ? theme.transitions.duration.enteringScreen
                  : theme.transitions.duration.leavingScreen,
              }),
            },
          }}
        >
          {drawerContent(desktopOpen)}
        </Drawer>
      )}

      {/* Main content */}
      <Box
        component="main"
        id="main-content"
        sx={{
          flexGrow: 1,
          minWidth: 0,
          transition: theme.transitions.create('margin', {
            easing: theme.transitions.easing.sharp,
            duration: theme.transitions.duration.enteringScreen,
          }),
          ...(isMobile && { mt: '64px' }),
        }}
      >
        <Container maxWidth="lg" sx={{ mt: 4, mb: 4 }}>
          {children}
        </Container>
      </Box>
    </Box>
  );
};

export default Layout;
