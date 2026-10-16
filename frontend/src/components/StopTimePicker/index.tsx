import { TextField } from '@mui/material';
import {
  formatStopTime,
  fromLocalInputValue,
  stopTimeError,
  toLocalInputValue,
  type StopTimeBounds,
} from '../../utils/stopTime';

interface StopTimePickerProps {
  /** The stop time as an ISO string, or null when none is chosen. */
  value: string | null;
  /** Called with the new stop time (ISO), or null when the input is empty or incomplete. */
  onChange: (iso: string | null) => void;
  /** Allowed range (see `stopTimeBounds`). Later and earlier times cannot be picked. */
  bounds: StopTimeBounds;
  /** Field label. */
  label?: string;
  /** A value that is valid as it is, also outside the bounds (a saved stop time). */
  acceptValue?: string | null;
  disabled?: boolean;
}

/**
 * Date and time picker for a stop time, in the local time zone of the browser.
 * Uses the native `datetime-local` input with `min` and `max`, and shows the
 * chosen time with the time left, or why the time is not allowed.
 */
const StopTimePicker = ({ value, onChange, bounds, label = 'Stop at', acceptValue, disabled }: StopTimePickerProps) => {
  const error = value !== null && value === acceptValue ? null : stopTimeError(value, bounds);
  return (
    <TextField
      type="datetime-local"
      label={label}
      value={value ? toLocalInputValue(new Date(value)) : ''}
      onChange={(e) => onChange(fromLocalInputValue(e.target.value))}
      disabled={disabled}
      size="small"
      error={error !== null}
      helperText={error ?? formatStopTime(value)}
      sx={{ minWidth: 240 }}
      slotProps={{
        inputLabel: { shrink: true },
        htmlInput: { min: toLocalInputValue(bounds.min), max: toLocalInputValue(bounds.max), step: 60 },
      }}
    />
  );
};

export default StopTimePicker;
