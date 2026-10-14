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
	trace, _ := validEvidenceTrace(t)
	// <, > and & are escaped by encoding/json inside nested canonical bytes; the
	// round trip must survive that.
	trace.ToolCalls[0].Receipt.ActionReceipt.ResultSummary = "2>&1 <out> & done"

	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	decoded := &AgentTrace{}
	require.NoError(t, json.Unmarshal(body, decoded))

	require.NotNil(t, decoded.ToolCalls[0].Receipt)
	assert.True(t, proto.Equal(trace.ToolCalls[0].Receipt.ActionReceipt, decoded.ToolCalls[0].Receipt.ActionReceipt))
	assert.True(t, proto.Equal(trace.DataOperatorAnchor.ConformanceAnchor, decoded.DataOperatorAnchor.ConformanceAnchor))
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
	trace, _ := validEvidenceTrace(t)
	trace.Bracket.Before = AgentTraceReceipt{}

	_, err := SealAgentTrace(trace)

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestAgentTrace_SealedBodyCarriesReceiptsAsCanonicalProtojson(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
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
