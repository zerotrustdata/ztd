// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { Link } from 'react-router-dom';
import {
  ABOUT_AVAILABILITY,
  ABOUT_AVAILABILITY_DETAIL,
  ABOUT_AVAILABILITY_HEADING,
  ABOUT_BACKGROUND,
  ABOUT_EVALUATION_GOALS,
  ABOUT_EVALUATION_INTRO,
  ABOUT_EVIDENCE_INTRO,
  ABOUT_EVIDENCE_PRINCIPLES,
  ABOUT_EXPERIENCE,
  ABOUT_HEADING,
  ABOUT_OPENING_AFTER,
  ABOUT_OPENING_BEFORE,
  ABOUT_ORIGIN,
  ABOUT_ORIGIN_HEADING,
  ABOUT_PORTFOLIO,
  ABOUT_PRINCIPLES,
  ABOUT_PURPOSE,
  ABOUT_PURPOSE_HEADING,
  ABOUT_SITE_INTRO_AFTER,
  ABOUT_SITE_INTRO_BEFORE,
  ZTD_CORE_DOCS,
  ZTD_REPO_URL,
  PLATFORM_CONTACT_CALENDLY,
  PLATFORM_CONTACT_EMAIL,
  PLATFORM_CONTACT_LINKEDIN,
} from '../content/platform';
import { StreamStatusIndicator } from '../components/shared';
import { PageHero, PanelSection } from '../components/evidence';
import { useConnection, useStoreState } from '../state/store';

export function AboutView() {
  const connection = useConnection();
  const streamConnection = useStoreState((state) => state.streamConnection);

  return (
    <section className="about" aria-label="About this deployment">
      <PageHero
        kicker="About this live evaluation project"
        title={ABOUT_PURPOSE_HEADING}
        tone="info"
        verdict={
          <StreamStatusIndicator
            streamConnection={streamConnection}
            feedConnection={connection}
          />
        }
      >
        <p className="sys-platform-lede">
          {ABOUT_SITE_INTRO_BEFORE}
          <strong>ztd</strong>
          {ABOUT_SITE_INTRO_AFTER}
        </p>
      </PageHero>
      <PanelSection title="What these evaluations are for">
        <p className="sys-platform-lede">{ABOUT_EVALUATION_INTRO}</p>
        <ol className="about-highlights" aria-label="Evaluation goals">
          {ABOUT_EVALUATION_GOALS.map((goal) => (
            <li key={goal.label}>
              <strong>{goal.label}</strong> {goal.detail}
            </li>
          ))}
        </ol>
      </PanelSection>
      <PanelSection title="Follow the evidence">
        <p className="sys-platform-lede">{ABOUT_EVIDENCE_INTRO}</p>
        <ul className="about-highlights">
          {ABOUT_EVIDENCE_PRINCIPLES.map((principle) => <li key={principle}>{principle}</li>)}
        </ul>
        <p className="sys-platform-lede">
          Follow the <Link to="/">live evaluations</Link>, inspect <Link to="/evaluations">published runs</Link>,
          {' '}explore <Link to="/models">model results</Link>, or read the <Link to="/methodology">methodology</Link>.
        </p>
      </PanelSection>
      <div className="about-grid">
        <PanelSection title="The platform behind the evaluations">
          <p className="sys-platform-lede">{ABOUT_PURPOSE}</p>
          <ul className="about-highlights" aria-label="ztd principles">
            {ABOUT_PRINCIPLES.map((item) => (
              <li key={item.label}>
                <strong>{item.label}:</strong> {item.detail}
              </li>
            ))}
          </ul>
          <p className="sys-platform-lede">
            Read <a href={ZTD_CORE_DOCS.about} target="_blank" rel="noopener noreferrer">About ztd</a>
            {' '}and the{' '}
            <a href={ZTD_CORE_DOCS.position} target="_blank" rel="noopener noreferrer">position paper</a>.
          </p>

        </PanelSection>
        <PanelSection title={ABOUT_ORIGIN_HEADING}>
          <p className="sys-platform-lede">{ABOUT_ORIGIN}</p>

        </PanelSection>
        <PanelSection title={ABOUT_HEADING}>
          <p className="sys-platform-lede">
            {ABOUT_OPENING_BEFORE}
            <strong>Danny Barbour</strong>
            {ABOUT_OPENING_AFTER}
          </p>
          <p className="sys-platform-lede">{ABOUT_BACKGROUND}</p>
          <ul className="about-highlights" aria-label="Career highlights">
            {ABOUT_EXPERIENCE.map((item) => (
              <li key={item.company}>
                <strong>{item.company}</strong>, {item.text}
              </li>
            ))}
          </ul>

        </PanelSection>
        <PanelSection title={ABOUT_AVAILABILITY_HEADING}>
          <p className="sys-platform-lede about-availability">{ABOUT_PORTFOLIO}</p>
          <p className="sys-platform-lede about-availability">{ABOUT_AVAILABILITY}</p>
          <p className="sys-platform-lede about-availability">{ABOUT_AVAILABILITY_DETAIL}</p>
          <p className="sys-platform-lede about-availability">
            Reach me at{' '}
            <a href={`mailto:${PLATFORM_CONTACT_EMAIL}`}>{PLATFORM_CONTACT_EMAIL}</a>.
          </p>

        </PanelSection>
      </div>
      <div className="sys-platform-cta about-actions">
        <div className="sys-platform-links">
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
