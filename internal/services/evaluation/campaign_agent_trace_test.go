// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestAgentTrace_SealRoundTripsReceiptsAnchorAndBracketByteForByte(t *testing.T) {
	trace := v7EvidenceTraceWithToolCalls(t)
	// <, > and & are escaped by encoding/json inside nested canonical bytes; the
	// round trip must survive that.
	trace.ToolCalls[0].Receipt.ResultSummary = "2>&1 <out> & done"

	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	decoded := &AgentTrace{}
	require.NoError(t, json.Unmarshal(body, decoded))

	require.NotNil(t, decoded.ToolCalls[0].Receipt)
	assert.True(t, proto.Equal(trace.ToolCalls[0].Receipt.ActionReceipt, decoded.ToolCalls[0].Receipt.ActionReceipt))
	assert.True(t, proto.Equal(trace.Inference.Anchor.ConformanceAnchor, decoded.Inference.Anchor.ConformanceAnchor))
	assert.True(t, proto.Equal(trace.Data.Anchor.ConformanceAnchor, decoded.Data.Anchor.ConformanceAnchor))
	assert.True(t, proto.Equal(trace.ModelCalls[0].Receipt.ActionReceipt, decoded.ModelCalls[0].Receipt.ActionReceipt))
	require.NotNil(t, decoded.Bracket)
	assert.True(t, proto.Equal(trace.Bracket.After.ActionReceipt, decoded.Bracket.After.ActionReceipt))

	again, err := SealAgentTrace(decoded)
	require.NoError(t, err)
	assert.Equal(t, body, again)
}

func TestAgentTrace_ReceiptDecodeRejectsUnknownField(t *testing.T) {
	receipt := &AgentTraceReceipt{}

	err := receipt.UnmarshalJSON([]byte(`{"transaction_id":"tx-1","not_a_receipt_field":"x"}`))

	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestAgentTrace_ReceiptDecodeRejectsNonCanonicalSpelling(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	canonical, err := evalv1.MarshalCanonical(trace.ToolCalls[0].Receipt.ActionReceipt)
	require.NoError(t, err)
	// Same value, different bytes: protojson accepts it, the trace does not.
	spaced := strings.Replace(string(canonical), `":"`, `": "`, 1)
	require.NotEqual(t, string(canonical), spaced)

	err = (&AgentTraceReceipt{}).UnmarshalJSON([]byte(spaced))

	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestAgentTrace_ReceiptMarshalRejectsAbsentReceipt(t *testing.T) {
	_, err := AgentTraceReceipt{}.MarshalJSON()

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestAgentTrace_SealRejectsBracketWithAbsentSentinelReceipt(t *testing.T) {
	trace := v7EvidenceTraceWithToolCalls(t)
	trace.Bracket.Before = AgentTraceReceipt{}

	_, err := SealAgentTrace(trace)

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestAgentTrace_SealedBodyCarriesReceiptsAsCanonicalProtojson(t *testing.T) {
	trace := v7EvidenceTraceWithToolCalls(t)
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)

	var raw struct {
		ToolCalls []struct {
			Receipt json.RawMessage `json:"receipt"`
		} `json:"tool_calls"`
	}
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Len(t, raw.ToolCalls, 2)
	want, err := evalv1.MarshalCanonical(trace.ToolCalls[0].Receipt.ActionReceipt)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(raw.ToolCalls[0].Receipt))
	assert.Equal(t, agentTraceSchemaVersion, trace.SchemaVersion)
}

func TestAgentTrace_SealRejectsMissingInput(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	trace.Input = nil

	_, err := SealAgentTrace(trace)

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestAgentTrace_SealRefusesToRestampEarlierVersion(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.SchemaVersion = agentTraceSchemaVersionV3

	_, err := SealAgentTrace(trace)

	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestAgentTrace_V3TraceValidatesOnlyWithoutInput(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.Input = nil
	trace.SchemaVersion = agentTraceSchemaVersionV3
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest

	require.NoError(t, ValidateAgentTraceDigest(trace))

	trace.Input = testAgentTraceInput()
	trace.EvidenceDigest, err = ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	assert.ErrorIs(t, ValidateAgentTraceDigest(trace), constants.ErrEvidenceArtifactMalformed, "a v3 trace never claims a captured input")
}

func TestAgentTrace_V4TraceRequiresInput(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	_, err := SealAgentTrace(trace)
	require.NoError(t, err)
	trace.Input = nil
	trace.EvidenceDigest, err = ComputeAgentTraceDigest(trace)
	require.NoError(t, err)

	assert.ErrorIs(t, ValidateAgentTraceDigest(trace), constants.ErrMissingRequiredField)
}

func TestAgentTrace_V5TraceVerifiesWithoutInvestigationMetadata(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.SchemaVersion = agentTraceSchemaVersionV5
	trace.InvestigationID = ""
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest
	before, err := MarshalCanonicalJSONObject(trace)
	require.NoError(t, err)
	decoded, err := DecodeAgentTrace(before)
	require.NoError(t, err)
	require.NoError(t, VerifyAgentTraceEvidence(decoded))
	after, err := MarshalCanonicalJSONObject(decoded)
	require.NoError(t, err)
	assert.Equal(t, before, after, "old evidence must retain its exact digest and schema")
	_, err = SealAgentTrace(decoded)
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestDecodeAgentTrace_PublishedBodyDecodesAndVerifies(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)

	decoded, err := DecodeAgentTrace(body)

	require.NoError(t, err)
	assert.Equal(t, trace.EvidenceDigest, decoded.EvidenceDigest)
	require.NoError(t, VerifyAgentTraceEvidence(decoded))
}

func TestDecodeAgentTrace_RejectsTamperedAndNonCanonicalBodies(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)

	tampered := []byte(strings.Replace(string(body), "Why is checkout failing?", "Why is checkout passing?", 1))
	_, err = DecodeAgentTrace(tampered)
	assert.ErrorIs(t, err, constants.ErrChecksumMismatch)

	_, err = DecodeAgentTrace(append(append([]byte{}, body...), '\n'))
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

// TestAgentTrace_DigestPreimageIsBodyWithDigestEmptied pins the property the
// public explorer verifies evidence_digest by: the trace encodes its fields
// in fixed order with schema_version then evidence_digest first, so the
// digest preimage is the sealed body with only the digest value emptied.
func TestAgentTrace_DigestPreimageIsBodyWithDigestEmptied(t *testing.T) {
	trace := v7EvidenceTraceWithToolCalls(t)
	trace.ToolCalls[0].ResultJSON = `{"stdout":"2>&1 <out> & done  "}`
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)

	prefix := `{"schema_version":"` + agentTraceSchemaVersion + `","evidence_digest":"` + trace.EvidenceDigest + `",`
	require.True(t, strings.HasPrefix(string(body), prefix))
	preimage := `{"schema_version":"` + agentTraceSchemaVersion + `","evidence_digest":"",` + strings.TrimPrefix(string(body), prefix)
	assert.Equal(t, trace.EvidenceDigest, hashBytes([]byte(preimage)))
}
