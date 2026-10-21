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
	"errors"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// SearchDirectoryRequest is the params for the "search_directory" tool.
type SearchDirectoryRequest struct {
	Path       string   `json:"path"`
	Pattern    string   `json:"pattern"`
	Include    []string `json:"include,omitempty"`
	MaxMatches int      `json:"max_matches,omitempty"`
}

// SearchDirectoryResult is the result for the "search_directory" tool.
type SearchDirectoryResult struct {
	Path         string        `json:"path"`
	Pattern      string        `json:"pattern"`
	Matches      []SearchMatch `json:"matches"`
	TotalMatches int           `json:"total_matches"`
	Truncated    bool          `json:"truncated"`
}

// SearchDirectoryTool returns the lines matching a regex across every file
// under one directory.
type SearchDirectoryTool struct{}

// Name returns the tool identifier.
func (t *SearchDirectoryTool) Name() string {
	return constants.MCPToolSearchDirectory
}

// Description returns a human-readable description.
func (t *SearchDirectoryTool) Description() string {
	return fmt.Sprintf("Recursively searches every file under one directory for lines matching a regular expression and returns each match with its file path and line number. "+
		"Hidden subdirectories are skipped. The path must be a directory; use %s to search one file.", constants.MCPToolSearchFile)
}

// InputSchema returns the JSON Schema for tool validation.
func (t *SearchDirectoryTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the directory to search, absolute or relative to the working directory",
			},
			"pattern": {
				Type:        "string",
				Description: "Regular expression (Go RE2 syntax) matched against each line",
			},
			"include": {
				Type:        "array",
				Description: "File name globs to search, each its own string, e.g. [\"*.log\"] (optional, defaults to every file)",
			},
			"max_matches": {
				Type:        "integer",
				Description: fmt.Sprintf("Maximum matching lines to return (default %d, at most %d)", constants.FsGrepDefaultMaxMatches, constants.FsGrepMaxMatches),
			},
		},
		Required: []string{"path", "pattern"},
	}
}

// Execute implements the tool logic. Every failure returns an error so the
// Actuator records a FAILED receipt.
func (t *SearchDirectoryTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req SearchDirectoryRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("search_directory: %w: %w", constants.ErrMCPUnmarshalArguments, err)
	}
	if req.Path == "" {
		return CallToolResult{}, fmt.Errorf("search_directory: path: %w", constants.ErrMissingRequiredField)
	}
	if req.Pattern == "" {
		return CallToolResult{}, fmt.Errorf("search_directory: pattern: %w", constants.ErrMissingRequiredField)
	}
	grep := fsGrepServiceFromContext(ctx)
	if grep == nil {
		return CallToolResult{}, fmt.Errorf("search_directory: %w", constants.ErrMCPFsGrepNotConfigured)
	}

	found, err := grep.SearchDirectory(ctx, &models.FsGrepRequest{
		Path:       req.Path,
		Pattern:    req.Pattern,
		Includes:   req.Include,
		MaxMatches: req.MaxMatches,
	})
	if errors.Is(err, constants.ErrNotADirectory) {
		return CallToolResult{}, fmt.Errorf("search_directory: %w; use %s to search one file", err, constants.MCPToolSearchFile)
	}
	if err != nil {
		return CallToolResult{}, fmt.Errorf("search_directory: %w", err)
	}

	result := SearchDirectoryResult{
		Path:         found.Path,
		Pattern:      found.Pattern,
		Matches:      scrubbedSearchMatches(found.Matches),
		TotalMatches: found.TotalMatches,
		Truncated:    found.Truncated,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("search_directory: %w: %w", constants.ErrMCPMarshalResult, err)
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
