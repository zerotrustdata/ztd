// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// scenarioWorkspaceDigestHexLength is how many hex characters of the
// run/attempt digest name one workspace directory.
const scenarioWorkspaceDigestHexLength = 16

// ScenarioWorkspace is the attempt-scoped fixture workspace on the bound Data
// Operator. Paths are paths on the remote operator host, whose OS need not be
// the CLI's: a POSIX working directory keeps POSIX paths, and a drive-letter
// working directory makes every path a slash-form Windows path ("D:/x/y"),
// which Windows file APIs accept. The root sits under the operator's reported
// working directory, so the production `./` instruction and the prompt's
// "Working Directory" line resolve to its parent, and it is unique per run and
// attempt, so concurrent or repeated attempts never see each other's fixtures.
// The one exception is a held-context repetition, which deliberately reuses
// the previous repetition's workspace (heldScenarioWorkspace).
type ScenarioWorkspace struct {
	Root                     string
	OperatorWorkingDirectory string
}

// ValidateAgentDataOperator reports whether a Data Operator may host
// model-role scenarios. The model under evaluation runs arbitrary shell
// commands in the operator working directory, so that directory MUST be
// disposable: either directly under <project>/.ztd/operators/ (the embedded
// Operator's own directory included) or a directory that neither is, contains,
// nor sits inside projectRoot. Run execution checks it before any assignment is
// marked running.
func ValidateAgentDataOperator(operatorWorkingDirectory, projectRoot string) error {
	workingDirectory, err := cleanOperatorWorkingDirectory(operatorWorkingDirectory)
	if err != nil {
		return err
	}
	project, ok := cleanOperatorAbsPath(filepath.ToSlash(projectRoot))
	if !ok {
		return fmt.Errorf("%w: project root %q is not an absolute path", constants.ErrEvaluationWorkspaceUnavailable, projectRoot)
	}
	// The one sanctioned location inside the project is a throwaway Operator
	// directly under <project>/.ztd/operators/; anything else that overlaps
	// the project would let scenario fixtures touch ztd source or state.
	operators := path.Join(project, constants.RuntimeDirname, constants.OperatorsDirname)
	if path.Dir(workingDirectory) == operators {
		return nil
	}
	if pathWithin(workingDirectory, project) || pathWithin(project, workingDirectory) {
		return fmt.Errorf("%w: operator working directory %q overlaps the ztd project %q; start the Data Operator under %s/<name> or in a disposable directory outside the project", constants.ErrEvaluationWorkspaceUnavailable, workingDirectory, project, operators)
	}
	return nil
}

func cleanOperatorWorkingDirectory(operatorWorkingDirectory string) (string, error) {
	workingDirectory, ok := cleanOperatorAbsPath(operatorWorkingDirectory)
	if !ok || strings.Contains(operatorWorkingDirectory, "\x00") {
		return "", fmt.Errorf("%w: operator working directory %q is not an absolute path", constants.ErrEvaluationWorkspaceUnavailable, operatorWorkingDirectory)
	}
	return workingDirectory, nil
}

// NewScenarioWorkspace derives the attempt-scoped workspace for one attempt.
func NewScenarioWorkspace(operatorWorkingDirectory, runID, attemptID string) (ScenarioWorkspace, error) {
	workingDirectory, err := cleanOperatorWorkingDirectory(operatorWorkingDirectory)
	if err != nil {
		return ScenarioWorkspace{}, err
	}
	if runID == "" || attemptID == "" {
		return ScenarioWorkspace{}, fmt.Errorf("evaluation: scenario workspace: %w", constants.ErrMissingRequiredField)
	}
	sum := sha256.Sum256([]byte(runID + "\x00" + attemptID))
	return ScenarioWorkspace{
		Root:                     path.Join(workingDirectory, constants.EvaluationWorkspaceDirname, constants.EvaluationWorkspacePrefix+hex.EncodeToString(sum[:])[:scenarioWorkspaceDigestHexLength]),
		OperatorWorkingDirectory: workingDirectory,
	}, nil
}

// Render replaces every ScenarioWorkspaceToken in text with the workspace root.
func (w ScenarioWorkspace) Render(text string) string {
	return strings.ReplaceAll(text, ScenarioWorkspaceToken, w.Root)
}

// SentinelPath is the harness-owned file the attempt's ledger sentinels read.
// It sits next to the workspace root, outside the model's workspace.
func (w ScenarioWorkspace) SentinelPath() string {
	return path.Join(path.Dir(w.Root), path.Base(w.Root)+constants.EvaluationWorkspaceSentinelSuffix)
}

// FilePath returns the absolute path of one workspace-relative fixture path.
func (w ScenarioWorkspace) FilePath(relPath string) (string, error) {
	if err := validateWorkspaceRelPath(relPath); err != nil {
		return "", err
	}
	return path.Join(w.Root, relPath), nil
}

