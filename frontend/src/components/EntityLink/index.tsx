import { Link as RouterLink } from 'react-router-dom';
import { Link, Typography } from '@mui/material';

interface EntityLinkProps {
  entityType: string;
  entityId: string;
  /** Shown instead of the ID when set. */
  entityName?: string;
  /** Audit action: a deleted entity gets no link. */
  action?: string;
  /** Admin-only pages (clusters, users) get a link only for an admin. */
  isAdmin?: boolean;
}

const ENTITY_ROUTES: Record<string, (id: string) => string> = {
  stack_template: (id) => `/templates/${id}`,
  stack_definition: (id) => `/stack-definitions/${id}/edit`,
  stack_instance: (id) => `/stack-instances/${id}`,
};

const ADMIN_ROUTES: Record<string, string> = {
  cluster: '/admin/clusters',
  user: '/admin/users',
};

/**
 * Returns the page of an audit entry's entity, or null when there is none:
 * a delete, an empty ID, an unknown type, or an admin page for a non-admin.
 */
export function entityPath(entityType: string, entityId: string, action = '', isAdmin = false): string | null {
  if (!entityId || action === 'delete') return null;
  const routeFn = ENTITY_ROUTES[entityType];
  if (routeFn) return routeFn(entityId);
  if (isAdmin && ADMIN_ROUTES[entityType]) return ADMIN_ROUTES[entityType];
  return null;
}

const EntityLink = ({ entityType, entityId, entityName, action, isAdmin }: EntityLinkProps) => {
  const path = entityPath(entityType, entityId, action, isAdmin);
  const label = entityName || entityId;
  const sx = entityName ? undefined : { fontFamily: 'monospace', fontSize: 12 };
  if (!path) {
    return (
      <Typography variant="body2" component="span" sx={sx}>
        {label}
      </Typography>
    );
  }
  return (
    <Link component={RouterLink} to={path} sx={sx}>
      {label}
    </Link>
  );
};

export default EntityLink;
