// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func testEvalLoopCmd() *cobra.Command {
	return testEvalLoopCmdWithRunner(realE2ERunner(os.Stdout, os.Stderr))
}

// Test binary flags read by the live (Tier 4) eval loop, which is built only
// with the e2e and live tags.
const (
	evalLoopLiveEndpointFlag = "eval-loop-live-endpoint"
	evalLoopLiveModelFlag    = "eval-loop-live-model"
)

// The cross-process qualification owns its isolated Gateway and Operators;
// unlike the live e2e suite, it needs no pre-existing platform. With
// --live-ollama-endpoint it is the Tier 4 live-provider target instead.
func testEvalLoopCmdWithRunner(runner e2eCommandRunner) *cobra.Command {
	var posture, liveEndpoint, liveModel string
	timeout := 15 * time.Minute
	cmd := &cobra.Command{
		Use:   "eval-loop",
		Short: "Qualify eval execution across real Gateway and Operator processes",
		Long:  "Start isolated Gateway, Data and Inference processes under ratify and notary, exercise eval through CLI commands, approve through separate CLI processes, and verify each Operator's authoritative local receipts. By default only the Ollama provider replies are scripted. With --live-ollama-endpoint and --live-model, the Inference Operator reaches that live provider instead (Tier 4; default timeout 40m). Run make build first. Evidence and private command logs remain under .ztd/operators/.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			live := liveEndpoint != "" || liveModel != ""
			selector, tags := "^TestEvalLoopCrossProcess$", "-tags=e2e"
			if live {
				if err := validateEvalLoopLiveTarget(liveEndpoint, liveModel); err != nil {
					return err
				}
				selector, tags = "^TestEvalLoopLive$", "-tags=e2e,live"
				if !cmd.Flags().Changed("timeout") {
					timeout = 40 * time.Minute
				}
			}
			if posture != "" {
				if posture != string(constants.PostureRatify) && posture != string(constants.PostureNotary) {
					return fmt.Errorf("eval-loop posture %q: %w", posture, constants.ErrInvalidPosture)
				}
				selector += "/^" + posture + "$"
			}
			if timeout <= 0 {
				return fmt.Errorf("eval-loop timeout: %w", constants.ErrValidationFailed)
			}
			args := []string{"test", tags, "-count=1", "-parallel=1", "-timeout", timeout.String(), "-v", "-run", selector}
			if runtime.GOOS != "windows" {
				args = append(args, "-race")
			}
			args = append(args, "./internal/cli/cmd/test")
			if live {
				args = append(args, "-args", "-"+evalLoopLiveEndpointFlag+"="+liveEndpoint, "-"+evalLoopLiveModelFlag+"="+liveModel)
			}
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
	cmd.Flags().StringVar(&liveEndpoint, "live-ollama-endpoint", "", "Run the live Tier 4 loop against this Ollama endpoint, reached only through the governed Inference Operator")
	cmd.Flags().StringVar(&liveModel, "live-model", "", "Provider model for the live Tier 4 loop (requires --live-ollama-endpoint)")
	return cmd
}

// validateEvalLoopLiveTarget requires both live flags and an absolute
// http(s) endpoint before any child process starts.
func validateEvalLoopLiveTarget(endpoint, model string) error {
	if endpoint == "" || strings.TrimSpace(model) == "" {
		return fmt.Errorf("eval-loop live target needs --live-ollama-endpoint and --live-model: %w", constants.ErrValidationFailed)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("eval-loop live endpoint %q: %w", endpoint, constants.ErrValidationFailed)
	}
	return nil
}
