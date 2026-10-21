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
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/services/logging"
)

// isValidIdentifier validates SQLite identifiers to prevent SQL injection.
// SQLite identifiers must start with a letter or underscore, followed by letters, digits, or underscores.
func isValidIdentifier(name string) bool {
	if len(name) == 0 {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '_' {
				return false
			}
		} else {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
				return false
			}
		}
	}
	return true
}

// NativeToolHandler executes native tools compiled into the Node binary.
type NativeToolHandler struct {
	registry          *ToolRegistry
	logger            *slog.Logger
	auditReceiptQuery AuditReceiptQuery
	// operatorAuthority is set only on an executing Operator's handler. It
	// scopes audit_receipt_list to the Operator's own authoritative ledger.
	operatorAuthority *auditReceiptListAuthority
	// fsGrep runs search_file and search_directory against the executing
	// runtime's WorkDir.
	fsGrep *execution.FsGrepService
}

// NewNativeToolHandler creates a new native tool handler with all native tools
// explicitly registered. This avoids init()-based auto-registration and
// mutable global state.
func NewNativeToolHandler(logger *slog.Logger) (*NativeToolHandler, error) {
	registry := NewToolRegistry()
	if err := RegisterNativeTools(registry); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrMCPNativeToolRegistration, err)
	}
	return &NativeToolHandler{
		registry: registry,
		logger:   logger,
	}, nil
}

// NewOperatorNativeToolHandler constructs the native tool host for an
// executing Operator's Actuator. It serves the same registry as the Gateway
// against the Operator's own local audit store, and audit_receipt_list runs
// under the Operator-local ledger authority: investigation_id is mandatory and
// the query is scoped to operatorID across that Operator's sessions. The
// identity and local store are required because a missing one cannot
// establish authoritative evidence; grep is required so the search tools run
// against the Operator's own WorkDir.
func NewOperatorNativeToolHandler(logger *slog.Logger, operatorID string, query AuditReceiptQuery, grep *execution.FsGrepService) (*NativeToolHandler, error) {
	if operatorID == "" {
		return nil, fmt.Errorf("operator native tool handler: %w", constants.ErrOperatorIDMissing)
	}
	if query == nil {
		return nil, fmt.Errorf("operator native tool handler: %w", constants.ErrMCPAuditReceiptQueryNotConfigured)
	}
	if grep == nil {
		return nil, fmt.Errorf("operator native tool handler: %w", constants.ErrMCPFsGrepNotConfigured)
	}
	h, err := NewNativeToolHandler(logger)
	if err != nil {
		return nil, fmt.Errorf("operator native tool handler: %w", err)
	}
	h.auditReceiptQuery = query
	h.operatorAuthority = &auditReceiptListAuthority{operatorID: operatorID, name: "operator_local_ledger"}
	h.fsGrep = grep
	return h, nil
}

// SetAuditReceiptQuery wires the configured local audit-store read interface into the
// handler so the audit_receipt_list and audit_receipt_get native tools can
// query signed ActionReceipt records under governed dispatch. Called after
// construction once the Gateway's local audit store is available.
func (h *NativeToolHandler) SetAuditReceiptQuery(q AuditReceiptQuery) {
	h.auditReceiptQuery = q
}

// SetFsGrepService wires the runtime's FsGrepService into the handler so the
// search_file and search_directory native tools resolve paths against its
// WorkDir. Without it those tools fail closed with
// constants.ErrMCPFsGrepNotConfigured.
func (h *NativeToolHandler) SetFsGrepService(grep *execution.FsGrepService) {
	h.fsGrep = grep
}

