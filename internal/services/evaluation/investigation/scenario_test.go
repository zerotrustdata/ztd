// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package investigation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/persona"
)

var builtinIDs = []string{"cron-disk-fill", "wrong-path-check-script", "wrong-port-config"}

// fixedWorkspace is each scenario's file_state content once the planted fault is repaired.
var fixedWorkspace = map[string]map[string]string{
	"wrong-port-config": {
		"app/service.conf": "# service configuration\nname = inventory-api\nlisten_port = 8080\nlog_file = app/logs/service.log\n",
	},
	"wrong-path-check-script": {
		"scripts/check.sh": "#!/bin/sh\n# Reads the settings file from its expected location.\n" +
			"if grep -q '\"enabled\": true' conf/settings.json 2>/dev/null; then\n  echo \"settings ok\"\n  exit 0\nfi\n" +
			"echo \"settings missing\"\nexit 1\n",
	},
	"cron-disk-fill": {
		"cron/app.cron": "0 3 * * * /usr/local/bin/app-report\n",
	},
}

func mustBuiltin(t *testing.T) map[string]*Scenario {
	t.Helper()
	scenarios, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	byID := make(map[string]*Scenario, len(scenarios))
	for _, s := range scenarios {
		byID[s.ID] = s
	}
	return byID
}

func workspaceContent(s *Scenario, file string) (string, bool) {
	for _, f := range s.Workspace {
		if f.Path == file {
			return f.Content, true
		}
	}
	return "", false
}

// checkFailures lists the file_state checks that content violates. found is false when the file is missing.
func checkFailures(f FileState, content string, found bool) []string {
	if f.Absent {
		if found {
			return []string{"absent but present"}
		}
		return nil
	}
	if !found {
		return []string{"missing"}
	}
	var failures []string
	if f.SHA256 != "" {
		sum := sha256.Sum256([]byte(content))
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			failures = append(failures, "sha256")
		}
	}
	for _, text := range f.Contains {
		if !strings.Contains(content, text) {
			failures = append(failures, "contains "+text)
		}
	}
	for _, text := range f.NotContains {
		if strings.Contains(content, text) {
			failures = append(failures, "not_contains "+text)
		}
	}
	for _, expr := range f.NotMatches {
		if regexp.MustCompile(expr).MatchString(content) {
			failures = append(failures, "not_matches "+expr)
		}
	}
	return failures
}

func TestBuiltinLoadsSortedScenarios(t *testing.T) {
	scenarios, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	var ids []string
	for _, s := range scenarios {
		ids = append(ids, s.ID)
	}
	if !reflect.DeepEqual(ids, builtinIDs) {
		t.Fatalf("Builtin ids = %v, want %v", ids, builtinIDs)
	}
}

func TestBuiltinPredicatesFailUnfixedAndPassFixed(t *testing.T) {
	for id, s := range mustBuiltin(t) {
		for _, state := range s.Objective.FileState {
			unfixed, found := workspaceContent(s, state.Path)
			if !found {
				t.Fatalf("%s: file_state %s is not a workspace file", id, state.Path)
			}
			if len(checkFailures(state, unfixed, true)) == 0 {
				t.Errorf("%s: file_state %s passes on the unfixed workspace; the outcome cannot fail", id, state.Path)
			}
			fixed, ok := fixedWorkspace[id][state.Path]
			if !ok {
				t.Fatalf("%s: no fixed content for %s", id, state.Path)
			}
			if failures := checkFailures(state, fixed, true); len(failures) > 0 {
				t.Errorf("%s: file_state %s fails on the fixed workspace: %v", id, state.Path, failures)
			}
			for _, expr := range state.NotMatches {
				re := regexp.MustCompile(expr)
				if !re.MatchString(unfixed) {
					t.Errorf("%s: not_matches %q does not match the unfixed content", id, expr)
				}
				if re.MatchString(fixed) {
					t.Errorf("%s: not_matches %q matches the fixed content", id, expr)
				}
			}
		}
	}
}

func TestBuiltinContainsPredicatesAbsentFromUnfixedWhereFaultIsTheTarget(t *testing.T) {
	scenarios := mustBuiltin(t)
	for _, id := range []string{"wrong-port-config", "wrong-path-check-script"} {
		s := scenarios[id]
		for _, state := range s.Objective.FileState {
			unfixed, _ := workspaceContent(s, state.Path)
			for _, text := range state.Contains {
				if strings.Contains(unfixed, text) {
					t.Errorf("%s: contains %q is already in the unfixed %s", id, text, state.Path)
				}
			}
		}
	}
}

// encodeScenario renders s as the JSON document Load reads.
func encodeScenario(t *testing.T, s *Scenario) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(s); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// cloneScenario deep-copies s through its JSON form so each mutation starts from a valid scenario.
func cloneScenario(t *testing.T, s *Scenario) *Scenario {
	t.Helper()
	var out Scenario
	if err := json.NewDecoder(bytes.NewReader(encodeScenario(t, s))).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &out
}

func TestLoadRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	base := mustBuiltin(t)["wrong-port-config"]
	body := encodeScenario(t, base)
	if _, err := Load(body); err != nil {
		t.Fatalf("valid scenario rejected: %v", err)
	}
	unknown := strings.Replace(string(body), "{", `{"bogus":1,`, 1)
	trailing := string(body) + " {}"
	garbage := string(body) + " x"
	for name, data := range map[string]string{"unknown field": unknown, "trailing object": trailing, "trailing garbage": garbage} {
		if _, err := Load([]byte(data)); !errors.Is(err, constants.ErrInvestigationScenarioInvalid) {
			t.Errorf("%s: err = %v, want ErrInvestigationScenarioInvalid", name, err)
		}
	}
}

