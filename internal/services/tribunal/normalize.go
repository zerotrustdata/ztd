// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"strings"
)

// NormaliseCommand standardizes command strings emitted by models:
// 1. Strips markdown code fences.
// 2. Strips conversational prefixes (e.g. "Command:", "$ ").
// 3. Strips comment lines and shebangs.
// 4. Trims trailing semicolons and extraneous whitespace.
func NormaliseCommand(raw string) string {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return ""
	}

	// Strip outer markdown code blocks if present
	if strings.HasPrefix(cleaned, "```") {
		lines := strings.Split(cleaned, "\n")
		var inside []string
		inBlock := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				inside = append(inside, line)
			}
		}
		if len(inside) > 0 {
			cleaned = strings.Join(inside, "\n")
		} else {
			// Strip single line ```command```
			cleaned = strings.TrimPrefix(cleaned, "```")
			cleaned = strings.TrimPrefix(cleaned, "bash")
			cleaned = strings.TrimPrefix(cleaned, "sh")
			cleaned = strings.TrimSuffix(cleaned, "```")
		}
	}

	// Filter comment lines and shebangs
	lines := strings.Split(cleaned, "\n")
	var cmdLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cmdLines = append(cmdLines, trimmed)
	}

	if len(cmdLines) == 0 {
		return ""
	}

	// Join multiple lines with space or take the primary line
	result := strings.Join(cmdLines, " ")

	// Strip common conversational prefixes
	prefixes := []string{
		"Command:",
		"The command is:",
		"Final command:",
		"Suggested command:",
		"Run:",
		"$ ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(strings.ToLower(result), strings.ToLower(p)) {
			result = strings.TrimSpace(result[len(p):])
			break
		}
	}

	// Strip trailing semicolon
	result = strings.TrimSuffix(result, ";")
	return strings.TrimSpace(result)
}
