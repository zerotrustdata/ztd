// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

// Re-exports the executor's unexported trace vocabulary for the external
// real-stack executor tests (campaign_agent_executor_real_stack_integration_test.go),
// so assertions compare against the production values rather than literals.
const (
	ExportedAgentToolErrorExecution  = agentToolErrorExecution
	ExportedAgentToolErrorSecurity   = agentToolErrorSecurity
	ExportedAgentPolicyDecisionAllow = agentPolicyDecisionAllow
	ExportedAgentPolicyDecisionDeny  = agentPolicyDecisionDeny
	ExportedAgentTraceSchemaVersion  = agentTraceSchemaVersion
)
