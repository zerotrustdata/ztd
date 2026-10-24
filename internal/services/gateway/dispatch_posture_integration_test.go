// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govsvc "github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TestDispatch_MutationWithoutRequiredProofFailsClosedPerPosture proves that
// governed HTTP-style dispatch never synthesizes missing L2/L3 proof. A FILE_EDIT
// mutation carries neither proof, so under each posture that requires one it
// must fail closed and leave the target file untouched with no COMPLETED
// receipt. Under ratify and notary the Gateway rejects the mutation before any
// envelope is published (L3 is unmintable on the dispatch path). Under consensus
// the envelope is published with no L2 votes, the Operator's L4 warden rejects
// it with a signed FAILED receipt, and that receipt ends the dispatch as a typed
// rejection rather than a result timeout.
func TestDispatch_MutationWithoutRequiredProofFailsClosedPerPosture(t *testing.T) {
	const (
		seed   = "seed"
		marker = "posture-proof-marker"
	)

	tests := []struct {
		name    string
		posture constants.Posture
		// upFrontErr is the sentinel Dispatch returns before publishing, when the
		// Gateway rejects the mutation at envelope construction.
		upFrontErr error
		// l4Sentinel is the sentinel carried by the Operator's signed FAILED
		// receipt when the envelope reaches the L4 warden and is rejected there.
		l4Sentinel error
	}{
		{
			name:       "consensus_rejected_at_L4_for_missing_L2",
			posture:    constants.PostureConsensus,
			l4Sentinel: constants.ErrTxL2SignatureMissing,
		},
		{
			name:       "ratify_rejected_up_front_for_missing_L3",
			posture:    constants.PostureRatify,
			upFrontErr: constants.ErrTxL3ProofUnmintable,
		},
		{
			name:       "notary_rejected_up_front_for_missing_L3",
			posture:    constants.PostureNotary,
			upFrontErr: constants.ErrTxL3ProofUnmintable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const (
				userID      = "user-posture-proof"
				operatorID  = "operator-posture-proof"
				sessionID   = "session-posture-proof"
				executionID = "execution-posture-proof"
			)

			infra := setupTestInfrastructure(t, false)
			seedActiveUser(t, infra, userID)
			seedInferenceOperator(t, infra, userID, operatorID, sessionID, false)
			targetDir := testutil.OperatorDir(t)
			targetPath := filepath.Join(targetDir, constants.TestEvaluationTargetFilename)
			require.NoError(t, os.WriteFile(targetPath, []byte(seed), constants.PermFilePrivate))
			pubKey := startFileEditOperator(t, infra, operatorID, sessionID, targetDir)

			published := make(chan struct{}, 1)
			unregisterCmd := infra.Pubsub.RegisterHandler(pubsub.CmdChannel(operatorID, sessionID), func(_ string, _ []byte) {
				select {
				case published <- struct{}{}:
				default:
				}
			})
			t.Cleanup(unregisterCmd)

			receipts := make(chan *operatorv1.EventReceipt, 16)
			unregisterReceipt := infra.Pubsub.RegisterHandler(pubsub.ReceiptsChannel(operatorID, sessionID), func(_ string, data []byte) {
				envelope := &commonv1.GovernanceEnvelope{}
				if err := protojson.Unmarshal(data, envelope); err != nil {
					return
				}
				receipt := &operatorv1.EventReceipt{}
				if err := proto.Unmarshal(envelope.Payload, receipt); err == nil {
					receipts <- receipt
				}
			})
			t.Cleanup(unregisterReceipt)

			payload, err := proto.Marshal(&operatorv1.FileEditRequested{
				FilePath:    targetPath,
				Operation:   string(constants.FileOperationReplace),
				ExecutionId: executionID,
				OldContent:  seed,
				NewContent:  seed + "\n" + marker,
			})
			require.NoError(t, err)

			dispatchSvc := NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, tc.posture, govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil, nil)
			result, dispatchErr := dispatchSvc.Dispatch(t.Context(), DispatchRequest{
				TargetOperatorSessionID: sessionID,
				EventType:               string(constants.Event.Operator.FileEdit.Requested),
				Payload:                 payload,
				TargetResource:          targetPath,
				RequestorUserID:         userID,
				Timeout:                 10 * time.Second,
			})

			// (a) The dispatch must not succeed.
			assert.Nil(t, result, "mutation without required proof must not produce a result")
			require.Error(t, dispatchErr, "mutation without required proof must be rejected")

			if tc.upFrontErr != nil {
				// Rejected at envelope construction: nothing reaches the cmd channel.
				require.ErrorIs(t, dispatchErr, tc.upFrontErr)
				select {
				case <-published:
					t.Fatal("up-front rejected mutation reached the operator cmd channel")
				default:
				}
			}

			if tc.l4Sentinel != nil {
				// Published with no proof; the Operator's L4 warden rejects it and
				// its signed receipt ends the wait with the L4 sentinel's text.
				require.ErrorIs(t, dispatchErr, constants.ErrDispatchGovernanceRejected)
				require.NotErrorIs(t, dispatchErr, constants.ErrDispatchResultTimeout)
				assert.Contains(t, dispatchErr.Error(), tc.l4Sentinel.Error())
				status, _ := dispatchErrorCode(dispatchErr)
				assert.Equal(t, http.StatusForbidden, status)
				rejection := govsvc.RejectionResponse(dispatchErr)
				require.NotNil(t, rejection, "the rejection carries the Operator's signed receipt to the caller")
				assert.Equal(t, govsvc.ClassifyGovernanceRejection(tc.l4Sentinel), rejection.GetReceipt().GetRejectionCode())
				require.Eventually(t, func() bool {
					select {
					case <-published:
						return true
					default:
						return false
					}
				}, time.Second, 10*time.Millisecond, "envelope must reach the operator for L4 rejection")
			}

			// Collect the receipts emitted for this mutation.
			var failed *operatorv1.EventReceipt
			if tc.l4Sentinel != nil {
				timer := time.NewTimer(time.Second)
				defer timer.Stop()
				for failed == nil {
					select {
					case receipt := <-receipts:
						require.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.Status, "unproven mutation must not complete")
						if receipt.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
							failed = receipt
						}
					case <-timer.C:
						t.Fatal("signed FAILED receipt for unproven mutation was not published")
					}
				}
				assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, failed.FailureCode)
				assert.Contains(t, failed.ResultSummary, tc.l4Sentinel.Error(), "receipt must carry the missing-proof sentinel")
				require.NoError(t, govsvc.VerifyEventReceiptSignature(failed, pubKey))
				require.NoError(t, govsvc.VerifyReceiptPersistenceAttestation(failed, pubKey))
			}

			// (c) No COMPLETED receipt may have been emitted.
			drained := true
			for drained {
				select {
				case receipt := <-receipts:
					assert.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.Status, "unproven mutation must not complete")
				default:
					drained = false
				}
			}

			// (b) The target file must be unchanged.
			body, err := os.ReadFile(targetPath)
			require.NoError(t, err)
			assert.Equal(t, seed, string(body))
			assert.Equal(t, 0, strings.Count(string(body), marker))
		})
	}
}
