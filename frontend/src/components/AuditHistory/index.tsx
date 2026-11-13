import { useEffect, useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import { auditService } from '../../api/client';
import type { AuditLog } from '../../types';
import { useAuth } from '../../context/AuthContext';
import { hasAtLeastRole } from '../../utils/roles';
import { visuallyHidden } from '../../utils/visuallyHidden';
import LoadingState from '../LoadingState';
import AuditEntryRow from './AuditEntryRow';

const PAGE_SIZE = 10;

interface AuditHistoryProps {
  /** Entity ID. The filter has no entity type, so it also finds the override and chart entries that carry this ID. */
  entityId: string;
}

/** The audit entries of one entity, newest first, 10 per page. */
const AuditHistory = ({ entityId }: AuditHistoryProps) => {
  const { user } = useAuth();
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    auditService
      .list({ entity_id: entityId, limit: PAGE_SIZE, offset: page * PAGE_SIZE })
      .then((res) => {
        if (cancelled) return;
        setLogs(res.data || []);
        setTotal(res.total);
      })
      .catch(() => {
        if (!cancelled) setError('Failed to load the audit history');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [entityId, page]);

  if (error) return <Alert severity="error">{error}</Alert>;
  if (loading && logs.length === 0) return <LoadingState label="Loading audit history..." />;
  if (logs.length === 0) {
    return <Typography color="text.secondary">No audit entries.</Typography>;
  }
  return (
    <>
      <TableContainer>
        <Table size="small" aria-label="Audit history">
          <TableHead>
            <TableRow>
              <TableCell padding="checkbox"><Box component="span" sx={visuallyHidden}>Details</Box></TableCell>
              <TableCell>Timestamp</TableCell>
              <TableCell>User</TableCell>
              <TableCell>Action</TableCell>
              <TableCell>Type</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {logs.map((log) => (
              <AuditEntryRow key={log.id} log={log} showType isAdmin={user?.role === 'admin'} />
            ))}
          </TableBody>
        </Table>
      </TableContainer>
      <TablePagination
        component="div"
        count={total}
        page={page}
        onPageChange={(_e, p) => setPage(p)}
        rowsPerPage={PAGE_SIZE}
        rowsPerPageOptions={[]}
      />
    </>
  );
};

/**
 * A collapsed "Audit history" section for an entity page. It renders only
 * for devops and admin (the audit API requires devops) and loads on open.
 */
export const AuditHistorySection = ({ entityId }: AuditHistoryProps) => {
  const { user } = useAuth();
  const [expanded, setExpanded] = useState(false);
  if (!hasAtLeastRole(user?.role, 'devops')) return null;
  return (
    <Accordion
      expanded={expanded}
      onChange={(_e, open) => setExpanded(open)}
      disableGutters
      variant="outlined"
      slots={{ heading: 'h2' }}
      slotProps={{ region: { 'aria-label': 'Audit history' } }}
      sx={{ '&:before': { display: 'none' }, mt: 3 }}
    >
      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
        <Typography variant="subtitle2" component="span">Audit history</Typography>
      </AccordionSummary>
      <AccordionDetails>
        {expanded && <AuditHistory key={entityId} entityId={entityId} />}
      </AccordionDetails>
    </Accordion>
  );
};

export default AuditHistory;
