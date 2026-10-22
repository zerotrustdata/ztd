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
	"github.com/zerotrustdata/ztd/internal/services/mcp"
)

func TestParseAgentToolArguments_RejectsNullArguments(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolAuditEventReceiptList)
	require.True(t, ok)

	_, failure := parseAgentToolArguments(spec, "null")
	require.NotNil(t, failure)
	assert.Equal(t, agentToolErrorExecution, failure.ErrorType)
	assert.Contains(t, failure.Error, constants.MCPToolAuditEventReceiptList)
	assert.Contains(t, failure.Error, "null")
}

func TestParseAgentToolArguments_EmptyArgumentsAcceptInjectedDefaults(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolAuditEventReceiptList)
	require.True(t, ok)

	args, failure := parseAgentToolArguments(spec, "")
	require.Nil(t, failure)
	args.Set("exclude_audit_reads", true)
	assert.Equal(t, true, args.Values()["exclude_audit_reads"])
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

func TestAgentToolScopeRequiresRunShellCommandForTribunalAuthoring(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	_, err = catalog.Scope(ScenarioToolExpectations{TribunalCommands: true, AllowedTools: []string{"read_file"}})
	assert.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)

	_, err = catalog.Scope(ScenarioToolExpectations{TribunalCommands: true, AllowedTools: []string{constants.MCPToolRunShellCommand}})
	assert.NoError(t, err)
	_, err = catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}})
	assert.NoError(t, err)
}
