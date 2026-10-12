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
	E2EFleetSessions   EnvVarKey
	Shell              EnvVarKey
	Lang               EnvVarKey
	Term               EnvVarKey
	TZ                 EnvVarKey
	Home               EnvVarKey
	User               EnvVarKey
	SSHAuthSock        EnvVarKey
	XDGRuntimeDir      EnvVarKey
	CloudflareAPIToken EnvVarKey
}{
	E2EFleetSessions:   EnvVarKey("ZTD_E2E_FLEET_SESSIONS"),
	Shell:              EnvVarKey("SHELL"),
	Lang:               EnvVarKey("LANG"),
	Term:               EnvVarKey("TERM"),
	TZ:                 EnvVarKey("TZ"),
	Home:               EnvVarKey("HOME"),
	User:               EnvVarKey("USER"),
	SSHAuthSock:        EnvVarKey("SSH_AUTH_SOCK"),
	XDGRuntimeDir:      EnvVarKey("XDG_RUNTIME_DIR"),
	CloudflareAPIToken: EnvVarKey("CLOUDFLARE_API_TOKEN"),
}

// SecretEnvVars are the registry keys in category "secret". They are ztd's own
// credentials and are withheld from governed child processes.
var SecretEnvVars = []EnvVarKey{
	EnvVar.CloudflareAPIToken,
}
