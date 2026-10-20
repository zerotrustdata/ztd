// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

// Re-exports the Tribunal's unexported outcome vocabulary for the external
// real-stack Tribunal tests (campaign_tribunal_real_stack_integration_test.go),
// so assertions compare against the production messages rather than copies.
const (
	ExportedAgentToolErrorPipeline     = agentToolErrorPipeline
	ExportedTribunalMarshalConflict    = tribunalMarshalConflict
	ExportedTribunalNoConsensusError   = tribunalNoConsensusError
	ExportedTribunalAuditFailedError   = tribunalAuditFailedError
	ExportedTribunalInferenceFailedMsg = tribunalInferenceFailedMsg
)
