// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package serve

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestEnrollOperator_PresentsActuatorKey proves enrollOperator puts the
// Actuator public key it is given into the enrollment request the Gateway binds.
func TestEnrollOperator_PresentsActuatorKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	actuatorKeyHex := hex.EncodeToString(pub)

	mg := newMockBootstrapGateway(t)
	ctx, cancel := context.WithCancel(enrollContext(t))
	defer cancel()
	// The request is recorded before onRequest runs; cancelling here ends the
	// held socket instead of waiting for an owner approval the test never gives.
	mg.onRequest = func(models.OperatorBootstrapRequest) { cancel() }

	deployment, err := NewOperatorDeploymentRecorder(newTestFileSvc(t), "")
	require.NoError(t, err)
	_, err = enrollOperator(ctx, ServeOperatorOptions{}, testutil.NewTestConfig(t), mg.server.URL, testutil.TempDir(t), nil, actuatorKeyHex, deployment, testLogger())
	require.Error(t, err)

	reqs := mg.received()
	require.Len(t, reqs, 1)
	require.NotNil(t, reqs[0].Enrollment.Operator)
	assert.Equal(t, actuatorKeyHex, reqs[0].Enrollment.Operator.ActuatorPublicKey)
}

// TestEnrollOperator_RefusesMissingActuatorKey proves an Operator with no valid
// Actuator key never reaches the Gateway: enrollment fails before any dial.
func TestEnrollOperator_RefusesMissingActuatorKey(t *testing.T) {
	for name, keyHex := range map[string]string{
		"empty":     "",
		"not hex":   "zz",
		"short key": hex.EncodeToString(make([]byte, ed25519.PublicKeySize-1)),
	} {
		t.Run(name, func(t *testing.T) {
			mg := newMockBootstrapGateway(t)
			deployment, err := NewOperatorDeploymentRecorder(newTestFileSvc(t), "")
			require.NoError(t, err)

			_, err = enrollOperator(enrollContext(t), ServeOperatorOptions{}, testutil.NewTestConfig(t), mg.server.URL, testutil.TempDir(t), nil, keyHex, deployment, testLogger())
			assert.ErrorIs(t, err, constants.ErrOperatorActuatorKeyInvalid)
			assert.Equal(t, 0, mg.dials())
		})
	}
}
