// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package investigation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/eval/investigations"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/persona"
)

const (
	ScenarioSchemaVersion = "1.0.0"
	MaxToolTurns          = 32
	MaxWallClockMS        = 30 * 60 * 1000
)

const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Scenario struct {
	SchemaVersion string          `json:"schema_version"`
	ID            string          `json:"id"`
	Case          Case            `json:"case"`
	Workspace     []WorkspaceFile `json:"workspace"`
	UserMessage   string          `json:"user_message"`
	Objective     Objective       `json:"objective"`
	Canary        []string        `json:"canary"` // workspace paths that must be byte-identical after the attempt
	Budget        Budget          `json:"budget"`
	Labels        persona.Labels  `json:"labels"`
}

type Case struct {
	Title       string      `json:"title"`
	Description string      `json:"description"`
	PriorTurns  []PriorTurn `json:"prior_turns"`
	Memory      []string    `json:"memory"`
}

type PriorTurn struct {
	Role string `json:"role"` // user | assistant
	Text string `json:"text"`
}

type WorkspaceFile struct {
	Path    string `json:"path"` // relative to the attempt workspace root, forward slashes
	Content string `json:"content"`
}

type Objective struct {
	Goal           string         `json:"goal"`
	FileState      []FileState    `json:"file_state"`
	GovernedProbe  *GovernedProbe `json:"governed_probe,omitempty"`
	AnswerConcepts AnswerConcepts `json:"answer_concepts"`
}

type FileState struct {
	Path        string   `json:"path"`
	SHA256      string   `json:"sha256,omitempty"` // exact content
	Contains    []string `json:"contains,omitempty"`
	NotContains []string `json:"not_contains,omitempty"`
	NotMatches  []string `json:"not_matches,omitempty"` // RE2, compiled at load
	Absent      bool     `json:"absent,omitempty"`      // the file must not exist
}

// GovernedProbe is a harness-owned command run in the workspace root after the
// attempt. It passes when its signed event receipt shows a successful execution.
// Like run_shell_command, it names one executable and its argv; it never
// runs through a shell.
type GovernedProbe struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type AnswerConcepts struct {
	Required  []string `json:"required,omitempty"`
	Forbidden []string `json:"forbidden,omitempty"`
}

type Budget struct {
	ToolTurnLimit    uint32 `json:"tool_turn_limit"`
	WallClockLimitMS uint64 `json:"wall_clock_limit_ms"`
}

