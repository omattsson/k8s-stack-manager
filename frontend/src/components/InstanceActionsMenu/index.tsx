import { useEffect, useId, useState } from 'react';
import { Button, ListItemText, Menu, MenuItem } from '@mui/material';
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown';
import { instanceService } from '../../api/client';
import type { InstanceAction } from '../../types';
import ActionDialog from './ActionDialog';

interface InstanceActionsMenuProps {
  /** ID of the instance that the actions run on. */
  instanceId: string;
}

/**
 * "Actions" button with a menu of the custom actions registered on the
 * server. Hidden when no action is registered. An action that the current
 * user may not run is shown disabled. A click opens a dialog with the
 * confirmation text and the parameter form.
 */
const InstanceActionsMenu = ({ instanceId }: InstanceActionsMenuProps) => {
  const [actions, setActions] = useState<InstanceAction[]>([]);
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [selected, setSelected] = useState<InstanceAction | null>(null);
  const baseId = useId();
  const buttonId = `${baseId}-button`;
  const menuId = `${baseId}-menu`;

  useEffect(() => {
    let cancelled = false;
    instanceService
      .listActions(instanceId)
      .then((res) => {
        if (!cancelled) setActions(Array.isArray(res?.actions) ? res.actions : []);
      })
      .catch(() => {
        // The menu is optional: without the list, it stays hidden.
        if (!cancelled) setActions([]);
      });
    return () => {
      cancelled = true;
    };
  }, [instanceId]);

  if (actions.length === 0) return null;

  return (
    <>
      <Button
        id={buttonId}
        variant="outlined"
        aria-controls={anchor ? menuId : undefined}
        aria-haspopup="menu"
        aria-expanded={anchor ? 'true' : undefined}
        endIcon={<ArrowDropDownIcon />}
        onClick={(e) => setAnchor(e.currentTarget)}
      >
        Actions
      </Button>
      <Menu
        id={menuId}
        anchorEl={anchor}
        open={Boolean(anchor)}
        onClose={() => setAnchor(null)}
        slotProps={{ list: { 'aria-labelledby': buttonId } }}
      >
        {actions.map((a) => (
          <MenuItem
            key={a.name}
            disabled={!a.can_invoke}
            onClick={() => {
              setAnchor(null);
              setSelected(a);
            }}
          >
            <ListItemText
              primary={a.label}
              secondary={a.can_invoke ? a.description : 'Only the owner, an admin or a devops user can run this action'}
            />
          </MenuItem>
        ))}
      </Menu>
      {selected && (
        <ActionDialog
          key={selected.name}
          instanceId={instanceId}
          action={selected}
          onClose={() => setSelected(null)}
        />
      )}
    </>
  );
};

export default InstanceActionsMenu;
