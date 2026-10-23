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
	"os"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// SysInfoTool provides system information including hostname, OS version, kernel, uptime, and load average.
type SysInfoTool struct{}

// Name returns the tool identifier.
func (t *SysInfoTool) Name() string {
	return constants.MCPToolSysInfo
}

// Description returns a human-readable description.
func (t *SysInfoTool) Description() string {
	return "Provides system information including hostname, OS version, kernel, uptime, and load average."
}

// InputSchema returns the JSON Schema for tool validation.
func (t *SysInfoTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type:       "object",
		Properties: make(map[string]*PropertySchema),
	}
}

// Execute implements the tool logic.
func (t *SysInfoTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req SysInfoRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("sys_info: unmarshal arguments: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return CallToolResult{}, fmt.Errorf("sys_info: get hostname: %w", err)
	}

	if ctx.Err() != nil {
		return CallToolResult{}, ctx.Err()
	}

	osInfo := collectSysInfo(ctx)

	result := SysInfoResult{
		Hostname: hostname,
		OS:       osInfo,
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("sys_info: marshal result: %w", err)
	}

	return CallToolResult{
		Content: []TextContent{
			{
				Type: "text",
				Text: string(resultJSON),
			},
		},
	}, nil
}

// formatUptime formats an uptime duration in seconds into human-readable format.
func formatUptime(seconds int64) string {
	if seconds < 0 {
		return "unknown"
	}

	duration := time.Duration(seconds) * time.Second
	days := int(duration.Hours() / 24)
	hours := int(duration.Hours()) % 24
	minutes := int(duration.Minutes()) % 60
	return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
}
