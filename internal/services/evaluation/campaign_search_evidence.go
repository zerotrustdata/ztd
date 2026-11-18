// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
)

// searchMatchLine identifies one matching fixture line. Content is not part of
// the key: the receipted result carries the scrubbed line, so a fixture line
// holding a sensitive value never equals its raw text.
type searchMatchLine struct {
	member     int
	path       string
	lineNumber int
}

// fixtureSearchMatches derives the expected observations from frozen fixture
// files, never from the model's chosen filters or its answer.
func fixtureSearchMatches(input ScenarioInputFixture, ws ScenarioWorkspace, pattern string) (map[searchMatchLine]struct{}, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("evaluation: search evidence pattern: %w", err)
	}
	matches := make(map[searchMatchLine]struct{})
	for _, file := range input.WorkspaceFiles {
		filePath, err := ws.FilePath(file.RelPath)
		if err != nil {
			return nil, fmt.Errorf("evaluation: search evidence fixture: %w", err)
		}
		for i, line := range strings.Split(file.Content, "\n") {
			if re.MatchString(line) {
				matches[searchMatchLine{path: ws.Resolve(filePath), lineNumber: i + 1}] = struct{}{}
			}
		}
	}
	return matches, nil
}

// evaluateSearchEvidence checks retrieval independently of the semantic judge.
// A successful empty query or an over-restrictive include/max_matches argument
// is insufficient even when a judge mistakenly credits the final answer.
func evaluateSearchEvidence(req AssignmentExecutionRequest, view agentGradingView) (bool, string) {
	// Every roster member the model searched must yield every matching
	// fixture line it holds.
	expected := make(map[searchMatchLine]struct{})
	searched := make(map[int]bool)
	observed := make(map[searchMatchLine]struct{})
	for _, call := range view.Calls {
		if call.ToolName != constants.MCPToolSearchDirectory || !call.Success || call.TargetIndex == 0 {
			continue
		}
		ws := view.Trace.CallWorkspace(call)
		if valid, _ := validateToolCall(call, req.ScenarioGold.ArgumentValidators, ws); !valid {
			continue
		}
		var result mcp.SearchDirectoryResult
		if json.Unmarshal([]byte(call.ResultJSON), &result) != nil || result.Truncated || result.TotalMatches != len(result.Matches) {
			continue
		}
		if !searched[call.TargetIndex] {
			searched[call.TargetIndex] = true
			input := req.ScenarioInput
			input.WorkspaceFiles = workspaceFilesFor(input.WorkspaceFiles, call.TargetIndex)
			lines, err := fixtureSearchMatches(input, ws, req.ScenarioGold.ContentCheck.SearchPattern)
			if err != nil {
				return false, "search evidence contract has no valid matching fixture lines"
			}
			for line := range lines {
				line.member = call.TargetIndex
				expected[line] = struct{}{}
			}
		}
		for _, match := range result.Matches {
			line := searchMatchLine{member: call.TargetIndex, path: ws.Resolve(match.Path), lineNumber: match.LineNumber}
			if _, ok := expected[line]; ok {
				observed[line] = struct{}{}
			}
		}
	}
	if len(expected) == 0 {
		if len(searched) == 0 {
			return false, "search retrieved 0 required matching fixture lines"
		}
		return false, "search evidence contract has no valid matching fixture lines"
	}
	if len(observed) != len(expected) {
		return false, fmt.Sprintf("search retrieved %d of %d required matching fixture lines", len(observed), len(expected))
	}
	return true, ""
}
