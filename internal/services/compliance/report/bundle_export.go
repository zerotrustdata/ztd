// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	ztdfs "github.com/zerotrustdata/ztd/internal/services/fs"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

// ExportBundle opens the sealed bundle persisted for reportID and writes it
// in the clear to outDir, the replayable form "compliance report verify" and
// "compliance release-evidence" read. It is the only path by which a persisted
// bundle leaves the runtime unsealed, so it runs only when an owner chooses to
// disclose the bundle: outDir must not exist and must lie outside the runtime
// root, and every body must match its descriptor before it is written. It
// returns the path of the exported descriptor.
func ExportBundle(ctx context.Context, fileSvc ztdfs.RuntimeFileService, reportID, outDir string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("%w: context is required", constants.ErrBundleExportFailed)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrBundleExportFailed, err)
	}
	if fileSvc == nil || outDir == "" {
		return "", fmt.Errorf("%w: file service and output directory are required", constants.ErrBundleExportFailed)
	}
	if !safeReportID(reportID) {
		return "", fmt.Errorf("%w: unsafe report ID %q", constants.ErrBundleExportFailed, reportID)
	}
	bundleDir := path.Join(constants.ComplianceBundlesDirname, reportID)
	if !evaluation.SealsPath(fileSvc, bundleDir) {
		return "", fmt.Errorf("%w: report bundles are sealed at rest and the file service does not open %s", constants.ErrBundleExportFailed, bundleDir)
	}
	descriptorBody, err := fileSvc.ReadFile(ctx, path.Join(bundleDir, constants.ComplianceBundleManifestPath))
	if err != nil {
		return "", fmt.Errorf("%w: read sealed bundle descriptor: %w", constants.ErrBundleExportFailed, err)
	}
	bundle := &compliancev1.ComplianceReportBundle{}
	if err := compliancev1.UnmarshalCanonical(descriptorBody, bundle); err != nil {
		return "", fmt.Errorf("%w: decode sealed bundle descriptor: %w", constants.ErrBundleExportFailed, err)
	}
	if bundle.GetManifest().GetReportId() != reportID {
		return "", fmt.Errorf("%w: sealed bundle descriptor names report %q", constants.ErrBundleExportFailed, bundle.GetManifest().GetReportId())
	}
	bodies := make(map[string][]byte, len(bundle.GetArtifacts()))
	for _, artifact := range bundle.GetArtifacts() {
		bundlePath := artifact.GetBundlePath()
		if !fs.ValidPath(bundlePath) || bundlePath == "." || bundlePath == constants.ComplianceBundleManifestPath {
			return "", fmt.Errorf("%w: unsafe artifact path %q", constants.ErrBundleExportFailed, bundlePath)
		}
		if _, duplicate := bodies[bundlePath]; duplicate {
			return "", fmt.Errorf("%w: duplicate artifact path %s", constants.ErrBundleExportFailed, bundlePath)
		}
		body, err := fileSvc.ReadFile(ctx, path.Join(bundleDir, bundlePath))
		if err != nil {
			return "", fmt.Errorf("%w: read sealed body %s: %w", constants.ErrBundleExportFailed, bundlePath, err)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != artifact.GetSha256() || int64(len(body)) != artifact.GetByteLength() {
			return "", fmt.Errorf("%w: sealed body %s does not match its descriptor", constants.ErrBundleExportFailed, bundlePath)
		}
		bodies[bundlePath] = body
	}

	exportDir, err := newExportDir(fileSvc, outDir)
	if err != nil {
		return "", err
	}
	if err := writeExportedBundle(ctx, exportDir, bundle, bodies, descriptorBody); err != nil {
		if removeErr := os.RemoveAll(exportDir); removeErr != nil {
			return "", fmt.Errorf("%w: %w; remove incomplete export: %w", constants.ErrBundleExportFailed, err, removeErr)
		}
		return "", fmt.Errorf("%w: %w", constants.ErrBundleExportFailed, err)
	}
	return filepath.Join(exportDir, constants.ComplianceBundleManifestPath), nil
}

// newExportDir creates outDir, which must not exist yet and must resolve
// outside the runtime root: a plaintext bundle inside .ztd/ would undo the
// sealing it is exported from.
func newExportDir(fileSvc ztdfs.RuntimeFileService, outDir string) (string, error) {
	absolute, err := filepath.Abs(outDir)
	if err != nil {
		return "", fmt.Errorf("%w: resolve output directory: %w", constants.ErrBundleExportFailed, err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", fmt.Errorf("%w: output directory %s already exists", constants.ErrBundleExportFailed, absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect output directory: %w", constants.ErrBundleExportFailed, err)
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, constants.PermDirPrivate); err != nil {
		return "", fmt.Errorf("%w: create output parent: %w", constants.ErrBundleExportFailed, err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("%w: resolve output parent: %w", constants.ErrBundleExportFailed, err)
	}
	resolved := filepath.Join(resolvedParent, filepath.Base(absolute))
	runtimeDir := fileSvc.Resolve("")
	if resolvedRuntime, err := filepath.EvalSymlinks(runtimeDir); err == nil {
		runtimeDir = resolvedRuntime
	}
	if rel, err := filepath.Rel(runtimeDir, resolved); err == nil && rel != constants.PathParentDir && !strings.HasPrefix(rel, constants.PathParentDir+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: output directory %s is inside the runtime root %s", constants.ErrBundleExportFailed, resolved, runtimeDir)
	}
	if err := os.Mkdir(resolved, constants.PermDirPrivate); err != nil {
		return "", fmt.Errorf("%w: create output directory: %w", constants.ErrBundleExportFailed, err)
	}
	return resolved, nil
}

func writeExportedBundle(ctx context.Context, exportDir string, bundle *compliancev1.ComplianceReportBundle, bodies map[string][]byte, descriptorBody []byte) error {
	root, err := os.OpenRoot(exportDir)
	if err != nil {
		return fmt.Errorf("open output directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, artifact := range bundle.GetArtifacts() {
		if err := ctx.Err(); err != nil {
			return err
		}
		bundlePath := artifact.GetBundlePath()
		if dir := path.Dir(bundlePath); dir != "." {
			if err := root.MkdirAll(dir, constants.PermDirPrivate); err != nil {
				return fmt.Errorf("create directory for %s: %w", bundlePath, err)
			}
		}
		if err := root.WriteFile(bundlePath, bodies[bundlePath], constants.PermFilePrivate); err != nil {
			return fmt.Errorf("write body %s: %w", bundlePath, err)
		}
	}
	if err := root.WriteFile(constants.ComplianceBundleManifestPath, descriptorBody, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("write bundle descriptor: %w", err)
	}
	return nil
}
