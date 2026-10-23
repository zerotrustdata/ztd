// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
package drive

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// testAttempt is a DIRECT attempt for one fault and action with fixed paths.
func testAttempt(fault cases.Fault, action cases.Action) cases.Attempt {
	return cases.Attempt{
		RunID:     "run-1",
		AttemptID: "attempt-1",
		Case: cases.Case{
			ID:      "row-test-" + string(fault),
			Ingress: cases.IngressDirect,
			Fault:   fault,
			Action:  action,
			Command: "echo conformance",
		},
		TargetAbs: "/work/target.txt",
		ReadAbs:   "/work/read.txt",
		Marker:    cases.Marker("run-1", "attempt-1"),
	}
}

// testIdentity is the embedded Operator session and the CLI persona that owns it.
func testIdentity() Identity {
	return Identity{
		Persona: client.Persona{ID: "conformance", UserID: "user-1", CLISessionID: "cli-session-1"},
		Target: operator.Target{
			OperatorID:        string(constants.DocIDEmbeddedOperator),
			OperatorSessionID: "operator-session-1",
		},
	}
}

func TestBaselineHashesAndReverifiesForEachKind(t *testing.T) {
	tests := []struct {
		name    string
		attempt cases.Attempt
		action  constants.ActionType
		event   constants.EventType
	}{
		{"file edit mutation", testAttempt(cases.FaultNone, cases.ActionMutation), constants.ActionTypeFileEdit, constants.EventOperatorFileEditRequested},
		{"fs read", testAttempt(cases.FaultReadBaseline, cases.ActionRead), constants.ActionTypeFsRead, constants.EventOperatorFilesystemReadRequested},
		{"doctrine command", testAttempt(cases.FaultDoctrine, cases.ActionMutation), constants.ActionTypeExecuteBash, constants.EventOperatorCommandRequested},
		{"ungoverned collection", testAttempt(cases.FaultUngovernedCollection, cases.ActionMutation), constants.ActionTypeDocumentUpdate, constants.EventAppDocumentUpdateRequested},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := Baseline(tt.attempt, testIdentity(), "root-1", time.Now())
			require.NoError(t, err)

			assert.Equal(t, string(tt.action), env.ActionType)
			assert.Equal(t, string(tt.event), env.EventType)
			assert.Equal(t, client.ProtocolVersion, env.ProtocolVersion)
			assert.Equal(t, "root-1", env.StateMerkleRoot)
			assert.Equal(t, "operator-session-1", env.OperatorSessionId)
			assert.Equal(t, "cli-session-1", env.CliSessionId)
			assert.Equal(t, "user-1", env.RequestorUserId)
			assert.NotEmpty(t, env.Nonce)
			assert.NotEmpty(t, env.Payload)

			require.NoError(t, governance.VerifyTransactionHash(env, env.TransactionHash))
			assert.Equal(t, env.TransactionHash, env.Id)
		})
	}
}

func TestBaselineCarriesNoPostureOrGovernance(t *testing.T) {
	env, err := Baseline(testAttempt(cases.FaultNone, cases.ActionMutation), testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	assert.Empty(t, env.Posture)
	assert.Nil(t, env.Governance)
}

func TestBaselineExpiresFiveMinutesAfterNow(t *testing.T) {
	now := time.Now()
	env, err := Baseline(testAttempt(cases.FaultNone, cases.ActionMutation), testIdentity(), "root-1", now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(baselineTTL).Unix(), env.ExpiresAt.AsTime().Unix())
}

func TestBaselineNonceIsFreshPerAttempt(t *testing.T) {
	a := testAttempt(cases.FaultNone, cases.ActionMutation)
	first, err := Baseline(a, testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	second, err := Baseline(a, testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	assert.NotEqual(t, first.Nonce, second.Nonce)
}

func TestUngovernedCollectionIsNotGoverned(t *testing.T) {
	_, _, msg, err := Payload(testAttempt(cases.FaultUngovernedCollection, cases.ActionMutation))
	require.NoError(t, err)
	doc, ok := msg.(*operatorv1.DocumentUpdateRequested)
	require.True(t, ok)
	assert.Equal(t, ungovernedCollection, doc.GetCollection())
	assert.False(t, constants.CollectionName(doc.GetCollection()).IsGovernedDocument())
}

func TestPayloadRejectsActionNotExpressible(t *testing.T) {
	a := testAttempt(cases.FaultNone, cases.Action("unknown"))
	_, _, _, err := Payload(a)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)
}

func TestPayloadDoctrineRequiresCommand(t *testing.T) {
	a := testAttempt(cases.FaultDoctrine, cases.ActionMutation)
	a.Case.Command = ""
	_, _, _, err := Payload(a)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestRehashMatchesGenerateMessageID(t *testing.T) {
	env, err := Baseline(testAttempt(cases.FaultNone, cases.ActionMutation), testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	want, err := governance.GenerateMessageID(env)
	require.NoError(t, err)
	assert.Equal(t, want, env.Id)

	env.Nonce = "changed"
	require.NoError(t, Rehash(env))
	want, err = governance.GenerateMessageID(env)
	require.NoError(t, err)
	assert.Equal(t, want, env.TransactionHash)
	assert.Equal(t, want, env.Id)
}

func TestRehashRejectsUnhashableEnvelope(t *testing.T) {
	env := &commonv1.GovernanceEnvelope{ProtocolVersion: "0.0"}
	err := Rehash(env)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrTxProtocolVersionUnsupported)
}

func TestBaselinePayloadDecodesToItsAttemptMessage(t *testing.T) {
	a := testAttempt(cases.FaultNone, cases.ActionMutation)
	env, err := Baseline(a, testIdentity(), "root-1", time.Now())
	require.NoError(t, err)
	_, _, want, err := Payload(a)
	require.NoError(t, err)
	got := &operatorv1.FileEditRequested{}
	require.NoError(t, proto.Unmarshal(env.Payload, got))
	assert.True(t, proto.Equal(want, got))
	assert.Equal(t, a.TargetAbs, got.GetFilePath())
	assert.Equal(t, a.Marker, got.GetContent())
}
