// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { Link } from 'react-router-dom';
import {
  PLATFORM_CONTACT_CALENDLY,
  PLATFORM_CONTACT_EMAIL,
  PLATFORM_CONTACT_LINKEDIN,
  SPONSOR_URL,
  SPONSORS_ABOUT_PARAGRAPH,
  SPONSORS_AUTOMATION_NOTE,
  SPONSORS_HERO_TITLE,
  SPONSORS_URGENCY_HEADING,
  SPONSORS_URGENCY_PARAGRAPHS,
  SPONSORSHIP_LEDE,
  TOP_SPONSORS,
  ZTD_REPO_URL,
  type SponsorEntry,
  type SponsorTier,
} from '../content/platform';
import { PageHero, PanelSection } from '../components/evidence';
import { formatRank, StreamStatusIndicator } from '../components/shared';
import { useConnection, useStoreState } from '../state/store';

const TIER_LABELS: Record<SponsorTier, string> = {
  founding: 'Founding Sponsor',
  principal: 'Principal Sponsor',
  supporter: 'Supporter',
  community: 'Community',
};

function rankBadgeClass(rank: number): string {
  if (rank === 1) return 'leaderboard-rank rank-1';
  if (rank === 2) return 'leaderboard-rank rank-2';
  if (rank === 3) return 'leaderboard-rank rank-3';
  return 'leaderboard-rank rank-default';
}

const OPEN_PODIUM_SLOTS: ReadonlyArray<{
  rank: number;
  tier: SponsorTier;
  support: string;
  note: string;
}> = [
  {
    rank: 1,
    tier: 'founding',
    support: 'Any amount',
    note: 'Nobody’s here yet.',
  },
  {
    rank: 2,
    tier: 'principal',
    support: '—',
    note: 'Reserved for #2.',
  },
  {
    rank: 3,
    tier: 'supporter',
    support: '—',
    note: 'Reserved for #3.',
  },
];

