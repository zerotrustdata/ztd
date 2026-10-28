// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The campaign-frozen scenario fixture behind an assignment, fetched from its
// content-addressed proof artifacts and verified in the browser before any of
// it renders: the exact user prompt, inline context, workspace files (decoys
// included) and seeded investigation the model ran in, and the gold criteria
// it was graded against. Nothing is screened.

import { useEffect, useState } from 'react';
import {
  verifyScenarioGoldArtifact,
  verifyScenarioInputArtifact,
  type ScenarioFixtureBinding,
  type ScenarioGoldCriteria,
  type ScenarioInputFixture,
} from '../contract/scenario-fixture';
import type { ProofCatalogEntry, PublicEvidenceBinding } from '../contract/types';
import { ValidationError } from '../contract/validators';
import { runnableVerifyCommand } from '../verifyCommand';
import { useMirrorOrigin } from '../state/mirror';
import { useProofArtifact } from '../state/proof';
import { TextBlock } from './AgentTraceSection';
import { PanelSection } from './evidence';
import { CopyButton } from './shared';

type Verified<T> = { status: 'loading' } | { status: 'verified'; value: T } | { status: 'failed'; reason: string };

function useVerifiedArtifact<T>(
  url: string | undefined,
  binding: ScenarioFixtureBinding | undefined,
  verify: (body: Uint8Array, binding: ScenarioFixtureBinding) => Promise<T>,
): Verified<T> {
  const [state, setState] = useState<Verified<T>>({ status: 'loading' });
  const sha256 = binding?.sha256;
  const scenarioId = binding?.scenarioId;
  useEffect(() => {
    if (!url || !sha256 || !scenarioId) return;
    let cancelled = false;
    setState({ status: 'loading' });
    fetch(url)
      .then((response) => {
        if (!response.ok) throw new Error(`fetch returned HTTP ${response.status}`);
        return response.arrayBuffer();
      })
      .then((buffer) => verify(new Uint8Array(buffer), { sha256, scenarioId }))
      .then((value) => {
        if (!cancelled) setState({ status: 'verified', value });
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        const reason = error instanceof ValidationError ? `${error.path}: ${error.message}` : error instanceof Error ? error.message : String(error);
        setState({ status: 'failed', reason });
      });
    return () => {
      cancelled = true;
    };
  }, [url, sha256, scenarioId, verify]);
  return state;
}

function ArtifactProof({ binding, artifact, url, noun }: {
  binding: PublicEvidenceBinding; artifact: ProofCatalogEntry; url: string | undefined; noun: string;
}) {
  return (
    <div className="agent-trace-proof">
      <code>sha256 {binding.sha256}</code>
      {url ? <a href={url} download={artifact.filename}>Download {noun}</a> : <span>Download {noun}</span>}
      <CopyButton text={runnableVerifyCommand(artifact.verification_command)} label={`${noun} verify command`} />
    </div>
  );
}

function Status<T>({ state, noun }: { state: Verified<T>; noun: string }) {
  if (state.status === 'loading') return <p className="model-response-empty">Loading and verifying the {noun}…</p>;
  if (state.status === 'failed') {
    return <p className="agent-trace-failed" role="alert">{`${noun[0]!.toUpperCase()}${noun.slice(1)} not shown: verification failed (${state.reason}).`}</p>;
  }
  return null;
}

function InputBody({ input }: { input: ScenarioInputFixture }) {
  return (
    <>
      <TextBlock title="User prompt" text={input.user_prompt} copyLabel="user prompt" />
      {input.inline_context.length > 0 ? (
        <ol className="agent-trace-messages" aria-label="Inline context">
          {input.inline_context.map((entry, index) => (
            <li key={index}>
              <TextBlock title={`Inline context · ${entry.label} (${entry.kind})`} text={entry.content} copyLabel={`${entry.label} context`} />
            </li>
          ))}
        </ol>
      ) : null}
      {input.workspace_files.length > 0 ? (
        <details className="agent-trace-tools">
          <summary>Workspace files ({input.workspace_files.length})</summary>
          <ul>
            {input.workspace_files.map((file) => (
              <li key={file.rel_path}>
                <TextBlock title={`${file.rel_path}${file.decoy ? ' · decoy' : ''}`} text={file.content} copyLabel={file.rel_path} />
              </li>
            ))}
          </ul>
        </details>
      ) : null}
      <details className="agent-trace-raw">
        <summary>Seeded investigation</summary>
        <TextBlock title="Seed" text={JSON.stringify(input.seed)} copyLabel="seed" json />
      </details>
    </>
  );
}

function GoldBody({ gold }: { gold: ScenarioGoldCriteria }) {
  return (
    <>
      <TextBlock title="Expected behavior" text={gold.expected_behavior} copyLabel="expected behavior" />
      <details className="agent-trace-raw">
        <summary>Gold criteria JSON</summary>
        <TextBlock title="Criteria" text={JSON.stringify(gold.body)} copyLabel="gold criteria" json />
      </details>
    </>
  );
}

export function scenarioFixtureBindings(bindings: PublicEvidenceBinding[] | undefined) {
  return {
    input: bindings?.find((binding) => binding.kind === 'scenario_input_fixture'),
    gold: bindings?.find((binding) => binding.kind === 'scenario_gold_criteria'),
  };
}

export function ScenarioFixtureSection({ id, bindings, scenarioId }: {
  id: string; bindings: PublicEvidenceBinding[] | undefined; scenarioId: string | undefined;
}) {
  const origin = useMirrorOrigin();
  const { input, gold } = scenarioFixtureBindings(bindings);
  const inputArtifact = useProofArtifact(input);
  const goldArtifact = useProofArtifact(gold);
  const inputURL = origin && inputArtifact ? `${origin}${inputArtifact.immutable_url}` : undefined;
  const goldURL = origin && goldArtifact ? `${origin}${goldArtifact.immutable_url}` : undefined;
  const inputState = useVerifiedArtifact(inputURL, input && scenarioId ? { sha256: input.sha256, scenarioId } : undefined, verifyScenarioInputArtifact);
  const goldState = useVerifiedArtifact(goldURL, gold && scenarioId ? { sha256: gold.sha256, scenarioId } : undefined, verifyScenarioGoldArtifact);

  if (!scenarioId || !input || !gold || !inputArtifact || !goldArtifact) return null;

  return (
    <PanelSection id={id} className="assignment-scenario-fixture" title="Scenario fixture">
      <p className="task-section-note">
        The exact scenario bytes the campaign froze, verified in this browser against their published SHA-256 and this
        assignment's scenario before display.
      </p>
      <div className="model-response-block">
        <div className="model-response-header"><span className="model-response-label">Input</span></div>
        <ArtifactProof binding={input} artifact={inputArtifact} url={inputURL} noun="input" />
        <Status state={inputState} noun="input" />
        {inputState.status === 'verified' ? <InputBody input={inputState.value} /> : null}
      </div>
      <div className="model-response-block">
        <div className="model-response-header"><span className="model-response-label">Gold criteria</span></div>
        <ArtifactProof binding={gold} artifact={goldArtifact} url={goldURL} noun="gold criteria" />
        <Status state={goldState} noun="gold criteria" />
        {goldState.status === 'verified' ? <GoldBody gold={goldState.value} /> : null}
      </div>
    </PanelSection>
  );
}
