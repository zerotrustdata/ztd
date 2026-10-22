// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// Keep the CLI's production maintenance dispatcher: the only scripted component
// is the external Ollama provider whose inventory the Operator reads.
func TestListOllamaProviderInventory_UsesTypedInventoryAction(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureDoctrine, constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			env := newRealStackEnvironment(t, fixtures.EvalStackOptions{Posture: posture}, runControlDeps{})
			authContext, err := auth.LoadClientAuthContext(env.fileSvc, env.cfg)
			require.NoError(t, err)
			dispatcher, err := newHarnessOllamaModelCommandDispatcher(env.cfg, authContext, env.deps.gatewayDeps())
			require.NoError(t, err)
			workload, err := env.stack.Inference.Workload()
			require.NoError(t, err)
			const caseID = "inventory-maintenance"
			entries, err := evaluation.ListOllamaProviderInventory(t.Context(), dispatcher, evaluation.OllamaModelMaintenanceContext{
				TargetOperatorSessionID: workload.OperatorSessionID,
				CaseID:                  caseID,
				NewID:                   adaptNewID(env.deps.newID),
			})
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, testQwenVariant().GetServedModelTag(), entries[0].ServedModelTag)
			assert.Equal(t, testQwenVariant().GetModelDigest(), entries[0].ModelDigest)
			assert.Equal(t, uint64(4_000_000_000), entries[0].ParameterCount)
			assert.Equal(t, uint32(8192), entries[0].ContextLimit)
			assert.Equal(t, []string{"completion", "tools"}, entries[0].AdvertisedCapabilities)

			// Use the Gateway projection only to discover the transaction ID;
			// the Inference Operator's own store is authoritative for evidence.
			projected, err := env.stack.Gateway.Service.GetAuditStore().ListEventReceipts(models.AuditScope{OperatorSessionID: workload.OperatorSessionID, InvestigationID: "ollama-model-inventory"}, 10, 0)
			require.NoError(t, err)
			require.Len(t, projected, 1)
			files, err := fs.NewRuntimeFileService(env.stack.InferenceHost.WorkingDir, testutil.NewTestLogger())
			require.NoError(t, err)
			local, err := storage.ReadLocalEventReceipt(files, testutil.NewTestLogger(), keystore.Options{Keyring: env.stack.InferenceHost.Keyring}, projected[0].TransactionID)
			require.NoError(t, err)
			require.NotNil(t, local)
			assert.Equal(t, workload.OperatorSessionID, local.OperatorSessionID)
			assert.Equal(t, constants.ActionTypeOllamaModelInventory, local.ActionType)
			require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, local.EventReceipt.GetStatus())
			// Inventory is not a state change, so neither L2 nor L3 gates it.
			assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, local.EventReceipt.GetL2Status())
			assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, local.EventReceipt.GetL3Status())
			operator, err := env.stack.Gateway.Service.GetRegistrationService().GetOperator(t.Context(), workload.OperatorID)
			require.NoError(t, err)
			publicKey, err := models.DecodeActuatorPublicKey(operator.GetActuatorPublicKey())
			require.NoError(t, err)
			require.NoError(t, governance.VerifyEventReceiptSignature(local.EventReceipt, publicKey))
			require.NoError(t, governance.VerifyEventReceiptPersistenceAttestation(local.EventReceipt, publicKey))
			assert.Zero(t, env.stack.Provider.ChatCount())
		})
	}
}
