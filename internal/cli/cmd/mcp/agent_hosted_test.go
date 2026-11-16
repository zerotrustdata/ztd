// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type mockAgentGatewayClient struct {
	operatorsBody []byte
	getErr        error
	lastPath      string
}

func (m *mockAgentGatewayClient) Get(path string) ([]byte, error) {
	m.lastPath = path
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.operatorsBody, nil
}

func (m *mockAgentGatewayClient) Post(string, interface{}) ([]byte, error) {
	return nil, fmt.Errorf("unexpected Post")
}

func (m *mockAgentGatewayClient) Put(string, interface{}) ([]byte, error) {
	return nil, fmt.Errorf("unexpected Put")
}

func (m *mockAgentGatewayClient) Delete(string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected Delete")
}

func writeTestEvalRegistry(t *testing.T, fileSvc fs.RuntimeFileService, variants ...*evalv1.ModelVariant) {
	t.Helper()
	rawVariants := make([]json.RawMessage, 0, len(variants))
	for _, variant := range variants {
		raw, err := protojson.Marshal(variant)
		require.NoError(t, err)
		rawVariants = append(rawVariants, raw)
	}
	payload, err := json.Marshal(struct {
		Variants []json.RawMessage `json:"variants"`
	}{Variants: rawVariants})
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), evaluation.DefaultModelInventoryRelPath, payload, constants.PermFileReadOnly))
}

func wrapWithJSONRoot(t *testing.T, sub *cobra.Command, jsonEnabled bool) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	root := &cobra.Command{Use: "ztd"}
	root.PersistentFlags().Bool("json", false, "Emit machine-readable JSON output")
	if jsonEnabled {
		require.NoError(t, root.PersistentFlags().Set("json", "true"))
	}
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.AddCommand(sub)
	return root, &buf
}

func activeInferenceOperatorDoc(id, opType string, hosted []string, decisionOnly bool) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                id,
		OperatorType:      opType,
		OperatorSessionId: "sess-" + id,
		Status:            string(constants.OperatorStatusActive),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
			Capabilities:             models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityInference}),
			InferenceHostedProviders: hosted,
			InferenceDecisionOnly:    decisionOnly,
		},
	}
}

