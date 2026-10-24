// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { evalStore } from '../src/state/store';
import { SponsorsView } from '../src/views/SponsorsView';
import {
  PLATFORM_CONTACT_CALENDLY,
  PLATFORM_CONTACT_EMAIL,
  PLATFORM_CONTACT_LINKEDIN,
  SPONSOR_URL,
  SPONSORS_HERO_TITLE,
  ZTD_REPO_URL,
  type SponsorEntry,
} from '../src/content/platform';

describe('SponsorsView', () => {
  beforeEach(() => {
    evalStore.setConnection('live');
    evalStore.setStreamConnection('connected');
  });

  it('renders the urgency note, first-sponsor CTA, static Top Sponsors leaderboard, and condensed About sections', () => {
    render(
      <MemoryRouter>
        <SponsorsView />
      </MemoryRouter>,
    );

    const view = within(screen.getByRole('region', { name: 'Sponsors and project sustainability' }));
    expect(view.getByRole('heading', { level: 1, name: SPONSORS_HERO_TITLE })).toBeInTheDocument();
    expect(view.getByRole('heading', { name: 'Why I need sponsors right now' })).toBeInTheDocument();
    expect(view.getByText(/completely out of personal runway/i)).toBeInTheDocument();
    expect(view.getByText(/going cold/i)).toBeInTheDocument();
    expect(view.getByText(/convenience store/i)).toBeInTheDocument();

    // First-sponsor CTA
    const firstCta = within(view.getByRole('region', { name: 'First sponsor call to action' }));
    expect(firstCta.getByRole('heading', { name: /Be the very first sponsor on the leaderboard/i })).toBeInTheDocument();
    expect(firstCta.getByRole('link', { name: /Claim the #1 Sponsor Spot/i })).toHaveAttribute('href', SPONSOR_URL);

    // Top Sponsors Leaderboard table (open podium state)
    const table = view.getByRole('table', { name: 'Top sponsors leaderboard' });
    expect(table).toBeInTheDocument();
    expect(within(table).getByText('1st')).toBeInTheDocument();
    expect(within(table).getByText(/Unclaimed — Be the very first sponsor/i)).toBeInTheDocument();
    expect(within(table).getByRole('link', { name: /Sponsor via Stripe/i })).toHaveAttribute('href', SPONSOR_URL);

    // Static-now / automation-ready note
    expect(view.getByText(/Static today, structured for automation later:/i)).toBeInTheDocument();
    expect(view.getByText(/automated Stripe updates can be plugged in later/i)).toBeInTheDocument();

    // Condensed About & builder sections
    expect(view.getByRole('heading', { name: 'About the project (condensed)' })).toBeInTheDocument();
    expect(view.getByRole('heading', { name: 'About the builder & hiring availability' })).toBeInTheDocument();
    expect(view.getByText(/Danny Barbour/i)).toBeInTheDocument();

    // Payment & contact links
    expect(view.getByRole('link', { name: /Become the First Sponsor via Stripe/i })).toHaveAttribute('href', SPONSOR_URL);
    expect(view.getByRole('link', { name: 'Book a call with Calendly' })).toHaveAttribute('href', PLATFORM_CONTACT_CALENDLY);
    expect(view.getByRole('link', { name: 'Connect on LinkedIn' })).toHaveAttribute('href', PLATFORM_CONTACT_LINKEDIN);
    expect(view.getByRole('link', { name: PLATFORM_CONTACT_EMAIL })).toHaveAttribute('href', `mailto:${PLATFORM_CONTACT_EMAIL}`);
    expect(view.getByRole('link', { name: 'Explore the ztd Codebase on GitHub' })).toHaveAttribute('href', ZTD_REPO_URL);
  });

  it('renders populated sponsor rows sorted by rank when sponsors are present', () => {
    const mockSponsors: SponsorEntry[] = [
      {
        id: 'sp-2',
        rank: 2,
        name: 'Beta Systems',
        tier: 'principal',
        contributionDisplay: '$100 / mo',
        totalCents: 10000,
        cadence: 'monthly',
        joinedAt: '2026-10',
        note: 'Keep the evaluations running!',
      },
      {
        id: 'sp-1',
        rank: 1,
        name: 'Alpha Security',
        url: 'https://example.com',
        tier: 'founding',
        contributionDisplay: '$500 / mo',
        totalCents: 50000,
        cadence: 'monthly',
        joinedAt: '2026-10',
        note: 'First sponsor on the board.',
      },
    ];

    render(
      <MemoryRouter>
        <SponsorsView sponsors={mockSponsors} />
      </MemoryRouter>,
    );

    const table = screen.getByRole('table', { name: 'Top sponsors leaderboard' });
    const rows = within(table).getAllByRole('row');
    expect(rows).toHaveLength(3); // 1 header + 2 data rows
    expect(within(rows[1]!).getByText('1st')).toBeInTheDocument();
    expect(within(rows[1]!).getByRole('link', { name: 'Alpha Security' })).toHaveAttribute('href', 'https://example.com');
    expect(within(rows[1]!).getByText('Founding Sponsor')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('First sponsor on the board.')).toBeInTheDocument();

    expect(within(rows[2]!).getByText('2nd')).toBeInTheDocument();
    expect(within(rows[2]!).getByText('Beta Systems')).toBeInTheDocument();
    expect(within(rows[2]!).getByText('Principal Sponsor')).toBeInTheDocument();
  });
});

