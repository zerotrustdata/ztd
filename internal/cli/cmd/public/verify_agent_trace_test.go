// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package public

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// sealedBracketOnlyTrace seals a trace whose Data runtime ledger holds only
// the two harness sentinels, linked to each other.
func sealedBracketOnlyTrace(t *testing.T) []byte {
	t.Helper()
	data, inference := oracletest.NewSigner(t), oracletest.NewSigner(t)
	sentinel := func(txID, prior, hash string) evaluation.AgentTraceReceipt {
		opts := oracletest.ReceiptOpts{
			TransactionID: txID, OperatorID: "operator-data", SessionID: "session-data", ActionType: string(constants.ActionTypeFsRead),
			L2: operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, L3: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
		}
		return evaluation.AgentTraceReceipt{ActionReceipt: data.Executed(t, opts, prior, hash)}
	}
	body, err := evaluation.SealAgentTrace(&evaluation.AgentTrace{
		RunID: "run-1", AssignmentID: "assignment-1", EvaluationAttemptID: "attempt-1",
		Inference: evaluation.AgentTraceRuntime{OperatorID: "operator-inference", SessionID: "session-inference", Anchor: &evaluation.AgentTraceAnchor{ConformanceAnchor: inference.Anchor("operator-inference")}},
		Data:      evaluation.AgentTraceRuntime{OperatorID: "operator-data", SessionID: "session-data", Anchor: &evaluation.AgentTraceAnchor{ConformanceAnchor: data.Anchor("operator-data")}},
		Input:     &evaluation.AgentTraceInput{Messages: []evaluation.AgentTraceMessage{{Role: "INFERENCE_MESSAGE_ROLE_USER", Text: "Diagnose the outage."}}},
		Bracket:   &evaluation.AgentTraceBracket{Before: sentinel("tx-before", "h-0", "h-1"), After: sentinel("tx-after", "h-1", "h-2")},
	})
	require.NoError(t, err)
	return body
}

func runVerifyAgentTrace(t *testing.T, body []byte) (string, error) {
	t.Helper()
	path := filepath.Join(testutil.TempDir(t), "assignment-1.agent-trace.json")
	require.NoError(t, os.WriteFile(path, body, constants.PermFilePrivate))
	cmd := publicVerifyAgentTraceCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--trace", path})
	err := cmd.Execute()
	return out.String(), err
}

func TestPublicVerifyAgentTraceCmd_VerifiesPublishedTrace(t *testing.T) {
	body := sealedBracketOnlyTrace(t)

	out, err := runVerifyAgentTrace(t, body)

	require.NoError(t, err)
	assert.Contains(t, out, "Agent trace verified (agent-trace.v4, sha256 "+models.SHA256Hex(body)+")")
}

func TestPublicVerifyAgentTraceCmd_RejectsEditedTrace(t *testing.T) {
	body := []byte(strings.Replace(string(sealedBracketOnlyTrace(t)), "Diagnose the outage.", "Diagnose the outages", 1))

	_, err := runVerifyAgentTrace(t, body)

	assert.ErrorIs(t, err, constants.ErrChecksumMismatch)
}