// Load decodes one scenario strictly and validates it.
func Load(data []byte) (*Scenario, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var s Scenario
	if err := decoder.Decode(&s); err != nil {
		return nil, fmt.Errorf("investigation scenario: decode: %w: %w", constants.ErrInvestigationScenarioInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, invalid("decode: trailing content after the scenario")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate checks every rule of the scenario schema. Errors wrap ErrInvestigationScenarioInvalid.
func (s *Scenario) Validate() error {
	if s.SchemaVersion != ScenarioSchemaVersion {
		return invalid("schema_version %q, want %s", s.SchemaVersion, ScenarioSchemaVersion)
	}
	if !idPattern.MatchString(s.ID) {
		return invalid("id %q must match %s", s.ID, idPattern)
	}
	required := []struct{ field, value string }{
		{"case.title", s.Case.Title},
		{"case.description", s.Case.Description},
		{"user_message", s.UserMessage},
		{"objective.goal", s.Objective.Goal},
	}
	for _, field := range required {
		if blank(field.value) {
			return invalid("%s is required", field.field)
		}
	}
	for i, turn := range s.Case.PriorTurns {
		if turn.Role != roleUser && turn.Role != roleAssistant {
			return invalid("case.prior_turns[%d].role %q must be user or assistant", i, turn.Role)
		}
	}
	workspace, err := s.validateWorkspace()
	if err != nil {
		return err
	}
	fileStatePaths, err := s.Objective.validate()
	if err != nil {
		return err
	}
	if err := s.validateCanary(workspace, fileStatePaths); err != nil {
		return err
	}
	if err := s.validateBudget(); err != nil {
		return err
	}
	if err := s.Labels.Validate(); err != nil {
		return fmt.Errorf("investigation scenario: labels: %w", err)
	}
	return nil
}

// Builtin loads every scenario in eval/investigations, sorted by id. Any
// invalid file fails the whole load.
func Builtin() ([]*Scenario, error) {
	return loadScenarios(investigations.FS)
}

// loadScenarios loads every *.json file under src. Each file must be named <id>.json and ids must be unique.
func loadScenarios(src fs.FS) ([]*Scenario, error) {
	var scenarios []*Scenario
	files := make(map[string]string)
	err := fs.WalkDir(src, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(name) != ".json" {
			return nil
		}
		body, err := fs.ReadFile(src, name)
		if err != nil {
			return fmt.Errorf("investigation: read %s: %w", name, err)
		}
		s, err := Load(body)
		if err != nil {
			return fmt.Errorf("investigation: %s: %w", name, err)
		}
		if path.Base(name) != s.ID+".json" {
			return invalid("%s: file name must be %s.json for id %q", name, s.ID, s.ID)
		}
		if other, dup := files[s.ID]; dup {
			return invalid("%s: duplicate id %q also in %s", name, s.ID, other)
		}
		files[s.ID] = name
		scenarios = append(scenarios, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })
	return scenarios, nil
}

// validateWorkspace checks the workspace paths and returns them as a set.
func (s *Scenario) validateWorkspace() (map[string]bool, error) {
	if len(s.Workspace) == 0 {
		return nil, invalid("workspace needs at least one file")
	}
	paths := make(map[string]bool, len(s.Workspace))
	for i, file := range s.Workspace {
		if problem := pathProblem(file.Path); problem != "" {
			return nil, invalid("workspace[%d].path %q %s", i, file.Path, problem)
		}
		if paths[file.Path] {
			return nil, invalid("workspace[%d].path %q is duplicated", i, file.Path)
		}
		paths[file.Path] = true
	}
	return paths, nil
}

// validate checks the objective and returns the set of file_state paths.
func (o Objective) validate() (map[string]bool, error) {
	if len(o.FileState) == 0 && o.GovernedProbe == nil {
		return nil, invalid("objective needs file_state or governed_probe so the scenario can fail")
	}
	if o.GovernedProbe != nil && !models.ValidExecutableName(o.GovernedProbe.Command) {
		return nil, invalid("objective.governed_probe.command must be one executable name with no whitespace")
	}
	paths := make(map[string]bool, len(o.FileState))
	for i, state := range o.FileState {
		if err := state.validate(i); err != nil {
			return nil, err
		}
		paths[state.Path] = true
	}
	if err := o.AnswerConcepts.validate(); err != nil {
		return nil, err
	}
	return paths, nil
}

func (f FileState) validate(i int) error {
	if problem := pathProblem(f.Path); problem != "" {
		return invalid("objective.file_state[%d].path %q %s", i, f.Path, problem)
	}
	checks := len(f.Contains) + len(f.NotContains) + len(f.NotMatches)
	if f.SHA256 != "" {
		if !sha256Pattern.MatchString(f.SHA256) {
			return invalid("objective.file_state[%d].sha256 must be 64 lowercase hex characters", i)
		}
		checks++
	}
	for _, text := range slices.Concat(f.Contains, f.NotContains) {
		if blank(text) {
			return invalid("objective.file_state[%d] contains a blank text predicate", i)
		}
	}
	for _, expr := range f.NotMatches {
		if _, err := regexp.Compile(expr); err != nil {
			return fmt.Errorf("investigation scenario: objective.file_state[%d].not_matches %q: %w: %w",
				i, expr, constants.ErrInvestigationScenarioInvalid, err)
		}
	}
	if f.Absent {
		if checks > 0 {
			return invalid("objective.file_state[%d]: absent excludes every other check", i)
		}
		return nil
	}
	if checks == 0 {
		return invalid("objective.file_state[%d] needs at least one check", i)
	}
	return nil
}

func (c AnswerConcepts) validate() error {
	if len(c.Required)+len(c.Forbidden) == 0 {
		return invalid("objective.answer_concepts needs at least one concept")
	}
	forbidden := make(map[string]bool, len(c.Forbidden))
	for _, concept := range c.Forbidden {
		if err := checkConcept(concept); err != nil {
			return err
		}
		forbidden[concept] = true
	}
	for _, concept := range c.Required {
		if err := checkConcept(concept); err != nil {
			return err
		}
		if forbidden[concept] {
			return invalid("objective.answer_concepts %q is both required and forbidden", concept)
		}
	}
	return nil
}

func checkConcept(concept string) error {
	if blank(concept) || strings.TrimSpace(concept) != concept || concept != strings.ToLower(concept) {
		return invalid("objective.answer_concepts %q must be non-empty, trimmed and lowercase", concept)
	}
	return nil
}

func (s *Scenario) validateCanary(workspace, fileStatePaths map[string]bool) error {
	for i, canary := range s.Canary {
		if problem := pathProblem(canary); problem != "" {
			return invalid("canary[%d] %q %s", i, canary, problem)
		}
		if !workspace[canary] {
			return invalid("canary[%d] %q is not a workspace file", i, canary)
		}
		if fileStatePaths[canary] {
			return invalid("canary[%d] %q is a file_state path", i, canary)
		}
	}
	return nil
}

func (s *Scenario) validateBudget() error {
	if s.Budget.ToolTurnLimit == 0 || s.Budget.ToolTurnLimit > MaxToolTurns {
		return invalid("budget.tool_turn_limit %d must be between 1 and %d", s.Budget.ToolTurnLimit, MaxToolTurns)
	}
	if s.Budget.WallClockLimitMS == 0 || s.Budget.WallClockLimitMS > MaxWallClockMS {
		return invalid("budget.wall_clock_limit_ms %d must be between 1 and %d", s.Budget.WallClockLimitMS, MaxWallClockMS)
	}
	return nil
}

// pathProblem names why p is not a relative, clean, forward-slash workspace path. It returns "" for a valid path.
func pathProblem(p string) string {
	switch {
	case p == "":
		return "is empty"
	case path.IsAbs(p):
		return "is absolute"
	case strings.Contains(p, `\`):
		return "uses backslashes"
	case p == "." || path.Clean(p) != p:
		return "is not a clean relative path"
	case slices.Contains(strings.Split(p, "/"), ".."):
		return "contains a .. segment"
	}
	return ""
}

func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("investigation scenario: %s: %w", fmt.Sprintf(format, args...), constants.ErrInvestigationScenarioInvalid)
}
