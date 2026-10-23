// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

// EnvVarKey is a typed string for environment variable names.
type EnvVarKey string

// EnvVar groups all environment variable name constants consumed by ztdo.
var EnvVar = struct {
	OllamaEndpoint        EnvVarKey
	E2EFleetSessions      EnvVarKey
	E2ERuntimeRoot        EnvVarKey
	E2EFleetSize          EnvVarKey
	E2EFleetBin           EnvVarKey
	E2EFleetDir           EnvVarKey
	E2EFleetSoak          EnvVarKey
	E2EFleetConcurrency   EnvVarKey
	E2EFleetRounds        EnvVarKey
	E2EFleetReport        EnvVarKey
	E2EFleetRestartReport EnvVarKey
	E2EGatewayHTTPPort    EnvVarKey
	E2EGatewayHTTPSPort   EnvVarKey
	Shell                 EnvVarKey
	Lang                  EnvVarKey
	Term                  EnvVarKey
	TZ                    EnvVarKey
	Home                  EnvVarKey
	User                  EnvVarKey
	SSHAuthSock           EnvVarKey
	XDGRuntimeDir         EnvVarKey
	DBusSessionBusAddress EnvVarKey
	Display               EnvVarKey
	WaylandDisplay        EnvVarKey
	Browser               EnvVarKey
	CloudflareAPIToken    EnvVarKey
}{
	OllamaEndpoint:        EnvVarKey("ZTD_OLLAMA_ENDPOINT"),
	E2EFleetSessions:      EnvVarKey("ZTD_E2E_FLEET_SESSIONS"),
	E2ERuntimeRoot:        EnvVarKey("ZTD_E2E_RUNTIME_ROOT"),
	E2EFleetSize:          EnvVarKey("ZTD_E2E_FLEET_SIZE"),
	E2EFleetBin:           EnvVarKey("ZTD_E2E_FLEET_BIN"),
	E2EFleetDir:           EnvVarKey("ZTD_E2E_FLEET_DIR"),
	E2EFleetSoak:          EnvVarKey("ZTD_E2E_FLEET_SOAK"),
	E2EFleetConcurrency:   EnvVarKey("ZTD_E2E_FLEET_CONCURRENCY"),
	E2EFleetRounds:        EnvVarKey("ZTD_E2E_FLEET_ROUNDS"),
	E2EFleetReport:        EnvVarKey("ZTD_E2E_FLEET_REPORT"),
	E2EFleetRestartReport: EnvVarKey("ZTD_E2E_FLEET_RESTART_REPORT"),
	E2EGatewayHTTPPort:    EnvVarKey("ZTD_E2E_GATEWAY_HTTP_PORT"),
	E2EGatewayHTTPSPort:   EnvVarKey("ZTD_E2E_GATEWAY_HTTPS_PORT"),
	Shell:                 EnvVarKey("SHELL"),
	Lang:                  EnvVarKey("LANG"),
	Term:                  EnvVarKey("TERM"),
	TZ:                    EnvVarKey("TZ"),
	Home:                  EnvVarKey("HOME"),
	User:                  EnvVarKey("USER"),
	SSHAuthSock:           EnvVarKey("SSH_AUTH_SOCK"),
	XDGRuntimeDir:         EnvVarKey("XDG_RUNTIME_DIR"),
	DBusSessionBusAddress: EnvVarKey("DBUS_SESSION_BUS_ADDRESS"),
	Display:               EnvVarKey("DISPLAY"),
	WaylandDisplay:        EnvVarKey("WAYLAND_DISPLAY"),
	Browser:               EnvVarKey("BROWSER"),
	CloudflareAPIToken:    EnvVarKey("CLOUDFLARE_API_TOKEN"),
}

// SecretEnvVars are the registry keys in category "secret". They are ztd's own
// credentials and are withheld from governed child processes.
var SecretEnvVars = []EnvVarKey{
	EnvVar.CloudflareAPIToken,
}

// UserEndpointEnvVars are the registry keys in category "user_endpoint".
var UserEndpointEnvVars = []EnvVarKey{
	EnvVar.OllamaEndpoint,
}
