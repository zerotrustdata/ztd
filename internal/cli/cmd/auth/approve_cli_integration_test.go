//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func writeCLIApprovalIdentity(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) []byte {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	cmdtest.WriteSealedKey(t, fileSvc, cfg, cfg.CLIKeyFile(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))

	wid := protocol.NewWorkloadIdentity()
	uri, err := wid.CLISPIFFEURL("user-test", "cli-sess-test")
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(99),
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{uri},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, fileSvc.WriteFile(context.Background(), cmdtest.MustRel(t, fileSvc, cfg.CLICertFile()), certPEM, constants.PermFilePrivate))

	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, &auth.Credentials{
		OperatorSessionID: "op-sess-test",
		UserID:            "user-test",
		OperatorID:        "op-test",
		CLISessionID:      "cli-sess-test",
	}))
	return certDER
}

func cliApprovalReceipt(t *testing.T, txHash string, status operatorv1.ExecutionStatus, l3 operatorv1.L3Status) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
		TransactionId:   "tx-approval-test",
		TransactionHash: txHash,
		Status:          status,
		L3Status:        l3,
		ResultSummary:   "completed",
	})
	require.NoError(t, err)
	return body
}

func TestApproveCmd_CLI_SignsExactHashAndSkipsBrowser(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	txHash := bytes.Repeat([]byte("a"), sha256.Size)
	txHashHex := hex.EncodeToString(txHash)
	certDER := writeCLIApprovalIdentity(t, fileSvc, cfg)

	mockClient := &cmdtest.MockAPIClient{PostResp: cliApprovalReceipt(t, txHashHex,
		operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		operatorv1.L3Status_L3_STATUS_REQUIRED_VALID)}
	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil }
	openedBrowser := false
	cmd := approveCmdWithConfig(loader, factory, cmdtest.FileSvcFactoryFor(fileSvc), func(string) error {
		openedBrowser = true
		return nil
	})
	require.NoError(t, cmd.Flags().Set("cli", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{txHashHex})
	require.NoError(t, err)
	assert.False(t, openedBrowser)
	assert.Len(t, mockClient.PostCalls, 1)
	assert.Equal(t, constants.APIPaths.ApprovalsRespond, mockClient.PostCalls[0].Path)
	request, ok := mockClient.PostCalls[0].Body.(models.ApprovalRespondRequest)
	require.True(t, ok)
	assert.Equal(t, txHashHex, request.ApprovalID)
	assert.True(t, request.Approved)
	assert.NotEmpty(t, request.CLISignature)
	proof := &commonv1.L3Proof{CliSignature: request.CLISignature, CliCertificateDer: certDER}
	fingerprint := sha256.Sum256(certDER)
	proof.MtlsCertFingerprint = hex.EncodeToString(fingerprint[:])
	_, err = governance.VerifyCLIApproval("user-test", txHashHex, proof)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "approved successfully")
}

func TestApproveCmd_CLI_RejectsReceiptThatDoesNotConfirmRequestedApproval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hash   string
		status operatorv1.ExecutionStatus
		l3     operatorv1.L3Status
	}{
		{name: "wrong transaction hash", hash: hex.EncodeToString(bytes.Repeat([]byte("b"), sha256.Size)), status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, l3: operatorv1.L3Status_L3_STATUS_REQUIRED_VALID},
		{name: "transaction did not complete", hash: hex.EncodeToString(bytes.Repeat([]byte("a"), sha256.Size)), status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, l3: operatorv1.L3Status_L3_STATUS_REQUIRED_VALID},
		{name: "L3 proof was not accepted", hash: hex.EncodeToString(bytes.Repeat([]byte("a"), sha256.Size)), status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, l3: operatorv1.L3Status_L3_STATUS_REQUIRED_FAILED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			writeCLIApprovalIdentity(t, fileSvc, cfg)
			txHash := hex.EncodeToString(bytes.Repeat([]byte("a"), sha256.Size))
			mockClient := &cmdtest.MockAPIClient{PostResp: cliApprovalReceipt(t, tc.hash, tc.status, tc.l3)}
			cmd := approveCmdWithConfig(func(string) (*config.Config, error) { return cfg, nil },
				func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil },
				cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
			require.NoError(t, cmd.Flags().Set("cli", "true"))
			err := cmd.RunE(cmd, []string{txHash})
			require.Error(t, err)
			switch tc.name {
			case "wrong transaction hash":
				require.ErrorIs(t, err, constants.ErrTxTransactionHashMismatch)
			case "transaction did not complete":
				require.ErrorIs(t, err, constants.ErrActionExecutionFailed)
			case "L3 proof was not accepted":
				require.ErrorIs(t, err, constants.ErrTxL3ProofInvalid)
			}
			assert.Empty(t, mockClient.GetCalls)
			assert.NotContains(t, err.Error(), "approved successfully")
		})
	}
}

