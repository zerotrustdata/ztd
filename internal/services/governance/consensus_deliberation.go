// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ConsensusDeliberation keeps admission proofs and the governed executions
// that produced them together. Inferences includes attempts that cast no vote.
type ConsensusDeliberation struct {
	Envelope   []byte
	Inferences []*operatorv1.ConsensusVoteInference
}

// ConsensusInferenceError preserves executions when subject admission fails.
type ConsensusInferenceError struct {
	Inferences []*operatorv1.ConsensusVoteInference
	Err        error
}

func (e *ConsensusInferenceError) Error() string { return e.Err.Error() }
func (e *ConsensusInferenceError) Unwrap() error { return e.Err }

// WithConsensusInferences carries vote executions through a failed dispatch.
func WithConsensusInferences(inferences []*operatorv1.ConsensusVoteInference, err error) error {
	if err == nil || len(inferences) == 0 {
		return err
	}
	return &ConsensusInferenceError{Inferences: inferences, Err: err}
}

// ConsensusInferences returns the vote executions retained by a failed dispatch.
func ConsensusInferences(err error) []*operatorv1.ConsensusVoteInference {
	var carried *ConsensusInferenceError
	if errors.As(err, &carried) {
		return carried.Inferences
	}
	return nil
}

// ConsensusVoteAttemptID binds one member's provider attempt to its subject
// hash and retry ordinal. The first attempt (retryCount 0) is
// "l2-<hash prefix>-<member>"; the nth retry of a transient provider failure
// appends "-r<n>". A canonical member name never ends in "-r<digits>", so no
// two attempts of any member or subject share an ID.
func ConsensusVoteAttemptID(txHash, memberAppID string, retryCount uint32) string {
	prefix := txHash
	if len(prefix) > 32 {
		prefix = prefix[:32]
	}
	id := "l2-" + prefix + "-" + strings.TrimPrefix(memberAppID, constants.TribunalConsensusAppIDPrefix)
	if retryCount == 0 {
		return id
	}
	return id + "-r" + strconv.FormatUint(uint64(retryCount), 10)
}

// ConsensusVoteAttempts returns every provider attempt behind one member's
// vote record in execution order: the superseded attempts (retry_count
// 0..n-1), then vote itself (retry_count n). It is the evidence walk: a caller
// that verifies receipts, claims ledger commitments or collects observation
// receipts visits every attempt, while a caller that reads the vote reads only
// vote, which is the one record that casts it.
func ConsensusVoteAttempts(vote *operatorv1.ConsensusVoteInference) []*operatorv1.ConsensusVoteInference {
	if vote == nil {
		return nil
	}
	attempts := make([]*operatorv1.ConsensusVoteInference, 0, len(vote.GetSupersededAttempts())+1)
	attempts = append(attempts, vote.GetSupersededAttempts()...)
	return append(attempts, vote)
}

// ConsensusInference encodes the protobuf evidence canonically inside JSON responses.
type ConsensusInference struct {
	*operatorv1.ConsensusVoteInference
}

func (c ConsensusInference) MarshalJSON() ([]byte, error) {
	if c.ConsensusVoteInference == nil {
		return nil, fmt.Errorf("consensus inference: %w", constants.ErrMissingRequiredField)
	}
	return response.MarshalProtoJSONCanonical(c.ConsensusVoteInference)
}
func (c *ConsensusInference) UnmarshalJSON(b []byte) error {
	inference := &operatorv1.ConsensusVoteInference{}
	if err := protojson.Unmarshal(b, inference); err != nil {
		return fmt.Errorf("consensus inference: %w", err)
	}
	c.ConsensusVoteInference = inference
	return nil
}

// WrapConsensusInferences preserves the typed protobuf evidence in JSON-owned contracts.
func WrapConsensusInferences(inferences []*operatorv1.ConsensusVoteInference) []ConsensusInference {
	if len(inferences) == 0 {
		return nil
	}
	out := make([]ConsensusInference, len(inferences))
	for i, inference := range inferences {
		out[i] = ConsensusInference{inference}
	}
	return out
}
