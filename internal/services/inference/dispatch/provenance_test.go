// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package dispatch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type recordingProvenanceNotifier struct {
	commands    []*evalv1.ModelProvenanceObservationCommand
	beginErr    error
	finalizeErr error
}

func (n *recordingProvenanceNotifier) NotifyAttemptBegin(_ context.Context, _, attemptID, model, digest, registryDigest, campaignID string, startedAt int64, retry uint32) (*operatorv1.EventReceipt, error) {
	n.commands = append(n.commands, &evalv1.ModelProvenanceObservationCommand{
		ProviderAttemptId: attemptID, ServedModelTag: model, ExpectedModelDigest: digest,
		ModelRegistryDigest: registryDigest, CampaignId: campaignID, RetryCount: retry,
		AttemptStartedAtUnixMs: startedAt, Phase: evalv1.ModelProvenanceObservationPhase_MODEL_PROVENANCE_OBSERVATION_PHASE_BEGIN,
	})
	if n.beginErr != nil {
		return nil, n.beginErr
	}
	return &operatorv1.EventReceipt{TransactionId: "provenance-begin"}, nil
}

func (n *recordingProvenanceNotifier) NotifyAttemptFinalize(_ context.Context, _, attemptID, transactionID, model, digest, registryDigest, campaignID string, startedAt, completedAt int64, _ bool, retry uint32) (*operatorv1.EventReceipt, error) {
	n.commands = append(n.commands, &evalv1.ModelProvenanceObservationCommand{
		ProviderAttemptId: attemptID, InferenceTransactionId: transactionID, ServedModelTag: model, ExpectedModelDigest: digest,
		ModelRegistryDigest: registryDigest, CampaignId: campaignID, RetryCount: retry,
		AttemptStartedAtUnixMs: startedAt, AttemptCompletedAtUnixMs: completedAt,
		Phase: evalv1.ModelProvenanceObservationPhase_MODEL_PROVENANCE_OBSERVATION_PHASE_FINALIZE,
	})
	if n.finalizeErr != nil {
		return nil, n.finalizeErr
	}
	return &operatorv1.EventReceipt{TransactionId: "provenance-finalize"}, nil
}

func TestDispatchInference_PerAttemptProvenanceWithoutPreflight(t *testing.T) {
	for _, phase := range []string{"completed", "begin undelivered", "finalize undelivered"} {
		t.Run(phase, func(t *testing.T) {
			req := baseRequest()
			configureCampaignRequest(t, &req)
			dispatcher := &stubCommandDispatcher{result: successDispatchResultFor(t, req)}
			notifier := &recordingProvenanceNotifier{}
			switch phase {
			case "begin undelivered":
				notifier.beginErr = constants.ErrDispatchNoDelivery
			case "finalize undelivered":
				notifier.finalizeErr = constants.ErrDispatchNoDelivery
			}
			svc := NewDispatchService(dispatcher, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{capableOp("sess-inf")}}, testLogger())
			svc.SetProvenanceObservationNotifier(notifier)
			_, err := svc.DispatchInference(t.Context(), req)
			if phase == "completed" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, constants.ErrDispatchNoDelivery)
			}
			if phase == "begin undelivered" {
				require.Zero(t, dispatcher.calls, "undeliverable provenance must stop inference")
				require.Len(t, notifier.commands, 1)
			} else {
				require.Equal(t, 1, dispatcher.calls)
				require.Len(t, notifier.commands, 2)
				require.Equal(t, "tx-1", notifier.commands[1].GetInferenceTransactionId())
				require.Equal(t, evalv1.ModelProvenanceObservationPhase_MODEL_PROVENANCE_OBSERVATION_PHASE_FINALIZE, notifier.commands[1].GetPhase())
			}
			require.Equal(t, evalv1.ModelProvenanceObservationPhase_MODEL_PROVENANCE_OBSERVATION_PHASE_BEGIN, notifier.commands[0].GetPhase())
			for _, command := range notifier.commands {
				require.Equal(t, req.ProviderAttemptID, command.GetProviderAttemptId())
				require.Equal(t, req.Model, command.GetServedModelTag())
				require.Equal(t, req.ModelDigest, command.GetExpectedModelDigest())
				require.Equal(t, req.ModelRegistryDigest, command.GetModelRegistryDigest())
				require.Equal(t, req.CampaignID, command.GetCampaignId())
			}
		})
	}
}

// Provenance is optional: with no provenance Operator connected, campaign and
// non-campaign attempts both reach the provider unattested, and no FINALIZE is
// sent for a window that never opened.
func TestDispatchInference_ZeroProvenanceOperatorProceedsUnattested(t *testing.T) {
	notFound := fmt.Errorf("provenance observation: %w", constants.ErrProvenanceOperatorNotFound)
	lister := &stubOperatorLister{ops: []*operatorv1.OperatorDocument{capableOp("sess-inf")}}

	for _, campaign := range []bool{true, false} {
		t.Run(fmt.Sprintf("campaign=%t", campaign), func(t *testing.T) {
			req := baseRequest()
			if campaign {
				configureCampaignRequest(t, &req)
			} else {
				req.ModelDigest = strings.Repeat("a", 64)
			}
			dispatcher := &stubCommandDispatcher{result: successDispatchResultFor(t, req)}
			svc := NewDispatchService(dispatcher, lister, testLogger())
			notifier := &recordingProvenanceNotifier{beginErr: notFound}
			svc.SetProvenanceObservationNotifier(notifier)

			result, err := svc.DispatchInference(t.Context(), req)
			require.NoError(t, err)
			require.Equal(t, 1, dispatcher.calls)
			require.Len(t, notifier.commands, 1)
			require.Equal(t, evalv1.ModelProvenanceObservationPhase_MODEL_PROVENANCE_OBSERVATION_PHASE_BEGIN, notifier.commands[0].GetPhase())
			require.Empty(t, result.Response().GetObservationEventReceipts())
		})
	}

	// A provenance Operator that is connected but fails FINALIZE is not an absent
	// one: the failure surfaces for a non-campaign attempt too.
	t.Run("non-campaign finalize failure surfaces", func(t *testing.T) {
		req := baseRequest()
		req.ModelDigest = strings.Repeat("a", 64)
		dispatcher := &stubCommandDispatcher{result: successDispatchResultFor(t, req)}
		svc := NewDispatchService(dispatcher, lister, testLogger())
		svc.SetProvenanceObservationNotifier(&recordingProvenanceNotifier{finalizeErr: constants.ErrDispatchNoDelivery})

		_, err := svc.DispatchInference(t.Context(), req)
		require.ErrorIs(t, err, constants.ErrDispatchNoDelivery)
		require.Equal(t, 1, dispatcher.calls)
	})
}
