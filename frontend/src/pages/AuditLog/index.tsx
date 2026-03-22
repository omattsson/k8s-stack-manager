import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  Alert,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  TextField,
  MenuItem,
  Button,
  TablePagination,
  Menu,
  ListItemText,
  Chip,
} from '@mui/material';
import FileDownloadIcon from '@mui/icons-material/FileDownload';
import { auditService } from '../../api/client';
import type { AuditLog as AuditLogType } from '../../types';
import EntityLink from '../../components/EntityLink';
import { useAuth } from '../../context/AuthContext';
import LoadingState from '../../components/LoadingState';

const ENTITY_TYPES = ['All', 'stack_template', 'stack_definition', 'stack_instance', 'value_override', 'user'];
const ACTIONS = ['All', 'create', 'update', 'delete', 'publish', 'unpublish', 'clone', 'instantiate'];

const AuditLog = () => {
  const { user } = useAuth();
  const [logs, setLogs] = useState<AuditLogType[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [entityType, setEntityType] = useState('All');
  const [action, setAction] = useState('All');
  const [userID, setUserID] = useState('');
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);
  const [exportAnchor, setExportAnchor] = useState<null | HTMLElement>(null);
  const [startDate, setStartDate] = useState('');
  const [endDate, setEndDate] = useState('');

  const fetchLogs = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const filters: Record<string, string | number> = {
        limit: rowsPerPage,
        offset: page * rowsPerPage,
      };
      if (entityType !== 'All') filters.entity_type = entityType;
      if (action !== 'All') filters.action = action;
      if (userID) filters.user_id = userID;
      if (startDate) filters.start_date = startDate;
      if (endDate) filters.end_date = endDate;

      const response = await auditService.list(filters);
      setLogs(response.data || []);
      setTotal(response.total);
    } catch {
      setError('Failed to load audit logs');
    } finally {
      setLoading(false);
    }
  }, [entityType, action, userID, page, rowsPerPage, startDate, endDate]);

  useEffect(() => {
    fetchLogs();
  }, [fetchLogs]);

  const handleFilter = () => {
    setPage(0);
    fetchLogs();
  };

  const handleExport = async (format: 'csv' | 'json') => {
    setExportAnchor(null);
    try {
      const filters: Record<string, string> = {};
      if (entityType !== 'All') filters.entity_type = entityType;
      if (action !== 'All') filters.action = action;
      if (userID) filters.user_id = userID;
      if (startDate) filters.start_date = startDate;
      if (endDate) filters.end_date = endDate;
      await auditService.export(filters, format);
    } catch {
      setError('Failed to export audit logs');
    }
  };

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        Audit Log
      </Typography>

      <Paper sx={{ p: 2, mb: 3 }}>
        <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <TextField
            label="User ID"
            value={userID}
            onChange={(e) => setUserID(e.target.value)}
            size="small"
            sx={{ minWidth: 150 }}
          />
          <TextField
            label="Entity Type"
            value={entityType}
            onChange={(e) => setEntityType(e.target.value)}
            select
            size="small"
            sx={{ minWidth: 180 }}
          >
            {ENTITY_TYPES.map((t) => (
              <MenuItem key={t} value={t}>{t === 'All' ? 'All Types' : t}</MenuItem>
            ))}
          </TextField>
          <TextField
            label="Action"
            value={action}
            onChange={(e) => setAction(e.target.value)}
            select
            size="small"
            sx={{ minWidth: 150 }}
          >
            {ACTIONS.map((a) => (
              <MenuItem key={a} value={a}>{a === 'All' ? 'All Actions' : a}</MenuItem>
            ))}
          </TextField>
          <TextField
            label="From"
            type="date"
            value={startDate}
            onChange={(e) => { setStartDate(e.target.value); setPage(0); }}
            size="small"
            slotProps={{ inputLabel: { shrink: true } }}
            sx={{ minWidth: 150 }}
          />
          <TextField
            label="To"
            type="date"
            value={endDate}
            onChange={(e) => { setEndDate(e.target.value); setPage(0); }}
            size="small"
            slotProps={{ inputLabel: { shrink: true } }}
            sx={{ minWidth: 150 }}
          />
          <Button variant="outlined" onClick={handleFilter}>
            Filter
          </Button>
          {user?.role === 'admin' && (
            <>
              <Button
                variant="outlined"
                startIcon={<FileDownloadIcon />}
                onClick={(e) => setExportAnchor(e.currentTarget)}
              >
                Export
              </Button>
              <Menu
                anchorEl={exportAnchor}
                open={Boolean(exportAnchor)}
                onClose={() => setExportAnchor(null)}
              >
                <MenuItem onClick={() => handleExport('json')}>
                  <ListItemText>Export as JSON</ListItemText>
                </MenuItem>
                <MenuItem onClick={() => handleExport('csv')}>
                  <ListItemText>Export as CSV</ListItemText>
                </MenuItem>
              </Menu>
            </>
          )}
        </Box>
        <Box sx={{ display: 'flex', gap: 1, mt: 1, alignItems: 'center' }}>
          <Typography variant="body2" color="text.secondary">Quick:</Typography>
          {[
            { label: 'Last 24h', days: 1 },
            { label: 'Last 7 days', days: 7 },
            { label: 'Last 30 days', days: 30 },
          ].map(({ label, days }) => {
            const now = new Date();
            const expectedStart = new Date(now.getTime() - days * 24 * 60 * 60 * 1000).toISOString().split('T')[0];
            const expectedEnd = now.toISOString().split('T')[0];
            const isActive = startDate === expectedStart && endDate === expectedEnd;
            return (
              <Chip
                key={label}
                label={label}
                size="small"
                onClick={() => {
                  const n = new Date();
                  const s = new Date(n.getTime() - days * 24 * 60 * 60 * 1000);
                  setStartDate(s.toISOString().split('T')[0]);
                  setEndDate(n.toISOString().split('T')[0]);
                  setPage(0);
                }}
                color={isActive ? 'primary' : 'default'}
                variant={isActive ? 'filled' : 'outlined'}
              />
            );
          })}
          {(startDate || endDate) && (
            <Chip
              label="Clear dates"
              size="small"
              variant="outlined"
              onDelete={() => { setStartDate(''); setEndDate(''); setPage(0); }}
            />
          )}
        </Box>
      </Paper>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <LoadingState label="Loading audit logs..." />
      ) : (
        <Paper>
          <TableContainer sx={{ maxHeight: 600 }}>
            <Table size="small" stickyHeader>
              <TableHead>
                <TableRow>
                  <TableCell>Timestamp</TableCell>
                  <TableCell>User</TableCell>
                  <TableCell>Action</TableCell>
                  <TableCell>Entity Type</TableCell>
                  <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>Entity ID</TableCell>
                  <TableCell>Details</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {logs.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} align="center">
                      <Typography color="text.secondary" sx={{ py: 2 }}>
                        No audit log entries found.
                      </Typography>
                    </TableCell>
                  </TableRow>
                ) : (
                  logs.map((log) => (
                    <TableRow key={log.id}>
                      <TableCell sx={{ whiteSpace: 'nowrap' }}>
                        {new Date(log.timestamp).toLocaleString()}
                      </TableCell>
                      <TableCell>{log.username}</TableCell>
                      <TableCell>{log.action}</TableCell>
                      <TableCell>{log.entity_type}</TableCell>
                      <TableCell sx={{ display: { xs: 'none', md: 'table-cell' } }}>
                        <EntityLink entityType={log.entity_type} entityId={log.entity_id} />
                      </TableCell>
                      <TableCell sx={{ maxWidth: 300 }}>
                        <Typography variant="body2" noWrap>
                          {log.details}
                        </Typography>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </TableContainer>
          <TablePagination
            component="div"
            count={total}
            page={page}
            onPageChange={(_e, newPage) => setPage(newPage)}
            rowsPerPage={rowsPerPage}
            onRowsPerPageChange={(e) => {
              setRowsPerPage(parseInt(e.target.value, 10));
              setPage(0);
            }}
          />
        </Paper>
      )}
    </Box>
  );
};

export default AuditLog;
