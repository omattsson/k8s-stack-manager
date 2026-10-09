import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Alert,
  Card,
  CardContent,
  Grid,
  Chip,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  Button,
  LinearProgress,
  Breadcrumbs,
  Link as MuiLink,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import NavigateNextIcon from '@mui/icons-material/NavigateNext';
import { analyticsService } from '../../api/client';
import type { OverviewStats, TemplateStats, UserStats } from '../../types';
import LoadingState from '../../components/LoadingState';
import { Link } from 'react-router-dom';
import { useAuth } from '../../context/AuthContext';

const formatRelativeTime = (dateStr: string | null): string => {
  if (!dateStr) return 'N/A';
  try {
    const date = new Date(dateStr);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    if (diffMins < 1) return 'Just now';
    if (diffMins < 60) return `${diffMins} minute${diffMins === 1 ? '' : 's'} ago`;
    const diffHours = Math.floor(diffMins / 60);
    if (diffHours < 24) return `${diffHours} hour${diffHours === 1 ? '' : 's'} ago`;
    const diffDays = Math.floor(diffHours / 24);
    if (diffDays < 30) return `${diffDays} day${diffDays === 1 ? '' : 's'} ago`;
    return date.toLocaleDateString();
  } catch {
    return 'N/A';
  }
};

const successRateColor = (rate: number): 'success' | 'warning' | 'error' => {
  if (rate > 80) return 'success';
  if (rate > 50) return 'warning';
  return 'error';
};

