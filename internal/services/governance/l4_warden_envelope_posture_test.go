// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestVerifyEnvelope_ReadsPostureFromEnvelope verifies that the warden reads
// posture per-envelope from GovernanceEnvelope.Posture rather than from any
// constructor-supplied value. Under notary posture, a mutation requires L3
// proof; under doctrine posture, the same mutation passes without L3. The
// warden is constructed identically in both cases — only the envelope's
// Posture field differs, proving the envelope is the authoritative source.
func TestVerifyEnvelope_ReadsPostureFromEnvelope(t *testing.T) {
	t.Parallel()

	t.Run("notary posture requires L3 for mutation", func(t *testing.T) {
		t.Parallel()
		verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "notary")
		env.Governance.L3 = nil

		_, err := verifier.VerifyEnvelope(context.Background(), env)
		assert.ErrorIs(t, err, constants.ErrTxL3ProofMissing)
	})

	t.Run("doctrine posture does not require L3 for mutation", func(t *testing.T) {
		t.Parallel()
		verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
		env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "doctrine")
		env.Governance.L3 = nil

		verified, err := verifier.VerifyEnvelope(context.Background(), env)
		require.NoError(t, err)
		assert.NotNil(t, verified)
		assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, verified.L3Status)
		assert.Equal(t, constants.PostureDoctrine, verified.Posture)
	})
}

// TestVerifyEnvelope_ReadOnlyNeedsNoL3UnderAnyPosture verifies the read-only
// exception of INV-GOV-LAY-04: with the L3 proof removed, a read-only action
// still passes under ratify and notary, where L3 gates mutations.
func TestVerifyEnvelope_ReadOnlyNeedsNoL3UnderAnyPosture(t *testing.T) {
	t.Parallel()
	require.False(t, constants.ActionTypeFsRead.IsMutation())

	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			t.Parallel()
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			env := signedEnvelope(t, constants.ActionTypeFsRead, typedPayload(t, constants.ActionTypeFsRead), privKey, posture)
			env.Governance.L3 = nil

			verified, err := verifier.VerifyEnvelope(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, verified.L3Status)
			assert.Equal(t, posture, verified.Posture)
		})
	}
}

// TestVerifyEnvelope_MissingEnvelopePostureFailsClosed verifies that an
// envelope with an empty Posture field is rejected with
// ErrEnvelopePostureMissing. This indicates a gateway bug (the constructor
// was called without wiring cfg.Gateway.Posture), not an operator config
// gap. The warden fails closed per-transaction rather than inventing a posture.
func TestVerifyEnvelope_RatifyRequiresL3WithoutL2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		invalidL2 bool
		removeL3  bool
		wantErr   error
	}{
		{name: "valid L3 without L2 passes"},
		{name: "valid L3 with invalid audited L2 passes", invalidL2: true},
		{name: "missing L3 rejects mutation", removeL3: true, wantErr: constants.ErrTxL3ProofMissing},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "ratify")
			if tc.invalidL2 {
				env.Governance.L2.Votes = append(env.Governance.L2.Votes, env.Governance.L2.Votes[0])
			} else {
				env.Governance.L2 = nil
			}
			if tc.removeL3 {
				env.Governance.L3 = nil
			}

			verified, err := verifier.VerifyEnvelope(context.Background(), env)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, verified)
			assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, verified.L2Status)
			assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, verified.L3Status)
			assert.Equal(t, constants.PostureRatify, verified.Posture)
		})
	}
}

func TestVerifyEnvelope_MissingEnvelopePostureFailsClosed(t *testing.T) {
	t.Parallel()

	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "")
	env.Posture = ""

	_, err := verifier.VerifyEnvelope(context.Background(), env)
	assert.ErrorIs(t, err, constants.ErrEnvelopePostureMissing)
}

// TestVerifyEnvelope_EnvelopePostureIsTheOnlyPosture verifies that the
// envelope-derived posture is the sole authority for L2/L3 gating. The
// warden constructor no longer accepts a posture parameter, so there is no
// config-derived posture to conflict with. This test proves the envelope
// value flows through to VerifiedTransaction.Posture unchanged.
func TestVerifyEnvelope_EnvelopePostureIsTheOnlyPosture(t *testing.T) {
	t.Parallel()

	for _, posture := range []constants.Posture{constants.PostureDoctrine, constants.PostureConsensus, constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			t.Parallel()
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, posture)

			verified, err := verifier.VerifyEnvelope(context.Background(), env)
			require.NoError(t, err)
			assert.NotNil(t, verified)
			assert.Equal(t, posture, verified.Posture)
		})
	}
}

// TestVerifyEnvelope_InvalidEnvelopePostureFailsClosed verifies that an
// envelope with an unrecognized posture string is rejected. The warden
// parses the posture via constants.ParsePosture, which fails closed on
// unknown values.
func TestVerifyEnvelope_InvalidEnvelopePostureFailsClosed(t *testing.T) {
	t.Parallel()

	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeFsList, typedPayload(t, constants.ActionTypeFsList), privKey, "bogus-posture")

	_, err := verifier.VerifyEnvelope(context.Background(), env)
	require.Error(t, err)
}

