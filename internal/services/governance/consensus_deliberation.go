// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
 "errors"

 operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ConsensusDeliberation keeps admission proofs and the governed executions
// that produced them together. Inferences includes attempts that cast no vote.
type ConsensusDeliberation struct {
 Envelope []byte
 Inferences []*operatorv1.ConsensusVoteInference
}

// ConsensusInferenceError preserves executions when subject admission fails.
type ConsensusInferenceError struct {
 Inferences []*operatorv1.ConsensusVoteInference
 Err error
}
func (e *ConsensusInferenceError) Error() string { return e.Err.Error() }
func (e *ConsensusInferenceError) Unwrap() error { return e.Err }

// WithConsensusInferences carries vote executions through a failed dispatch.
func WithConsensusInferences(inferences []*operatorv1.ConsensusVoteInference, err error) error {
 if err == nil || len(inferences) == 0 { return err }
 return &ConsensusInferenceError{Inferences: inferences, Err: err}
}

// ConsensusInferences returns the vote executions retained by a failed dispatch.
func ConsensusInferences(err error) []*operatorv1.ConsensusVoteInference {
 var carried *ConsensusInferenceError
 if errors.As(err, &carried) { return carried.Inferences }
 return nil
}
