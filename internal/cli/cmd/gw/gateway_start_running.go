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
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// startupSetting is one `gw start` flag that is fixed for the lifetime of a
// Gateway process, with the profile value rendered for comparison. env names
// the user_endpoint key that supplies the flag when it is blank.
type startupSetting struct {
	flag  string
	value func(serve.GatewayConfig) string
	env   constants.EnvVarKey
}

// requested reports whether the caller asked for this setting: the flag was
// set, or its env key (exported or loaded from .env) supplies it.
func (s startupSetting) requested(flags *pflag.FlagSet) bool {
	if flags.Changed(s.flag) {
		return true
	}
	return s.env != "" && strings.TrimSpace(os.Getenv(string(s.env))) != ""
}

// startupSettings lists the flags a running Gateway cannot change. Each value
// is normalized so an explicit default compares equal to the running default.
var startupSettings = []startupSetting{
	{flag: "capabilities", value: func(c serve.GatewayConfig) string { return c.OperatorCapabilities.WithData().String() }},
	{flag: "posture", value: func(c serve.GatewayConfig) string { return string(c.Posture) }},
	{flag: "listen-host", value: func(c serve.GatewayConfig) string {
		if c.ListenHost == "" {
			return constants.LocalhostIP
		}
		return c.ListenHost
	}},
	{flag: "http-port", value: func(c serve.GatewayConfig) string { return portOrDefault(c.HTTPPort, constants.Ports.OperatorHttp) }},
	{flag: "https-port", value: func(c serve.GatewayConfig) string { return portOrDefault(c.HTTPSPort, constants.Ports.OperatorHttps) }},
	{flag: "model-storage-root", value: func(c serve.GatewayConfig) string { return c.ProvenanceOperatorModelStorageRoot }},
	{flag: "inference-ollama-endpoint", value: func(c serve.GatewayConfig) string { return c.InferenceOllamaEndpoint }, env: constants.EnvVar.OllamaEndpoint},
	{flag: "public-base-url", value: func(c serve.GatewayConfig) string { return c.PublicBaseURL }},
	{flag: "cors-origin", value: func(c serve.GatewayConfig) string { return joinOrigins(c.AllowedOrigins) }},
	{flag: "passkey-rp-id", value: func(c serve.GatewayConfig) string { return c.PasskeyRpID }},
	{flag: "passkey-rp-name", value: func(c serve.GatewayConfig) string { return c.PasskeyRpName }},
	{flag: "passkey-rp-origin", value: func(c serve.GatewayConfig) string { return joinOrigins(c.PasskeyRpOrigins) }},
}

func portOrDefault(port, fallback int) string {
	if port == 0 {
		port = fallback
	}
	return strconv.Itoa(port)
}

// runningConfigDeltas compares the startup settings the caller requested (by
// flag or env key) with the running Gateway's launch profile. Settings left at
// their defaults are not compared, so a bare `gw start` reuses whatever
// Gateway is running.
func runningConfigDeltas(flags *pflag.FlagSet, requested, running serve.GatewayConfig) []configDelta {
	var deltas []configDelta
	for _, setting := range startupSettings {
		if !setting.requested(flags) {
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
		if setting.requested(flags) {
			explicit = append(explicit, "--"+setting.flag)
		}
	}
	return explicit
}
