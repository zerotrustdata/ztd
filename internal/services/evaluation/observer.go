// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// RuntimeTargetObserverID names the independent observer in evaluation
// deployment identities. The observer runs in-process on the host and reads
// the controlled target through the target runtime's RuntimeFileService.
const RuntimeTargetObserverID = constants.EvaluationRuntimeTargetObserverID

// controlledTargetDirRel is the controlled target directory, relative to the
// runtime root (.ztd/). Targets are written by the Data Operator through a
// governed file edit and observed only through this directory.
const controlledTargetDirRel = constants.EvaluationControlledTargetDirname

type TargetState struct {
	Present    bool
	Content    []byte
	ObservedAt time.Time
}

// ControlledTargetResource returns the absolute host path of the controlled
// target file for runID inside the target runtime's .ztd/ tree.
func ControlledTargetResource(fileSvc fs.RuntimeFileService, runID string) string {
	return fileSvc.Resolve(path.Join(controlledTargetDirRel, controlledTargetFilename(runID)))
}

// ControlledTargetDirectory returns the relative runtime path of the directory
// that holds controlled targets, for callers that must create it before a run.
func ControlledTargetDirectory() string {
	return controlledTargetDirRel
}

func controlledTargetFilename(runID string) string {
	return constants.EvaluationTargetFilenamePrefix + runID + constants.FileExtText
}

type runtimeTargetReader struct {
	fileSvc fs.RuntimeFileService
	now     func() time.Time
}

// NewRuntimeTargetReader observes controlled targets through the target
// runtime's RuntimeFileService. No external process is started.
func NewRuntimeTargetReader(fileSvc fs.RuntimeFileService) TargetObserver {
	return newRuntimeTargetReader(fileSvc, time.Now)
}

func newRuntimeTargetReader(fileSvc fs.RuntimeFileService, now func() time.Time) *runtimeTargetReader {
	return &runtimeTargetReader{fileSvc: fileSvc, now: now}
}

func (o *runtimeTargetReader) Observe(ctx context.Context, targetResource string) (*TargetState, error) {
	if o == nil || o.fileSvc == nil || o.now == nil {
		return nil, fmt.Errorf("%w: runtime file service and clock are required", constants.ErrEvaluationObservationUnavailable)
	}
	rel, err := o.controlledRelPath(targetResource)
	if err != nil {
		return nil, err
	}
	content, readErr := o.fileSvc.ReadFile(ctx, rel)
	observedAt := o.now().UTC()
	if errors.Is(readErr, constants.ErrNotFound) {
		return &TargetState{ObservedAt: observedAt}, nil
	}
	if readErr != nil {
		return nil, fmt.Errorf("%w: read controlled target: %w", constants.ErrEvaluationObservationUnavailable, readErr)
	}
	return &TargetState{Present: true, Content: content, ObservedAt: observedAt}, nil
}

// controlledRelPath maps an absolute target to its runtime-relative path and
// rejects anything that is not exactly a controlled target file.
func (o *runtimeTargetReader) controlledRelPath(targetResource string) (string, error) {
	if !filepath.IsAbs(targetResource) {
		return "", fmt.Errorf("%w: controlled target must be an absolute path", constants.ErrEvaluationObservationUnavailable)
	}
	filename := filepath.Base(targetResource)
	if !complianceevidence.ValidPathElement(filename) {
		return "", fmt.Errorf("%w: controlled target filename is invalid", constants.ErrEvaluationObservationUnavailable)
	}
	rel := path.Join(controlledTargetDirRel, filename)
	if filepath.Clean(targetResource) != filepath.Clean(o.fileSvc.Resolve(rel)) {
		return "", fmt.Errorf("%w: target is outside the controlled target directory", constants.ErrEvaluationObservationUnavailable)
	}
	return rel, nil
}
