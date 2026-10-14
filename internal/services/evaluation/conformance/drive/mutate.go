// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
package drive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/uuid"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// Mutator changes exactly one property of a valid envelope so that the case's
// fault is the first check to fail. Apply may call Rehash when the property is
// hashed and the fault is not the hash itself.
type Mutator struct {
	Apply func(ctx context.Context, env *commonv1.GovernanceEnvelope, a cases.Attempt) error
	// SetsL2 marks a mutator that writes governance.l2 itself (the L2 faults,
	// T18). Direct attaches no L2 proof for it.
	SetsL2 bool
}

// MutatorSet maps faults to mutators. Sets from different files are merged by NewDirect.
type MutatorSet map[cases.Fault]Mutator

// noop is the mutator for a baseline that already carries its case.
var noop = Mutator{Apply: func(context.Context, *commonv1.GovernanceEnvelope, cases.Attempt) error { return nil }}

// BaseMutators is the set for rows 1–15 and 17 that this task owns. Row 17 body
// faults are not envelope mutators; the DIRECT driver applies them to the body.
//
// The L4 check that each fault trips first (verifyPreDoctrine runs before the
// hash check, verifyTransactionHash before verifyStateful, verifyPosture last):
//   - payload_tampered: transaction hash mismatch (after L1 decodes and passes)
//   - id_mismatch: transaction id mismatch (transaction_hash still valid)
//   - expired: transaction expired (step 1, before nonce reservation)
//   - protocol_version: protocol version unsupported (verifyPreDoctrine)
//   - event_action_mismatch: event/action mismatch (verifyPreDoctrine, and the
//     gateway process path runs the same field check before L4)
//   - wrong_target: target session mismatch (verifyPreDoctrine, verifyTargetSession)
//   - stale_state_root: state root mismatch (verifyStateful)
//   - caller_posture: refused at the gateway handler (stampEnvelopePosture)
//   - requestor_mismatch: identity binding failed at the gateway handler
//     (verifyEnvelopeIdentityBinding), before L4
//   - ungoverned collection: document collection not governed (verifyPreDoctrine,
//     before doctrine, the hash, and L2/L3)
func BaseMutators() MutatorSet {
	return MutatorSet{
		cases.FaultNone:                 noop,
		cases.FaultReadBaseline:         noop,
		cases.FaultReplay:               noop,
		cases.FaultDoctrine:             noop,
		cases.FaultUngovernedCollection: noop,
		cases.FaultPayloadTampered: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			if len(env.Payload) == 0 {
				return fmt.Errorf("tamper payload: %w", constants.ErrTxPayloadMissing)
			}
			// Flip the low bit of the last byte: it stays a valid ASCII byte, so
			// the payload still decodes and doctrine still runs on it.
			env.Payload[len(env.Payload)-1] ^= 0x01
			return nil
		}},
		cases.FaultIDMismatch: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			sum := sha256.Sum256([]byte("conformance-id-mismatch|" + env.Id))
			env.Id = hex.EncodeToString(sum[:])
			return nil
		}},
		cases.FaultExpired: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			env.ExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
			return Rehash(env)
		}},
		cases.FaultProtocolVersion: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			env.ProtocolVersion = "0.0"
			return nil
		}},
		cases.FaultEventActionMismatch: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			env.EventType = string(constants.EventOperatorCommandRequested)
			return Rehash(env)
		}},
		cases.FaultWrongTarget: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			other, err := uuid.NewString()
			if err != nil {
				return fmt.Errorf("wrong target session: %w", err)
			}
			env.OperatorSessionId = other
			return Rehash(env)
		}},
		cases.FaultStaleStateRoot: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			env.StateMerkleRoot = strings.Repeat("0", 64)
			return Rehash(env)
		}},
		cases.FaultCallerPosture: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			env.Posture = string(config.PostureDoctrine)
			return nil
		}},
		cases.FaultRequestorMismatch: {Apply: func(_ context.Context, env *commonv1.GovernanceEnvelope, _ cases.Attempt) error {
			other, err := uuid.NewString()
			if err != nil {
				return fmt.Errorf("requestor mismatch: %w", err)
			}
			env.RequestorUserId = other
			return Rehash(env)
		}},
	}
}