export function SponsorsView({
  sponsors = TOP_SPONSORS,
}: {
  sponsors?: readonly SponsorEntry[];
}) {
  const connection = useConnection();
  const streamConnection = useStoreState((state) => state.streamConnection);

  const sortedSponsors = [...sponsors].sort(
    (a, b) => a.rank - b.rank || b.totalCents - a.totalCents,
  );

  return (
    <section className="sponsors-view about" aria-label="Sponsors and project sustainability">
      <PageHero
        kicker="Sponsors"
        title={SPONSORS_HERO_TITLE}
        tone="warn"
        meta={
          <>
            <span>Solo builder · Portland, OR</span>
            <span>Runway: basically empty</span>
          </>
        }
        verdict={
          <StreamStatusIndicator
            streamConnection={streamConnection}
            feedConnection={connection}
          />
        }
      >
        <p className="sys-platform-lede">{SPONSORSHIP_LEDE}</p>
        <div className="sponsors-hero-actions">
          <a
            className="sponsors-cta-primary"
            href={SPONSOR_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            Sponsor via Stripe →
          </a>
          <a
            className="sponsors-cta-secondary"
            href={`mailto:${PLATFORM_CONTACT_EMAIL}?subject=Sponsoring%20or%20Working%20Together%20(OpenDevOps.ai%20%2F%20ztd)`}
          >
            Email me
          </a>
          <a
            className="sponsors-cta-secondary"
            href={PLATFORM_CONTACT_CALENDLY}
            target="_blank"
            rel="noopener noreferrer"
          >
            Book a call
          </a>
        </div>
      </PageHero>

      <PanelSection
        title={SPONSORS_URGENCY_HEADING}
        className="sponsors-urgency-panel"
      >
        {SPONSORS_URGENCY_PARAGRAPHS.map((paragraph, index) => (
          <p key={index} className="sys-platform-lede">
            {paragraph}
          </p>
        ))}
        <div className="sponsors-first-cta-box" role="region" aria-label="First sponsor call to action">
          <div className="sponsors-first-cta-copy">
            <span className="sponsors-first-cta-kicker">#1 spot is open</span>
            <h3>Be the first sponsor on the board</h3>
            <p>Nobody’s claimed #1 yet. Any amount helps, one-time or monthly.</p>
          </div>
          <div className="sponsors-first-cta-action">
            <a
              className="sponsors-cta-primary"
              href={SPONSOR_URL}
              target="_blank"
              rel="noopener noreferrer"
            >
              Claim #1 →
            </a>
          </div>
        </div>
      </PanelSection>

      <PanelSection title="Sponsors" note="Ranked by total support.">
        <div className="data-table-wrap">
          <table className="data-table lab-table sponsors-leaderboard-table" aria-label="Top sponsors leaderboard">
            <thead>
              <tr>
                <th scope="col" className="leaderboard-rank-cell">Rank</th>
                <th scope="col">Sponsor</th>
                <th scope="col">Tier</th>
                <th scope="col">Support</th>
                <th scope="col">Since</th>
                <th scope="col">Note</th>
              </tr>
            </thead>
            <tbody>
              {sortedSponsors.length > 0 ? (
                sortedSponsors.map((entry) => (
                  <tr key={entry.id} className="sponsors-leaderboard-row">
                    <td className="leaderboard-rank-cell">
                      <span className={rankBadgeClass(entry.rank)}>{formatRank(entry.rank)}</span>
                    </td>
                    <td>
                      {entry.url ? (
                        <a
                          href={entry.url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="role-leader-name"
                        >
                          {entry.name}
                        </a>
                      ) : (
                        <span className="role-leader-name">{entry.name}</span>
                      )}
                    </td>
                    <td>
                      <span className={`sponsor-tier-badge sponsor-tier-${entry.tier}`}>
                        {TIER_LABELS[entry.tier]}
                      </span>
                    </td>
                    <td className="role-leader-score">
                      {entry.contributionDisplay}{' '}
                      <span className="sponsors-cadence-label">({entry.cadence})</span>
                    </td>
                    <td>{entry.joinedAt}</td>
                    <td>{entry.note ?? '—'}</td>
                  </tr>
                ))
              ) : (
                OPEN_PODIUM_SLOTS.map((slot) => (
                  <tr
                    key={slot.rank}
                    className={`sponsors-leaderboard-row sponsors-placeholder-row${slot.rank === 1 ? ' sponsors-placeholder-first' : ''}`}
                  >
                    <td className="leaderboard-rank-cell">
                      <span className={rankBadgeClass(slot.rank)}>{formatRank(slot.rank)}</span>
                    </td>
                    <td>
                      <span className="role-leader-name">Open</span>
                    </td>
                    <td>
                      <span className={`sponsor-tier-badge sponsor-tier-${slot.tier}`}>
                        {TIER_LABELS[slot.tier]}
                      </span>
                    </td>
                    <td className="role-leader-score">{slot.support}</td>
                    <td>Open now</td>
                    <td>{slot.note}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        <p className="docs-section-note sponsors-automation-note">{SPONSORS_AUTOMATION_NOTE}</p>
      </PanelSection>

      <PanelSection title="About me">
        <p className="sys-platform-lede">{SPONSORS_ABOUT_PARAGRAPH}</p>
        <p className="sys-platform-lede">
          <a href={`mailto:${PLATFORM_CONTACT_EMAIL}`}>{PLATFORM_CONTACT_EMAIL}</a>
          {' · '}
          <a href={PLATFORM_CONTACT_LINKEDIN} target="_blank" rel="noopener noreferrer">
            LinkedIn
          </a>
          {' · '}
          <a href={ZTD_REPO_URL} target="_blank" rel="noopener noreferrer">
            ztd on GitHub
          </a>
          {' · '}
          <Link to="/about">Full About page</Link>
        </p>
      </PanelSection>
    </section>
  );
}
