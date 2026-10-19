// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package config holds everything the evaluation harness client needs to dial
// a real Gateway over mTLS. Callers build Config from the CLI's resolved
// configuration and credentials.
package config

import (
	"crypto/tls"
)

// Auth selects how Agent Harness authenticates to the Gateway's mTLS surface.
// The TLS listener negotiates client certs, so a cert is the default.
type Auth struct {
	// ClientCert / ClientKey are the BYO-client mTLS material minted by
	// `ztd auth bootstrap` / `ztd auth login` (PEM paths).
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
	// Certificate is an already-loaded client identity. It takes precedence
	// over ClientCert/ClientKey, which must be plaintext PEM. The CLI and app
	// keys under .ztd/ are sealed by the keystore, so callers unseal them in
	// memory and pass the result here.
	Certificate *tls.Certificate `json:"-"`
	// CABundle is the Gateway root/hub bundle (.well-known/ztd/pki/hub-bundle.pem).
	CABundle string `json:"ca_bundle"`
}

// Config is the full Agent Harness runtime configuration.
type Config struct {
	// MTLSBaseURL is the Gateway mTLS API surface (governance envelope, MCP/A2A,
	// audit).
	MTLSBaseURL string `json:"mtls_base_url"`
	// PublicBaseURL is the Gateway public surface used for the L3
	// suspend/approve out-of-band notary flow. The harness dials this
	// URL directly (SSE subscription, approval status verification), so
	// it must be reachable from wherever the harness process runs — in
	// the demo topology that is inside the agent container, hence the
	// container-internal ztd.local address.
	PublicBaseURL string `json:"public_base_url"`
	// ApprovalDisplayURL is the host-reachable base URL used to render
	// the human-facing approval link printed to stderr. It is distinct
	// from PublicBaseURL because the harness process (inside a
	// container) and the human approver (host browser) reach the
	// gateway via different addresses/ports. When empty, the printed
	// link falls back to PublicBaseURL.
	ApprovalDisplayURL string `json:"approval_display_url"`

	Auth Auth `json:"auth"`

	// UseCLIConfig uses the CLI credentials directory for all paths.
	UseCLIConfig bool `json:"use_cli_config"`

	// CLIAuth holds the host CLI mTLS material used for notary-scenario
	// transaction submits. When populated, the harness client builds a
	// second http.Client with this cert pair so handleCLIAuth stamps the
	// host user's identity onto the suspended transaction. Non-notary MCP
	// calls continue using the operator cert in Auth.
	CLIAuth Auth `json:"cli_auth"`

	// OperatorSessionID scopes audit receipt queries to the real Operator that
	// executed the work. If empty, Agent Harness tries to discover it from /api/operators.
	OperatorSessionID string `json:"operator_session_id"`

	// OperatorID is the logical ID of the Operator (e.g. "embedded-operator").
	// When empty, Agent Harness tries to discover it or load it from CLI credentials.
	OperatorID string `json:"operator_id"`

	// UserID is the host CLI user_id used to scope the SSE approval subscription.
	// When set, SubscribeApprovals waits for approvals tagged with this user_id instead of the
	// operator id, so the harness receives events for transactions tagged with
	// the host user's identity.
	UserID string `json:"user_id"`

	// CLISessionID is the host CLI session id sent as X-CLI-Session-ID on
	// notary submit so handleCLIAuth stamps the host user onto the suspended
	// transaction.
	CLISessionID string `json:"cli_session_id"`

	// OutDir is where the auditor writes its detailed run report and receipt export.
	OutDir string `json:"out_dir"`

	// Verbose echoes every request/response to stderr as it happens.
	Verbose bool `json:"verbose"`
}
