import { Autocomplete, Box, TextField, Typography } from '@mui/material';
import type { NotificationChannelFilters } from '../../../types';
import type { FilterOption, FilterOptions } from './channelFilters';

type IdFilterKey = 'owner_ids' | 'definition_ids' | 'cluster_ids';

interface ChannelFiltersEditorProps {
  value: NotificationChannelFilters;
  onChange: (value: NotificationChannelFilters) => void;
  options: FilterOptions;
  /** Text under the owner picker, for example where the user list comes from. */
  ownerHelperText?: string;
  disabled?: boolean;
}

/**
 * Filters section of the channel dialog. Name patterns are free text chips;
 * owners, definitions and clusters are pickers. An ID without a matching
 * option (for example a deleted cluster) is shown as the ID.
 */
const ChannelFiltersEditor = ({ value, onChange, options, ownerHelperText, disabled }: ChannelFiltersEditorProps) => {
  const idPicker = (key: IdFilterKey, label: string, choices: FilterOption[], helperText?: string) => {
    const byId = new Map(choices.map((o) => [o.id, o]));
    const selected = (value[key] ?? []).map((id) => byId.get(id) ?? { id, label: id });
    return (
      <Autocomplete
        multiple
        options={choices}
        value={selected}
        disabled={disabled}
        getOptionLabel={(o) => o.label}
        isOptionEqualToValue={(o, v) => o.id === v.id}
        onChange={(_e, items) => onChange({ ...value, [key]: items.map((o) => o.id) })}
        renderInput={(params) => (
          <TextField {...params} label={label} size="small" helperText={helperText} />
        )}
      />
    );
  };

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Box>
        <Typography variant="subtitle1">Filters</Typography>
        <Typography variant="body2" color="text.secondary">
          Leave all filters empty to get the events of all instances. Each set filter must match; one value of a filter is enough.
          Events without an instance (for example quota warnings) go only to channels without filters.
        </Typography>
      </Box>
      <Autocomplete
        multiple
        freeSolo
        options={[] as string[]}
        value={value.instance_name_patterns ?? []}
        disabled={disabled}
        onChange={(_e, items) => onChange({
          ...value,
          instance_name_patterns: items.map((p) => p.trim().toLowerCase()).filter((p) => p !== ''),
        })}
        renderInput={(params) => (
          <TextField
            {...params}
            label="Instance name patterns"
            size="small"
            placeholder="rdbtest-*"
            helperText="Press Enter after each pattern. Use * for any text, ? for one character."
          />
        )}
      />
      {idPicker('owner_ids', 'Owners', options.users, ownerHelperText)}
      {idPicker('definition_ids', 'Stack definitions', options.definitions)}
      {idPicker('cluster_ids', 'Clusters', options.clusters)}
    </Box>
  );
};

export default ChannelFiltersEditor;
