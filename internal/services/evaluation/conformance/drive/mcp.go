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
	tool, args, err := mcpCall(a)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("mcp %s: %w", a.Case.Fault, err)
	}
	outcome, err := m.client.MCPToolsCall(ctx, m.persona, tool, args)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("mcp %s: tools/call: %w", a.Case.Fault, err)
	}
	return cases.Driven{Observations: []*evalv1.ConformanceObservation{Observe(outcome)}}, nil
}

// mcpCall maps a fault to the tool and arguments that submit it.
func mcpCall(a cases.Attempt) (string, client.ToolArgs, error) {
	switch a.Case.Fault {
	case cases.FaultNone, cases.FaultUnattendedMutation, cases.FaultAttendedApprove, cases.FaultAttendedDeny:
		argv := cases.MarkerArgv(a)
		return mcpShellTool, client.ShellCommandArgs{Command: argv[0], Args: argv[1:]}, nil
	case cases.FaultReadBaseline:
		return mcpReadTool, client.FSPathArgs{Path: a.ReadAbs}, nil
	case cases.FaultDoctrine:
		return mcpShellTool, client.ShellCommandArgs{Command: a.Case.Command}, nil
	default:
		return "", nil, constants.ErrConformanceIngressUnsupported
	}
}
