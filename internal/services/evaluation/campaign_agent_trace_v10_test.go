// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// relabelV9 restamps trace as a v9 history trace without validating it, so a
// test can check how the validator treats v9 traces. SealAgentTrace refuses to
// produce v9 traces.
func relabelV9(t *testing.T, trace *AgentTrace) {
	t.Helper()
	trace.SchemaVersion = agentTraceSchemaVersionV9
	trace.EvidenceDigest = ""
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest
}

// agent-trace.v9 predates recorded content judgments. A v9 trace without them
// stays readable and verifiable through the decode entrypoint.
func TestAgentTraceV10_PreservesV9HistoryTrace(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	relabelV9(t, trace)

	body, err := MarshalCanonicalJSONObject(trace)
	require.NoError(t, err)
	decoded, err := DecodeAgentTrace(body)
	require.NoError(t, err)
	require.NoError(t, VerifyAgentTraceEvidence(decoded))
	assert.Equal(t, agentTraceSchemaVersionV9, decoded.SchemaVersion)
	assert.True(t, decoded.CarriesModelIdentity())
	assert.True(t, decoded.carriesObservationEvidence())
	assert.Empty(t, decoded.ContentJudgments)
}

// Content judgments are v10 evidence. A v9 trace that carries them is refused
// with ErrEvidenceArtifactMalformed, while the same judgments sealed under the
// current version validate.
func TestAgentTraceV10_RejectsV9ContentJudgments(t *testing.T) {
	judgments := []AgentContentJudgment{{CriterionID: "criterion-1", Met: true, Rationale: "judged"}}

	current, _ := v7EvidenceTrace(t)
	current.ContentJudgments = judgments
	relabelAgentTrace(t, current, agentTraceSchemaVersion)

	historical, _ := v7EvidenceTrace(t)
	historical.ContentJudgments = judgments
	relabelV9(t, historical)

	body, err := MarshalCanonicalJSONObject(historical)
	require.NoError(t, err)
	_, err = DecodeAgentTrace(body)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.ErrorIs(t, ValidateAgentTraceDigest(historical), constants.ErrEvidenceArtifactMalformed)
}
