import { useId, useState } from 'react';
import { Button, CircularProgress, Menu, MenuItem } from '@mui/material';
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown';
import { instanceService } from '../../api/client';
import { useNotification } from '../../context/NotificationContext';
import { formatExpiry } from '../../utils/expiry';
import type { StackInstance } from '../../types';

/** Extension choices in minutes, with their menu labels. */
export const EXTEND_OPTIONS: ReadonlyArray<{ minutes: number; label: string }> = [
  { minutes: 60, label: '+1 h' },
  { minutes: 240, label: '+4 h' },
  { minutes: 1440, label: '+24 h' },
];

interface ExtendTtlMenuProps {
  /** ID of the instance to extend. */
  instanceId: string;
  /** Instance name, used in the error message. */
  instanceName?: string;
  /** Button label. */
  label?: string;
  /** Called with the updated instance after a successful extend. */
  onExtended: (updated: StackInstance) => void;
  /** Called with an error message on failure. Default: an error toast. */
  onError?: (message: string) => void;
}

/**
 * Button with a menu (+1 h, +4 h, +24 h) that adds time to the expiry of an instance.
 * Shows the new expiry in a success message.
 */
const ExtendTtlMenu = ({ instanceId, instanceName, label = 'Extend', onExtended, onError }: ExtendTtlMenuProps) => {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [extending, setExtending] = useState(false);
  const { showSuccess, showError } = useNotification();
  const baseId = useId();
  const buttonId = `${baseId}-button`;
  const menuId = `${baseId}-menu`;

  const handleExtend = async (option: { minutes: number; label: string }) => {
    setAnchor(null);
    setExtending(true);
    try {
      const updated = await instanceService.extend(instanceId, option.minutes);
      onExtended(updated);
      const expiry = formatExpiry(updated.expires_at);
      showSuccess(`Extended by ${option.label.slice(1)}.${expiry ? ` ${expiry}` : ''}`);
    } catch {
      const message = instanceName ? `Failed to extend TTL for ${instanceName}` : 'Failed to extend TTL';
      if (onError) onError(message);
      else showError(message);
    } finally {
      setExtending(false);
    }
  };

  return (
    <>
      <Button
        id={buttonId}
        variant="outlined"
        size="small"
        aria-controls={anchor ? menuId : undefined}
        aria-haspopup="menu"
        aria-expanded={anchor ? 'true' : undefined}
        endIcon={extending ? <CircularProgress size={14} /> : <ArrowDropDownIcon />}
        onClick={(e) => setAnchor(e.currentTarget)}
        disabled={extending}
        sx={{ whiteSpace: 'nowrap' }}
      >
        {label}
      </Button>
      <Menu
        id={menuId}
        anchorEl={anchor}
        open={Boolean(anchor)}
        onClose={() => setAnchor(null)}
        slotProps={{ list: { 'aria-labelledby': buttonId } }}
      >
        {EXTEND_OPTIONS.map((option) => (
          <MenuItem key={option.minutes} onClick={() => handleExtend(option)}>
            {option.label}
          </MenuItem>
        ))}
      </Menu>
    </>
  );
};

export default ExtendTtlMenu;
