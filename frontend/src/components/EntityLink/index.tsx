import { Link } from 'react-router-dom';
import { Typography } from '@mui/material';

interface EntityLinkProps {
  entityType: string;
  entityId: string;
}

const ENTITY_ROUTES: Record<string, (id: string) => string> = {
  stack_template: (id) => `/templates/${id}`,
  stack_definition: (id) => `/stack-definitions/${id}/edit`,
  stack_instance: (id) => `/stack-instances/${id}`,
};

const EntityLink = ({ entityType, entityId }: EntityLinkProps) => {
  const routeFn = ENTITY_ROUTES[entityType];
  if (!routeFn) {
    return (
      <Typography variant="body2" component="span" sx={{ fontFamily: 'monospace', fontSize: 12 }}>
        {entityId}
      </Typography>
    );
  }
  return (
    <Link to={routeFn(entityId)} style={{ fontFamily: 'monospace', fontSize: 12 }}>
      {entityId}
    </Link>
  );
};

export default EntityLink;
