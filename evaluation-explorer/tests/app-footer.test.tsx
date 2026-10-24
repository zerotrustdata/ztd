// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';
import { AppFooter } from '../src/components/AppFooter';
import { evalStore } from '../src/state/store';

describe('AppFooter', () => {
  beforeEach(() => {
    evalStore.loadFixtures([], []);
    evalStore.setMirrorOrigin('https://mirror.example');
  });

  it('links mirror transport endpoints and site pages in one row', async () => {
    render(<MemoryRouter><AppFooter /></MemoryRouter>);
    expect(await screen.findByRole('link', { name: /Live updates/ })).toHaveAttribute('href', 'https://mirror.example/stream');
    expect(screen.getByRole('link', { name: /History/ })).toHaveAttribute('href', 'https://mirror.example/history?cursor=0&limit=500');
    expect(screen.getByRole('link', { name: /Bootstrap/ })).toHaveAttribute('href', 'https://mirror.example/bootstrap');
    expect(screen.getByRole('link', { name: /Schema/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Methodology' })).toHaveAttribute('href', '/methodology');
    expect(screen.getByRole('link', { name: 'Sponsors' })).toHaveAttribute('href', '/sponsors');
    expect(screen.queryByRole('link', { name: /Proof manifest/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Proof catalog/ })).not.toBeInTheDocument();
  });

  it('offers the proof manifest and full catalog as links once proofs are published', async () => {
    evalStore.initBootstrap({
      protocol_version: '1.0.0', source_id: 'source-a', high_water_sequence: 0, feed_chain_hash: '0'.repeat(64),
      withdrawal_digest: '0'.repeat(64), batch_count: 0, generated_at: '2026-09-20T00:00:00Z', freshness: 'active',
    }, [], 3);
    render(<MemoryRouter><AppFooter /></MemoryRouter>);
    expect(await screen.findByRole('link', { name: /Proof catalog/ })).toHaveAttribute('href', 'https://mirror.example/proof-catalog');
    expect(screen.getByRole('link', { name: /Proof manifest/ })).toHaveAttribute('href', 'https://mirror.example/proof-manifest');
    evalStore.loadFixtures([], []);
  });
});
