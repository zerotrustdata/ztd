// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { MethodologyView } from '../src/views/MethodologyView';

describe('MethodologyView', () => {
  it('keeps every section navigable through the shared anchor behavior', () => {
    render(<MethodologyView />);
    const navigation = within(screen.getByRole('complementary', { name: 'Documentation navigation' }));
    const links = navigation.getAllByRole('link');
    expect(links).toHaveLength(11);
    for (const link of links) {
      const section = document.getElementById(link.getAttribute('href')!.slice(1))!;
      expect(section).toBeInTheDocument();
      section.scrollIntoView = vi.fn();
      fireEvent.click(link);
      expect(section.scrollIntoView).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' });
      expect(link).toHaveAttribute('aria-current', 'location');
    }
    expect(within(screen.getByRole('navigation', { name: 'Evidence chain' })).getByText('Operator PEP')).toBeInTheDocument();
  });
});
