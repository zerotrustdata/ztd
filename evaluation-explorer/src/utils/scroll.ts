// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { MouseEvent } from 'react';

/** Scrolls an in-page section into view, smoothly unless the reader prefers
 *  reduced motion. A missing target is a no-op. */
export function scrollToAnchor(id: string): void {
  const target = document.getElementById(id);
  if (!target) return;
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false;
  target.scrollIntoView({ behavior: reduced ? 'auto' : 'smooth', block: 'start' });
}

/** Click handler for an in-page `#id` link: scrolls instead of letting the
 *  router treat the fragment as a navigation. */
export function scrollToAnchorOnClick(id: string) {
  return (event: MouseEvent<HTMLAnchorElement>) => {
    event.preventDefault();
    scrollToAnchor(id);
  };
}