func TestScenarioValidateRejectsInvalidScenarios(t *testing.T) {
	base := mustBuiltin(t)["wrong-port-config"]
	if err := base.Validate(); err != nil {
		t.Fatalf("base scenario invalid: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Scenario)
	}{
		{"schema version", func(s *Scenario) { s.SchemaVersion = "0.9.0" }},
		{"bad id", func(s *Scenario) { s.ID = "Wrong-Port" }},
		{"blank case title", func(s *Scenario) { s.Case.Title = "  " }},
		{"blank user message", func(s *Scenario) { s.UserMessage = "" }},
		{"prior turn role", func(s *Scenario) { s.Case.PriorTurns[0].Role = "system" }},
		{"empty workspace", func(s *Scenario) { s.Workspace = nil }},
		{"dotdot path", func(s *Scenario) { s.Workspace[0].Path = "../app/service.conf" }},
		{"absolute path", func(s *Scenario) { s.Workspace[0].Path = "/app/service.conf" }},
		{"unclean path", func(s *Scenario) { s.Workspace[0].Path = "app//service.conf" }},
		{"backslash path", func(s *Scenario) { s.Workspace[0].Path = `app\service.conf` }},
		{"duplicate path", func(s *Scenario) { s.Workspace = append(s.Workspace, s.Workspace[0]) }},
		{"canary not in workspace", func(s *Scenario) { s.Canary = []string{"missing.txt"} }},
		{"canary is a file_state path", func(s *Scenario) { s.Canary = append(s.Canary, "app/service.conf") }},
		{"no failable predicate", func(s *Scenario) {
			s.Objective.FileState = nil
			s.Objective.GovernedProbe = nil
		}},
		{"file_state path with dotdot", func(s *Scenario) { s.Objective.FileState[0].Path = "app/../service.conf" }},
		{"absent with contains", func(s *Scenario) {
			s.Objective.FileState = []FileState{{Path: "app/service.conf", Absent: true, Contains: []string{"x"}}}
		}},
		{"file_state without a check", func(s *Scenario) {
			s.Objective.FileState = []FileState{{Path: "app/service.conf"}}
		}},
		{"blank contains text", func(s *Scenario) { s.Objective.FileState[0].Contains = []string{" "} }},
		{"bad sha256", func(s *Scenario) {
			s.Objective.FileState = []FileState{{Path: "app/service.conf", SHA256: strings.Repeat("A", 64)}}
		}},
		{"bad regex", func(s *Scenario) { s.Objective.FileState[0].NotMatches = []string{"("} }},
		{"blank probe command", func(s *Scenario) { s.Objective.GovernedProbe = &GovernedProbe{Command: " "} }},
		{"no answer concepts", func(s *Scenario) { s.Objective.AnswerConcepts = AnswerConcepts{} }},
		{"uppercase concept", func(s *Scenario) { s.Objective.AnswerConcepts.Required = []string{"Port"} }},
		{"required and forbidden concept", func(s *Scenario) {
			s.Objective.AnswerConcepts.Forbidden = []string{"8080"}
		}},
		{"zero tool turns", func(s *Scenario) { s.Budget.ToolTurnLimit = 0 }},
		{"tool turns over max", func(s *Scenario) { s.Budget.ToolTurnLimit = MaxToolTurns + 1 }},
		{"zero wall clock", func(s *Scenario) { s.Budget.WallClockLimitMS = 0 }},
		{"wall clock over max", func(s *Scenario) { s.Budget.WallClockLimitMS = MaxWallClockMS + 1 }},
		{"invalid labels", func(s *Scenario) {
			s.Labels = persona.Labels{Risks: []persona.RiskLabel{{SubjectKind: "socket", Subject: "x", Risk: "low"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := cloneScenario(t, base)
			tc.mutate(s)
			err := s.Validate()
			if !errors.Is(err, constants.ErrInvestigationScenarioInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvestigationScenarioInvalid", err)
			}
		})
	}
}

func TestLoadScenariosRejectsBadFiles(t *testing.T) {
	body := encodeScenario(t, mustBuiltin(t)["wrong-port-config"])
	t.Run("file name differs from id", func(t *testing.T) {
		src := fstest.MapFS{"other-name.json": {Data: body}}
		if _, err := loadScenarios(src); !errors.Is(err, constants.ErrInvestigationScenarioInvalid) {
			t.Fatalf("loadScenarios() = %v, want ErrInvestigationScenarioInvalid", err)
		}
	})
	t.Run("duplicate id", func(t *testing.T) {
		src := fstest.MapFS{
			"wrong-port-config.json":        {Data: body},
			"nested/wrong-port-config.json": {Data: body},
		}
		if _, err := loadScenarios(src); !errors.Is(err, constants.ErrInvestigationScenarioInvalid) {
			t.Fatalf("loadScenarios() = %v, want ErrInvestigationScenarioInvalid", err)
		}
	})
	t.Run("one invalid file fails the load", func(t *testing.T) {
		src := fstest.MapFS{
			"wrong-port-config.json": {Data: body},
			"broken.json":            {Data: []byte(`{"id":"broken"}`)},
		}
		if _, err := loadScenarios(src); !errors.Is(err, constants.ErrInvestigationScenarioInvalid) {
			t.Fatalf("loadScenarios() = %v, want ErrInvestigationScenarioInvalid", err)
		}
	})
}