// HandleTool executes a native tool by name and returns the result.
func (h *NativeToolHandler) HandleTool(ctx context.Context, toolName string, arguments json.RawMessage) (CallToolResult, error) {
	if h.logger != nil {
		h.logger.Info("Executing native tool", "tool", toolName)
	}
	tool, ok := h.registry.Get(toolName)
	if !ok {
		if h.logger != nil {
			h.logger.Error("Unknown native tool requested", "tool", toolName)
		}
		availableTools := h.registry.List()
		toolNames := make([]string, 0, len(availableTools))
		for _, t := range availableTools {
			toolNames = append(toolNames, t.Name())
		}
		return CallToolResult{}, fmt.Errorf("%w: %s (available tools: %s)", constants.ErrMCPNativeToolUnknown, toolName, strings.Join(toolNames, ", "))
	}
	if h.auditReceiptQuery != nil {
		ctx = withAuditReceiptQuery(ctx, h.auditReceiptQuery)
	}
	if h.operatorAuthority != nil {
		ctx = withAuditReceiptListAuthority(ctx, *h.operatorAuthority)
	}
	if h.fsGrep != nil {
		ctx = withFsGrepService(ctx, h.fsGrep)
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage("{}")
	}
	result, err := tool.Execute(ctx, arguments)
	if h.logger != nil {
		if err != nil {
			// Native tool errors embed the requested paths and arguments.
			h.logger.Error("Native tool execution failed", slog.Any("tool", toolName), logging.ErrorKind(err))
		} else {
			h.logger.Info("Native tool execution completed", "tool", toolName)
		}
	}
	return result, err
}

// ListTools returns all registered native tools.
func (h *NativeToolHandler) ListTools() []NativeTool {
	return h.registry.List()
}

// ToolResultSummary joins a tool result's text content into the receipt
// summary stamped by the Actuator, defaulting to "completed" for tools that
// return no text.
func ToolResultSummary(result CallToolResult) string {
	var sb strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
			sb.WriteString("\n")
		}
	}
	summary := strings.TrimRight(sb.String(), "\n")
	if summary == "" {
		return "completed"
	}
	return summary
}

// scrubLine redacts sensitive patterns from log lines.
func scrubLine(line string) string {
	scrubbed := line

	sensitivePatterns := []struct {
		pattern string
		repl    string
	}{
		{`password[=:]\s*\S+`, "password=REDACTED"},
		{`api[_-]?key[=:]\s*\S+`, "api_key=REDACTED"},
		{`secret[=:]\s*\S+`, "secret=REDACTED"},
		{`token[=:]\s*\S+`, "token=REDACTED"},
		{`bearer\s+\S+`, "bearer REDACTED"},
	}

	for _, sp := range sensitivePatterns {
		re := regexp.MustCompile(`(?i)` + sp.pattern)
		scrubbed = re.ReplaceAllString(scrubbed, sp.repl)
	}

	return scrubbed
}

// maskSecret redacts secret values in configuration lines.
func maskSecret(line string) string {
	if strings.Contains(strings.ToLower(line), "password") ||
		strings.Contains(strings.ToLower(line), "secret") ||
		strings.Contains(strings.ToLower(line), "token") ||
		strings.Contains(strings.ToLower(line), "key") {
		return "REDACTED"
	}
	return line
}

// parseSocketAddr parses /proc/net socket address format.
func parseSocketAddr(hexAddr string) (string, int, error) {
	if len(hexAddr) < 8 {
		return "0.0.0.0", 0, nil
	}

	portHex := hexAddr[len(hexAddr)-4:]
	ipHex := hexAddr[:len(hexAddr)-4]

	port, err := strconv.ParseInt(portHex, 16, 32)
	if err != nil {
		return "0.0.0.0", 0, fmt.Errorf("%w: %w", constants.ErrMCPParseSocketPort, err)
	}

	var ip string
	if len(ipHex) == 8 {
		octets := make([]int64, 4)
		for i := 0; i < 4; i++ {
			start := 6 - (i * 2)
			end := start + 2
			octet, err := strconv.ParseInt(ipHex[start:end], 16, 32)
			if err != nil {
				return "0.0.0.0", 0, fmt.Errorf("%w: %w", constants.ErrMCPParseSocketIPOctet, err)
			}
			octets[i] = octet
		}
		ip = fmt.Sprintf("%d.%d.%d.%d", octets[0], octets[1], octets[2], octets[3])
	} else {
		ip = "unknown"
	}

	return ip, int(port), nil
}