const Analytics = () => {
  const { user } = useAuth();
  // GET /analytics/users is admin-only. Devops users see the overview and
  // the template statistics only.
  const isAdmin = user?.role === 'admin';
  const [overview, setOverview] = useState<OverviewStats | null>(null);
  const [templates, setTemplates] = useState<TemplateStats[]>([]);
  const [users, setUsers] = useState<UserStats[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [templatesError, setTemplatesError] = useState(false);
  const [usersError, setUsersError] = useState(false);
  const [allFailed, setAllFailed] = useState(false);

  const fetchData = useCallback(async () => {
    setLoading(true);
    setError(null);
    setTemplatesError(false);
    setUsersError(false);
    // Load each section on its own, so that one failed request does not
    // hide the sections that loaded.
    const [overviewResult, templateResult, userResult] = await Promise.allSettled([
      analyticsService.getOverview(),
      analyticsService.getTemplateStats(),
      isAdmin ? analyticsService.getUserStats() : Promise.resolve([] as UserStats[]),
    ]);
    if (overviewResult.status === 'fulfilled') {
      setOverview(overviewResult.value);
    } else {
      setOverview(null);
    }
    if (templateResult.status === 'fulfilled') {
      setTemplates(templateResult.value || []);
    } else {
      setTemplates([]);
      setTemplatesError(true);
    }
    if (userResult.status === 'fulfilled') {
      setUsers(userResult.value || []);
    } else {
      setUsers([]);
      setUsersError(true);
    }
    const failed = overviewResult.status === 'rejected'
      && templateResult.status === 'rejected'
      && (!isAdmin || userResult.status === 'rejected');
    setAllFailed(failed);
    if (failed) {
      setError('Failed to load analytics data');
    } else if (overviewResult.status === 'rejected') {
      setError('Failed to load the analytics overview');
    }
    setLoading(false);
  }, [isAdmin]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  return (
    <Box>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <MuiLink component={Link} to="/" underline="hover" color="inherit">Home</MuiLink>
        <Typography color="text.primary">Analytics</Typography>
      </Breadcrumbs>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 3 }}>
        <Typography variant="h4" component="h1">
          Analytics
        </Typography>
        <Button
          variant="outlined"
          startIcon={<RefreshIcon />}
          onClick={fetchData}
          disabled={loading}
        >
          Refresh
        </Button>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading && <LoadingState label="Loading analytics..." />}

      {!loading && !allFailed && (
        <>
          {/* Overview Cards */}
          {overview && (
            <Grid container spacing={2} sx={{ mb: 4 }}>
              <Grid size={{ xs: 12, sm: 6, md: 4 }}>
                <Card>
                  <CardContent>
                    <Typography color="text.secondary" gutterBottom>
                      Templates
                    </Typography>
                    <Typography variant="h4">{overview.total_templates}</Typography>
                  </CardContent>
                </Card>
              </Grid>
              <Grid size={{ xs: 12, sm: 6, md: 4 }}>
                <Card>
                  <CardContent>
                    <Typography color="text.secondary" gutterBottom>
                      Definitions
                    </Typography>
                    <Typography variant="h4">{overview.total_definitions}</Typography>
                  </CardContent>
                </Card>
              </Grid>
              <Grid size={{ xs: 12, sm: 6, md: 4 }}>
                <Card>
                  <CardContent>
                    <Typography color="text.secondary" gutterBottom>
                      Instances
                    </Typography>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                      <Typography variant="h4">{overview.total_instances}</Typography>
                      <Chip
                        label={`${overview.running_instances} running`}
                        size="small"
                        color="success"
                      />
                    </Box>
                  </CardContent>
                </Card>
              </Grid>
              <Grid size={{ xs: 12, sm: 6, md: 4 }}>
                <Card>
                  <CardContent>
                    <Typography color="text.secondary" gutterBottom>
                      Running Instances
                    </Typography>
                    <Typography variant="h4">{overview.running_instances}</Typography>
                  </CardContent>
                </Card>
              </Grid>
              <Grid size={{ xs: 12, sm: 6, md: 4 }}>
                <Card>
                  <CardContent>
                    <Typography color="text.secondary" gutterBottom>
                      Total Deploys
                    </Typography>
                    <Typography variant="h4">{overview.total_deploys}</Typography>
                  </CardContent>
                </Card>
              </Grid>
              <Grid size={{ xs: 12, sm: 6, md: 4 }}>
                <Card>
                  <CardContent>
                    <Typography color="text.secondary" gutterBottom>
                      Users
                    </Typography>
                    <Typography variant="h4">{overview.total_users}</Typography>
                  </CardContent>
                </Card>
              </Grid>
            </Grid>
          )}

          {/* Template Usage Table */}
          <Typography variant="h6" sx={{ mb: 1 }}>
            Template Usage
          </Typography>
          {templatesError ? (
            <Alert severity="error" sx={{ mb: 4 }}>Failed to load template statistics</Alert>
          ) : (
            <TableContainer component={Paper} sx={{ mb: 4 }}>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Name</TableCell>
                    <TableCell>Category</TableCell>
                    <TableCell>Published</TableCell>
                    <TableCell align="right">Definitions</TableCell>
                    <TableCell align="right">Instances</TableCell>
                    <TableCell align="right">Deploys</TableCell>
                    <TableCell>Success Rate</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {templates.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={7} align="center">
                        <Typography color="text.secondary" sx={{ py: 2 }}>
                          No template data available
                        </Typography>
                      </TableCell>
                    </TableRow>
                  ) : (
                    templates.map((t) => (
                      <TableRow key={t.template_id}>
                        <TableCell>{t.template_name}</TableCell>
                        <TableCell>{t.category || '—'}</TableCell>
                        <TableCell>
                          <Chip
                            label={t.is_published ? 'Published' : 'Draft'}
                            size="small"
                            color={t.is_published ? 'success' : 'default'}
                            variant="outlined"
                          />
                        </TableCell>
                        <TableCell align="right">{t.definition_count}</TableCell>
                        <TableCell align="right">{t.instance_count}</TableCell>
                        <TableCell align="right">{t.deploy_count}</TableCell>
                        <TableCell sx={{ minWidth: 150 }}>
                          {t.deploy_count === 0 ? (
                            <Typography variant="body2" color="text.secondary" aria-label="No deploys">
                              —
                            </Typography>
                          ) : (
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                              <LinearProgress
                                variant="determinate"
                                value={t.success_rate}
                                color={successRateColor(t.success_rate)}
                                sx={{ flexGrow: 1 }}
                                aria-label={`Success rate ${t.template_name}`}
                              />
                              <Typography variant="body2" sx={{ minWidth: 40 }}>
                                {t.success_rate.toFixed(0)}%
                              </Typography>
                            </Box>
                          )}
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </TableContainer>
          )}

          {/* User Activity Table (admin only) */}
          {isAdmin && (
            <>
              <Typography variant="h6" sx={{ mb: 1 }}>
                User Activity
              </Typography>
              {usersError ? (
                <Alert severity="error">Failed to load user activity</Alert>
              ) : (
                <TableContainer component={Paper}>
                  <Table size="small">
                    <TableHead>
                      <TableRow>
                        <TableCell>Username</TableCell>
                        <TableCell align="right">Instances</TableCell>
                        <TableCell align="right">Deploys</TableCell>
                        <TableCell>Last Active</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {users.length === 0 ? (
                        <TableRow>
                          <TableCell colSpan={4} align="center">
                            <Typography color="text.secondary" sx={{ py: 2 }}>
                              No user data available
                            </Typography>
                          </TableCell>
                        </TableRow>
                      ) : (
                        users.map((u) => (
                          <TableRow key={u.user_id}>
                            <TableCell>{u.username}</TableCell>
                            <TableCell align="right">{u.instance_count}</TableCell>
                            <TableCell align="right">{u.deploy_count}</TableCell>
                            <TableCell>{formatRelativeTime(u.last_active)}</TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                </TableContainer>
              )}
            </>
          )}
        </>
      )}
    </Box>
  );
};

export default Analytics;
