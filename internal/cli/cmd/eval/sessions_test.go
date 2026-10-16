// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func remoteOperator(id, session string, cfg *operatorv1.OperatorRuntimeConfig) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                id,
		OperatorSessionId: session,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		RuntimeConfig:     cfg,
	}
}

func inferenceOperatorFixture(id, session string) *operatorv1.OperatorDocument {
	return remoteOperator(id, session, &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}, InferenceOllamaEndpoint: "http://provider.example:11434"})
}

// dataOperatorFixture is a dedicated data Operator (no explicit capability flags, so
// it defaults to the data role).
func dataOperatorFixture(id, session string) *operatorv1.OperatorDocument {
	return remoteOperator(id, session, &operatorv1.OperatorRuntimeConfig{})
}

func TestEvalGroupHasNoSessionFlags(t *testing.T) {
	group := evalCmdWithConfig(nativeEvalDeps{})
	assert.Nil(t, group.PersistentFlags().Lookup("data-session"))
	assert.Nil(t, group.PersistentFlags().Lookup("inference-session"))
}
