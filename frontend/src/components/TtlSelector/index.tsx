import { Box, MenuItem, TextField } from '@mui/material';
import StopTimePicker from '../StopTimePicker';
import { useBranding } from '../../context/BrandingContext';
import { formatDurationShort } from '../../utils/expiry';
import { stopTimeBounds } from '../../utils/stopTime';

interface TtlSelectorProps {
  /** TTL in minutes (0: no expiry). Used while no stop time is set. */
  value: number;
  /** Called with the TTL in minutes when the user picks a preset. */
  onChange: (minutes: number) => void;
  /**
   * Fixed stop time: an ISO string, null while the picker input is
   * incomplete, or undefined for no stop time (a preset). When it is not
   * undefined, the selector shows "Custom stop time" with the picker.
   */
  stopTime?: string | null;
  /**
   * Called with the stop time when the user picks "Custom stop time" or
   * changes the picker (null while the input is incomplete), and with
   * undefined when the user picks a preset. Without this prop, the selector
   * has no "Custom stop time".
   */
  onStopTimeChange?: (iso: string | null | undefined) => void;
  disabled?: boolean;
}

const PRESETS = [
  { label: 'No expiry', value: 0 },
  { label: '4 hours', value: 240 },
  { label: '8 hours', value: 480 },
  { label: '24 hours', value: 1440 },
  { label: '48 hours', value: 2880 },
] as const;

const PRESET_VALUES = new Set<number>(PRESETS.map((p) => p.value));
const CUSTOM_SENTINEL = -1;

/**
 * TTL field of the instance forms: presets (TTL from the deploy) and
 * "Custom", a fixed stop time from a date and time picker. The picker allows
 * times up to now plus MAX_INSTANCE_TTL_DAYS (ui-config).
 */
const TtlSelector = ({ value, onChange, stopTime, onStopTimeChange, disabled }: TtlSelectorProps) => {
  const { maxTtlDays } = useBranding();
  // undefined: no stop time; null: "Custom" with an incomplete input.
  const custom = stopTime !== undefined && onStopTimeChange !== undefined;
  // A TTL that is not a preset (set through the API or an older form) keeps
  // its own entry, so the field shows it and does not change it.
  const extraTtl = value > 0 && !PRESET_VALUES.has(value) ? value : null;
  const selectValue = custom ? CUSTOM_SENTINEL : value;
  const bounds = stopTimeBounds(maxTtlDays);

  const handleSelectChange = (newVal: number) => {
    if (newVal === CUSTOM_SENTINEL) {
      if (!custom && onStopTimeChange) {
        // Start with tomorrow at the same time, within the limit.
        const tomorrow = Math.min(Date.now() + 24 * 60 * 60_000, bounds.max.getTime());
        onStopTimeChange(new Date(Math.floor(tomorrow / 60_000) * 60_000).toISOString());
      }
      return;
    }
    onStopTimeChange?.(undefined);
    onChange(newVal);
  };

  return (
    <Box sx={{ display: 'flex', gap: 2, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <TextField
        select
        label="TTL (Time to Live)"
        value={selectValue}
        onChange={(e) => handleSelectChange(Number(e.target.value))}
        disabled={disabled}
        size="small"
        sx={{ minWidth: 180 }}
      >
        {PRESETS.map((p) => (
          <MenuItem key={p.value} value={p.value}>
            {p.label}
          </MenuItem>
        ))}
        {extraTtl !== null && <MenuItem value={extraTtl}>{formatDurationShort(extraTtl)}</MenuItem>}
        {onStopTimeChange && <MenuItem value={CUSTOM_SENTINEL}>Custom stop time</MenuItem>}
      </TextField>

      {custom && (
        <StopTimePicker value={stopTime ?? null} onChange={onStopTimeChange} bounds={bounds} disabled={disabled} />
      )}
    </Box>
  );
};

export default TtlSelector;
