// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
)

func newHarnessOllamaModelCommandDispatcher(cfg *config.Config, authContext *auth.ClientAuthContext, deps gatewayEvalDeps) (*evaluation.GovernedOllamaModelCommandDispatcher, error) {
	client, err := deps.clientFactory(nativeEvalClientConfig(cfg, authContext))
	if err != nil {
		return nil, err
	}
	// Deliberately no OperatorID/OperatorSessionID here: those headers only
	// assert a persisted CLI-operator bind (established out of band via CLI
	// bind, not part of this flow) and the Gateway's CLI auth middleware
	// fails closed with 403 when a session with no such bind sends them. The
	// actual dispatch target travels in the request body's
	// target_operator_session_id, which DispatchService validates on its
	// own regardless of these headers.
	return evaluation.NewGovernedOllamaModelCommandDispatcher(client, harnessclient.Persona{
		ID:           "ztd-campaign-model-maintenance",
		UserAgent:    "ztd-eval-campaign",
		UserID:       authContext.UserID,
		CLISessionID: authContext.CLISessionID,
	})
}

// modelCommandEnvironment names the provider endpoint for the `ztd operator
// model` maintenance subcommands, which read ZTD_OLLAMA_ENDPOINT and fail
// closed without it.
func modelCommandEnvironment(endpoint string) map[string]string {
	return map[string]string{string(constants.EnvVar.OllamaEndpoint): endpoint}
}
