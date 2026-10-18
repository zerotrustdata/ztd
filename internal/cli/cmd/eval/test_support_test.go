// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// testActuatorSigner is the actuator key derived from seed, signing receipts
// the way that Operator's L5 actuator does.
func testActuatorSigner(seed byte) oracletest.Signer {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	pub := priv.Public().(ed25519.PublicKey)
	return oracletest.Signer{Priv: priv, Pub: pub, KeyID: hex.EncodeToString(pub)}
}

// testDataOperatorActuatorKey is the enrollment-bound actuator key the data-op
// fixture documents carry, so oracle.BoundAnchor admits them.
var testDataOperatorActuatorKey = testActuatorSigner(7).KeyID

// testInferenceOperatorActuatorKey is the enrollment-bound actuator key the
// infer-op run fixture carries, so a split topology anchors both runtimes
// (INV-EVAL-CAMP-35).
var testInferenceOperatorActuatorKey = testActuatorSigner(8).KeyID

func writeTestFrozenInventory(t *testing.T, root string, variants ...*evalv1.ModelVariant) {
	t.Helper()
	bodies := make([]json.RawMessage, 0, len(variants))
	for _, variant := range variants {
		raw, err := protojson.Marshal(variant)
		require.NoError(t, err)
		bodies = append(bodies, raw)
	}
	payload, err := json.Marshal(struct {
		Variants []json.RawMessage `json:"variants"`
	}{Variants: bodies})
	require.NoError(t, err)
	fileSvc, err := fs.NewRuntimeFileService(root, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	require.NoError(t, fileSvc.WriteFile(context.Background(), evaluation.DefaultModelInventoryRelPath, payload, constants.PermFileReadOnly))
}

func TestModelInventoryFreezeJSON_RejectsNilFreeze(t *testing.T) {
	_, err := modelInventoryFreezeJSON(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

// fakeBindClient stands in for the enrollment client: it reports the bound
// sessions it holds and records every bind call, replacing the held sessions
// with the requested ones.
type fakeBindClient struct {
	bound   []string
	binds   [][]string
	infoErr error
	bindErr error
}

func (f *fakeBindClient) SessionInfo(context.Context, fs.RuntimeFileService) (auth.CLISessionInfo, error) {
	if f.infoErr != nil {
		return auth.CLISessionInfo{}, f.infoErr
	}
	info := auth.CLISessionInfo{CLISessionID: "cli-1", UserID: "user-1", BoundOperatorSessionIDs: f.bound}
	if len(f.bound) > 0 {
		info.OperatorSessionID = f.bound[0]
	}
	return info, nil
}

func (f *fakeBindClient) Bind(_ context.Context, _ fs.RuntimeFileService, operatorSessionIDs []string) (auth.CLISessionBind, error) {
	if f.bindErr != nil {
		return auth.CLISessionBind{}, f.bindErr
	}
	f.binds = append(f.binds, operatorSessionIDs)
	f.bound = operatorSessionIDs
	bound := make([]models.CLIBoundOperator, 0, len(operatorSessionIDs))
	for _, sessionID := range operatorSessionIDs {
		bound = append(bound, models.CLIBoundOperator{OperatorSessionID: sessionID, OperatorID: "operator-of-" + sessionID})
	}
	return auth.CLISessionBind{
		CLISessionID:      "cli-bound",
		UserID:            "user-1",
		OperatorSessionID: operatorSessionIDs[0],
		OperatorID:        "operator-of-" + operatorSessionIDs[0],
		Bound:             bound,
	}, nil
}

func campaignOrchestrateOperators() []*operatorv1.OperatorDocument {
	return []*operatorv1.OperatorDocument{
		{
			Id:                "infer-op",
			OperatorSessionId: "infer-session",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:            []string{string(constants.OperatorCapabilityInference)},
				InferenceOllamaEndpoint: "http://provider.example:11434",
			},
		},
		{
			Id:                "data-op",
			OperatorSessionId: "data-session",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{},
			ActuatorPublicKey: testDataOperatorActuatorKey,
			ActuatorKeyId:     testDataOperatorActuatorKey,
		},
	}
}
