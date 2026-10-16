// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { CSSProperties, ReactNode } from 'react';
import type { ConfidenceInterval, TerminalStatus } from '../contract/types';
import { terminalOutcomeBreakdown, type EvidenceTone } from '../views/derived';
import { scrollToAnchorOnClick } from '../utils/scroll';
import { formatNumber, formatPercent } from '../utils/format';
import { UnavailableValue } from './shared';

export function ToneDot({ tone }: { tone: EvidenceTone }) {
  return <span className={`tone-dot tone-dot-${tone}`} aria-hidden="true" />;
}

export function TonePill({ tone, children, title, className = '' }: {
  tone: EvidenceTone; children: ReactNode; title?: string; className?: string;
}) {
  return <span className={`terminal-pill terminal-pill-${tone} ${className}`} title={title} data-tone={tone}>{children}</span>;
}

export function PageHero({ kicker, title, meta, verdict, tone = 'neutral', children }: {
  kicker: string; title: string; meta?: ReactNode; verdict?: ReactNode; tone?: EvidenceTone; children?: ReactNode;
}) {
  return (
    <header className={`evidence-hero evidence-hero-${tone}`}>
      <div className="evidence-hero-top">
        <div className="evidence-hero-identity">
          <p className="evidence-kicker">{kicker}</p>
          <h1 className="evidence-title">{title}</h1>
          {meta ? <div className="evidence-meta">{meta}</div> : null}
        </div>
        {verdict ? <div className="evidence-verdict">{verdict}</div> : null}
      </div>
      {children}
    </header>
  );
}

export interface ChainStep {
  key: string; label: string; value: string; tone: EvidenceTone; detail?: string;
}

export function EvidenceChain({ steps, targets = {} }: {
  steps: readonly ChainStep[]; targets?: Partial<Record<string, string>>;
}) {
  return (
    <nav className="evidence-chain" aria-label="Evidence chain">
      <ol style={{ '--chain-steps': steps.length } as CSSProperties}>
        {steps.map((step) => {
          const target = targets[step.key];
          const body = <>
            <span className="evidence-step-label">{step.label}</span>
            <span className="evidence-step-value">{step.value}</span>
            {step.detail ? <span className="evidence-step-detail">{step.detail}</span> : null}
          </>;
          return <li key={step.key} className={`evidence-step evidence-step-${step.tone}`}>
            {target ? <a href={`#${target}`} className="evidence-step-body" onClick={scrollToAnchorOnClick(target)}>{body}</a>
              : <div className="evidence-step-body">{body}</div>}
          </li>;
        })}
      </ol>
    </nav>
  );
}

export function PanelSection({ title, note, id, className = '', children }: {
  title: string; note?: string; id?: string; className?: string; children: ReactNode;
}) {
  return <section id={id} className={`evidence-panel ${className}`} aria-label={title}>
    <h2>{title}</h2>
    {note ? <p className="panel-note">{note}</p> : null}
    {children}
  </section>;
}

export function DetailLayout({ children, aside, asideLabel = 'Evidence and context' }: {
  children: ReactNode; aside?: ReactNode; asideLabel?: string;
}) {
  return <div className={`evidence-layout${aside ? '' : ' evidence-layout-single'}`}>
    <div className="evidence-main">{children}</div>
    {aside ? <aside className="evidence-aside" aria-label={asideLabel}>{aside}</aside> : null}
  </div>;
}

export const NON_VERDICT_NOTE = 'Not a verdict on the model; not counted for or against it.';

export function PassRateBar({ interval }: { interval: ConfidenceInterval | undefined }) {
  if (!interval) return <span className="not-evaluated">Not evaluated</span>;
  return <div className="pass-rate-bar">
    <span>{formatPercent(interval.estimate)} · n={formatNumber(interval.denominator)}</span>
    <meter min={0} max={1} value={interval.estimate} aria-label={`Pass rate, ${interval.denominator} eligible observations`} />
  </div>;
}

export function OutcomeBreakdown({ outcomes }: { outcomes: Partial<Record<TerminalStatus, number>> | undefined }) {
  const breakdown = terminalOutcomeBreakdown(outcomes);
  if (!breakdown) return <UnavailableValue reason="terminal outcomes not published" />;
  if (breakdown.total === 0) return <span>No terminal outcomes</span>;
  return <div className="outcome-breakdown" data-testid="outcome-breakdown">
    {breakdown.verdictCount > 0 ? <div className="outcome-bar" aria-label={`${breakdown.verdictCount} model verdicts`}>
      {breakdown.verdicts.map(({ status, count, tone }) => <span key={status} className={`outcome-segment outcome-segment-${tone}`} style={{ flexGrow: count }} title={`${status}: ${count}`} />)}
    </div> : null}
    <ul className="outcome-legend">
      {[...breakdown.verdicts, ...breakdown.nonVerdicts].map(({ status, count, tone }) => <li key={status}><ToneDot tone={tone} />{status.replace(/_/g, ' ')}: {formatNumber(count)}</li>)}
    </ul>
    {breakdown.nonVerdictCount > 0 ? <p className="panel-note" title={NON_VERDICT_NOTE}>{formatNumber(breakdown.nonVerdictCount)} non-verdict · {NON_VERDICT_NOTE}</p> : null}
  </div>;
}
