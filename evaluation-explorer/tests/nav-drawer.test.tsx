// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, fireEvent, within } from '@testing-library/react';
import { App } from '../src/App';
import { evalStore } from '../src/state/store';
import { fixtureEvaluationSummaries } from '../src/fixtures/fixtures';

vi.mock('../src/state/startup', () => ({ startFeed: vi.fn(), stopFeed: vi.fn() }));

const run = { ...fixtureEvaluationSummaries[0]!, run_status: 'running' as const };

beforeEach(() => {
  window.history.replaceState(null, '', '/');
  evalStore.loadFixtures([run], []);
});

describe('Navigation and runs drawer', () => {
  it('opens by default and keeps live Runs mounted across menu and run navigation', () => {
    render(<App />);
    const toggle = within(screen.getByRole('banner')).getByRole('button', { name: 'Close navigation drawer' });
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    const drawer = screen.getByRole('complementary', { name: 'Navigation and runs' });
    const nav = within(drawer).getByRole('navigation', { name: 'Primary navigation' });
    expect(within(nav).getAllByRole('link')).toHaveLength(8);
    const runs = within(drawer).getByRole('region', { name: 'Runs' });
    expect(within(runs).getByText('running')).toBeInTheDocument();

    for (const label of ['Leaderboard', 'Evals', 'Tasks', 'Models', 'Docs', 'About', 'Sponsors', 'Live']) {
      fireEvent.click(within(nav).getByRole('link', { name: new RegExp(`^${label}`) }));
      expect(toggle).toHaveAttribute('aria-expanded', 'true');
      expect(screen.getByRole('region', { name: 'Runs' })).toBe(runs);
    }
    fireEvent.click(within(runs).getByRole('link', { name: run.run_id }));
    expect(window.location.pathname).toBe(`/evaluations/${run.dataset_id}/${run.run_id}`);
    expect(screen.getByRole('region', { name: 'Runs' })).toBe(runs);
    act(() => evalStore.acceptProjection({ sequence: 1, record_type: 'projection', record_bytes: JSON.stringify({ ...run, run_status: 'cancelled' }) }));
    expect(within(runs).getByText('cancelled')).toBeInTheDocument();
  });

  it('collapses with either button or Escape and restores focus to the header toggle', () => {
    render(<App />);
    const toggle = within(screen.getByRole('banner')).getByRole('button', { name: 'Close navigation drawer' });
    const drawer = screen.getByRole('complementary', { name: 'Navigation and runs' });
    const close = within(drawer).getByRole('button', { name: 'Close navigation drawer' });
    close.focus();
    fireEvent.click(close);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(drawer).toHaveAttribute('inert');
    expect(toggle).toHaveFocus();
    fireEvent.click(toggle);
    expect(drawer).not.toHaveAttribute('inert');
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(toggle).toHaveFocus();
    fireEvent.click(toggle);
    fireEvent.click(toggle);
    expect(drawer).toHaveAttribute('inert');
  });
});
