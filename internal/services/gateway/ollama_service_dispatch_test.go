// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

func TestValidateWitnessCommandDispatch(t *testing.T) {
	payload, err := proto.Marshal(&operatorv1.CommandRequested{Command: "ollama stop qwen3:0.6b", ExecutionId: "exec-1"})
	require.NoError(t, err)

	ordinary := &operatorv1.OperatorDocument{
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
	}
	require.NoError(t, validateWitnessCommandDispatch(ordinary, string(constants.ActionTypeExecuteBash), payload))

	observer := &operatorv1.OperatorDocument{
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
	}
	err = validateWitnessCommandDispatch(observer, string(constants.ActionTypeExecuteBash), payload)
	require.ErrorIs(t, err, constants.ErrWitnessCommandNotCapable)

	provenance := &operatorv1.OperatorDocument{
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityProvenance)}},
	}
	err = validateWitnessCommandDispatch(provenance, string(constants.ActionTypeExecuteBash), payload)
	require.ErrorIs(t, err, constants.ErrWitnessCommandNotCapable)
}
