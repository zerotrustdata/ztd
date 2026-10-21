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

func testConformanceLoopCmd() *cobra.Command {
	return testConformanceLoopCmdWithRunner(realE2ERunner(os.Stdout, os.Stderr))
}

// conformanceLoopPostureTimeout is the default deadline per selected posture.
const conformanceLoopPostureTimeout = 12 * time.Minute

// The cross-process conformance qualification owns its isolated Gateway and
// Operators; like eval-loop, it needs no pre-existing platform.
func testConformanceLoopCmdWithRunner(runner e2eCommandRunner) *cobra.Command {
	var posture string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "conformance-loop",
		Short: "Qualify governance conformance across real Gateway and Operator processes",
		Long:  "For each posture, start an isolated Gateway with A2A ingress and a Data Operator, then drive `ztd mcp stdio` and run `ztd eval conformance run` once against the Data Operator and once against the embedded Operator. Each run must cover every applicable case and pass; under ratify and notary the runs are attended, and the enrolled CLI owner answers each suspended case with `ztd auth approve --cli` or `--deny`. Each run is re-verified offline, the audit chain read through the stdio bridge must advance, and both runtimes' `report all` evidence must verify. Run make build first. Evidence and private command logs remain under .ztd/operators/.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selector := "^TestConformanceLoopCrossProcess$"
			postures := len(constants.AllPostures)
			if posture != "" {
				if !constants.Posture(posture).Valid() {
					return fmt.Errorf("conformance-loop posture %q: %w", posture, constants.ErrInvalidPosture)
				}
				selector += "/^" + posture + "$"
				postures = 1
			}
			if !cmd.Flags().Changed("timeout") {
				timeout = conformanceLoopPostureTimeout * time.Duration(postures)
			}
			if timeout <= 0 {
				return fmt.Errorf("conformance-loop timeout: %w", constants.ErrValidationFailed)
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
	cmd.Flags().StringVar(&posture, "posture", "", "Select doctrine, consensus, ratify or notary (default: all four)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Overall test deadline (default: 12m per selected posture)")
	return cmd
}
