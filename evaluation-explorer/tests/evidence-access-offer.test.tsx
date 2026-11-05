// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { EvidenceAccessOffer, filterNonAuditEvidenceBindings } from '../src/components/EvidenceAccessOffer';
import { PLATFORM_CONTACT_EMAIL } from '../src/content/platform';
import type { PublicEvidenceBinding } from '../src/contract/types';

const trace: PublicEvidenceBinding = { kind: 'assignment_agent_trace', sha256: 'c'.repeat(64), schema_ref: 'ztd.eval.v1.PublicAssignmentAgentTrace' };
const slice: PublicEvidenceBinding = { kind: 'assignment_audit_slice', sha256: 'a'.repeat(64), schema_ref: 'ztd.eval.v1.PublicAssignmentAuditSlice' };
const manifest: PublicEvidenceBinding = { kind: 'source_manifest', sha256: 'd'.repeat(64), schema_ref: 'ztd.eval.v1.SourceManifest' };

function requestLink(): URL {
  return new URL(screen.getByRole('link', { name: 'Request access' }).getAttribute('href') ?? '');
}

describe('EvidenceAccessOffer', () => {
  it('offers the trace and audit database by email, naming the run, assignment, and digests', () => {
    render(<EvidenceAccessOffer bindings={[trace, slice, manifest]} runId="run-1" assignmentId="assignment-1" />);
    expect(screen.getByText(/The full agent trace and the signed audit database for this assignment are available under a custom agreement/)).toBeTruthy();
    const link = requestLink();
    expect(link.protocol).toBe('mailto:');
    expect(link.pathname).toBe(PLATFORM_CONTACT_EMAIL);
    const body = link.searchParams.get('body') ?? '';
    expect(body).toContain('Run: run-1');
    expect(body).toContain('Assignment: assignment-1');
    expect(body).toContain(`Agent trace SHA-256: ${trace.sha256}`);
    expect(body).toContain(`Audit database SHA-256: ${slice.sha256}`);
    expect(body).not.toContain(manifest.sha256);
  });

  it('names only the evidence the record commits to', () => {
    render(<EvidenceAccessOffer bindings={[slice]} runId="run-1" assignmentId="assignment-1" />);
    expect(screen.getByText(/The signed audit database for this assignment is available/)).toBeTruthy();
    expect(requestLink().searchParams.get('body')).not.toContain('Agent trace');
  });

  it('renders nothing without offered evidence', () => {
    const { container } = render(<EvidenceAccessOffer bindings={[manifest]} runId="run-1" assignmentId="assignment-1" />);
    expect(container.innerHTML).toBe('');
  });
});

describe('filterNonAuditEvidenceBindings', () => {
  it('removes offered kinds and returns undefined when nothing remains', () => {
    expect(filterNonAuditEvidenceBindings([trace, slice])).toBeUndefined();
  });

  it('removes the offered evidence and the scenario fixture, which render in their own sections', () => {
    const input: PublicEvidenceBinding = { kind: 'scenario_input_fixture', sha256: 'e'.repeat(64), schema_ref: 'ztd.eval.v1.PublicScenarioInputFixture' };
    const gold: PublicEvidenceBinding = { kind: 'scenario_gold_criteria', sha256: 'f'.repeat(64), schema_ref: 'ztd.eval.v1.PublicScenarioGoldCriteria' };
    expect(filterNonAuditEvidenceBindings([trace, slice, input, gold, manifest])).toEqual([manifest]);
  });
});