func TestAgentVerifyAndShow_HostedProviderAndRegistryReport(t *testing.T) {
	sampleVariants := []*evalv1.ModelVariant{
		{
			VariantId:      "var-claude-sonnet",
			ProviderClass:  constants.InferenceProviderAnthropic,
			ServedModelTag: "claude-sonnet-4-5",
			ModelDigest:    "digest-claude-sonnet",
		},
		{
			VariantId:      "var-claude-haiku",
			ProviderClass:  constants.InferenceProviderAnthropic,
			ServedModelTag: "claude-haiku-4-5",
			ModelDigest:    "digest-claude-haiku",
		},
		{
			VariantId:      "var-gemini-flash",
			ProviderClass:  constants.InferenceProviderGemini,
			ServedModelTag: "gemini-3.6-flash",
			ModelDigest:    "digest-gemini-flash",
		},
		{
			VariantId:      "var-openai-gpt",
			ProviderClass:  constants.InferenceProviderOpenAI,
			ServedModelTag: "gpt-5",
			ModelDigest:    "digest-openai-gpt",
		},
		{
			VariantId:      "var-ollama-qwen",
			ProviderClass:  constants.InferenceProviderOllama,
			ServedModelTag: "qwen3:4b",
			ModelDigest:    "digest-ollama-qwen",
		},
	}

	cases := []struct {
		name               string
		agentID            string
		operators          []*operatorv1.OperatorDocument
		clientFactoryErr   error
		getErr             error
		registryVariants   []*evalv1.ModelVariant
		wantProviderClass  string
		wantGatewayStatus  string
		wantServedBy       []string
		wantRegistryStatus string
		wantRegistryTags   []string
		wantHumanSubstrs   []string
	}{
		{
			name:    "claude served by embedded inference operator with matching registry models",
			agentID: "claude",
			operators: []*operatorv1.OperatorDocument{
				activeInferenceOperatorDoc(string(constants.DocIDEmbeddedOperator), string(constants.OperatorTypeEmbedded), []string{constants.InferenceProviderAnthropic, constants.InferenceProviderGemini}, false),
			},
			registryVariants:   sampleVariants,
			wantProviderClass:  constants.InferenceProviderAnthropic,
			wantGatewayStatus:  gatewayHostedStatusServed,
			wantServedBy:       []string{string(constants.DocIDEmbeddedOperator)},
			wantRegistryStatus: evalRegistryStatusLoaded,
			wantRegistryTags:   []string{"claude-haiku-4-5", "claude-sonnet-4-5"},
			wantHumanSubstrs:   []string{"anthropic", "served (" + string(constants.DocIDEmbeddedOperator) + ")", "claude-haiku-4-5, claude-sonnet-4-5"},
		},
		{
			name:    "codex served by remote inference operator with empty matching registry",
			agentID: "codex",
			operators: []*operatorv1.OperatorDocument{
				activeInferenceOperatorDoc("remote-gpu-1", string(constants.OperatorTypeRemote), []string{constants.InferenceProviderOpenAI}, false),
			},
			registryVariants: []*evalv1.ModelVariant{
				{VariantId: "var-gemini-flash", ProviderClass: constants.InferenceProviderGemini, ServedModelTag: "gemini-3.6-flash", ModelDigest: "digest-gemini-flash"},
			},
			wantProviderClass:  constants.InferenceProviderOpenAI,
			wantGatewayStatus:  gatewayHostedStatusServed,
			wantServedBy:       []string{"remote-gpu-1"},
			wantRegistryStatus: evalRegistryStatusEmpty,
			wantRegistryTags:   nil,
			wantHumanSubstrs:   []string{"openai", "served (remote-gpu-1)", "none"},
		},
		{
			name:    "gemini not served when inference operator lacks gemini and decision-only operator is ignored",
			agentID: "gemini",
			operators: []*operatorv1.OperatorDocument{
				activeInferenceOperatorDoc("embedded-operator", string(constants.OperatorTypeEmbedded), []string{constants.InferenceProviderAnthropic}, false),
				activeInferenceOperatorDoc("decision-only-op", string(constants.OperatorTypeRemote), []string{constants.InferenceProviderGemini}, true),
				{
					Id:                "stopped-op",
					OperatorType:      string(constants.OperatorTypeRemote),
					OperatorSessionId: "sess-stopped",
					Status:            string(constants.OperatorStatusStopped),
					RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
						Capabilities:             models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityInference}),
						InferenceHostedProviders: []string{constants.InferenceProviderGemini},
					},
				},
			},
			registryVariants:   sampleVariants,
			wantProviderClass:  constants.InferenceProviderGemini,
			wantGatewayStatus:  gatewayHostedStatusNotServed,
			wantRegistryStatus: evalRegistryStatusLoaded,
			wantRegistryTags:   []string{"gemini-3.6-flash"},
			wantHumanSubstrs:   []string{"gemini", "not served", "--inference-hosted-providers gemini", "gemini-3.6-flash"},
		},
		{
			name:               "claude with CLI not enrolled and absent registry",
			agentID:            "claude",
			clientFactoryErr:   constants.ErrNotAuthenticated,
			wantProviderClass:  constants.InferenceProviderAnthropic,
			wantGatewayStatus:  gatewayHostedStatusUnavailable,
			wantRegistryStatus: evalRegistryStatusEmpty,
			wantHumanSubstrs:   []string{"anthropic", "unavailable (CLI not enrolled)", "none"},
		},
		{
			name:               "gemini with gateway query failure",
			agentID:            "gemini",
			getErr:             fmt.Errorf("dial tcp refused"),
			registryVariants:   sampleVariants,
			wantProviderClass:  constants.InferenceProviderGemini,
			wantGatewayStatus:  gatewayHostedStatusUnavailable,
			wantRegistryStatus: evalRegistryStatusLoaded,
			wantRegistryTags:   []string{"gemini-3.6-flash"},
			wantHumanSubstrs:   []string{"gemini", "unavailable (registry query failed)", "gemini-3.6-flash"},
		},
		{
			name:             "goose has no matching hosted provider class",
			agentID:          "goose",
			clientFactoryErr: fmt.Errorf("must not fail even if unauthenticated"),
			registryVariants: sampleVariants,
		},
		{
			name:             "devin has no matching hosted provider class",
			agentID:          "devin",
			clientFactoryErr: fmt.Errorf("must not fail even if unauthenticated"),
			registryVariants: sampleVariants,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := testutil.TempDir(t)
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)

			fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
			if len(tc.registryVariants) > 0 {
				writeTestEvalRegistry(t, fileSvc, tc.registryVariants...)
			}

			var mockBody []byte
			if tc.operators != nil {
				var err error
				mockBody, err = json.Marshal(models.OperatorSlotResponse{Success: true, Operators: tc.operators})
				require.NoError(t, err)
			}
			clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
				if tc.clientFactoryErr != nil {
					return nil, tc.clientFactoryErr
				}
				return &mockAgentGatewayClient{operatorsBody: mockBody, getErr: tc.getErr}, nil
			}

			// 1. verify human output
			verifyCmd := agentVerifyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
			verifyRoot, verifyBuf := wrapWithJSONRoot(t, verifyCmd, false)
			verifyRoot.SetArgs([]string{"verify", tc.agentID})
			require.NoError(t, verifyRoot.Execute())
			assert.Contains(t, verifyBuf.String(), "PASS "+tc.agentID+":")
			for _, sub := range tc.wantHumanSubstrs {
				assert.Contains(t, verifyBuf.String(), sub)
			}
			if tc.wantProviderClass == "" {
				assert.NotContains(t, verifyBuf.String(), "Hosted provider")
				assert.NotContains(t, verifyBuf.String(), "Eval registry")
			}

			// 2. verify --json output
			verifyJSONCmd := agentVerifyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
			verifyJSONRoot, verifyJSONBuf := wrapWithJSONRoot(t, verifyJSONCmd, true)
			verifyJSONRoot.SetArgs([]string{"verify", tc.agentID})
			require.NoError(t, verifyJSONRoot.Execute())

			var decodedVerify agentVerifyJSON
			require.NoError(t, json.Unmarshal(verifyJSONBuf.Bytes(), &decodedVerify))
			assert.Equal(t, tc.agentID, decodedVerify.Agent)
			assert.True(t, decodedVerify.Passed)
			assertHostedReport(t, decodedVerify.HostedProvider, tc.wantProviderClass, tc.wantGatewayStatus, tc.wantServedBy, tc.wantRegistryStatus, tc.wantRegistryTags)

			// 3. show human output
			showCmd := agentShowCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
			showRoot, showBuf := wrapWithJSONRoot(t, showCmd, false)
			showRoot.SetArgs([]string{"show", tc.agentID})
			require.NoError(t, showRoot.Execute())
			assert.Contains(t, showBuf.String(), "ztd Gateway MCP Configurations")
			assert.Contains(t, showBuf.String(), "Stdio Transport")
			for _, sub := range tc.wantHumanSubstrs {
				assert.Contains(t, showBuf.String(), sub)
			}
			for _, forbidden := range []string{"ztd.local", "IP Address", "mTLS", "clientCertificate", "clientKey", "caCertificate", ".key"} {
				assert.NotContains(t, showBuf.String(), forbidden)
			}
			if tc.wantProviderClass == "" {
				assert.NotContains(t, showBuf.String(), "Hosted Inference & Eval Registry")
			}

			// 4. show --json output
			showJSONCmd := agentShowCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
			showJSONRoot, showJSONBuf := wrapWithJSONRoot(t, showJSONCmd, true)
			showJSONRoot.SetArgs([]string{"show", tc.agentID})
			require.NoError(t, showJSONRoot.Execute())

			var decodedShow agentShowJSON
			require.NoError(t, json.Unmarshal(showJSONBuf.Bytes(), &decodedShow))
			assert.Equal(t, tc.agentID, decodedShow.Agent)
			require.NotNil(t, decodedShow.MCPConfig)
			assert.Contains(t, decodedShow.MCPConfig.MCPServers, "ztd-native")
			assertHostedReport(t, decodedShow.HostedProvider, tc.wantProviderClass, tc.wantGatewayStatus, tc.wantServedBy, tc.wantRegistryStatus, tc.wantRegistryTags)
		})
	}
}

