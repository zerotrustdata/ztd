// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AppFooter } from '../src/components/AppFooter';
import { evalStore } from '../src/state/store';
import type { FeedStatus } from '../src/utils/feed-state';

/** Replaces the feed status a test renders with. Undefined leaves the store's own. */
let statusOverride: FeedStatus | null | undefined;

vi.mock('../src/state/store', async (original) => {
  const actual = await original<typeof import('../src/state/store')>();
  return { ...actual, useFeedStatus: () => (statusOverride === undefined ? actual.useFeedStatus() : statusOverride) };
});

describe('AppFooter', () => {
  beforeEach(() => {
    evalStore.loadFixtures([], []);
    evalStore.setMirrorOrigin('https://mirror.example');
  });

  it('links the mirror layout files and site pages in one row', async () => {
    render(<MemoryRouter><AppFooter /></MemoryRouter>);
    expect(await screen.findByRole('link', { name: /Live status/ })).toHaveAttribute('href', 'https://mirror.example/live/status.json');
    expect(screen.getByRole('link', { name: /Pointer/ })).toHaveAttribute('href', 'https://mirror.example/pointer.json');
    expect(screen.getByRole('link', { name: /Schema/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Methodology' })).toHaveAttribute('href', '/methodology');
    expect(screen.getByRole('link', { name: 'Sponsors' })).toHaveAttribute('href', '/sponsors');
    expect(screen.queryByRole('link', { name: /Proof head/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /History/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Bootstrap/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Proof catalog/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Proof segments/ })).not.toBeInTheDocument();
  });

  it('offers the proof head once proofs are published', async () => {
    evalStore.initBootstrap({
      protocol_version: '1.0.0', source_id: 'source-a', high_water_sequence: 0, feed_chain_hash: '0'.repeat(64),
      withdrawal_digest: '0'.repeat(64), batch_count: 0, generated_at: '2026-09-20T00:00:00Z', freshness: 'active',
    }, [], 3);
    render(<MemoryRouter><AppFooter /></MemoryRouter>);
    expect(await screen.findByRole('link', { name: /Proof head/ })).toHaveAttribute('href', 'https://mirror.example/proof-head.json');
    expect(screen.queryByRole('link', { name: /Proof catalog/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Proof segments/ })).not.toBeInTheDocument();
    evalStore.loadFixtures([], []);
  });
});

describe('AppFooter feed telemetry', () => {
  const status = (lastAcceptedAt: string | undefined): FeedStatus => ({
    connection: 'live',
    freshness: 'active',
    highWaterSequence: 451843,
    lastAcceptedAt,
    message: 'ok',
  });
  const renderFooter = () => render(<MemoryRouter><AppFooter /></MemoryRouter>);

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(2026, 10, 5, 12, 0));
  });
  afterEach(() => {
    statusOverride = undefined;
    vi.useRealTimers();
  });

  it('renders nothing before the feed reports', () => {
    statusOverride = null;
    renderFooter();
    expect(screen.queryByTestId('footer-telemetry')).not.toBeInTheDocument();
  });

  it('shows the sequence alone until a record is accepted', () => {
    statusOverride = status(undefined);
    renderFooter();
    expect(screen.getByTestId('footer-telemetry')).toHaveTextContent(/^Seq 451,843$/);
  });

  it("shows only the time for today's data, with the full timestamp on hover", () => {
    const accepted = new Date(2026, 10, 5, 7, 58).toISOString();
    statusOverride = status(accepted);
    renderFooter();
    expect(screen.getByTestId('footer-telemetry')).toHaveTextContent('Seq 451,843 · Accepted 07:58');
    const time = screen.getByText('07:58');
    expect(time).toHaveAttribute('datetime', accepted);
    expect(time.getAttribute('title')).toContain('2026');
  });

  it('adds the date once the data is older, so a stale feed cannot read as current', () => {
    statusOverride = status(new Date(2026, 10, 4, 7, 58).toISOString());
    renderFooter();
    expect(screen.getByTestId('footer-telemetry')).toHaveTextContent('Accepted Nov 4 07:58');
  });
});
