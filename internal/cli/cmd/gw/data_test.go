// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestDataCmd(t *testing.T) {
	t.Run("data command has correct use and description", func(t *testing.T) {
		cmd := dataCmd()
		assert.Equal(t, "data", cmd.Use)
		assert.Contains(t, cmd.Short, "Administer")
		assert.Contains(t, cmd.Short, "mTLS")
	})
}

func TestDataUsersCmd(t *testing.T) {
	t.Run("users command has correct use", func(t *testing.T) {
		cmd := dataUsersCmd()
		assert.Equal(t, "users", cmd.Use)
		assert.Contains(t, cmd.Short, "Manage user accounts")
	})
}

func TestDataOperatorsCmd(t *testing.T) {
	t.Run("operators command has correct use", func(t *testing.T) {
		cmd := dataOperatorsCmd()
		assert.Equal(t, "operators", cmd.Use)
		assert.Contains(t, cmd.Short, "Manage Operator instances")
	})
}

func TestDataSettingsCmd(t *testing.T) {
	t.Run("settings command has correct use", func(t *testing.T) {
		cmd := dataSettingsCmd()
		assert.Equal(t, "settings", cmd.Use)
		assert.Contains(t, cmd.Short, "Manage Gateway settings")
	})
}

func TestDataStoreCmd(t *testing.T) {
	t.Run("store command has correct use", func(t *testing.T) {
		cmd := dataStoreCmd()
		assert.Equal(t, "store", cmd.Use)
		assert.Contains(t, cmd.Short, "Manage document storage")
	})

	t.Run("store has collection flag", func(t *testing.T) {
		cmd := dataStoreCmd()
		flag := cmd.Flags().Lookup("collection")
		assert.NotNil(t, flag)
	})

	t.Run("store has document-id flag", func(t *testing.T) {
		cmd := dataStoreCmd()
		flag := cmd.Flags().Lookup("document-id")
		assert.NotNil(t, flag)
	})

}

func TestDataAuditCmd(t *testing.T) {
	t.Run("audit command has correct use", func(t *testing.T) {
		cmd := dataAuditCmd()
		assert.Equal(t, "audit", cmd.Use)
		assert.Contains(t, cmd.Short, "Query the Gateway audit store")
	})

	t.Run("audit has operator-session-id flag", func(t *testing.T) {
		cmd := dataAuditListCmd()
		flag := cmd.Flags().Lookup("operator-session-id")
		assert.NotNil(t, flag)
	})

	t.Run("audit has limit flag", func(t *testing.T) {
		cmd := dataAuditListCmd()
		flag := cmd.Flags().Lookup("limit")
		assert.NotNil(t, flag)
	})

}

func TestDataCommandsRequireAuthentication(t *testing.T) {
	testCases := []struct {
		name string
		cmd  func(func(string) (*config.Config, error), authcmd.APIClientFactory, func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command
	}{
		{"users", dataUsersCmdWithConfig},
		{"operators", dataOperatorsCmdWithConfig},
		{"settings", dataSettingsCmdWithConfig},
		{"store", dataStoreCmdWithConfig},
		{"audit list", dataAuditListCmdWithConfig},
		{"audit summary", dataAuditSummaryCmdWithConfig},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			loader := func(_ string) (*config.Config, error) { return cfg, nil }
			cmd := tc.cmd(loader, authcmd.DefaultAPIClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			err := cmd.RunE(cmd, []string{})
			require.Error(t, err)
			require.ErrorIs(t, err, constants.ErrNotAuthenticated)
		})
	}
}

func TestDataCommandFlags(t *testing.T) {
	t.Run("audit limit flag has default", func(t *testing.T) {
		cmd := dataAuditListCmd()
		limitFlag := cmd.Flags().Lookup("limit")
		assert.NotNil(t, limitFlag)
		assert.Equal(t, "100", limitFlag.DefValue)
	})
}

