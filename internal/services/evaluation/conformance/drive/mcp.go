// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// mcpShellTool and mcpReadTool are the native tool names the MCP driver calls.
// They come from the tool types, so a rename moves the driver with it.
var (
	mcpShellTool = (&mcp.RunShellCommandTool{}).Name()
	mcpReadTool  = (&mcp.FileReadTool{}).Name()
)

// MCP submits attempts as tools/call on the Gateway's MCP route. The executing
// Operator is the embedded one.
type MCP struct {
	client  *client.Client
	persona client.Persona
}

// NewMCP checks that the native tools this driver calls are served, by reading
// the Gateway's tools/list. The Gateway lists the native tools whether or not a
// downstream is configured, so the check holds in both cases.
func NewMCP(ctx context.Context, c *client.Client, p client.Persona) (*MCP, error) {
	if c == nil {
		return nil, fmt.Errorf("mcp: client: %w", constants.ErrMissingRequiredField)
	}
	resp, err := c.MCPToolsList(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("mcp: tools/list: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp: tools/list: %w: %s", constants.ErrJSONRPCErrorResponse, resp.Error.Message)
	}
	// Only names are read: the driver checks which tools are served and never
	// uses the descriptions or schemas.
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/list result: %w: %w", constants.ErrInvalidJSONResponse, err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, t := range list.Tools {
		names = append(names, t.Name)
	}
	if err := checkNativeTools(names); err != nil {
		return nil, err
	}
	return &MCP{client: c, persona: p}, nil
}

// checkNativeTools fails with ErrConformanceIngressUnsupported when a tool the
// driver calls is not among names.
func checkNativeTools(names []string) error {
	served := make(map[string]bool, len(names))
	for _, name := range names {
		served[name] = true
	}
	for _, name := range []string{mcpShellTool, mcpReadTool} {
		if !served[name] {
			return fmt.Errorf("mcp: native tool %q is not served: %w", name, constants.ErrConformanceIngressUnsupported)
		}
	}
	return nil
}

// Drive calls one tool for the attempt and returns one observation.
func (m *MCP) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	call, err := mcpCall(a)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("mcp %s: %w", a.Case.Fault, err)
	}
	outcome, err := m.client.MCPToolsCall(ctx, m.persona, call)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("mcp %s: tools/call: %w", a.Case.Fault, err)
	}
	return cases.Driven{Observations: []*evalv1.ConformanceObservation{Observe(outcome)}}, nil
}

// mcpCall maps a fault to the tools/call request that submits it. The arguments
// are the production request type of the tool, marshaled here.
func mcpCall(a cases.Attempt) (mcp.CallToolRequest, error) {
	var name string
	var args any
	switch a.Case.Fault {
	case cases.FaultNone, cases.FaultUnattendedMutation, cases.FaultAttendedApprove, cases.FaultAttendedDeny:
		argv := cases.MarkerArgv(a)
		name, args = mcpShellTool, models.RunShellCommandRequest{Command: argv[0], Args: argv[1:]}
	case cases.FaultReadBaseline:
		name, args = mcpReadTool, mcp.FileReadRequest{Path: a.ReadAbs}
	case cases.FaultDoctrine:
		name, args = mcpShellTool, models.RunShellCommandRequest{Command: a.Case.Command}
	default:
		return mcp.CallToolRequest{}, constants.ErrConformanceIngressUnsupported
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return mcp.CallToolRequest{}, fmt.Errorf("mcp: marshal %s arguments: %w", name, err)
	}
	return mcp.CallToolRequest{Name: name, Arguments: raw}, nil
}
