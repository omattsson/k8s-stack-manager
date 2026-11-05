// In px as a string: in `sx`, a width of 1 means 100%.
export const visuallyHidden = {
  position: 'absolute',
  width: '1px',
  height: '1px',
  margin: '-1px',
  padding: 0,
  overflow: 'hidden',
  clipPath: 'inset(50%)',
  whiteSpace: 'nowrap',
  border: 0,
} as const;

/** Focus ring of a scroll box that is a Tab stop. Inside, because the page clips an outer ring. */
export const scrollBoxFocusSx = {
  '&:focus-visible': { outline: '2px solid', outlineColor: 'primary.main', outlineOffset: -2 },
} as const;
