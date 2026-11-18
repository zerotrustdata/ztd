// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// ResolveHomePath expands a leading ~/ or ~\ relative to the current user's home directory.
func ResolveHomePath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir for %s: %w", p, err)
		}
		if p == "~" {
			return home, nil
		}
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, strings.TrimPrefix(p, "~/")), nil
		}
		return filepath.Join(home, strings.TrimPrefix(p, `~\`)), nil
	}
	return p, nil
}

// ReadOperatorDeploymentState reads the worker's recorded non-secret deployment progress.
func ReadOperatorDeploymentState(ctx context.Context, fileSvc fs.RuntimeFileService) (*models.OperatorDeploymentState, error) {
	data, err := fileSvc.ReadFile(ctx, filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator))
	if errors.Is(err, constants.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read operator deployment state: %w", err)
	}
	return DecodeOperatorDeploymentState(data)
}

// DecodeOperatorDeploymentState parses and validates an OperatorDeploymentState JSON payload.
func DecodeOperatorDeploymentState(data []byte) (*models.OperatorDeploymentState, error) {
	var state *models.OperatorDeploymentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("%w: decode deployment state: %w", constants.ErrOperatorDeployFailed, err)
	}
	if state == nil {
		return nil, nil
	}
	valid := !state.UpdatedAt.IsZero()
	switch state.Phase {
	case models.OperatorDeploymentPhaseRequesting:
		valid = valid && state.Fingerprints.Operator != "" && state.Fingerprints.CLI != ""
	case models.OperatorDeploymentPhasePendingApproval:
		valid = valid && state.RequestID != ""
	case models.OperatorDeploymentPhaseEnrolled, models.OperatorDeploymentPhaseReady:
		valid = valid && state.OperatorSessionID != ""
	case models.OperatorDeploymentPhaseFailed:
		valid = valid && state.Error != ""
	default:
		valid = false
	}
	if !valid {
		return nil, fmt.Errorf("%w: invalid deployment phase or missing progress fields", constants.ErrOperatorDeployFailed)
	}
	return state, nil
}
