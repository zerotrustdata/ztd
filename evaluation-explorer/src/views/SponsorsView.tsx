// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { Link } from 'react-router-dom';
import {
  ABOUT_AVAILABILITY,
  ABOUT_AVAILABILITY_DETAIL,
  ABOUT_BACKGROUND,
  ABOUT_EXPERIENCE,
  ABOUT_OPENING_AFTER,
  ABOUT_OPENING_BEFORE,
  ABOUT_PURPOSE,
  PLATFORM_CONTACT_CALENDLY,
  PLATFORM_CONTACT_EMAIL,
  PLATFORM_CONTACT_LINKEDIN,
  SPONSOR_URL,
  SPONSORS_AUTOMATION_NOTE,
  SPONSORS_HERO_TITLE,
  SPONSORS_MANIFEST,
  SPONSORS_URGENCY_HEADING,
  SPONSORS_URGENCY_PARAGRAPHS,
  SPONSORSHIP_LEDE,
  SPONSORSHIP_USES,
  TOP_SPONSORS,
  WORKSTATION_SPECS,
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
  title: string;
  tier: SponsorTier;
  support: string;
  note: string;
}> = [
  {
    rank: 1,
    title: 'Unclaimed — Be the very first sponsor',
    tier: 'founding',
    support: 'Any amount',
    note: 'Claim the #1 Founding Sponsor spot on this leaderboard and help keep the evaluation pipeline online.',
  },
  {
    rank: 2,
    title: 'Open slot — #2 Sponsor',
    tier: 'principal',
    support: '—',
    note: 'Reserved for our second supporter.',
  },
  {
    rank: 3,
    title: 'Open slot — #3 Sponsor',
    tier: 'supporter',
    support: '—',
    note: 'Reserved for our third supporter.',
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
        kicker="Sponsors & project sustainability"
        title={SPONSORS_HERO_TITLE}
        tone="warn"
        meta={
          <>
            <span>Solo builder · Portland, OR</span>
            <span>Runway status: Out of runway</span>
            <span>Leaderboard mode: {SPONSORS_MANIFEST.mode === 'static_manual' ? 'Static (manual updates)' : 'Automated'}</span>
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
            Become the First Sponsor via Stripe →
          </a>
          <a
            className="sponsors-cta-secondary"
            href={`mailto:${PLATFORM_CONTACT_EMAIL}?subject=Sponsoring%20or%20Working%20Together%20(OpenDevOps.ai%20%2F%20ztd)`}
          >
            Email {PLATFORM_CONTACT_EMAIL}
          </a>
          <a
            className="sponsors-cta-secondary"
            href={PLATFORM_CONTACT_CALENDLY}
            target="_blank"
            rel="noopener noreferrer"
          >
            Book a call on Calendly
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
            <span className="sponsors-first-cta-kicker">Rank #1 Available · Founding Sponsor</span>
            <h3>Be the very first sponsor on the leaderboard</h3>
            <p>
              No one has claimed the #1 spot yet. Whether it is a small one-time contribution or ongoing support,
              the first sponsor on the board sets the foundation to keep these evaluations running while I work
              through this runway gap.
            </p>
          </div>
          <div className="sponsors-first-cta-action">
            <a
              className="sponsors-cta-primary"
              href={SPONSOR_URL}
              target="_blank"
              rel="noopener noreferrer"
            >
              Claim the #1 Sponsor Spot →
            </a>
          </div>
        </div>
      </PanelSection>

      <PanelSection
        title="Top Sponsors Leaderboard"
        note="Ranked by cumulative support. Every contribution directly funds hardware uptime, evaluation runs, and solo engineering runway."
      >
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
                      <span className="role-leader-name">{slot.title}</span>
                    </td>
                    <td>
                      <span className={`sponsor-tier-badge sponsor-tier-${slot.tier}`}>
                        {TIER_LABELS[slot.tier]}
                      </span>
                    </td>
                    <td className="role-leader-score">{slot.support}</td>
                    <td>Open now</td>
                    <td>
                      {slot.rank === 1 ? (
                        <>
                          {slot.note}{' '}
                          <a
                            href={SPONSOR_URL}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="sponsors-inline-claim-link"
                          >
                            Sponsor via Stripe →
                          </a>
                        </>
                      ) : (
                        slot.note
                      )}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        <p className="docs-section-note sponsors-automation-note">
          <strong>Static today, structured for automation later:</strong> {SPONSORS_AUTOMATION_NOTE}
        </p>
      </PanelSection>

      <div className="about-grid">
        <PanelSection title="About the project (condensed)">
          <p className="sys-platform-lede">{ABOUT_PURPOSE}</p>
          <p className="sys-platform-lede">
            Unlike synthetic benchmarks that call a model API in isolation, this Explorer scores models through the
            full governed <strong>ztd</strong> execution path on local workstation hardware (
            {WORKSTATION_SPECS.map((spec) => spec.value).join(' · ')}).
          </p>
          <ul className="about-highlights" aria-label="What sponsorship supports">
            {SPONSORSHIP_USES.map((use) => (
              <li key={use}>{use}</li>
            ))}
          </ul>
          <p className="sys-platform-lede">
            Read the full <Link to="/about">About page</Link>, explore the{' '}
            <Link to="/leaderboard">Model Leaderboard</Link>, or inspect the{' '}
            <Link to="/methodology">Evaluation Methodology</Link>.
          </p>
        </PanelSection>

        <PanelSection title="About the builder & hiring availability">
          <p className="sys-platform-lede">
            {ABOUT_OPENING_BEFORE}
            <strong>Danny Barbour</strong>
            {ABOUT_OPENING_AFTER}
          </p>
          <p className="sys-platform-lede">{ABOUT_BACKGROUND}</p>
          <ul className="about-highlights" aria-label="Builder background">
            {ABOUT_EXPERIENCE.slice(0, 3).map((item) => (
              <li key={item.company}>
                <strong>{item.company}</strong> — {item.text}
              </li>
            ))}
          </ul>
          <p className="sys-platform-lede about-availability">{ABOUT_AVAILABILITY}</p>
          <p className="sys-platform-lede">{ABOUT_AVAILABILITY_DETAIL}</p>
          <p className="sys-platform-lede">
            If your team is hiring or needs contract engineering help, reach me directly at{' '}
            <a href={`mailto:${PLATFORM_CONTACT_EMAIL}`}>{PLATFORM_CONTACT_EMAIL}</a>.
          </p>
        </PanelSection>
      </div>

      <div className="sys-platform-cta about-actions">
        <div className="sys-platform-links">
          <a
            className="sponsors-cta-primary"
            href={SPONSOR_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            Sponsor via Stripe
          </a>
          <a
            className="sys-platform-cta-architecture"
            href={PLATFORM_CONTACT_CALENDLY}
            target="_blank"
            rel="noopener noreferrer"
          >
            Book a call with Calendly
          </a>
          <a
            className="sys-platform-cta-architecture"
            href={PLATFORM_CONTACT_LINKEDIN}
            target="_blank"
            rel="noopener noreferrer"
          >
            Connect on LinkedIn
          </a>
          <a
            className="sys-platform-cta-architecture"
            href={ZTD_REPO_URL}
            target="_blank"
            rel="noopener noreferrer"
          >
            Explore the ztd Codebase on GitHub
          </a>
        </div>
      </div>
    </section>
  );
}

