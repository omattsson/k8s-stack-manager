import { useEffect, useState, useCallback } from 'react';
import {
  Box,
  Typography,
  CircularProgress,
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
} from '@mui/material';
import { auditService } from '../../api/client';
import type { AuditLog as AuditLogType } from '../../types';
import EntityLink from '../../components/EntityLink';

const ENTITY_TYPES = ['All', 'stack_template', 'stack_definition', 'stack_instance', 'value_override', 'user'];
const ACTIONS = ['All', 'create', 'update', 'delete', 'publish', 'unpublish', 'clone', 'instantiate'];

const AuditLog = () => {
  const [logs, setLogs] = useState<AuditLogType[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [entityType, setEntityType] = useState('All');
  const [action, setAction] = useState('All');
  const [username, setUsername] = useState('');
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);

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
      if (username) filters.user_id = username;

      const response = await auditService.list(filters);
      setLogs(response.data || []);
      setTotal(response.total);
    } catch {
      setError('Failed to load audit logs');
    } finally {
      setLoading(false);
    }
  }, [entityType, action, username, page, rowsPerPage]);

  useEffect(() => {
    fetchLogs();
  }, [fetchLogs]);

  const handleFilter = () => {
    setPage(0);
    fetchLogs();
  };

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        Audit Log
      </Typography>

      <Paper sx={{ p: 2, mb: 3 }}>
        <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <TextField
            label="Username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
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
          <Button variant="outlined" onClick={handleFilter}>
            Filter
          </Button>
        </Box>
      </Paper>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <Box display="flex" justifyContent="center" alignItems="center" minHeight="200px">
          <CircularProgress />
        </Box>
      ) : (
        <Paper>
          <TableContainer>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Timestamp</TableCell>
                  <TableCell>User</TableCell>
                  <TableCell>Action</TableCell>
                  <TableCell>Entity Type</TableCell>
                  <TableCell>Entity ID</TableCell>
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
                      <TableCell>
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