// Resolve turns a path argument a model supplied into a cleaned absolute
// path: absolute paths are cleaned, relative ones resolve against the
// operator working directory the way the operator itself resolves them. On a
// Windows operator, backslashes are separators and a rooted path without a
// drive ("\x") sits on the working directory's drive.
func (w ScenarioWorkspace) Resolve(value string) string {
	volume, workingRest, windows := splitWindowsVolume(w.OperatorWorkingDirectory)
	if !windows {
		if path.IsAbs(value) {
			return path.Clean(value)
		}
		return path.Clean(path.Join(w.OperatorWorkingDirectory, value))
	}
	if cleaned, ok := cleanOperatorAbsPath(value); ok {
		return cleaned
	}
	value = strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(value, "/") {
		return volume + path.Clean(value)
	}
	return volume + path.Clean(path.Join(workingRest, value))
}

// cleanOperatorAbsPath cleans an absolute operator-host path and reports
// whether it is one. POSIX paths clean as POSIX paths. A drive-letter path
// ("D:\x", "d:/x") becomes slash form with an upper-case drive, and only the
// part after the drive is cleaned, so ".." never climbs past the drive root.
func cleanOperatorAbsPath(value string) (string, bool) {
	if volume, rest, ok := splitWindowsVolume(value); ok {
		return volume + path.Clean(rest), true
	}
	if path.IsAbs(value) {
		return path.Clean(value), true
	}
	return "", false
}

// splitWindowsVolume splits a drive-letter absolute path into its upper-case
// volume ("D:") and its rooted, slash-form remainder ("/x/y").
func splitWindowsVolume(value string) (volume, rest string, ok bool) {
	if len(value) < 3 || value[1] != ':' || (value[2] != '\\' && value[2] != '/') {
		return "", "", false
	}
	drive := value[0]
	if (drive < 'a' || drive > 'z') && (drive < 'A' || drive > 'Z') {
		return "", "", false
	}
	return strings.ToUpper(value[:1]) + ":", strings.ReplaceAll(value[2:], `\`, "/"), true
}

// Contains reports whether value resolves to the root or a path under it.
func (w ScenarioWorkspace) Contains(value string) bool {
	return pathWithin(w.Resolve(value), w.Root)
}

// Validate checks that the workspace is the one this run and attempt derive.
func (w ScenarioWorkspace) Validate(runID, attemptID string) error {
	expected, err := NewScenarioWorkspace(w.OperatorWorkingDirectory, runID, attemptID)
	if err != nil {
		return err
	}
	if expected.Root != w.Root {
		return fmt.Errorf("%w: workspace root %q is not the attempt-scoped root %q", constants.ErrEvaluationWorkspaceUnavailable, w.Root, expected.Root)
	}
	return nil
}

// ValidateShape checks that the workspace is one NewScenarioWorkspace could
// derive under its operator working directory, for a held-context repetition
// that reuses an earlier attempt's workspace.
func (w ScenarioWorkspace) ValidateShape() error {
	workingDirectory, err := cleanOperatorWorkingDirectory(w.OperatorWorkingDirectory)
	if err != nil {
		return err
	}
	name := path.Base(w.Root)
	digest := strings.TrimPrefix(name, constants.EvaluationWorkspacePrefix)
	if path.Dir(w.Root) != path.Join(workingDirectory, constants.EvaluationWorkspaceDirname) || digest == name ||
		len(digest) != scenarioWorkspaceDigestHexLength || strings.Trim(digest, "0123456789abcdef") != "" {
		return fmt.Errorf("%w: workspace root %q is not an attempt-scoped root under %q", constants.ErrEvaluationWorkspaceUnavailable, w.Root, workingDirectory)
	}
	return nil
}

// materializeScenarioWorkspace writes every fixture file, decoys included, at
// its path under the attempt-scoped workspace on the bound Data Operator and
// returns on the first failure, so a scored request is never sent against an
// empty or partial workspace. A scenario with files and no writer fails closed
// with ErrEvaluationWorkspaceUnavailable.
func materializeScenarioWorkspace(ctx context.Context, writer WorkspaceFileWriter, target Target, runID, scenarioID, attemptID string, ws ScenarioWorkspace, files []ScenarioWorkspaceFile) error {
	if len(files) == 0 {
		return nil
	}
	if writer == nil {
		return fmt.Errorf("%w: scenario %s requires a workspace file writer", constants.ErrEvaluationWorkspaceUnavailable, scenarioID)
	}
	for _, file := range files {
		absPath, err := ws.FilePath(file.RelPath)
		if err != nil {
			return fmt.Errorf("%w: scenario %s: %v", constants.ErrEvaluationWorkspaceUnavailable, scenarioID, err)
		}
		if err := writer.WriteWorkspaceFile(ctx, target, runID, scenarioID, attemptID, absPath, ws.Render(file.Content)); err != nil {
			return fmt.Errorf("write workspace file %s: %w", absPath, err)
		}
	}
	return nil
}

func pathWithin(candidate, root string) bool {
	return candidate == root || strings.HasPrefix(candidate, strings.TrimSuffix(root, "/")+"/")
}

// WorkspaceFileWriter materializes one frozen scenario fixture file at an
// absolute path under the attempt-scoped workspace on the bound Data Operator
// through a governed file write. *CommandLane implements this.
type WorkspaceFileWriter interface {
	WriteWorkspaceFile(ctx context.Context, target Target, runID, scenarioID, attemptID, absPath, content string) error
}
