// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestModelsDiff_RejectsSeveralInferenceOperators(t *testing.T) {
	env, run := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	seedModelScopes(t, env.root)

	// A second real inference Operator makes provider selection ambiguous.
	secondInference := run.stack.Witness.LaunchOperator(t, fixtures.NewOperatorHost(t), fixtures.OperatorProcessOptions{
		Capabilities:            constants.OperatorCapabilities{constants.OperatorCapabilityInference},
		InferenceOllamaEndpoint: run.stack.Provider.Endpoint(),
	})
	ctx, cancel := context.WithTimeout(env.cmd.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, secondInference.AwaitStarted(ctx))
	_, err := secondInference.AwaitHeartbeat(ctx)
	require.NoError(t, err)

	command := evalCmdWithConfig(env.deps)
	command.SetContext(env.cmd.Context())
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"models", "diff", "--project-root", env.root})
	err = command.Execute()
	require.ErrorIs(t, err, constants.ErrInferenceOperatorAmbiguous)
}
