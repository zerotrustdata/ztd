// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// FSDiskProfileTool recursively calculates directory sizes. Zero bounds use
// the FsDiskProfile* defaults in constants.
type FSDiskProfileTool struct {
	maxEntries      int
	maxWalkEntries  int
	maxWalkDuration time.Duration
}

// Name returns the tool identifier.
func (t *FSDiskProfileTool) Name() string {
	return constants.MCPToolFsDiskProfile
}

// Description returns a human-readable description.
func (t *FSDiskProfileTool) Description() string {
	return "Recursively calculates directory sizes and disk usage."
}

// InputSchema returns the JSON Schema for tool validation.
func (t *FSDiskProfileTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to profile",
			},
			"max_depth": {
				Type:        "integer",
				Description: "Maximum directory depth (default 2)",
			},
		},
		Required: []string{"path"},
	}
}

// Execute implements the tool logic.
func (t *FSDiskProfileTool) Execute(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
	var req FSDiskProfileRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return CallToolResult{}, fmt.Errorf("fs_disk_profile: invalid arguments: %w", err)
	}

	if req.Path == "" {
		return CallToolResult{}, fmt.Errorf("fs_disk_profile: path required")
	}

	maxDepth := req.MaxDepth
	if maxDepth <= 0 {
		maxDepth = constants.DefaultDiskProfileDepth
	}

	maxEntries := cmp.Or(t.maxEntries, constants.FsDiskProfileMaxEntries)
	maxWalkEntries := cmp.Or(t.maxWalkEntries, constants.FsDiskProfileMaxWalkEntries)
	walkDeadline := time.Now().Add(cmp.Or(t.maxWalkDuration, constants.FsDiskProfileMaxWalkDuration))

	var entries []DirEntry
	var totalSize int64
	walkEntries := 0
	truncated := false

	// Either walk bound ends the profile with a truncated result, not a
	// failure; past the listing bound the walk only adds to the total.
	err := filepath.Walk(req.Path, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			return fmt.Errorf("fs_disk_profile: error accessing path %s: %w", path, err)
		}

		walkEntries++
		if walkEntries > maxWalkEntries || time.Now().After(walkDeadline) {
			truncated = true
			return filepath.SkipAll
		}

		relPath, err := filepath.Rel(req.Path, path)
		if err != nil {
			return fmt.Errorf("fs_disk_profile: error computing relative path: %w", err)
		}

		depth := len(strings.Split(relPath, string(filepath.Separator)))
		if depth > maxDepth+1 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		size := info.Size()
		totalSize += size

		if len(entries) == maxEntries {
			truncated = true
			return nil
		}
		entries = append(entries, DirEntry{
			Path:     relPath,
			SizeMB:   size / (1024 * 1024),
			IsDir:    info.IsDir(),
			Modified: info.ModTime().Unix(),
		})

		return nil
	})

	if err != nil {
		return CallToolResult{}, err
	}

	result := FSDiskProfileResult{
		Entries:   entries,
		TotalMB:   totalSize / (1024 * 1024),
		Truncated: truncated,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("fs_disk_profile: failed to marshal result: %w", err)
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
