// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// consensusStatusLine is the gw status Consensus row, built from the /health
// consensus summary and the posture the Gateway reports.
func consensusStatusLine(h models.HealthResponse) string {
	return "Consensus  " + h.Consensus.Describe(h.Posture)
}

// gatewayStatusJSON is the --json shape of `gw status`.
type gatewayStatusJSON struct {
	Running   bool                   `json:"running"`
	State     string                 `json:"state"`
	PID       int                    `json:"pid,omitempty"`
	Health    *models.HealthResponse `json:"health,omitempty"`
	Operators *operatorInventory     `json:"operators,omitempty"`
	Console   string                 `json:"console,omitempty"`
}

func printGatewayStatus(cmd *cobra.Command, cfg *config.Config, clientFactory authcmd.APIClientFactory, fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)) error {
	fileSvc, err := fileSvcFactory("", slog.Default())
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
	}
	client, clientErr := clientFactory(fileSvc, cfg)
	healthy, pid := false, 0
	var health models.HealthResponse
	if clientErr == nil {
		body, err := client.Get(constants.APIPaths.Health)
		healthy = err == nil && json.Unmarshal(body, &health) == nil && health.Status == constants.GatewayModeStatusOK
		if healthy {
			pid = health.PID
		}
	} else {
		client = nil
	}
	running := healthy
	if !healthy {
		pm, err := platform.NewProcessManager(fileSvc)
		if err != nil {
			return fmt.Errorf("%w: %w", constants.ErrInternal, err)
		}
		running, pid, err = pm.OperatorStatus()
		if err != nil {
			return fmt.Errorf("%w: %w", constants.ErrPIDReadFailed, err)
		}
	}
	if !running {
		if output.JSONEnabled(cmd) {
			return output.WriteJSON(cmd.OutOrStdout(), gatewayStatusJSON{State: "stopped"})
		}
		cmd.Println("Gateway  stopped")
		return nil
	}
	state := "running"
	if healthy {
		state = "online"
	}
	if output.JSONEnabled(cmd) {
		var launched serve.GatewayConfig
		if profile, err := serve.ReadLaunchProfile(fileSvc); err == nil {
			launched = profile.Config
		}
		inv := collectOperatorInventory(client, fileSvc, cfg)
		out := gatewayStatusJSON{Running: true, State: state, PID: pid, Operators: &inv, Console: consoleOriginFor(launched.PublicBaseURL, launched.HTTPSPort) + "/console/"}
		if healthy {
			out.Health = &health
		}
		return output.WriteJSON(cmd.OutOrStdout(), out)
	}
	if pid > 0 {
		cmd.Printf("Gateway  %s (PID %d)\n", state, pid)
	} else {
		cmd.Printf("Gateway  %s\n", state)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	printOperatorTables(cmd.OutOrStdout(), collectOperatorInventory(client, fileSvc, cfg))
	fmt.Fprintln(cmd.OutOrStdout())
	printEnrollmentSummary(cmd.OutOrStdout(), client)
	if healthy {
		cmd.Println(consensusStatusLine(health))
	}
	var launched serve.GatewayConfig
	if profile, err := serve.ReadLaunchProfile(fileSvc); err == nil {
		launched = profile.Config
	}
	origin := consoleOriginFor(launched.PublicBaseURL, launched.HTTPSPort)
	fmt.Fprintln(cmd.OutOrStdout())
	cmd.Printf("Console  %s/console/\n", origin)
	return nil
}
