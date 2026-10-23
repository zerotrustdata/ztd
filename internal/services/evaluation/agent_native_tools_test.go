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
	t.Parallel()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolAuditReceiptList)
	require.True(t, ok)

	_, failure := parseAgentToolArguments(spec, "null")
	require.NotNil(t, failure)
	assert.Equal(t, agentToolErrorExecution, failure.ErrorType)
	assert.Contains(t, failure.Error, constants.MCPToolAuditReceiptList)
	assert.Contains(t, failure.Error, "null")
}

func TestParseAgentToolArguments_EmptyArgumentsAcceptInjectedDefaults(t *testing.T) {
	t.Parallel()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolAuditReceiptList)
	require.True(t, ok)

	args, failure := parseAgentToolArguments(spec, "")
	require.Nil(t, failure)
	args.Set("exclude_audit_reads", true)
	assert.Equal(t, true, args.Values()["exclude_audit_reads"])
}

func TestDecodeAgentToolRequest_RecordsTypedShellForm(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	request := decodeAgentToolRequest(agentToolSpec{}, false, `{"command": "echo"}`)
	assert.Nil(t, request.Failure)
	assert.Nil(t, request.Shell)
	assert.Equal(t, `{"command":"echo"}`, request.ArgumentsJSON) //nolint:testifylint // exact bytes, not JSON equivalence
}

func TestAgentToolSpecFromDeclaration_RoundTripsCatalogSchema(t *testing.T) {
	t.Parallel()
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

func TestAgentToolCatalogContainsOnlyRegisteredNativeTools(t *testing.T) {
	t.Parallel()
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

func TestAgentToolScopeRequiresRunShellCommandForTribunalAuthoring(t *testing.T) {
	t.Parallel()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	_, err = catalog.Scope(ScenarioToolExpectations{TribunalCommands: true, AllowedTools: []string{"read_file"}})
	require.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)

	_, err = catalog.Scope(ScenarioToolExpectations{TribunalCommands: true, AllowedTools: []string{constants.MCPToolRunShellCommand}})
	require.NoError(t, err)
	_, err = catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}})
	assert.NoError(t, err)
}
