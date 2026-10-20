// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestParseAgentToolArguments_RejectsNullArguments(t *testing.T) {
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
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolAuditReceiptList)
	require.True(t, ok)

	args, failure := parseAgentToolArguments(spec, "")
	require.Nil(t, failure)
	args.Set("exclude_audit_reads", true)
	assert.Equal(t, true, args.Values()["exclude_audit_reads"])
}
