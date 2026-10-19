// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func testEvalLoopCmd() *cobra.Command {
	return testEvalLoopCmdWithRunner(realE2ERunner(os.Stdout, os.Stderr))
}

// The cross-process qualification owns its isolated Gateway and Operators;
// unlike the live e2e suite, it needs no pre-existing platform or model provider.
func testEvalLoopCmdWithRunner(runner e2eCommandRunner) *cobra.Command {
	var posture string
	timeout := 15 * time.Minute
	cmd := &cobra.Command{
		Use:   "eval-loop",
		Short: "Qualify eval execution across real Gateway and Operator processes",
		Long:  "Start isolated Gateway, Data and Inference processes under ratify and notary, exercise eval through CLI commands, approve through separate CLI processes, and verify each Operator's authoritative local receipts. Only the Ollama provider replies are scripted. Run make build first. Evidence and private command logs remain under .ztd/operators/.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selector := "^TestEvalLoopCrossProcess$"
			if posture != "" {
				if posture != string(constants.PostureRatify) && posture != string(constants.PostureNotary) {
					return fmt.Errorf("eval-loop posture %q: %w", posture, constants.ErrInvalidPosture)
				}
				selector += "/^" + posture + "$"
			}
			if timeout <= 0 {
				return fmt.Errorf("eval-loop timeout: %w", constants.ErrValidationFailed)
			}
			args := []string{"test", "-tags=e2e", "-count=1", "-parallel=1", "-timeout", timeout.String(), "-v", "-run", selector}
			if runtime.GOOS != "windows" {
				args = append(args, "-race")
			}
			args = append(args, "./internal/cli/cmd/test")
			code, err := runner(cmd.Context(), "go", args...)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrE2ETestsFailed, err)
			}
			if code != 0 {
				return fmt.Errorf("%w: exit code %d", constants.ErrE2ETestsFailed, code)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&posture, "posture", "", "Select ratify or notary (default: both)")
	cmd.Flags().DurationVar(&timeout, "timeout", timeout, "Overall test deadline")
	return cmd
}
