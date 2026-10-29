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
	expected, err := fixtureSearchMatches(req.ScenarioInput, view.Workspace, req.ScenarioGold.ContentCheck.SearchPattern)
	if err != nil || len(expected) == 0 {
		return false, "search evidence contract has no valid matching fixture lines"
	}
	observed := make(map[searchMatchLine]struct{})
	for _, call := range view.Calls {
		if call.ToolName != constants.MCPToolSearchDirectory || !call.Success {
			continue
		}
		if valid, _ := validateToolCall(call, req.ScenarioGold.ArgumentValidators, view.Workspace); !valid {
			continue
		}
		var result mcp.SearchDirectoryResult
		if json.Unmarshal([]byte(call.ResultJSON), &result) != nil || result.Truncated || result.TotalMatches != len(result.Matches) {
			continue
		}
		for _, match := range result.Matches {
			line := searchMatchLine{path: view.Workspace.Resolve(match.Path), lineNumber: match.LineNumber}
			if _, ok := expected[line]; ok {
				observed[line] = struct{}{}
			}
		}
	}
	if len(observed) != len(expected) {
		return false, fmt.Sprintf("search retrieved %d of %d required matching fixture lines", len(observed), len(expected))
	}
	return true, ""
}
