// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

/** The width at which the navigation drawer overlays the content. Matches the 768px breakpoint in styles/shell.css. */
const OVERLAY_DRAWER_QUERY = '(max-width: 768px)';

export function isOverlayDrawerViewport(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia(OVERLAY_DRAWER_QUERY).matches;
}
