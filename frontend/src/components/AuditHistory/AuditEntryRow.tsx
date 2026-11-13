import { Fragment, useState } from 'react';
import { Box, Collapse, IconButton, TableCell, TableRow, Typography } from '@mui/material';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import type { AuditLog } from '../../types';
import EntityLink from '../EntityLink';
import { detailRows } from './auditFormat';

interface AuditEntryRowProps {
  log: AuditLog;
  /** Show the entity type and entity columns (the audit log page). */
  showEntity?: boolean;
  isAdmin?: boolean;
}

/** One audit entry as a table row that expands to its details. */
const AuditEntryRow = ({ log, showEntity = false, isAdmin = false }: AuditEntryRowProps) => {
  const [open, setOpen] = useState(false);
  const rows = detailRows(log);
  const columns = showEntity ? 6 : 4;
  return (
    <Fragment>
      <TableRow sx={{ '& > *': { borderBottom: open ? 'unset' : undefined } }}>
        <TableCell padding="checkbox">
          <IconButton
            size="small"
            aria-label={open ? 'Hide details' : 'Show details'}
            aria-expanded={open}
            onClick={() => setOpen(!open)}
            disabled={rows.length === 0}
          >
            {open ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
          </IconButton>
        </TableCell>
        <TableCell sx={{ whiteSpace: 'nowrap' }}>{new Date(log.timestamp).toLocaleString()}</TableCell>
        <TableCell>{log.username}</TableCell>
        <TableCell>{log.action}</TableCell>
        {showEntity && <TableCell>{log.entity_type}</TableCell>}
        {showEntity && (
          <TableCell>
            <EntityLink
              entityType={log.entity_type}
              entityId={log.entity_id}
              entityName={log.entity_name}
              action={log.action}
              isAdmin={isAdmin}
            />
            {log.entity_name && log.entity_id && (
              <Typography variant="caption" component="div" color="text.secondary" sx={{ fontFamily: 'monospace' }}>
                {log.entity_id}
              </Typography>
            )}
          </TableCell>
        )}
      </TableRow>
      <TableRow>
        <TableCell sx={{ py: 0 }} colSpan={columns}>
          <Collapse in={open} timeout="auto" unmountOnExit>
            <Box component="dl" sx={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', columnGap: 2, rowGap: 0.5, my: 1 }}>
              {rows.map((r, i) => (
                <Fragment key={`${r.label}-${i}`}>
                  <Typography component="dt" variant="body2" color="text.secondary">{r.label}</Typography>
                  <Typography component="dd" variant="body2" sx={{ m: 0, wordBreak: 'break-word' }}>{r.value}</Typography>
                </Fragment>
              ))}
            </Box>
          </Collapse>
        </TableCell>
      </TableRow>
    </Fragment>
  );
};

export default AuditEntryRow;
