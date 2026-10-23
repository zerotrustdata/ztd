// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"encoding/json"
	"strings"
)

// The Tribunal's response schemas constrain each model answer, through the
// governed inference request's response format, to the one shape its strict
// decoder accepts. Every schema is canonical JSON (sorted keys, no
// whitespace), as the Inference Operator requires. A schema never relaxes a
// decoder: an answer the provider fails to constrain is still rejected.

// ConsensusVoteJSONSchema constrains the provider's vote answer to the shape
// ParseConsensusVote accepts.
const ConsensusVoteJSONSchema = `{"additionalProperties":false,"properties":{"decision":{"enum":["approve","deny"],"type":"string"},` +
	`"reason":{"type":"string"}},"required":["decision","reason"],"type":"object"}`

// authoredArgumentsJSONSchema is one model-authored run_shell_command
// argument object: the executable and its argv. Orchestration owns timeout
// and working_dir, so the object has no other field.
const authoredArgumentsJSONSchema = `{"additionalProperties":false,"properties":{"args":{"items":{"type":"string"},"type":"array"},` +
	`"command":{"minLength":1,"type":"string"}},"required":["command","args"],"type":"object"}`

// generationJSONSchema constrains a panel member's proposal to the shape
// generate accepts.
const generationJSONSchema = authoredArgumentsJSONSchema

// marshalJSONSchema constrains Marshal's answer to the shape assessRisk
// accepts.
const marshalJSONSchema = `{"additionalProperties":false,"properties":{"feedback":{"type":"string"},"rationale":{"type":"string"},` +
	`"risk":{"enum":["LOW","MEDIUM","HIGH"],"type":"string"}},"required":["risk","rationale"],"type":"object"}`

// Auditor answer shapes, one per verdict: APPROVE carries no revision or
// swap, REVISE carries only a complete authored argument object, and SWAP
// carries only the alternative cluster it names.
const (
	auditorApproveJSONSchema = `{"additionalProperties":false,"properties":{"rationale":{"type":"string"},"verdict":{"const":"APPROVE"}},` +
		`"required":["verdict","rationale"],"type":"object"}`
	auditorReviseJSONSchema = `{"additionalProperties":false,"properties":{"rationale":{"type":"string"},"revised_arguments":` +
		authoredArgumentsJSONSchema + `,"verdict":{"const":"REVISE"}},"required":["verdict","revised_arguments","rationale"],"type":"object"}`
)

// auditorJSONSchema constrains the Auditor's answer to one verdict shape.
// SWAP is offered only when alternatives is non-empty, and then names one of
// them.
func auditorJSONSchema(alternatives []string) string {
	branches := []string{auditorApproveJSONSchema, auditorReviseJSONSchema}
	if len(alternatives) > 0 {
		// A []string of cluster IDs always marshals.
		ids, _ := json.Marshal(alternatives)
		branches = append(branches, `{"additionalProperties":false,"properties":{"rationale":{"type":"string"},"swap_to_cluster":{"enum":`+
			string(ids)+`,"type":"string"},"verdict":{"const":"SWAP"}},"required":["verdict","swap_to_cluster","rationale"],"type":"object"}`)
	}
	return `{"oneOf":[` + strings.Join(branches, ",") + `]}`
}
