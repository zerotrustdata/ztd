// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
)

func TestParseAgentToolArguments_RejectsNullArguments(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolEventReceiptList)
	require.True(t, ok)

	_, failure := parseAgentToolArguments(spec, "null")
	require.NotNil(t, failure)
	assert.Equal(t, agentToolErrorExecution, failure.ErrorType)
	assert.Contains(t, failure.Error, constants.MCPToolEventReceiptList)
	assert.Contains(t, failure.Error, "null")
}

func TestParseAgentToolArguments_EmptyArgumentsAcceptInjectedDefaults(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolEventReceiptList)
	require.True(t, ok)

	args, failure := parseAgentToolArguments(spec, "")
	require.Nil(t, failure)
	args.SetEventReceiptListDefaults("investigation-1")
	request, ok := args.EventReceiptList()
	require.True(t, ok)
	assert.Equal(t, mcp.EventReceiptListRequest{InvestigationID: "investigation-1", ExcludeEventReceiptReads: true, Limit: agentToolDefaultHistoryLimit}, request)
	canonical, err := args.CanonicalJSON()
	require.NoError(t, err)
	assert.Equal(t, `{"exclude_event_receipt_reads":true,"investigation_id":"investigation-1","limit":20}`, canonical) //nolint:testifylint // canonical bytes prove defaults survive
}

func TestDecodeAgentToolRequest_ValidatesTypedArrayItems(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolSearchDirectory)
	require.True(t, ok)

	for _, raw := range []string{
		`{"path":"logs","pattern":"ERROR","include":[1]}`,
		`{"path":"logs","pattern":"ERROR","include":[null]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			request := decodeAgentToolRequest(spec, true, raw)
			require.NotNil(t, request.Failure)
			assert.Nil(t, request.Shell)
			assert.Contains(t, request.Failure.Error, "include")
			assert.Contains(t, request.Failure.Error, "string")
		})
	}
}

func TestParseAgentToolArguments_PreservesSchemaNullAndUnknownFieldBehavior(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolSearchDirectory)
	require.True(t, ok)

	args, failure := parseAgentToolArguments(spec, `{"path":"logs","pattern":"ERROR","include":null}`)
	require.Nil(t, failure)
	assert.Equal(t, `{"include":null,"path":"logs","pattern":"ERROR"}`, args.rawJSON) //nolint:testifylint // canonical bytes prove null preservation

	_, failure = parseAgentToolArguments(spec, `{"path":"logs","pattern":"ERROR","unexpected":true}`)
	require.NotNil(t, failure)
	assert.Contains(t, failure.Error, "unexpected")

	_, failure = parseAgentToolArguments(spec, `{"path":"logs","pattern":"ERROR"} {"path":"ignored"}`)
	require.NotNil(t, failure)
	assert.Contains(t, failure.Error, "multiple JSON values")
}

func TestParseAgentToolArguments_RejectsValuesOutsideDeclaredEnum(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolCloudMetadata)
	require.True(t, ok)

	_, failure := parseAgentToolArguments(spec, `{"operation":"metadata"}`)
	require.NotNil(t, failure)
	assert.Contains(t, failure.Error, "Input should be one of")
}

func TestParseAgentToolArguments_RejectsMissingNullAndCoercedArguments(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolReadFile)
	require.True(t, ok)

	for name, raw := range map[string]string{
		"missing required": `{"offset":1}`,
		"null required":    `{"path":null}`,
		"string integer":   `{"path":"notes.txt","offset":"2"}`,
		"fraction integer": `{"path":"notes.txt","offset":2.5}`,
		"integer overflow": `{"path":"notes.txt","offset":9223372036854775808}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, failure := parseAgentToolArguments(spec, raw)
			require.NotNil(t, failure)
			assert.Equal(t, agentToolErrorExecution, failure.ErrorType)
		})
	}
}

func TestDecodeAgentToolRequest_RecordsTypedShellForm(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolRunShellCommand)
	require.True(t, ok)

	request := decodeAgentToolRequest(spec, true, `{"args":["hello"], "command":"echo"}`)
	require.Nil(t, request.Failure)
	require.NotNil(t, request.Shell)
	assert.Equal(t, models.RunShellCommandRequest{Command: "echo", Args: []string{"hello"}}, *request.Shell)
	assert.Equal(t, request.Shell.CanonicalJSON(), request.ArgumentsJSON)
}

func TestDecodeAgentToolRequest_SchemaInvalidShellRecordsGenericForm(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolRunShellCommand)
	require.True(t, ok)

	request := decodeAgentToolRequest(spec, true, `{"args": ["hello"]}`)
	require.NotNil(t, request.Failure)
	assert.Nil(t, request.Shell)
	assert.Equal(t, `{"args":["hello"]}`, request.ArgumentsJSON) //nolint:testifylint // exact bytes, not JSON equivalence
}

func TestDecodeAgentToolRequest_UndeclaredToolRecordsGenericForm(t *testing.T) {
	request := decodeAgentToolRequest(agentToolSpec{}, false, `{"command": "echo"}`)
	assert.Nil(t, request.Failure)
	assert.Nil(t, request.Shell)
	assert.Equal(t, `{"command":"echo"}`, request.ArgumentsJSON) //nolint:testifylint // exact bytes, not JSON equivalence
}

func TestAgentToolSpecFromDeclaration_RoundTripsCatalogSchema(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	scope, err := catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}})
	require.NoError(t, err)
	want, ok := scope.Spec(constants.MCPToolRunShellCommand)
	require.True(t, ok)
	declaration := scope.Declarations()[0]

	got, err := agentToolSpecFromDeclaration(AgentTraceTool{Name: declaration.GetName(), Description: declaration.GetDescription(), JSONSchema: declaration.GetJsonSchema()})
	require.NoError(t, err)
	assert.Equal(t, want.Properties, got.Properties)
	assert.Equal(t, want.Required, got.Required)

	_, err = agentToolSpecFromDeclaration(AgentTraceTool{Name: "broken", JSONSchema: "{"})
	assert.Error(t, err)
}

func TestCanonicalizeToolJSON_RejectsTrailingJSON(t *testing.T) {
	_, err := canonicalizeToolJSON(`{"path":"logs"} {"path":"ignored"}`)
	require.ErrorIs(t, err, constants.ErrInvalidJSONBody)
}

func TestAgentToolCatalogContainsOnlyRegisteredNativeTools(t *testing.T) {
	registry := mcp.NewToolRegistry()
	require.NoError(t, mcp.RegisterNativeTools(registry))
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	var want []string
	for _, tool := range registry.List() {
		want = append(want, tool.Name())
	}
	sort.Strings(want)
	assert.Equal(t, want, catalog.ToolNames())
}

func TestAgentToolCatalogHasTypedContractsForEveryNativeTool(t *testing.T) {
	registry := mcp.NewToolRegistry()
	require.NoError(t, mcp.RegisterNativeTools(registry))

	for _, tool := range registry.List() {
		t.Run(tool.Name(), func(t *testing.T) {
			_, err := decodeAgentToolContract(tool.Name(), `{}`)
			require.NoError(t, err)
		})
	}
}

func TestAgentToolScopeRequiresRunShellCommandForTribunalAuthoring(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	_, err = catalog.Scope(ScenarioToolExpectations{TribunalCommands: true, AllowedTools: []string{"read_file"}})
	require.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)

	_, err = catalog.Scope(ScenarioToolExpectations{TribunalCommands: true, AllowedTools: []string{constants.MCPToolRunShellCommand}})
	require.NoError(t, err)
	_, err = catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}})
	assert.NoError(t, err)
}
