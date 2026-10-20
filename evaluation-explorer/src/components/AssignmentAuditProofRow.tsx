// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { PublicEvidenceBinding } from '../contract/types';
import { useMirrorOrigin } from '../state/mirror';
import { useStoreState } from '../state/store';
import { formatNumber } from './shared';

function bindingForKind(bindings: PublicEvidenceBinding[] | undefined, kind: PublicEvidenceBinding['kind']) {
  return bindings?.find((binding) => binding.kind === kind);
}

function formatBytes(value: number): string {
  if (value >= 1024 ** 3) return `${formatNumber(value / 1024 ** 3, 1)} GiB`;
  if (value >= 1024 ** 2) return `${formatNumber(value / 1024 ** 2, 1)} MiB`;
  if (value >= 1024) return `${formatNumber(value / 1024, 1)} KiB`;
  return `${formatNumber(value)} B`;
}

export function AssignmentAuditProofRow({ bindings }: { bindings: PublicEvidenceBinding[] | undefined }) {
  const origin = useMirrorOrigin();
  const sliceBinding = bindingForKind(bindings, 'assignment_audit_slice');
  const sliceArtifact = useStoreState((state) =>
    sliceBinding ? state.proofArtifacts.get(sliceBinding.sha256) : undefined,
  );

  if (!sliceBinding || !sliceArtifact) return null;

  const dbUrl = origin ? `${origin}${sliceArtifact.immutable_url}` : undefined;
  const verifyCommand = sliceArtifact.verification_command;

  return (
    <section className="assignment-audit-proof" aria-label="Audit slice">
      <div className="assignment-audit-proof-row">
        <span className="assignment-audit-proof-label">Audit slice</span>
        <span className="assignment-audit-proof-meta">
          <code>{sliceBinding.sha256.slice(0, 12)}…</code>
          <span>{formatBytes(sliceArtifact.byte_size)}</span>
        </span>
        <span className="assignment-audit-proof-actions">
          {dbUrl ? (
            <a href={dbUrl} download={sliceArtifact.filename}>
              Download SQLite
            </a>
          ) : (
            <span>Download SQLite</span>
          )}
          <button
            type="button"
            className="copy-verify-command"
            onClick={() => {
              if (navigator.clipboard?.writeText) {
                void navigator.clipboard.writeText(verifyCommand);
              }
            }}
          >
            Copy verify command
          </button>
        </span>
      </div>
      <p className="assignment-audit-proof-note">
        Files verify offline with <code>{verifyCommand}</code>. Browser download checks content addressing only; verifying the signed proof segment requires the CLI.
      </p>
    </section>
  );
}

export function filterNonAuditEvidenceBindings(bindings: PublicEvidenceBinding[] | undefined): PublicEvidenceBinding[] | undefined {
  if (!bindings?.length) return bindings;
  const filtered = bindings.filter(
    (binding) =>
      binding.kind !== 'assignment_audit_slice' &&
      binding.kind !== 'assignment_agent_trace' &&
      binding.kind !== 'scenario_input_fixture' &&
      binding.kind !== 'scenario_gold_criteria',
  );
  return filtered.length > 0 ? filtered : undefined;
}
