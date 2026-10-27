// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestDispatchRealStack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		posture constants.Posture
		wantL2  operatorv1.L2Status
		wantL3  operatorv1.L3Status
	}{
		{name: "ratify", posture: constants.PostureRatify, wantL2: operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, wantL3: operatorv1.L3Status_L3_STATUS_REQUIRED_VALID},
		{name: "notary", posture: constants.PostureNotary, wantL2: operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, wantL3: operatorv1.L3Status_L3_STATUS_REQUIRED_VALID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			t.Cleanup(cancel)
			stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{Posture: tc.posture})
			workload, err := stack.Data.Workload()
			require.NoError(t, err)
			approvalCount := len(stack.Owner.ApprovalReceipts())

			client := fixtures.CreateCLIMTLSClient(t, stack.Gateway, stack.Identity)
			payload, err := proto.Marshal(&operatorv1.CommandRequested{
				Command:       "echo ztd-real-dispatch-marker",
				ExecutionId:   "real-dispatch-marker",
				Justification: "Verify governed outbound command dispatch",
				Intent:        "Print a fixed verification marker",
			})
			require.NoError(t, err)
			body, err := json.Marshal(models.OperatorCommandRequest{
				TargetOperatorSessionID: workload.OperatorSessionID,
				EventType:               string(constants.Event.Operator.Command.Requested),
				Payload:                 payload,
			})
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				network.LocalhostHTTPSURL(stack.Gateway.Service.GetHTTPSPort())+constants.APIPaths.OperatorsCommands, bytes.NewReader(body))
			require.NoError(t, err)
			req.Close = true
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(constants.HeaderCLISessionID, stack.Identity.CLISessionID)
			resp, err := client.Do(req)
			require.NoError(t, err)
			respBody, readErr := io.ReadAll(resp.Body)
			require.NoError(t, resp.Body.Close())
			require.NoError(t, readErr)
			require.Equalf(t, http.StatusOK, resp.StatusCode, "dispatch response: %s", respBody)

			var result operatorv1.GovernedDispatchResult
			require.NoError(t, protojson.Unmarshal(respBody, &result), "dispatch response: %s", respBody)
			require.Equal(t, string(constants.Event.Operator.Command.Completed), result.GetEventType())
			require.NotEmpty(t, result.GetTransactionId())
			require.NotNil(t, result.GetReceipt())
			var commandResult operatorv1.CommandResult
			require.NoError(t, proto.Unmarshal(result.GetResultPayload(), &commandResult))
			require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, commandResult.GetStatus())
			require.Equal(t, "ztd-real-dispatch-marker\n", commandResult.GetStdout())
			require.Equal(t, int32(0), commandResult.GetReturnCode())
			require.Equal(t, tc.wantL2, result.GetReceipt().GetL2Status())
			require.Equal(t, tc.wantL3, result.GetReceipt().GetL3Status())

			approvalCtx, approvalCancel := context.WithTimeout(ctx, 10*time.Second)
			approvals, err := stack.Owner.AwaitApprovalReceipts(approvalCtx, approvalCount+1)
			approvalCancel()
			require.NoError(t, err)
			require.NotEmpty(t, approvals)
			approval := approvals[len(approvals)-1]
			require.Equal(t, result.GetReceipt().GetTransactionHash(), approval.GetTransactionHash())
			require.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, approval.GetL3Status())

			operatorDoc, err := stack.Gateway.Service.GetRegistrationService().GetOperator(ctx, workload.OperatorID)
			require.NoError(t, err)
			actuatorPublicKey, err := models.DecodeActuatorPublicKey(operatorDoc.GetActuatorPublicKey())
			require.NoError(t, err)
			require.NoError(t, governance.VerifyEventReceiptSignature(result.GetReceipt(), actuatorPublicKey))
			require.NoError(t, governance.VerifyReceiptPersistenceAttestation(result.GetReceipt(), actuatorPublicKey))

			dataFiles, err := fs.NewRuntimeFileService(stack.DataHost.WorkingDir, testutil.NewTestLogger())
			require.NoError(t, err)
			local, err := storage.ReadLocalEventReceipt(dataFiles, slog.Default(), keystore.Options{Keyring: stack.DataHost.Keyring}, result.GetTransactionId())
			require.NoError(t, err)
			require.NotNil(t, local)
			require.True(t, proto.Equal(result.GetReceipt(), local.EventReceipt), "returned receipt must equal Data Operator's authoritative local record")

			commitments := readDataCommitments(t, dataFiles, stack.DataHost.Keyring)
			found := false
			for _, commitment := range commitments {
				if commitment.TransactionID == result.GetTransactionId() {
					found = true
					break
				}
			}
			require.True(t, found, "Data Operator's local commitment ledger must include the dispatched transaction")
		})
	}
}

func readDataCommitments(t *testing.T, fileSvc fs.RuntimeFileService, keyring keystore.Keyring) []*storage.CommitmentRow {
	t.Helper()
	logger := testutil.NewTestLogger()
	ks, err := keystore.Open(fileSvc, logger, keystore.Options{Keyring: keyring})
	require.NoError(t, err)
	vaultKey, err := ks.LoadVaultKey()
	require.NoError(t, err)
	t.Cleanup(func() { vault.SecureZero(vaultKey) })
	localVault, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: logger})
	require.NoError(t, err)
	require.NoError(t, localVault.Unlock(vaultKey))
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	storeConfig := storage.DefaultLocalFirstEventsStoreConfig()
	storeConfig.EncryptionVault = localVault
	store, err := storage.NewLocalFirstEventsStore(storeConfig, db, logger, fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	rows, err := store.CommitmentLedger().ListCommitments()
	require.NoError(t, err)
	return rows
}