func TestAgentHosted_CorruptRegistryReportsUnavailableWithoutFailing(t *testing.T) {
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, fileSvc.WriteFile(context.Background(), evaluation.DefaultModelInventoryRelPath, []byte("{corrupt"), constants.PermFileReadOnly))

	clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return nil, constants.ErrNotAuthenticated
	}

	verifyCmd := agentVerifyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	verifyRoot, verifyBuf := wrapWithJSONRoot(t, verifyCmd, true)
	verifyRoot.SetArgs([]string{"verify", "claude"})
	require.NoError(t, verifyRoot.Execute())

	var decoded agentVerifyJSON
	require.NoError(t, json.Unmarshal(verifyBuf.Bytes(), &decoded))
	require.NotNil(t, decoded.HostedProvider)
	assert.Equal(t, evalRegistryStatusUnavailable, decoded.HostedProvider.EvalRegistryStatus)
	assert.NotEmpty(t, decoded.HostedProvider.EvalRegistryReason)
}

func TestAgentHosted_MultipleOperatorsCaseInsensitiveAndUserIDQuery(t *testing.T) {
	home := testutil.TempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, &auth.Credentials{
		UserID:       "user-42",
		CLISessionID: "cli-42",
	}))

	operatorsBody, err := json.Marshal(models.OperatorSlotResponse{
		Success: true,
		Operators: []*operatorv1.OperatorDocument{
			activeInferenceOperatorDoc("remote-b", string(constants.OperatorTypeRemote), []string{constants.InferenceProviderAnthropic}, false),
			activeInferenceOperatorDoc("embedded-operator", string(constants.OperatorTypeEmbedded), []string{constants.InferenceProviderAnthropic}, false),
			activeInferenceOperatorDoc("remote-b", string(constants.OperatorTypeRemote), []string{constants.InferenceProviderAnthropic}, false),
		},
	})
	require.NoError(t, err)

	mockClient := &mockAgentGatewayClient{operatorsBody: operatorsBody}
	clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return mockClient, nil
	}

	verifyCmd := agentVerifyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	verifyRoot, verifyBuf := wrapWithJSONRoot(t, verifyCmd, true)
	verifyRoot.SetArgs([]string{"verify", "CLAUDE"})
	require.NoError(t, verifyRoot.Execute())

	assert.Equal(t, constants.APIPaths.Operators+"?user_id=user-42", mockClient.lastPath)

	var decoded agentVerifyJSON
	require.NoError(t, json.Unmarshal(verifyBuf.Bytes(), &decoded))
	assert.Equal(t, "claude", decoded.Agent)
	require.NotNil(t, decoded.HostedProvider)
	assert.Equal(t, constants.InferenceProviderAnthropic, decoded.HostedProvider.ProviderClass)
	assert.Equal(t, gatewayHostedStatusServed, decoded.HostedProvider.GatewayStatus)
	assert.Equal(t, []string{"embedded-operator", "remote-b"}, decoded.HostedProvider.ServedByOperators)
}

func assertHostedReport(
	t *testing.T,
	report *agentHostedProviderReport,
	wantClass string,
	wantGatewayStatus string,
	wantServedBy []string,
	wantRegistryStatus string,
	wantTags []string,
) {
	t.Helper()
	if wantClass == "" {
		assert.Nil(t, report)
		return
	}
	require.NotNil(t, report)
	assert.Equal(t, wantClass, report.ProviderClass)
	assert.Equal(t, wantGatewayStatus, report.GatewayStatus)
	assert.Equal(t, wantServedBy, report.ServedByOperators)
	assert.Equal(t, wantRegistryStatus, report.EvalRegistryStatus)
	gotTags := make([]string, 0, len(report.EvalRegistryModels))
	for _, m := range report.EvalRegistryModels {
		gotTags = append(gotTags, m.ServedModelTag)
	}
	if len(wantTags) == 0 {
		assert.Empty(t, gotTags)
	} else {
		assert.Equal(t, wantTags, gotTags)
	}
}
