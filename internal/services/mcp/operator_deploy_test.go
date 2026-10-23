// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperatorDeployTool_Name(t *testing.T) {
	t.Parallel()
	tool := &OperatorDeployTool{}
	require.Equal(t, "operator_deploy", tool.Name())
}

func TestOperatorDeployTool_Description(t *testing.T) {
	t.Parallel()
	tool := &OperatorDeployTool{}
	require.NotEmpty(t, tool.Description())
	require.Contains(t, tool.Description(), "operator")
}

func TestOperatorDeployTool_InputSchema(t *testing.T) {
	t.Parallel()
	tool := &OperatorDeployTool{}
	schema := tool.InputSchema()

	require.Equal(t, "object", schema.Type)
	require.Contains(t, schema.Required, "hostnames")

	properties := schema.Properties
	require.Contains(t, properties, "hostnames")
	require.Contains(t, properties, "operator_binary")
	require.Contains(t, properties, "operator_args")
	require.Contains(t, properties, "timeout")

	require.Equal(t, "array", properties["hostnames"].Type)
	require.Equal(t, "string", properties["operator_binary"].Type)
	require.Equal(t, "array", properties["operator_args"].Type)
	require.Equal(t, "integer", properties["timeout"].Type)
}
