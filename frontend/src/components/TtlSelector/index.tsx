import { useEffect, useState } from 'react';
import { Box, MenuItem, TextField } from '@mui/material';

interface TtlSelectorProps {
  value: number;
  onChange: (minutes: number) => void;
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

const TtlSelector = ({ value, onChange, disabled }: TtlSelectorProps) => {
  const isCustom = value > 0 && !PRESET_VALUES.has(value);
  const [showCustom, setShowCustom] = useState(isCustom);

  useEffect(() => {
    setShowCustom(value > 0 && !PRESET_VALUES.has(value));
  }, [value]);

  const selectValue = showCustom || isCustom ? CUSTOM_SENTINEL : value;

  const handleSelectChange = (newVal: number) => {
    if (newVal === CUSTOM_SENTINEL) {
      setShowCustom(true);
      if (!isCustom) onChange(60);
    } else {
      setShowCustom(false);
      onChange(newVal);
    }
  };

  return (
    <Box sx={{ display: 'flex', gap: 2, alignItems: 'flex-start' }}>
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
        <MenuItem value={CUSTOM_SENTINEL}>Custom</MenuItem>
      </TextField>

      {(showCustom || isCustom) && (
        <TextField
          type="number"
          label="Minutes"
          value={value}
          onChange={(e) => {
            const v = Math.max(1, parseInt(e.target.value, 10) || 1);
            onChange(v);
          }}
          disabled={disabled}
          size="small"
          sx={{ width: 120 }}
          slotProps={{ htmlInput: { min: 1 } }}
        />
      )}
    </Box>
  );
};

export default TtlSelector;
