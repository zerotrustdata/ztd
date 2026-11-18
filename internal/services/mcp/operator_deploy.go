// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// OperatorDeployTool deploys the ztd operator to target hostnames as the typed
// OPERATOR_DEPLOY governed action.
type OperatorDeployTool struct{}

// Name returns the tool identifier.
func (t *OperatorDeployTool) Name() string {
	return constants.MCPToolOperatorDeploy
}

// Description returns a human-readable description.
func (t *OperatorDeployTool) Description() string {
	return "Deploys and starts the ztd operator on one or more target hostnames as a governed action. " +
		"Enrollment of deployed operators remains human-approved."
}

// InputSchema returns the JSON Schema for tool validation.
func (t *OperatorDeployTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"hostnames": {
				Type:        "array",
				Description: "List of target hostnames to deploy the ztd operator to",
			},
		},
		Required: []string{"hostnames"},
	}
}

// ActionType returns the typed governed action produced by this tool.
func (t *OperatorDeployTool) ActionType() constants.ActionType {
	return constants.ActionTypeOperatorDeploy
}

// Execute fails closed: operator_deploy must be routed as a typed
// OPERATOR_DEPLOY governed action, never as an MCP_CALL.
func (t *OperatorDeployTool) Execute(context.Context, json.RawMessage) (CallToolResult, error) {
	return governedActionOnly(t.Name())
}

// GovernedAction strictly decodes OperatorDeployRequest and builds the typed
// OperatorDeployRequested governed action.
func (t *OperatorDeployTool) GovernedAction(args json.RawMessage, binding GovernedActionBinding) (GovernedActionRequest, error) {
	req, err := decodeGovernedToolArguments[OperatorDeployRequest](t.Name(), args)
	if err != nil {
		return GovernedActionRequest{}, err
	}
	if err := validateHostnames(req.Hostnames); err != nil {
		return GovernedActionRequest{}, fmt.Errorf("%s: %w", t.Name(), err)
	}
	seen := make(map[string]struct{}, len(req.Hostnames))
	for _, h := range req.Hostnames {
		if strings.HasPrefix(h, "-") {
			return GovernedActionRequest{}, fmt.Errorf("%s: hostname %q must not start with '-': %w", t.Name(), h, constants.ErrGatewayInvalidToolArguments)
		}
		if _, dup := seen[h]; dup {
			return GovernedActionRequest{}, fmt.Errorf("%s: duplicate hostname %q: %w", t.Name(), h, constants.ErrGatewayInvalidToolArguments)
		}
		seen[h] = struct{}{}
	}
	return governedActionRequest(t.ActionType(), strings.Join(req.Hostnames, ","), &operatorv1.OperatorDeployRequested{
		ExecutionId: binding.ExecutionID,
		Hostnames:   req.Hostnames,
	})
}
