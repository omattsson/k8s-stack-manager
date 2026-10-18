import { useCallback, useEffect, useId, useState } from 'react';
import type { ReactElement } from 'react';
import { Button, ListItemText, Menu, MenuItem } from '@mui/material';
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown';
import { instanceService } from '../../api/client';
import type { InstanceAction } from '../../types';
import ActionDialog from './ActionDialog';

/** Secondary text of an action that the current user may not run. */
export const ACTION_NOT_ALLOWED_TEXT = 'Only the owner, an admin or a devops user can run this action';

/**
 * Secondary text of an action menu item: the action description, or
 * {@link ACTION_NOT_ALLOWED_TEXT} when the current user may not run it.
 *
 * @param action - The custom action.
 * @returns The text to show below the action label.
 */
export const actionSecondaryText = (action: InstanceAction): string | undefined =>
  action.can_invoke ? action.description : ACTION_NOT_ALLOWED_TEXT;

/** Result of {@link useInstanceActions}. */
export interface InstanceActionsState {
  /** Custom actions that the server lists. Empty when it lists none or the list call fails. */
  actions: InstanceAction[];
  /** Open the dialog of an action (confirmation text and parameter form). */
  openAction: (action: InstanceAction) => void;
  /** The open action dialog, or null. Render it once in the page. */
  dialog: ReactElement | null;
}

/**
 * Load the custom actions of an instance and hold the state of the action
 * dialog. A menu shows the actions and calls `openAction`; the caller
 * renders `dialog`.
 *
 * @param instanceId - ID of the instance that the actions run on. Without an ID the hook loads nothing.
 * @returns The actions, a function that opens an action, and the dialog element.
 */
export const useInstanceActions = (instanceId: string | undefined): InstanceActionsState => {
  const [actions, setActions] = useState<InstanceAction[]>([]);
  const [selected, setSelected] = useState<InstanceAction | null>(null);

  useEffect(() => {
    if (!instanceId) return undefined;
    let cancelled = false;
    instanceService
      .listActions(instanceId)
      .then((res) => {
        if (!cancelled) setActions(Array.isArray(res?.actions) ? res.actions : []);
      })
      .catch(() => {
        // The actions are optional: without the list, the menu shows none.
        if (!cancelled) setActions([]);
      });
    return () => {
      cancelled = true;
    };
  }, [instanceId]);

  const openAction = useCallback((action: InstanceAction) => setSelected(action), []);

  const dialog = selected && instanceId ? (
    <ActionDialog
      key={selected.name}
      instanceId={instanceId}
      action={selected}
      onClose={() => setSelected(null)}
    />
  ) : null;

  return { actions, openAction, dialog };
};

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
  const { actions, openAction, dialog } = useInstanceActions(instanceId);
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const baseId = useId();
  const buttonId = `${baseId}-button`;
  const menuId = `${baseId}-menu`;

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
              openAction(a);
            }}
          >
            <ListItemText primary={a.label} secondary={actionSecondaryText(a)} />
          </MenuItem>
        ))}
      </Menu>
      {dialog}
    </>
  );
};

export default InstanceActionsMenu;
