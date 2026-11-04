import type { FocusEvent } from 'react';

/**
 * `onFocus` for an element in a row that scrolls sideways. Chromium does not scroll
 * a focus target that is already partly visible, so the element and its focus ring
 * would stay cut off at the edge of the row.
 */
export const scrollFocusIntoView = (event: FocusEvent<HTMLElement>): void => {
  // jsdom has no scrollIntoView.
  event.currentTarget.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
};
