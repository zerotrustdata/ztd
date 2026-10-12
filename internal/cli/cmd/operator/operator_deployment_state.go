// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package operatorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func operatorDeploymentStateCmd() *cobra.Command {
	return operatorDeploymentStateCmdWithFactory(shared.NewFileSvc)
}

func operatorDeploymentStateCmdWithFactory(factory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use: "deployment-state", Short: "Read local non-secret Operator deployment progress as JSON", Args: cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileSvc, err := factory(dir, slog.Default())
			if err != nil {
				return fmt.Errorf("operator deployment state: %w: %w", constants.ErrFileServiceInit, err)
			}
			state, err := readOperatorDeploymentState(cmd.Context(), fileSvc)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
		},
	}
	cmd.Flags().StringVar(&dir, "working-dir", "", "Operator working directory (default: current directory)")
	return cmd
}

func readOperatorDeploymentState(ctx context.Context, fileSvc fs.RuntimeFileService) (*models.OperatorDeploymentState, error) {
	data, err := fileSvc.ReadFile(ctx, filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator))
	if errors.Is(err, constants.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read operator deployment state: %w", err)
	}
	return decodeOperatorDeploymentState(data)
}

func decodeOperatorDeploymentState(data []byte) (*models.OperatorDeploymentState, error) {
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

// awaitStaged waits for the Gateway to announce the worker's pending request.
func awaitStaged(ctx context.Context, watch *deploymentWatch, target deployTarget, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operatorDeployEnrollTimeout)
	defer cancel()
	requestID, err := watch.awaitStaged(ctx)
	if err != nil {
		return "", fmt.Errorf("await staged request in %s: %w", dir, explainDeployWait(target, dir, err))
	}
	return requestID, nil
}

// workerFingerprints reads the CSR fingerprints the worker recorded for this
// launch. The launch ID is visible to other processes, so approval binds to
// these keys rather than to whichever request announced the launch ID; a
// worker that recorded none fails closed.
func workerFingerprints(ctx context.Context, target deployTarget, dir string) (models.PlatformEnrollmentCSRFingerprints, error) {
	state, err := target.readDeploymentState(ctx, dir)
	if err != nil {
		return models.PlatformEnrollmentCSRFingerprints{}, fmt.Errorf("read worker keys in %s: %w", dir, err)
	}
	if state == nil || state.Fingerprints.Operator == "" || state.Fingerprints.CLI == "" {
		return models.PlatformEnrollmentCSRFingerprints{}, fmt.Errorf("%w: worker in %s recorded no enrollment keys for this launch", constants.ErrOperatorDeployFailed, dir)
	}
	return state.Fingerprints, nil
}
