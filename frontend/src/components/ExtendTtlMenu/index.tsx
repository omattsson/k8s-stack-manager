import { useId, useState } from 'react';
import {
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Divider,
  Menu,
  MenuItem,
} from '@mui/material';
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown';
import { instanceService } from '../../api/client';
import StopTimePicker from '../StopTimePicker';
import { useBranding } from '../../context/BrandingContext';
import { useNotification } from '../../context/NotificationContext';
import { describeApiError } from '../../utils/apiError';
import { formatExpiry } from '../../utils/expiry';
import { stopTimeBounds, stopTimeError } from '../../utils/stopTime';
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
  /** Current expiry (ISO). "Custom..." allows only later times. */
  expiresAt?: string | null;
  /** Called with the updated instance after a successful extend. */
  onExtended: (updated: StackInstance) => void;
  /** Called with an error message on failure. Default: an error toast. */
  onError?: (message: string) => void;
}

/**
 * Button with a menu (+1 h, +4 h, +24 h, Custom...) that adds time to the
 * expiry of an instance. "Custom..." opens a dialog with a date and time
 * picker, from the current expiry to now plus MAX_INSTANCE_TTL_DAYS.
 * Shows the new expiry in a success message.
 */
const ExtendTtlMenu = ({ instanceId, instanceName, label = 'Extend', expiresAt, onExtended, onError }: ExtendTtlMenuProps) => {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [extending, setExtending] = useState(false);
  const [customOpen, setCustomOpen] = useState(false);
  const [until, setUntil] = useState<string | null>(null);
  const { showSuccess, showError } = useNotification();
  const { maxTtlDays } = useBranding();
  const baseId = useId();
  const buttonId = `${baseId}-button`;
  const menuId = `${baseId}-menu`;

  const current = expiresAt ? new Date(expiresAt) : null;
  const bounds = stopTimeBounds(maxTtlDays, new Date(), current && current.getTime() > Date.now() ? current : null);
  const untilError = stopTimeError(until, bounds);

  const run = async (request: () => Promise<StackInstance>, done: string) => {
    setExtending(true);
    try {
      const updated = await request();
      onExtended(updated);
      const expiry = formatExpiry(updated.expires_at);
      showSuccess(`${done}${expiry ? ` ${expiry}` : ''}`);
    } catch (err) {
      const fallback = instanceName ? `Failed to extend TTL for ${instanceName}` : 'Failed to extend TTL';
      const message = await describeApiError(err, fallback);
      if (onError) onError(message);
      else showError(message);
    } finally {
      setExtending(false);
    }
  };

  const handleExtend = (option: { minutes: number; label: string }) => {
    setAnchor(null);
    void run(() => instanceService.extend(instanceId, option.minutes), `Extended by ${option.label.slice(1)}.`);
  };

  const openCustom = () => {
    setAnchor(null);
    // Start one day after the current expiry (or now), within the limit.
    const start = Math.min(Math.max(bounds.min.getTime(), (current?.getTime() ?? Date.now()) + 24 * 60 * 60_000), bounds.max.getTime());
    setUntil(new Date(Math.floor(start / 60_000) * 60_000).toISOString());
    setCustomOpen(true);
  };

  const handleCustom = () => {
    if (!until || untilError) return;
    setCustomOpen(false);
    void run(() => instanceService.extendUntil(instanceId, until), 'Extended.');
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
        <Divider />
        <MenuItem onClick={openCustom} aria-haspopup="dialog">Custom...</MenuItem>
      </Menu>
      <Dialog open={customOpen} onClose={() => setCustomOpen(false)} aria-labelledby={`${baseId}-custom-title`}>
        <DialogTitle id={`${baseId}-custom-title`}>Extend until</DialogTitle>
        <DialogContent>
          <DialogContentText sx={{ mb: 2 }}>
            {current && current.getTime() > Date.now()
              ? `The new expiry must be after the current expiry and at most ${maxTtlDays} days from now.`
              : `The new expiry must be at most ${maxTtlDays} days from now.`}
          </DialogContentText>
          <StopTimePicker value={until} onChange={setUntil} bounds={bounds} label="New expiry" />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCustomOpen(false)}>Cancel</Button>
          <Button variant="contained" onClick={handleCustom} disabled={untilError !== null}>
            Extend
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
};

export default ExtendTtlMenu;
