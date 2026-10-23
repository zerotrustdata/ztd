// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package audit

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

var errMockNetwork = fmt.Errorf("network failure")

func setupAuditAPITestEnv(t *testing.T) *config.Config {
	t.Helper()
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	creds := &auth.Credentials{
		OperatorSessionID: "op-sess-test",
		UserID:            "user-test",
		OperatorID:        "op-test",
		CLISessionID:      "cli-sess-test",
	}
	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, creds))
	return cfg
}

// --- Receipts ---

func TestAuditReceiptsCmd_API_MockHappyPath(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`{"event_receipts":[{"transaction_hash":"abc123","action_type":"file_edit","target_resource":"/path/to/file","status":2,"executed_at":"2026-01-01T00:00:00Z","l2_valid":true,"l3_valid":false}]}`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventReceiptsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "abc123")
	assert.Contains(t, buf.String(), "file_edit")
	assert.Contains(t, buf.String(), "L2 valid: 1")
}

func TestAuditReceiptsCmd_API_JSONOutput(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	rawJSON := `{"event_receipts":[]}`
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(rawJSON)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventReceiptsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmdtest.EnableGlobalJSON(t, cmd)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"event_receipts"`)
	assert.Contains(t, buf.String(), `[]`)
}

func TestAuditReceiptsCmd_API_EmptyReceipts(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`{"event_receipts":[]}`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventReceiptsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No event receipts found")
}

func TestAuditReceiptsCmd_API_GetError(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetErr: errMockNetwork}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventReceiptsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errMockNetwork)
}

func TestAuditReceiptsCmd_API_InvalidJSON(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`not json {{{`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventReceiptsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}

func TestAuditReceiptsCmd_API_ClientFactoryError(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return nil, constants.ErrNotAuthenticated
	}

	cmd := auditEventReceiptsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotAuthenticated)
}

// --- Export ---

func TestAuditExportCmd_API_MockHappyPath(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)
	tmpDir := testutil.TempDir(t)

	exportData := `{"event_receipts":[],"export_time":"2026-01-01T00:00:00Z"}`
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(exportData)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditExportCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	outFile := tmpDir + "/export.json"
	cmd.Flags().Set("out", outFile)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Receipts export written")

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Equal(t, exportData, string(data))
}

func TestAuditExportCmd_API_GetError(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetErr: errMockNetwork}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditExportCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errMockNetwork)
}

// --- Report ---

func TestAuditReportCmd_API_MockHappyPath(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)
	tmpDir := testutil.TempDir(t)

	reportJSON := `{"success":true,"report":{"generated_at":"2026-01-01T00:00:00Z","operator_session_id":"op-sess-test","events":[],"events_count":5,"event_receipts":[],"event_receipts_count":3,"total_records":8}}`
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(reportJSON)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditReportCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	cmd.Flags().Set("out", tmpDir)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Compliance report written")
	assert.Contains(t, buf.String(), "Events:   5")
	assert.Contains(t, buf.String(), "Event receipts: 3")
}

func TestAuditReportCmd_API_InvalidJSON(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)
	tmpDir := testutil.TempDir(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`not json {{{`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditReportCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	cmd.Flags().Set("out", tmpDir)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}

func TestAuditReportCmd_API_GetError(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)
	tmpDir := testutil.TempDir(t)

	mockClient := &cmdtest.MockAPIClient{GetErr: errMockNetwork}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditReportCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	cmd.Flags().Set("out", tmpDir)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errMockNetwork)
}

// --- Consensus votes ---

func TestAuditVotesCmd_API_PrintsEachMemberVote(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	votesJSON := `{"success":true,"transaction_id":"tx-1","votes":[` +
		`{"transaction_id":"tx-1","consensus_set_id":"tribunal","member_app_id":"tribunal-1","outcome":"deny","reason":"writes outside target","quorum":3,"model_tag":"gemma4:e2b","recorded_at":"2026-01-01T00:00:00Z"},` +
		`{"transaction_id":"tx-1","consensus_set_id":"tribunal","member_app_id":"tribunal-2","outcome":"no_vote","failure":"member inference: timeout","quorum":3,"recorded_at":"2026-01-01T00:00:01Z"},` +
		`{"transaction_id":"tx-1","consensus_set_id":"tribunal","member_app_id":"tribunal-3","outcome":"approve","reason":"read-only","quorum":3,"recorded_at":"2026-01-01T00:00:02Z"}]}`
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(votesJSON)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditVotesCmdWithConfig(loader, factory, shared.NewFileSvc)
	require.NoError(t, cmd.Flags().Set("tx", "tx-1"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.RunE(cmd, []string{}))
	require.Equal(t, []string{constants.APIPaths.AuditConsensusVotes + "?tx_id=tx-1"}, mockClient.GetCalls)
	out := buf.String()
	for _, want := range []string{"set tribunal, model gemma4:e2b, quorum 3", "tribunal-1", "reason: writes outside target",
		"no_vote", "failure: member inference: timeout", "approve", "Approvals: 1 of 3 votes, quorum 3"} {
		assert.Contains(t, out, want)
	}
}

func TestAuditVotesCmd_API_NoVotes(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`{"success":true,"transaction_id":"tx-2","votes":[]}`)}
	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditVotesCmdWithConfig(loader, factory, shared.NewFileSvc)
	require.NoError(t, cmd.Flags().Set("tx", "tx-2"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	require.NoError(t, cmd.RunE(cmd, []string{}))
	assert.Contains(t, buf.String(), "No consensus votes recorded for transaction tx-2")
}

// --- Events ---

func TestAuditEventsCmd_API_MockHappyPath(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	eventsJSON := `{"success":true,"events":[{"id":1,"operator_session_id":"op-sess-test","timestamp":"2026-01-01T00:00:00Z","type":"file_edit","command_raw":"echo hello","command_exit_code":0}],"count":1}`
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(eventsJSON)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "file_edit")
	assert.Contains(t, buf.String(), "echo hello")
	assert.Contains(t, buf.String(), "Total: 1 events")
}

func TestAuditEventsCmd_API_JSONOutput(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	rawJSON := `{"success":true,"events":[],"count":0}`
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(rawJSON)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmdtest.EnableGlobalJSON(t, cmd)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"events"`)
	assert.Contains(t, buf.String(), `"count": 0`)
}

func TestAuditEventsCmd_API_EmptyEvents(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`{"success":true,"events":[],"count":0}`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No audit events found")
}

func TestAuditEventsCmd_API_GetError(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetErr: errMockNetwork}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errMockNetwork)
}

func TestAuditEventsCmd_API_InvalidJSON(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`not json {{{`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditEventsCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}

// --- Summary ---

func TestAuditSummaryCmd_API_MockHappyPath(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	summaryJSON := `{"success":true,"events_summary":{"file_edit":5,"file_read":10},"events_total":15,"receipts_summary":{"approved":3,"rejected":1},"receipts_total":4,"total_records":19}`
	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(summaryJSON)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditSummaryCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Audit Summary")
	assert.Contains(t, buf.String(), "file_edit")
	assert.Contains(t, buf.String(), "Total records: 19")
}

func TestAuditSummaryCmd_API_EmptyRecords(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`{"success":true,"events_summary":{},"events_total":0,"receipts_summary":{},"receipts_total":0,"total_records":0}`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditSummaryCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No audit records found")
}

func TestAuditSummaryCmd_API_InvalidJSON(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetResp: []byte(`not json {{{`)}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditSummaryCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}

func TestAuditSummaryCmd_API_GetError(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := setupAuditAPITestEnv(t)

	mockClient := &cmdtest.MockAPIClient{GetErr: errMockNetwork}

	loader := func(string) (*config.Config, error) { return cfg, nil }
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mockClient, nil }

	cmd := auditSummaryCmdWithConfig(loader, factory, shared.NewFileSvc)
	cmd.Flags().Set("session", "op-sess-test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errMockNetwork)
}
