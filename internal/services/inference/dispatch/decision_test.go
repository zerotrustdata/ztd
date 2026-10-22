// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package dispatch

import (
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestDecisionPinnedOperatorNeverFallsBack(t *testing.T) {
	pinned := capableOp("decision-session")
	pinned.RuntimeConfig.InferenceDecisionBackend = "ollama-systemone"
	pinned.RuntimeConfig.InferenceDecisionModels = []string{"tev1:0.8b"}
	other := proto.Clone(pinned).(*operatorv1.OperatorDocument)
	other.Id, other.OperatorSessionId = "other", "other-session"
	req := baseRequest()
	req.Messages = nil
	req.Model = "tev1:0.8b"
	req.Decision = &operatorv1.InferenceDecisionRequest{}
	req.TargetOperatorID = pinned.Id
	lister := &stubOperatorLister{ops: []*operatorv1.OperatorDocument{pinned, other}}
	service := NewDispatchService(&stubCommandDispatcher{}, lister, testLogger())
	session, err := service.resolveInferenceOperator(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, pinned.OperatorSessionId, session)
	lister.ops = []*operatorv1.OperatorDocument{other}
	_, err = service.resolveInferenceOperator(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorNotFound)
	lister.ops = []*operatorv1.OperatorDocument{pinned, pinned}
	_, err = service.resolveInferenceOperator(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorAmbiguous)
	lister.ops = []*operatorv1.OperatorDocument{pinned, other}
	pinned.RuntimeConfig.InferenceDecisionModels = nil
	_, err = service.resolveInferenceOperator(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorNotFound)
	pinned.RuntimeConfig.InferenceDecisionModels = []string{req.Model}
	req.Decision.AllowHosted = true
	_, err = service.resolveInferenceOperator(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorNotFound)
}
