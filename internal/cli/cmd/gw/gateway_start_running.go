// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// startupSetting is one `gw start` flag that is fixed for the lifetime of a
// Gateway process, with the profile value rendered for comparison.
type startupSetting struct {
	flag  string
	value func(serve.GatewayConfig) string
}

// startupSettings lists the flags a running Gateway cannot change. Each value
// is normalized so an explicit default compares equal to the running default.
var startupSettings = []startupSetting{
	{"capabilities", func(c serve.GatewayConfig) string { return c.OperatorCapabilities.WithData().String() }},
	{"posture", func(c serve.GatewayConfig) string { return string(c.Posture) }},
	{"listen-host", func(c serve.GatewayConfig) string {
		if c.ListenHost == "" {
			return constants.LocalhostIP
		}
		return c.ListenHost
	}},
	{"http-port", func(c serve.GatewayConfig) string { return portOrDefault(c.HTTPPort, constants.Ports.OperatorHttp) }},
	{"https-port", func(c serve.GatewayConfig) string { return portOrDefault(c.HTTPSPort, constants.Ports.OperatorHttps) }},
	{"model-storage-root", func(c serve.GatewayConfig) string { return c.ProvenanceOperatorModelStorageRoot }},
	{"inference-ollama-endpoint", func(c serve.GatewayConfig) string { return c.InferenceOllamaEndpoint }},
	{"public-base-url", func(c serve.GatewayConfig) string { return c.PublicBaseURL }},
	{"cors-origin", func(c serve.GatewayConfig) string { return joinOrigins(c.AllowedOrigins) }},
	{"passkey-rp-id", func(c serve.GatewayConfig) string { return c.PasskeyRpID }},
	{"passkey-rp-name", func(c serve.GatewayConfig) string { return c.PasskeyRpName }},
	{"passkey-rp-origin", func(c serve.GatewayConfig) string { return joinOrigins(c.PasskeyRpOrigins) }},
}

func portOrDefault(port, fallback int) string {
	if port == 0 {
		port = fallback
	}
	return strconv.Itoa(port)
}

// runningConfigDeltas compares the startup flags the caller set explicitly
// with the running Gateway's launch profile. Flags left at their defaults are
// not compared, so a bare `gw start` reuses whatever Gateway is running.
func runningConfigDeltas(flags *pflag.FlagSet, requested, running serve.GatewayConfig) []configDelta {
	var deltas []configDelta
	for _, setting := range startupSettings {
		if !flags.Changed(setting.flag) {
			continue
		}
		want, have := setting.value(requested), setting.value(running)
		if want != have {
			deltas = append(deltas, configDelta{Field: "--" + setting.flag, Current: have, Proposed: want})
		}
	}
	return deltas
}

// reuseRunningGateway decides whether `gw start` may succeed against the
// Gateway that is already running. It succeeds when every explicitly requested
// startup flag matches the running launch profile, and otherwise fails with
// the differences and the stop-then-start remedy rather than reporting success
// while the old configuration stays active.
func reuseRunningGateway(cmd *cobra.Command, fileSvc fs.RuntimeFileService, requested serve.GatewayConfig, pid int) error {
	bin := getBinaryName()
	profile, err := serve.ReadLaunchProfile(fileSvc)
	if err != nil {
		if !errors.Is(err, constants.ErrLaunchProfileMissing) {
			return fmt.Errorf("gateway start: read launch profile: %w", err)
		}
		if explicit := explicitStartupFlags(cmd.Flags()); len(explicit) > 0 {
			cmd.Printf("ztd Gateway is already running (PID: %d) without a launch profile, so %v cannot be verified.\n", pid, explicit)
			cmd.Printf("Stop it with '%s gw stop', then rerun this command.\n", bin)
			return fmt.Errorf("%w: PID %d: %w", constants.ErrGatewayAlreadyRunning, pid, constants.ErrLaunchProfileMissing)
		}
		cmd.Printf("ztd Gateway is already running (PID: %d)\n", pid)
		return nil
	}

	deltas := runningConfigDeltas(cmd.Flags(), requested, profile.Config)
	if len(deltas) == 0 {
		cmd.Printf("ztd Gateway is already running (PID: %d)\n", pid)
		return nil
	}
	cmd.Printf("ztd Gateway is already running (PID: %d) with a different startup configuration.\n", pid)
	cmd.Println()
	renderConfigDelta(cmd, deltas)
	cmd.Printf("These settings are fixed while the Gateway runs. Stop it with '%s gw stop'\n", bin)
	cmd.Println("(or 'make down'), then rerun this command. Runtime state in .ztd/ is kept.")
	return fmt.Errorf("%w: PID %d: requested startup configuration differs", constants.ErrGatewayAlreadyRunning, pid)
}

func explicitStartupFlags(flags *pflag.FlagSet) []string {
	var explicit []string
	for _, setting := range startupSettings {
		if flags.Changed(setting.flag) {
			explicit = append(explicit, "--"+setting.flag)
		}
	}
	return explicit
}
