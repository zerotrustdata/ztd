// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package security

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ValidatePath ensures the path is safe and valid within the given root.
// It cleans the path, checks for traversal attempts, and resolves it against the root.
// For absolute paths, it validates they don't contain traversal attempts but doesn't
// enforce they're within root (allowing test fixtures and system paths).
func ValidatePath(path string, root string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path: %w", constants.ErrPathValidation)
	}

	// Clean the path to handle multiple slashes and redundant segments
	cleanPath := filepath.Clean(path)

	// SECURITY: Block obvious path traversal attempts before resolution
	if strings.Contains(cleanPath, "..") {
		return "", fmt.Errorf("path traversal attempt detected: %w", constants.ErrPathValidation)
	}

	// Resolve to absolute path
	var absPath string
	if filepath.IsAbs(cleanPath) {
		absPath = cleanPath
	} else {
		// Resolve relative paths against the root directory
		absPath = filepath.Join(root, cleanPath)
	}

	// Re-clean and re-validate absolute path
	absPath = filepath.Clean(absPath)
	if strings.Contains(absPath, "..") {
		return "", fmt.Errorf("path traversal detected after resolution: %w", constants.ErrPathValidation)
	}

	// For relative paths (now resolved), ensure they're within the root directory
	// For absolute paths that were originally absolute, allow them (for test fixtures, system paths)
	if !filepath.IsAbs(cleanPath) {
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return "", fmt.Errorf("failed to calculate relative path: %w", err)
		}
		if strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("path is outside of the root directory: %w", constants.ErrPathValidation)
		}
	}

	return absPath, nil
}

// sensitiveReadPaths are system credential files no read or search tool may
// open.
var sensitiveReadPaths = []string{
	constants.PathEtcShadow,
	constants.PathEtcPasswd,
	constants.PathEtcGshadow,
	constants.PathEtcSudoers,
}

// sensitiveReadBasenames are SSH private key file names refused wherever they
// live.
var sensitiveReadBasenames = []string{
	constants.SshKeyRSA,
	constants.SshKeyEd25519,
	constants.SshKeyECDSA,
}

// IsSensitiveReadPath reports whether path names a system credential file or
// an SSH private key that read and search tools refuse to open.
// System paths and candidates are cleaned using the same host path rules.
func IsSensitiveReadPath(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	for _, sensitive := range sensitiveReadPaths {
		if clean == filepath.Clean(sensitive) {
			return true
		}
	}
	base := filepath.Base(clean)
	for _, sensitive := range sensitiveReadBasenames {
		if base == sensitive {
			return true
		}
	}
	return false
}

// IsProtectedRuntimePath reports whether path names a runtime control or
// evidence file. Evaluation controlled targets are the sole writable subtree.
// The check is lexical so it is safe to use during L1 admission on a Gateway
// that does not know the Operator's filesystem root.
func IsProtectedRuntimePath(path string) bool {
	if path == "" {
		return false
	}

	clean := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(path, `\`, "/")))
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if !strings.EqualFold(part, constants.RuntimeDirname) {
			continue
		}
		if isEvaluationControlledTargetPathFromParts(parts, i) || isNestedOperatorRootFromParts(parts, i) {
			continue
		}
		return true
	}
	return false
}

// isNestedOperatorRootFromParts reports whether the runtime component at
// runtimeIndex only hosts a throwaway Operator's own root
// (.ztd/operators/<name>/...). That Operator's runtime is its own nested .ztd,
// which the caller's loop still protects; .ztd/operators itself stays protected.
func isNestedOperatorRootFromParts(parts []string, runtimeIndex int) bool {
	return runtimeIndex+3 < len(parts) && strings.EqualFold(parts[runtimeIndex+1], constants.OperatorsDirname)
}

// IsEvaluationControlledTargetPath reports whether path is lexically within
// the runtime's controlled evaluation target subtree.
func IsEvaluationControlledTargetPath(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(path, `\`, "/")))
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if strings.EqualFold(part, constants.RuntimeDirname) && isEvaluationControlledTargetPathFromParts(parts, i) {
			return true
		}
	}
	return false
}

func isEvaluationControlledTargetPathFromParts(parts []string, runtimeIndex int) bool {
	targetParts := strings.Split(constants.EvaluationControlledTargetDirname, "/")
	if runtimeIndex+len(targetParts) >= len(parts) {
		return false
	}
	for i, part := range targetParts {
		if !strings.EqualFold(parts[runtimeIndex+1+i], part) {
			return false
		}
	}
	return true
}

// IsEvaluationWorkspaceName reports whether name is one attempt workspace
// directory name: EvaluationWorkspacePrefix followed by
// EvaluationWorkspaceDigestHexLength lower-case hex characters.
func IsEvaluationWorkspaceName(name string) bool {
	digest, ok := strings.CutPrefix(name, constants.EvaluationWorkspacePrefix)
	return ok && len(digest) == constants.EvaluationWorkspaceDigestHexLength && strings.Trim(digest, "0123456789abcdef") == ""
}

// IsEvaluationWorkspaceSeedPath reports whether path is lexically a file an
// evaluation harness may seed under workDir: a file beneath an attempt
// workspace (<workDir>/workspaces/ws-<digest>/...) or that workspace's
// sentinel (<workDir>/workspaces/ws-<digest>.sentinel). Nothing else under
// workDir, and nothing outside it, qualifies.
func IsEvaluationWorkspaceSeedPath(path, workDir string) bool {
	if path == "" || workDir == "" || !filepath.IsAbs(path) || !filepath.IsAbs(workDir) {
		return false
	}
	rel, err := filepath.Rel(filepath.Join(workDir, constants.EvaluationWorkspaceDirname), filepath.Clean(path))
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 1 {
		name, ok := strings.CutSuffix(parts[0], constants.EvaluationWorkspaceSentinelSuffix)
		return ok && IsEvaluationWorkspaceName(name)
	}
	return IsEvaluationWorkspaceName(parts[0])
}

// IsShellRequired checks for shell-specific characters that might require a shell
func IsShellRequired(command string) bool {
	// Metacharacters that require shell processing:
	// |  - pipe
	// &  - background/logical AND
	// >  - output redirection
	// <  - input redirection
	// $  - variable expansion
	// ( ) - subshell
	// ;  - command separator
	// `  - backtick execution
	// \  - escape character
	// * ? [ ] - globbing
	// ~  - home directory expansion
	return strings.ContainsAny(command, "|&><$();`\\*?[]~")
}
