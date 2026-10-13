// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Environment registry checks (INV-ENV-04, docs/devs/devs.md).
//
// protocol/constants/env_vars.json is the single source of truth for env keys.
// These checks run in both -check and -write modes and fail on:
//   - malformed JSON entries (category, _go_const, value, description);
//   - .env.example (repo root, the single user template) drift: every
//     secret and user_endpoint key must appear as a commented assignment with
//     Purpose, Consumer, and Required-for-default-flow lines above it and a
//     non-empty example value. Every ZTD_* key named in .env.example must be a
//     JSON key or a ZTD_TEST_* key.

const (
	relPathEnvVars    = "protocol/constants/env_vars.json"
	relPathEnvExample = ".env.example"
)

var validEnvCategoryNames = map[string]bool{
	"secret": true, "user_endpoint": true, "host": true, "test": true,
}

type envRegistryFile struct {
	EnvVars map[string]envRegistryEntry `json:"env_vars"`
}

type envRegistryEntry struct {
	GoConst     string `json:"_go_const"`
	Value       string `json:"value"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

var (
	envGoConstPattern    = regexp.MustCompile(`^EnvVar\.[A-Z][A-Za-z0-9]*$`)
	envValuePattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	envExampleKeyRegex   = regexp.MustCompile(`^(#\s*)?(ZTD_[A-Z0-9_]+)=(.*)$`)
	envExampleFieldRegex = regexp.MustCompile(`^#\s+(Purpose|Consumer|Required for default flow):\s*(.*)$`)
)

// validateEnvRegistries checks env_vars.json for malformed entries and the
// repo-root .env.example against it. It returns every drift found.
func validateEnvRegistries(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, relPathEnvVars))
	if err != nil {
		return fmt.Errorf("read %s: %w", relPathEnvVars, err)
	}
	var doc envRegistryFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", relPathEnvVars, err)
	}

	var problems []string
	byValue := make(map[string]envRegistryEntry, len(doc.EnvVars))
	for name, entry := range doc.EnvVars {
		if !validEnvCategoryNames[entry.Category] {
			problems = append(problems, fmt.Sprintf("%s: category %q is not a known env category", name, entry.Category))
		}
		if !envGoConstPattern.MatchString(entry.GoConst) {
			problems = append(problems, fmt.Sprintf("%s: _go_const %q must match EnvVar.<Name>", name, entry.GoConst))
		}
		if !envValuePattern.MatchString(entry.Value) {
			problems = append(problems, fmt.Sprintf("%s: value %q is not an upper-snake environment variable name", name, entry.Value))
		}
		if strings.TrimSpace(entry.Description) == "" {
			problems = append(problems, fmt.Sprintf("%s: description is required", name))
		}
		if prev, dup := byValue[entry.Value]; dup {
			problems = append(problems, fmt.Sprintf("%s: value %q duplicates %s", name, entry.Value, prev.GoConst))
		}
		byValue[entry.Value] = entry
	}

	problems = append(problems, checkEnvExample(root, byValue)...)

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("environment registry drift (INV-ENV-04):\n  %s", strings.Join(problems, "\n  "))
}

// checkEnvExample verifies the repo-root .env.example against the registry.
func checkEnvExample(root string, byValue map[string]envRegistryEntry) []string {
	raw, err := os.ReadFile(filepath.Join(root, relPathEnvExample))
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", relPathEnvExample, err)}
	}
	lines := strings.Split(string(raw), "\n")

	var problems []string
	for _, line := range lines {
		m := envExampleKeyRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[2]
		if _, ok := byValue[key]; ok {
			continue
		}
		if strings.HasPrefix(key, "ZTD_TEST_") {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s: %s is not in %s and is not a ZTD_TEST_* key", relPathEnvExample, key, relPathEnvVars))
	}

	for value, entry := range byValue {
		if entry.Category != "secret" && entry.Category != "user_endpoint" {
			continue
		}
		problems = append(problems, checkEnvExampleEntry(lines, value)...)
	}
	return problems
}

// checkEnvExampleEntry verifies one required key is a commented assignment with
// a non-empty example value, preceded by Purpose, Consumer, and
// Required-for-default-flow lines (yes or no).
func checkEnvExampleEntry(lines []string, key string) []string {
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			return []string{fmt.Sprintf("%s: %s must be a commented line, not an active assignment", relPathEnvExample, key)}
		}
		if !strings.HasPrefix(line, "# "+key+"=") {
			continue
		}
		example := strings.TrimSpace(strings.TrimPrefix(line, "# "+key+"="))
		var problems []string
		if example == "" {
			problems = append(problems, fmt.Sprintf("%s: %s needs a non-empty example value", relPathEnvExample, key))
		}
		seen := map[string]string{}
		for j := i - 1; j >= 0 && strings.HasPrefix(lines[j], "#"); j-- {
			if f := envExampleFieldRegex.FindStringSubmatch(lines[j]); f != nil {
				if _, dup := seen[f[1]]; !dup {
					seen[f[1]] = strings.TrimSpace(f[2])
				}
			}
		}
		for _, label := range []string{"Purpose", "Consumer"} {
			if seen[label] == "" {
				problems = append(problems, fmt.Sprintf("%s: %s is missing a %s line above it", relPathEnvExample, key, label))
			}
		}
		if v := seen["Required for default flow"]; v != "yes" && v != "no" {
			problems = append(problems, fmt.Sprintf("%s: %s needs \"# Required for default flow: yes|no\" above it", relPathEnvExample, key))
		}
		return problems
	}
	return []string{fmt.Sprintf("%s: %s is missing (commented entry with Purpose, Consumer, Required for default flow, and example)", relPathEnvExample, key)}
}
