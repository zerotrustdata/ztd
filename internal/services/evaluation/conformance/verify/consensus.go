// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package verify

import (
	"fmt"
	"net/http"
	"net/url"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govhash "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// consensusClaims accounts for governed vote executions, including deliberation
// before a deliberately broken direct submission. Captured deliberations bind
// those votes to their original, valid subject; the fault can change that subject.
func (r *recordCheck) consensusClaims(runtime string, ev *evalv1.ConformanceCaseEvidence) ([]oracle.Outcome, error) {
	var calls []*operatorv1.ConsensusVoteInference
	for _, obs := range ev.GetObservations() {
		calls = append(calls, obs.GetConsensusInferences()...)
	}
	for _, exchange := range ev.GetExchanges() {
		endpoint, err := url.Parse(exchange.GetUrl())
		if err != nil || endpoint.Path != constants.APIPaths.GovernanceDeliberations || exchange.GetMethod() != http.MethodPost {
			continue
		}
		if exchange.GetStatus() != http.StatusOK {
			continue
		}
		var request commonv1.GovernanceEnvelope
		var result operatorv1.GovernanceDeliberationResult
		if err := protojson.Unmarshal(exchange.GetRequestBody(), &request); err != nil {
			return nil, fmt.Errorf("conformance: deliberation request: %w", constants.ErrConformanceReceiptUnclassifiable)
		}
		if err := protojson.Unmarshal(exchange.GetResponseBody(), &result); err != nil {
			return nil, fmt.Errorf("conformance: deliberation response: %w", constants.ErrConformanceReceiptUnclassifiable)
		}
		hash, err := govhash.GenerateMessageID(&request)
		if err != nil {
			return nil, err
		}
		signedHash, err := govhash.GenerateMessageID(result.GetEnvelope())
		if err != nil {
			return nil, err
		}
		if hash != request.GetTransactionHash() || signedHash != hash || result.GetEnvelope().GetId() != request.GetId() || result.GetEnvelope().GetTransactionHash() != hash {
			return nil, constants.ErrConformanceReceiptUnclassifiable
		}
		for _, call := range result.GetConsensusInferences() {
			if call.GetSubjectTransactionId() != request.GetId() || call.GetSubjectTransactionHash() != hash {
				return nil, constants.ErrConformanceReceiptUnclassifiable
			}
			calls = append(calls, call)
		}
	}
	var claims []oracle.Outcome
	seen := make(map[string]*operatorv1.ConsensusVoteInference)
	for _, call := range calls {
		if err := oracle.ValidateConsensusInference(call); err != nil {
			return nil, err
		}
		key := call.GetSubjectTransactionHash() + "/" + call.GetMemberAppId()
		if prior, exists := seen[key]; exists {
			if !proto.Equal(prior, call) {
				return nil, constants.ErrConformanceReceiptUnclassifiable
			}
			continue
		}
		seen[key] = call
		if receipt := call.GetReceipt(); receipt != nil {
			claimed, err := r.consensusReceipt(runtime, receipt, false)
			if err != nil {
				return nil, err
			}
			if claimed.Commitment != nil {
				claims = append(claims, claimed)
			}
		}
		for _, receipt := range call.GetObservationReceipts() {
			claimed, err := r.consensusReceipt(runtime, receipt, true)
			if err != nil {
				return nil, err
			}
			if claimed.Commitment != nil {
				claims = append(claims, claimed)
			}
		}
	}
	return claims, nil
}

func (r *recordCheck) consensusReceipt(bracketRuntime string, receipt *operatorv1.EventReceipt, observation bool) (oracle.Outcome, error) {
	if observation {
		switch constants.ActionType(receipt.GetActionType()) {
		case constants.ActionTypeProviderBoundaryObservation, constants.ActionTypeModelProvenanceObservation:
		default:
			return oracle.Outcome{}, constants.ErrConformanceReceiptUnclassifiable
		}
	}
	runtime, err := r.anchors.Runtime(receipt.GetSignerKeyId())
	if err != nil {
		return oracle.Outcome{}, err
	}
	outcome, err := classifyReceipt(r.anchors, runtime, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if observation && (outcome.Kind != oracle.OutcomeExecuted || outcome.Commitment == nil) {
		return oracle.Outcome{}, constants.ErrConformanceReceiptUnclassifiable
	}
	if runtime != bracketRuntime {
		return oracle.Outcome{}, nil
	}
	return outcome, nil
}
