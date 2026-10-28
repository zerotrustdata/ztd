// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { AssignmentAuditProofRow, filterNonAuditEvidenceBindings } from '../src/components/AssignmentAuditProofRow';
import type { ProofCatalogEntry } from '../src/contract/types';
import { evalStore } from '../src/state/store';

const { fetchProofCatalogEntry } = vi.hoisted(() => ({ fetchProofCatalogEntry: vi.fn() }));
vi.mock('../src/state/feed', () => ({
  fetchProofCatalogEntry,
}));

const sliceHash = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
const publishedCommand = 'ztd public verify-assignment --db assignment-1.db --segments proof-segments.jsonl --public-key <trusted-ed25519-key-hex>';
const verifyCommand = 'ztd public verify-assignment --db assignment-1.db --segments proof-segments.jsonl';

function catalogEntry(sha256: string, filename: string, byteSize = 4096): ProofCatalogEntry {
  return {
    artifact_id: sha256,
    filename,
    media_type: sha256 === sliceHash ? 'application/vnd.sqlite3' : 'text/plain',
    byte_size: byteSize,
    sha256,
    classification: 'public_safe',
    campaign_id: 'campaign-1',
    generated_at: '2026-09-20T00:00:00Z',
    verification_command: publishedCommand,
    immutable_url: `/proofs/${sha256}`,
  };
}

const auditBindings = [
  { kind: 'assignment_audit_slice' as const, sha256: sliceHash, schema_ref: 'ztd.eval.v1.PublicAssignmentAuditSlice' },
];

describe('AssignmentAuditProofRow', () => {
  beforeEach(() => {
    fetchProofCatalogEntry.mockReset().mockResolvedValue(null);
    evalStore.loadFixtures([], []);
    evalStore.setMirrorOrigin('http://127.0.0.1:8082');
    evalStore.loadProofCatalog([catalogEntry(sliceHash, 'assignment-1.db', 8192)]);
  });

  it('resolves an unresolved binding from the mirror once, on demand', async () => {
    evalStore.loadProofCatalog([]);
    fetchProofCatalogEntry.mockResolvedValue(catalogEntry(sliceHash, 'assignment-1.db', 8192));
    render(<><AssignmentAuditProofRow bindings={auditBindings} /><AssignmentAuditProofRow bindings={auditBindings} /></>);
    expect(await screen.findAllByRole('link', { name: 'Download SQLite' })).toHaveLength(2);
    expect(fetchProofCatalogEntry).toHaveBeenCalledTimes(1);
    expect(fetchProofCatalogEntry).toHaveBeenCalledWith('http://127.0.0.1:8082', undefined, sliceHash);
  });

  it('does not fetch an entry the store already holds', async () => {
    render(<AssignmentAuditProofRow bindings={auditBindings} />);
    await screen.findByRole('link', { name: 'Download SQLite' });
    expect(fetchProofCatalogEntry).not.toHaveBeenCalled();
  });

  it('renders the database download and the Gateway-published verify command', async () => {
    render(<AssignmentAuditProofRow bindings={auditBindings} />);
    expect(await screen.findByRole('link', { name: 'Download SQLite' })).toHaveAttribute(
      'href',
      `http://127.0.0.1:8082/proofs/${sliceHash}`,
    );
    expect(screen.queryByText(/vault/i)).not.toBeInTheDocument();
    expect(screen.getByText('8.0 KiB')).toBeInTheDocument();
    expect(screen.getByText(verifyCommand)).toBeInTheDocument();
    expect(screen.queryByText(publishedCommand)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy verify command' })).toBeInTheDocument();
  });

  it('is absent when the audit slice binding is missing', () => {
    const { container } = render(<AssignmentAuditProofRow bindings={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('is absent when catalog entries are missing even with bindings', () => {
    evalStore.loadProofCatalog([]);
    const { container } = render(<AssignmentAuditProofRow bindings={auditBindings} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe('filterNonAuditEvidenceBindings', () => {
  it('removes audit kinds and returns undefined when nothing remains', () => {
    expect(filterNonAuditEvidenceBindings(auditBindings)).toBeUndefined();
  });

  it('removes the agent trace, which renders in its own section', () => {
    const trace = { kind: 'assignment_agent_trace' as const, sha256: 'c'.repeat(64), schema_ref: 'ztd.eval.v1.PublicAssignmentAgentTrace' };
    const manifest = { kind: 'source_manifest' as const, sha256: 'd'.repeat(64), schema_ref: 'ztd.eval.v1.SourceManifest' };
    expect(filterNonAuditEvidenceBindings([...auditBindings, trace, manifest])).toEqual([manifest]);
  });

  it('removes the scenario fixture, which renders in its own section', () => {
    const input = { kind: 'scenario_input_fixture' as const, sha256: 'e'.repeat(64), schema_ref: 'ztd.eval.v1.PublicScenarioInputFixture' };
    const gold = { kind: 'scenario_gold_criteria' as const, sha256: 'f'.repeat(64), schema_ref: 'ztd.eval.v1.PublicScenarioGoldCriteria' };
    const manifest = { kind: 'source_manifest' as const, sha256: 'd'.repeat(64), schema_ref: 'ztd.eval.v1.SourceManifest' };
    expect(filterNonAuditEvidenceBindings([input, gold, manifest])).toEqual([manifest]);
  });
});
