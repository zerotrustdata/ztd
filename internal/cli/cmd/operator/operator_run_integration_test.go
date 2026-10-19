// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package operatorcmd

import (
	"bytes"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestOperatorRun_ExecutesThroughGovernedPipeline drives the real `operator run`
// cobra command against a real doctrine Gateway. The first-user bootstrap
// claims the Gateway's embedded (Data) Operator and mints its operator
// session, so the command targets the Gateway-local substrate through the
// production dispatch path (L1-L5), with no mocked client.
func TestOperatorRun_ExecutesThroughGovernedPipeline(t *testing.T) {
	ctx := t.Context()
	fixture := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: t.Name(),
		Posture:  constants.PostureDoctrine,
	})
	fixture.WaitForReady(t)

	identity := fixtures.BootstrapCLIIdentity(t, fixture, "operator-run-it-host")

	// Materialize the CLI runtime: cert, key, trust bundle (root + operator CA),
	// and saved credentials, exactly as `auth enroll` leaves them.
	fileSvc, cliCfg := cmdtest.NewCmdTestEnv(t)
	identity.InstallCLIRuntime(t, fixture, fileSvc, cliCfg)

	// Audit client over the same production CLI transport the command uses.
	auditClient, err := defaultOperatorRunClientFactory(fileSvc, cliCfg, api.ClientOptions{})
	require.NoError(t, err)
	before := countExecutedBashReceipts(t, auditClient, identity.OperatorSessionID)

	cmd := operatorRunCmdWithConfig(cmdtest.ConfigLoaderFor(cliCfg), defaultOperatorRunClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{identity.OperatorSessionID, "--cmd", "echo ztd-operator-run-it"})
	require.NoError(t, cmd.ExecuteContext(ctx), "operator run output: %s", out.String())

	assert.Contains(t, out.String(), "ztd-operator-run-it")
	assert.Contains(t, out.String(), identity.OperatorSessionID)

	after := countExecutedBashReceipts(t, auditClient, identity.OperatorSessionID)
	assert.Greater(t, after, before, "governed EXECUTE_BASH must append a COMPLETED action receipt for the embedded operator session")
}

// countExecutedBashReceipts counts COMPLETED EXECUTE_BASH receipts recorded for
// operatorSessionID through the Gateway audit receipts endpoint.
func countExecutedBashReceipts(t *testing.T, client authcmd.APIClient, operatorSessionID string) int {
	t.Helper()
	raw, err := client.Get(constants.APIPaths.AuditReceipts + "?operator_session_id=" + url.QueryEscape(operatorSessionID) + "&limit=500")
	require.NoError(t, err)
	var resp models.AuditReceiptsResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.True(t, resp.Success, "audit receipts response must report success")
	count := 0
	for _, receipt := range resp.Receipts {
		if receipt.ActionType == constants.ActionTypeExecuteBash && receipt.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
			count++
		}
	}
	return count
}