func TestApproveCmd_CLI_RequiresEnrolledCredentials(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	mockClient := &cmdtest.MockAPIClient{}
	cmd := approveCmdWithConfig(func(string) (*config.Config, error) { return cfg, nil },
		func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil },
		cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	require.NoError(t, cmd.Flags().Set("cli", "true"))
	err := cmd.RunE(cmd, []string{hex.EncodeToString(bytes.Repeat([]byte("a"), sha256.Size))})
	require.ErrorIs(t, err, constants.ErrNotAuthenticated)
	assert.Empty(t, mockClient.PostCalls)
}

func cliDenyReceipt(t *testing.T, txHash string, code operatorv1.GovernanceRejectionCode) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
		TransactionId:   "tx-deny-test",
		TransactionHash: txHash,
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		RejectionCode:   code,
	})
	require.NoError(t, err)
	return body
}

func TestApproveCmd_Deny_SubmitsUnsignedDenyAndSkipsBrowser(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	txHash := hex.EncodeToString(bytes.Repeat([]byte("a"), sha256.Size))
	mockClient := &cmdtest.MockAPIClient{PostResp: cliDenyReceipt(t, txHash, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED)}
	openedBrowser := false
	cmd := approveCmdWithConfig(func(string) (*config.Config, error) { return cfg, nil },
		func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil },
		cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { openedBrowser = true; return nil })
	require.NoError(t, cmd.Flags().Set("deny", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.RunE(cmd, []string{txHash}))
	assert.False(t, openedBrowser)
	require.Len(t, mockClient.PostCalls, 1)
	assert.Equal(t, constants.APIPaths.ApprovalsRespond, mockClient.PostCalls[0].Path)
	assert.Equal(t, models.ApprovalRespondRequest{ApprovalID: txHash}, mockClient.PostCalls[0].Body,
		"a deny is grounded in the mTLS session and carries no approval signature")
	assert.Contains(t, buf.String(), "denied (signed L3_DENIED receipt)")
}

func TestApproveCmd_Deny_RejectsReceiptThatIsNotTheRequestedDeny(t *testing.T) {
	txHash := hex.EncodeToString(bytes.Repeat([]byte("a"), sha256.Size))
	for _, tc := range []struct {
		name string
		body func(t *testing.T) []byte
		want error
	}{
		{"wrong transaction hash", func(t *testing.T) []byte {
			t.Helper()
			return cliDenyReceipt(t, hex.EncodeToString(bytes.Repeat([]byte("b"), sha256.Size)), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED)
		}, constants.ErrTxTransactionHashMismatch},
		{"not an L3 deny", func(t *testing.T) []byte {
			t.Helper()
			return cliApprovalReceipt(t, txHash, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID)
		}, constants.ErrValidationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			mockClient := &cmdtest.MockAPIClient{PostResp: tc.body(t)}
			cmd := approveCmdWithConfig(func(string) (*config.Config, error) { return cfg, nil },
				func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil },
				cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
			require.NoError(t, cmd.Flags().Set("deny", "true"))
			assert.ErrorIs(t, cmd.RunE(cmd, []string{txHash}), tc.want)
		})
	}
}

func TestApproveCmd_DenyAndCLIAreExclusive(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	mockClient := &cmdtest.MockAPIClient{}
	cmd := approveCmdWithConfig(func(string) (*config.Config, error) { return cfg, nil },
		func(fs.RuntimeFileService, *config.Config) (APIClient, error) { return mockClient, nil },
		cmdtest.FileSvcFactoryFor(fileSvc), func(string) error { return nil })
	cmd.SetArgs([]string{"--cli", "--deny", hex.EncodeToString(bytes.Repeat([]byte("a"), sha256.Size))})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.Error(t, cmd.Execute())
	assert.Empty(t, mockClient.PostCalls)
}
