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

// SearchFileRequest is the params for the "search_file" tool.
type SearchFileRequest struct {
	Path       string `json:"path"`
	Pattern    string `json:"pattern"`
	MaxMatches int    `json:"max_matches,omitempty"`
}

// SearchFileResult is the result for the "search_file" tool.
type SearchFileResult struct {
	Path         string        `json:"path"`
	Pattern      string        `json:"pattern"`
	Matches      []SearchMatch `json:"matches"`
	TotalMatches int           `json:"total_matches"`
	Truncated    bool          `json:"truncated"`
}

// SearchFileTool returns the lines of one file that match a regex.
type SearchFileTool struct{}

// Name returns the tool identifier.
func (t *SearchFileTool) Name() string {
	return constants.MCPToolSearchFile
}

// Description returns a human-readable description.
func (t *SearchFileTool) Description() string {
	return fmt.Sprintf("Searches one file for lines matching a regular expression and returns each match with its line number. "+
		"The path must be a file; use %s to search a directory.", constants.MCPToolSearchDirectory)
}

// InputSchema returns the JSON Schema for tool validation.
func (t *SearchFileTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to search, absolute or relative to the working directory",
			},
			"pattern": {
				Type:        "string",
				Description: "Regular expression (Go RE2 syntax) matched against each line",
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
// Actuator records a FAILED event receipt.
func (t *SearchFileTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req SearchFileRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("search_file: %w: %w", constants.ErrMCPUnmarshalArguments, err)
	}
	if req.Path == "" {
		return CallToolResult{}, fmt.Errorf("search_file: path: %w", constants.ErrMissingRequiredField)
	}
	if req.Pattern == "" {
		return CallToolResult{}, fmt.Errorf("search_file: pattern: %w", constants.ErrMissingRequiredField)
	}
	grep := fsGrepServiceFromContext(ctx)
	if grep == nil {
		return CallToolResult{}, fmt.Errorf("search_file: %w", constants.ErrMCPFsGrepNotConfigured)
	}

	found, err := grep.SearchFile(ctx, &models.FsGrepRequest{
		Path:       req.Path,
		Pattern:    req.Pattern,
		MaxMatches: req.MaxMatches,
	})
	if errors.Is(err, constants.ErrPathIsDirectory) {
		return CallToolResult{}, fmt.Errorf("search_file: %w; use %s to search a directory", err, constants.MCPToolSearchDirectory)
	}
	if err != nil {
		return CallToolResult{}, fmt.Errorf("search_file: %w", err)
	}

	result := SearchFileResult{
		Path:         found.Path,
		Pattern:      found.Pattern,
		Matches:      scrubbedSearchMatches(found.Matches),
		TotalMatches: found.TotalMatches,
		Truncated:    found.Truncated,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("search_file: %w: %w", constants.ErrMCPMarshalResult, err)
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
