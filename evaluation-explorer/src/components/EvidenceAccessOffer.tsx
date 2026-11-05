// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The assignment evidence the public site does not serve: the full agent trace
// and the signed audit database. Their SHA-256 digests are in the published
// record, so files delivered under agreement can be checked against them.

import type { PublicEvidenceBinding } from '../contract/types';
import { PLATFORM_CONTACT_EMAIL } from '../content/platform';
import { CopyButton } from './shared';

const OFFERED_EVIDENCE: ReadonlyArray<{ kind: PublicEvidenceBinding['kind']; label: string; noun: string }> = [
  { kind: 'assignment_agent_trace', label: 'Agent trace', noun: 'the full agent trace' },
  { kind: 'assignment_audit_slice', label: 'Audit database', noun: 'the signed audit database' },
];

/** Binding kinds other sections render, which the generic evidence list leaves out. */
const SECTION_EVIDENCE_KINDS: ReadonlySet<PublicEvidenceBinding['kind']> = new Set([
  ...OFFERED_EVIDENCE.map((entry) => entry.kind),
  'scenario_input_fixture',
  'scenario_gold_criteria',
]);

export interface OfferedEvidence {
  label: string;
  noun: string;
  sha256: string;
}

/** The offered evidence this assignment's record commits to, in display order. */
export function offeredEvidence(bindings: PublicEvidenceBinding[] | undefined): OfferedEvidence[] {
  return OFFERED_EVIDENCE.flatMap(({ kind, label, noun }) => {
    const binding = bindings?.find((candidate) => candidate.kind === kind);
    return binding ? [{ label, noun, sha256: binding.sha256 }] : [];
  });
}

/** The evidence request email, naming the run, the assignment, and each digest. */
export function evidenceRequestHref(runId: string, assignmentId: string, items: OfferedEvidence[]): string {
  const body = [
    'I would like access to the full evidence for this assignment.',
    '',
    `Run: ${runId}`,
    `Assignment: ${assignmentId}`,
    ...items.map((item) => `${item.label} SHA-256: ${item.sha256}`),
  ].join('\n');
  const subject = 'Full evidence access (OpenDevOps.ai)';
  return `mailto:${PLATFORM_CONTACT_EMAIL}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
}

function sentenceCase(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export function EvidenceAccessOffer({ bindings, runId, assignmentId }: {
  bindings: PublicEvidenceBinding[] | undefined; runId: string; assignmentId: string;
}) {
  const items = offeredEvidence(bindings);
  if (items.length === 0) return null;
  const subject = sentenceCase(items.map((item) => item.noun).join(' and '));
  return (
    <section className="evidence-offer" aria-label="Full evidence">
      <span className="evidence-offer-label">Full evidence</span>
      <p className="evidence-offer-note">
        {subject} for this assignment {items.length > 1 ? 'are' : 'is'} available under a custom agreement. Delivered files
        match the SHA-256 digests published here.
      </p>
      <ul className="evidence-offer-digests">
        {items.map((item) => (
          <li key={item.label}>
            <span>{item.label}</span>
            <code title={item.sha256}>{item.sha256.slice(0, 12)}…</code>
            <CopyButton text={item.sha256} label={`${item.label} SHA-256`} />
          </li>
        ))}
      </ul>
      <a className="evidence-offer-action" href={evidenceRequestHref(runId, assignmentId, items)}>
        Request access
      </a>
    </section>
  );
}

/** The bindings left for the generic evidence list, or undefined when none remain. */
export function filterNonAuditEvidenceBindings(bindings: PublicEvidenceBinding[] | undefined): PublicEvidenceBinding[] | undefined {
  if (!bindings?.length) return bindings;
  const filtered = bindings.filter((binding) => !SECTION_EVIDENCE_KINDS.has(binding.kind));
  return filtered.length > 0 ? filtered : undefined;
}