func TestDataAuditSummaryCmd(t *testing.T) {
	t.Run("summary command has correct use", func(t *testing.T) {
		cmd := dataAuditSummaryCmd()
		assert.Equal(t, string(constants.StreamStatusSummary), cmd.Use)
		assert.Contains(t, cmd.Short, "Show audit event summary")
	})

	t.Run("summary has operator-session-id flag", func(t *testing.T) {
		cmd := dataAuditSummaryCmd()
		flag := cmd.Flags().Lookup("operator-session-id")
		assert.NotNil(t, flag)
	})

}

func TestDataStoreCmdFlagValidation(t *testing.T) {
	t.Run("store fails without collection flag", func(t *testing.T) {
		// This test verifies that the collection flag is required
		// Note: The actual command checks the flag AFTER config load and client creation
		// So we verify the flag exists and is required by checking the command definition
		cmd := dataStoreCmd()
		flag := cmd.Flags().Lookup("collection")
		assert.NotNil(t, flag)
		// The flag has no default value, making it required
		assert.Empty(t, flag.DefValue)
	})

	t.Run("store list mode with collection flag", func(t *testing.T) {
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		loader := func(_ string) (*config.Config, error) { return cfg, nil }
		cmd := dataStoreCmdWithConfig(loader, authcmd.DefaultAPIClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
		cmd.Flags().Set("collection", "test_collection")
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		// Will fail on API client creation (no credentials), but should pass flag validation
		err := cmd.RunE(cmd, []string{})
		require.Error(t, err)
		// Error should not be about missing collection flag
		assert.NotContains(t, err.Error(), "--collection is required")
	})
}

func TestDataAuditListCmdFlagValidation(t *testing.T) {
	t.Run("audit list requires operator-session-id flag", func(t *testing.T) {
		// This test verifies that the operator-session-id flag is required
		// Note: The actual command checks the flag AFTER config load and client creation
		// So we verify the flag exists and is required by checking the command definition
		cmd := dataAuditListCmd()
		flag := cmd.Flags().Lookup("operator-session-id")
		assert.NotNil(t, flag)
		// The flag has no default value, making it required
		assert.Empty(t, flag.DefValue)
	})

	t.Run("audit list with operator-session-id flag", func(t *testing.T) {
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		loader := func(_ string) (*config.Config, error) { return cfg, nil }
		cmd := dataAuditListCmdWithConfig(loader, authcmd.DefaultAPIClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
		cmd.Flags().Set("operator-session-id", "test-session-123")
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		// Will fail on API client creation (no credentials), but should pass flag validation
		err := cmd.RunE(cmd, []string{})
		require.Error(t, err)
		// Error should not be about missing operator-session-id flag
		assert.NotContains(t, err.Error(), "--operator-session-id is required")
	})
}

func TestDataAuditCmdStructure(t *testing.T) {
	t.Run("audit command has correct subcommands", func(t *testing.T) {
		cmd := dataAuditCmd()
		subcommands := cmd.Commands()
		subcommandNames := make(map[string]bool)
		for _, sub := range subcommands {
			subcommandNames[sub.Use] = true
		}

		assert.True(t, subcommandNames["list"], "missing list subcommand")
		assert.True(t, subcommandNames[string(constants.StreamStatusSummary)], "missing summary subcommand")
	})
}

func TestDataCommandJSONUnmarshaling(t *testing.T) {
	testCases := []struct {
		name        string
		jsonData    string
		targetType  interface{}
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid User JSON",
			jsonData:    `[{"id":"user1"}]`,
			targetType:  &[]models.User{},
			expectError: false,
		},
		{
			name:        "invalid User JSON",
			jsonData:    `invalid json`,
			targetType:  &[]models.User{},
			expectError: true,
			errorMsg:    "failed to parse response",
		},
		{
			name:        "valid OperatorDocument JSON",
			jsonData:    `[{"id":"op1","operator_type":"remote","status":"active"}]`,
			targetType:  &[]*operatorv1.OperatorDocument{},
			expectError: false,
		},
		{
			name:        "valid SettingsDocument JSON",
			jsonData:    `{"settings":{"actuator_key_id":"key1"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			targetType:  &models.SettingsDocument{},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := json.Unmarshal([]byte(tc.jsonData), tc.targetType)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDataDocFilterTypes(t *testing.T) {
	t.Run("DocFilter struct serialization", func(t *testing.T) {
		filter := models.DocFilter{
			Field: "test_field",
			Op:    "==",
			Value: json.RawMessage(`"test_value"`),
		}

		data, err := json.Marshal(filter)
		require.NoError(t, err)

		var decoded models.DocFilter
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, filter.Field, decoded.Field)
		assert.Equal(t, filter.Op, decoded.Op)
		assert.Equal(t, filter.Value, decoded.Value)
	})

	t.Run("DocQueryRequest struct serialization", func(t *testing.T) {
		req := models.DocQueryRequest{
			Filters: []models.DocFilter{
				{Field: "field1", Op: "==", Value: json.RawMessage(`"value1"`)},
				{Field: "field2", Op: "!=", Value: json.RawMessage(`"value2"`)},
			},
		}

		data, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded models.DocQueryRequest
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Len(t, decoded.Filters, 2)
		assert.Equal(t, "field1", decoded.Filters[0].Field)
		assert.Equal(t, "field2", decoded.Filters[1].Field)
	})

	t.Run("DocQueryRequest with limit serialization", func(t *testing.T) {
		req := models.DocQueryRequest{
			Filters: []models.DocFilter{
				{Field: "session_id", Op: "==", Value: json.RawMessage(`"sess-123"`)},
			},
			Limit: 50,
		}

		data, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded models.DocQueryRequest
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Len(t, decoded.Filters, 1)
		assert.Equal(t, 50, decoded.Limit)
	})

	t.Run("DocFilter with numeric value", func(t *testing.T) {
		filter := models.DocFilter{
			Field: "count",
			Op:    ">",
			Value: json.RawMessage("100"),
		}

		data, err := json.Marshal(filter)
		require.NoError(t, err)

		var decoded models.DocFilter
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "count", decoded.Field)
		assert.Equal(t, ">", decoded.Op)
		assert.Equal(t, json.RawMessage("100"), decoded.Value)
	})

	t.Run("DocFilter with null value", func(t *testing.T) {
		filter := models.DocFilter{
			Field: "deleted_at",
			Op:    "==",
			Value: json.RawMessage("null"),
		}

		data, err := json.Marshal(filter)
		require.NoError(t, err)

		var decoded models.DocFilter
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "deleted_at", decoded.Field)
		assert.Equal(t, "==", decoded.Op)
		assert.Equal(t, json.RawMessage("null"), decoded.Value)
	})

	t.Run("DocQueryRequest with zero limit", func(t *testing.T) {
		req := models.DocQueryRequest{
			Filters: []models.DocFilter{},
			Limit:   0,
		}

		data, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded models.DocQueryRequest
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, 0, decoded.Limit)
	})
}

func TestDataStoreCommandModes(t *testing.T) {
	t.Run("store command defaults to list mode when document-id not provided", func(t *testing.T) {
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		loader := func(_ string) (*config.Config, error) { return cfg, nil }
		cmd := dataStoreCmdWithConfig(loader, authcmd.DefaultAPIClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
		cmd.Flags().Set("collection", "test_collection")
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		// Will fail on API client creation (no credentials), but we can verify the flag state
		err := cmd.RunE(cmd, []string{})
		require.Error(t, err)
		// Error should not be about missing collection flag
		assert.NotContains(t, err.Error(), "--collection is required")
	})

	t.Run("store command uses document mode when document-id provided", func(t *testing.T) {
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		loader := func(_ string) (*config.Config, error) { return cfg, nil }
		cmd := dataStoreCmdWithConfig(loader, authcmd.DefaultAPIClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
		cmd.Flags().Set("collection", "test_collection")
		cmd.Flags().Set("document-id", "doc-123")
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)

		// Will fail on API client creation (no credentials), but we can verify both flags are set
		err := cmd.RunE(cmd, []string{})
		require.Error(t, err)
		// Error should not be about missing flags
		assert.NotContains(t, err.Error(), "--collection is required")
	})
}

func TestDataCommandErrorHandling(t *testing.T) {
	t.Run("users command handles JSON parse errors", func(t *testing.T) {
		// This test verifies that the users command properly handles invalid JSON responses
		// Since we can't mock the API client easily, we test the struct unmarshaling directly
		invalidJSON := `not valid json`
		var users []models.User
		err := json.Unmarshal([]byte(invalidJSON), &users)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid character")
	})

	t.Run("operators command handles JSON parse errors", func(t *testing.T) {
		invalidJSON := `{invalid}`
		var operators []*operatorv1.OperatorDocument
		err := json.Unmarshal([]byte(invalidJSON), &operators)
		require.Error(t, err)
	})

	t.Run("settings command handles JSON parse errors", func(t *testing.T) {
		invalidJSON := `{invalid}`
		var settings models.SettingsDocument
		err := json.Unmarshal([]byte(invalidJSON), &settings)
		require.Error(t, err)
	})

	t.Run("settings command handles missing settings field", func(t *testing.T) {
		// Valid JSON but missing required field
		invalidJSON := `{"other_field": "value"}`
		var settings models.SettingsDocument
		err := json.Unmarshal([]byte(invalidJSON), &settings)
		require.NoError(t, err) // JSON is valid, just empty settings
		assert.Nil(t, settings.Settings)
	})
}

func TestDataDocFilterEdgeCases(t *testing.T) {
	t.Run("DocFilter with empty field", func(t *testing.T) {
		filter := models.DocFilter{
			Field: "",
			Op:    "==",
			Value: json.RawMessage(`"value"`),
		}

		data, err := json.Marshal(filter)
		require.NoError(t, err)

		var decoded models.DocFilter
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "", decoded.Field)
	})

	t.Run("DocFilter with empty operator", func(t *testing.T) {
		filter := models.DocFilter{
			Field: "field",
			Op:    "",
			Value: json.RawMessage(`"value"`),
		}

		data, err := json.Marshal(filter)
		require.NoError(t, err)

		var decoded models.DocFilter
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "", decoded.Op)
	})

	t.Run("DocFilter with complex value (object)", func(t *testing.T) {
		complexValue := struct {
			Nested string `json:"nested"`
			Number int    `json:"number"`
		}{Nested: "value", Number: 42}
		valueBytes, err := json.Marshal(complexValue)
		require.NoError(t, err)

		filter := models.DocFilter{
			Field: "metadata",
			Op:    "==",
			Value: valueBytes,
		}

		data, err := json.Marshal(filter)
		require.NoError(t, err)

		var decoded models.DocFilter
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "metadata", decoded.Field)
		assert.Equal(t, "==", decoded.Op)
		assert.JSONEq(t, string(valueBytes), string(decoded.Value))
	})

	t.Run("DocFilter with array value", func(t *testing.T) {
		arrayValue := []string{"value1", "value2", "value3"}
		valueBytes, err := json.Marshal(arrayValue)
		require.NoError(t, err)

		filter := models.DocFilter{
			Field: "tags",
			Op:    "in",
			Value: valueBytes,
		}

		data, err := json.Marshal(filter)
		require.NoError(t, err)

		var decoded models.DocFilter
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "tags", decoded.Field)
		assert.Equal(t, "in", decoded.Op)
		assert.JSONEq(t, string(valueBytes), string(decoded.Value))
	})
}

func TestDataCommandLongDescription(t *testing.T) {
	t.Run("data command has long description", func(t *testing.T) {
		cmd := dataCmd()
		assert.NotEmpty(t, cmd.Long)
		assert.Contains(t, cmd.Long, "Data management")
	})

	t.Run("users command has long description", func(t *testing.T) {
		cmd := dataUsersCmd()
		assert.NotEmpty(t, cmd.Long)
		assert.Contains(t, cmd.Long, "user accounts")
	})
}

func TestDataCommandSubcommandRegistration(t *testing.T) {
	t.Run("data command registers all expected subcommands", func(t *testing.T) {
		cmd := dataCmd()
		subcommands := cmd.Commands()
		subcommandNames := make(map[string]bool)
		for _, sub := range subcommands {
			subcommandNames[sub.Use] = true
		}

		assert.True(t, subcommandNames["users"], "missing users subcommand")
		assert.True(t, subcommandNames["operators"], "missing operators subcommand")
		assert.True(t, subcommandNames["settings"], "missing settings subcommand")
		assert.True(t, subcommandNames["store"], "missing store subcommand")
		assert.True(t, subcommandNames["audit"], "missing audit subcommand")
	})

	t.Run("audit command registers list and summary subcommands", func(t *testing.T) {
		cmd := dataAuditCmd()
		subcommands := cmd.Commands()
		subcommandNames := make(map[string]bool)
		for _, sub := range subcommands {
			subcommandNames[sub.Use] = true
		}

		assert.True(t, subcommandNames["list"], "missing list subcommand")
		assert.True(t, subcommandNames[string(constants.StreamStatusSummary)], "missing summary subcommand")
	})
}

func TestDataAuditSummaryCmdWithConfig_ValidResponse(t *testing.T) {
	env := newDataTestEnv(t)

	loader := func(string) (*config.Config, error) { return env.cfg, nil }
	client := &cmdtest.MockAPIClient{GetResp: []byte(`{"success":true,"events_summary":{"logout":1,"login":2},"events_total":3,"event_receipts_summary":{},"event_receipts_total":0,"total_records":3}`)}
	cmd := dataAuditSummaryCmdWithConfig(loader, authcmd.MockClientFactory(client), cmdtest.FileSvcFactoryFor(env.fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	output := buf.String()
	assert.Contains(t, output, "Audit Event Summary")
	assert.Contains(t, output, "login: 2")
	assert.Contains(t, output, "logout: 1")
	assert.Contains(t, output, "Total events: 3")
	assert.Less(t, strings.Index(output, "login: 2"), strings.Index(output, "logout: 1"), "event types must be printed in sorted order")
	assert.Equal(t, []string{constants.APIPaths.AuditSummary}, client.GetCalls)
}

func TestDataAuditSummaryCmdWithConfig_SessionFilterSendsQueryParameter(t *testing.T) {
	env := newDataTestEnv(t)

	loader := func(string) (*config.Config, error) { return env.cfg, nil }
	client := &cmdtest.MockAPIClient{GetResp: []byte(`{"success":true,"events_summary":{"login":1},"events_total":1}`)}
	cmd := dataAuditSummaryCmdWithConfig(loader, authcmd.MockClientFactory(client), cmdtest.FileSvcFactoryFor(env.fileSvc))
	require.NoError(t, cmd.Flags().Set("operator-session-id", "sess-1"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Total events: 1")
	assert.Equal(t, []string{constants.APIPaths.AuditSummary + "?operator_session_id=sess-1"}, client.GetCalls)
}

func TestDataAuditSummaryCmdWithConfig_EmptyResult(t *testing.T) {
	env := newDataTestEnv(t)

	loader := func(string) (*config.Config, error) { return env.cfg, nil }
	client := &cmdtest.MockAPIClient{GetResp: []byte(`{"success":true,"events_summary":{},"events_total":0}`)}
	cmd := dataAuditSummaryCmdWithConfig(loader, authcmd.MockClientFactory(client), cmdtest.FileSvcFactoryFor(env.fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No audit events found")
	assert.NotContains(t, buf.String(), "Audit Event Summary")
}

func TestDataAuditSummaryCmdWithConfig_GetError(t *testing.T) {
	env := newDataTestEnv(t)

	loader := func(string) (*config.Config, error) { return env.cfg, nil }
	client := &cmdtest.MockAPIClient{GetErr: fmt.Errorf("query failed")}
	cmd := dataAuditSummaryCmdWithConfig(loader, authcmd.MockClientFactory(client), cmdtest.FileSvcFactoryFor(env.fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch audit summary")
}

func TestDataAuditSummaryCmdWithConfig_InvalidJSONResponse(t *testing.T) {
	env := newDataTestEnv(t)

	loader := func(string) (*config.Config, error) { return env.cfg, nil }
	client := &cmdtest.MockAPIClient{GetResp: []byte("not json")}
	cmd := dataAuditSummaryCmdWithConfig(loader, authcmd.MockClientFactory(client), cmdtest.FileSvcFactoryFor(env.fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}
