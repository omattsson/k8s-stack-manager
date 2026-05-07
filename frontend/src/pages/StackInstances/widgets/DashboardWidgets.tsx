import { useEffect, useState, useCallback, useRef } from 'react';
import {
  Accordion,
  AccordionSummary,
  AccordionDetails,
  Typography,
  Box,
  CircularProgress,
  Chip,
  Alert,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import DnsIcon from '@mui/icons-material/Dns';
import RocketLaunchIcon from '@mui/icons-material/RocketLaunch';
import TimerIcon from '@mui/icons-material/Timer';
import ErrorOutlinedIcon from '@mui/icons-material/ErrorOutlined';
import { dashboardService } from '../../../api/client';
import { useWebSocket } from '../../../hooks/useWebSocket';
import type { DashboardResponse } from '../../../types';
import ClusterHealthWidget from './ClusterHealthWidget';
import RecentDeploymentsWidget from './RecentDeploymentsWidget';
import ExpiringSoonWidget from './ExpiringSoonWidget';
import FailingInstancesWidget from './FailingInstancesWidget';

const STORAGE_KEY = 'dashboard_widget_collapsed';

// Must match backend's buildExpiringSoon threshold (1 * time.Hour).
const EXPIRING_THRESHOLD_MS = 60 * 60 * 1000;

type WidgetKey = 'clusters' | 'deployments' | 'expiring' | 'failing';

const defaultCollapsed: Record<WidgetKey, boolean> = { clusters: false, deployments: false, expiring: false, failing: false };

function loadCollapsed(): Record<WidgetKey, boolean> {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) return { ...defaultCollapsed, ...JSON.parse(stored) };
  } catch { /* ignore */ }
  return { ...defaultCollapsed };
}

function saveCollapsed(state: Record<WidgetKey, boolean>) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
}

const DashboardWidgets = () => {
  const [data, setData] = useState<DashboardResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [collapsed, setCollapsed] = useState<Record<WidgetKey, boolean>>(loadCollapsed);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fetchData = useCallback(async () => {
    try {
      const resp = await dashboardService.getOverview();
      setData(resp);
      setError(false);
    } catch {
      setError(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [fetchData]);

  const debouncedRefetch = useCallback(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      fetchData();
    }, 2000);
  }, [fetchData]);

  useWebSocket(
    useCallback(
      (msg) => {
        if (
          msg.type === 'deployment.status' ||
          msg.type === 'instance.status' ||
          msg.type === 'cluster.health_changed'
        ) {
          debouncedRefetch();
        }
      },
      [debouncedRefetch],
    ),
  );

  const toggle = (key: WidgetKey) => {
    setCollapsed((prev) => {
      const next = { ...prev, [key]: !prev[key] };
      saveCollapsed(next);
      return next;
    });
  };

  const handleTtlExtended = (id: string, newExpiresAt: string) => {
    setData((prev) => {
      if (!prev) return prev;
      return {
        ...prev,
        expiring_soon: prev.expiring_soon
          .map((i) => (i.id === id ? { ...i, expires_at: newExpiresAt } : i))
          .filter((i) => {
            const ms = new Date(i.expires_at).getTime() - Date.now();
            return ms > 0 && ms <= EXPIRING_THRESHOLD_MS;
          }),
      };
    });
  };

  if (loading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', py: 3 }}>
        <CircularProgress size={24} />
      </Box>
    );
  }

  if (error && !data) {
    return (
      <Box sx={{ mb: 3 }}>
        <Alert severity="warning" variant="outlined">
          Failed to load dashboard data.
        </Alert>
      </Box>
    );
  }

  if (!data) return null;

  const widgets: { key: WidgetKey; label: string; icon: React.ReactNode; count?: number; content: React.ReactNode }[] = [
    {
      key: 'clusters',
      label: 'Cluster Health',
      icon: <DnsIcon fontSize="small" />,
      count: data.clusters.length,
      content: <ClusterHealthWidget clusters={data.clusters} />,
    },
    {
      key: 'deployments',
      label: 'Recent Deployments',
      icon: <RocketLaunchIcon fontSize="small" />,
      count: data.recent_deployments.length,
      content: <RecentDeploymentsWidget deployments={data.recent_deployments} />,
    },
    {
      key: 'expiring',
      label: 'Expiring Soon',
      icon: <TimerIcon fontSize="small" />,
      count: data.expiring_soon.length,
      content: <ExpiringSoonWidget instances={data.expiring_soon} onExtended={handleTtlExtended} />,
    },
    {
      key: 'failing',
      label: 'Failing Instances',
      icon: <ErrorOutlinedIcon fontSize="small" />,
      count: data.failing_instances.length,
      content: <FailingInstancesWidget instances={data.failing_instances} />,
    },
  ];

  return (
    <Box sx={{ mb: 3 }}>
      {widgets.map(({ key, label, icon, count, content }) => (
        <Accordion
          key={key}
          expanded={!collapsed[key]}
          onChange={() => toggle(key)}
          disableGutters
          variant="outlined"
          sx={{ '&:before': { display: 'none' }, mb: 1 }}
        >
          <AccordionSummary expandIcon={<ExpandMoreIcon />}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
              {icon}
              <Typography variant="subtitle2">{label}</Typography>
              {count != null && count > 0 && (
                <Chip
                  label={count}
                  size="small"
                  color={key === 'failing' && count > 0 ? 'error' : 'default'}
                  sx={{ height: 20, fontSize: '0.7rem' }}
                />
              )}
            </Box>
          </AccordionSummary>
          <AccordionDetails sx={{ pt: 0 }}>
            {content}
          </AccordionDetails>
        </Accordion>
      ))}
    </Box>
  );
};

export default DashboardWidgets;