// TestPostureGates_L2EnforcedAndL3RequiredByActionClass pins the two posture
// gates that L4 and every Gateway deliberation or refusal site share. L2 is
// enforced under a posture that requires it, but only for state changes and
// never for bootstrap actions. L3 is required under a posture that requires
// it, but only for mutations. Each
// posture is crossed with a mutation, a read, a non-mutating bootstrap action,
// and a mutating bootstrap action, so each exemption is checked on its own.
func TestPostureGates_L2EnforcedAndL3RequiredByActionClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		posture constants.Posture
		action  constants.ActionType
		wantL2  bool
		wantL3  bool
	}{
		{"doctrine mutation", constants.PostureDoctrine, constants.ActionTypeFileEdit, false, false},
		{"doctrine read", constants.PostureDoctrine, constants.ActionTypeFsRead, false, false},
		{"doctrine bootstrap", constants.PostureDoctrine, constants.ActionTypePlatformEnrollmentCreate, false, false},
		{"doctrine bootstrap mutation", constants.PostureDoctrine, constants.ActionTypePlatformEnrollmentDecide, false, false},

		{"consensus mutation", constants.PostureConsensus, constants.ActionTypeFileEdit, true, false},
		{"consensus read", constants.PostureConsensus, constants.ActionTypeFsRead, false, false},
		{"consensus bootstrap", constants.PostureConsensus, constants.ActionTypePlatformEnrollmentCreate, false, false},
		{"consensus bootstrap mutation", constants.PostureConsensus, constants.ActionTypePlatformEnrollmentDecide, false, false},

		{"ratify mutation", constants.PostureRatify, constants.ActionTypeFileEdit, false, true},
		{"ratify read", constants.PostureRatify, constants.ActionTypeFsRead, false, false},
		{"ratify bootstrap", constants.PostureRatify, constants.ActionTypePlatformEnrollmentCreate, false, false},
		{"ratify bootstrap mutation", constants.PostureRatify, constants.ActionTypePlatformEnrollmentDecide, false, true},

		{"notary mutation", constants.PostureNotary, constants.ActionTypeFileEdit, true, true},
		{"notary read", constants.PostureNotary, constants.ActionTypeFsRead, false, false},
		{"notary bootstrap", constants.PostureNotary, constants.ActionTypePlatformEnrollmentCreate, false, false},
		{"notary bootstrap mutation", constants.PostureNotary, constants.ActionTypePlatformEnrollmentDecide, false, true},

		// Follow-on actions carry out a committed DECIDE, so they are exempt from
		// L3 under every posture, and they are bootstrap actions, so exempt from L2.
		{"ratify issue authorized by decision", constants.PostureRatify, constants.ActionTypePlatformEnrollmentIssue, false, false},
		{"ratify persist policy authorized by decision", constants.PostureRatify, constants.ActionTypePlatformEnrollmentPersistPolicy, false, false},
		{"ratify create session authorized by decision", constants.PostureRatify, constants.ActionTypePlatformEnrollmentCreateSession, false, false},
		{"notary issue authorized by decision", constants.PostureNotary, constants.ActionTypePlatformEnrollmentIssue, false, false},
		{"notary persist policy authorized by decision", constants.PostureNotary, constants.ActionTypePlatformEnrollmentPersistPolicy, false, false},
		{"notary create session authorized by decision", constants.PostureNotary, constants.ActionTypePlatformEnrollmentCreateSession, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := &govtypes.GovernanceEnvelope{ActionType: string(tc.action)}
			assert.Equal(t, tc.wantL2, L2Enforced(envelope, tc.posture), "L2Enforced")
			assert.Equal(t, tc.wantL3, L3Required(tc.action, tc.posture), "L3Required")
		})
	}
}

// TestL2Enforced_ScopesTribunalToStateChanges pins the Tribunal scope: under
// an L2 posture only a state change gets a vote round. Inference, typed
// provider reads, the harness model release and read-only MCP tools do not.
// An MCP call to a tool the registry does not list as read-only, including an
// empty or unknown target, is a state change, so the gate fails closed.
func TestL2Enforced_ScopesTribunalToStateChanges(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		action constants.ActionType
		target string
		wantL2 bool
	}{
		{"inference", constants.ActionTypeInference, "", false},
		{"model residency read", constants.ActionTypeOllamaModelResidency, "", false},
		{"model inventory read", constants.ActionTypeOllamaModelInventory, "", false},
		{"harness model release", constants.ActionTypeOllamaModelRelease, "", false},
		{"read_file tool", constants.ActionTypeMcpCall, constants.MCPToolReadFile, false},
		{"search_directory tool", constants.ActionTypeMcpCall, constants.MCPToolSearchDirectory, false},
		{"git_ops tool", constants.ActionTypeMcpCall, constants.MCPToolGitOps, false},
		{"run_shell_command tool", constants.ActionTypeMcpCall, constants.MCPToolRunShellCommand, true},
		{"unknown tool", constants.ActionTypeMcpCall, "proc_signal_safe", true},
		{"empty tool", constants.ActionTypeMcpCall, "", true},
		{"read-only tool name on another action", constants.ActionTypeExecuteBash, constants.MCPToolReadFile, true},
		{"bash", constants.ActionTypeExecuteBash, "", true},
		{"file edit", constants.ActionTypeFileEdit, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := &govtypes.GovernanceEnvelope{ActionType: string(tc.action), TargetResource: tc.target}
			for _, posture := range []constants.Posture{constants.PostureConsensus, constants.PostureNotary} {
				assert.Equal(t, tc.wantL2, L2Enforced(envelope, posture), posture)
			}
			assert.False(t, L2Enforced(envelope, constants.PostureDoctrine), "doctrine never enforces L2")
			assert.False(t, L2Enforced(envelope, constants.PostureRatify), "ratify never enforces L2")
		})
	}
}
