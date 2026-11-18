// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import "encoding/json"

// The Tribunal's response schemas constrain each model answer, through the
// governed inference request's response format, to the one shape its strict
// decoder accepts. Every schema is canonical JSON (sorted keys, no
// whitespace), as the Inference Operator requires, and uses only the subset
// every provider applies: a root object, additionalProperties false, every
// property required, type, enum, items, and anyOf with a null branch for an
// absent value (schema_test.go). A schema never relaxes a decoder: an
// answer the provider fails to constrain is still rejected, and each decoder
// checks what the subset cannot express (a valid executable name, the fields
// a verdict carries).

// ConsensusVoteJSONSchema constrains the provider's vote answer to the shape
// ParseConsensusVote accepts.
const ConsensusVoteJSONSchema = `{"additionalProperties":false,"properties":{"decision":{"enum":["approve","deny"],"type":"string"},` +
	`"reason":{"type":"string"}},"required":["decision","reason"],"type":"object"}`

// authoredArgumentsJSONSchema is one model-authored run_shell_command
// argument object: the executable and its argv. Orchestration owns timeout
// and working_dir, so the object has no other field.
const authoredArgumentsJSONSchema = `{"additionalProperties":false,"properties":{"args":{"items":{"type":"string"},"type":"array"},` +
	`"command":{"type":"string"}},"required":["command","args"],"type":"object"}`

// generationJSONSchema constrains a panel member's proposal to the shape
// generate accepts.
const generationJSONSchema = authoredArgumentsJSONSchema

// marshalJSONSchema constrains Marshal's answer to the shape assessRisk
// accepts. feedback is always present and empty unless the risk is HIGH.
const marshalJSONSchema = `{"additionalProperties":false,"properties":{"feedback":{"type":"string"},"rationale":{"type":"string"},` +
	`"risk":{"enum":["LOW","MEDIUM","HIGH"],"type":"string"}},"required":["risk","rationale","feedback"],"type":"object"}`

// auditorJSONSchema constrains the Auditor's answer to one object that names
// its verdict. revised_arguments is a complete authored argument object for
// REVISE and null otherwise. swap_to_cluster, offered only when alternatives
// is non-empty, names one of them for SWAP and is null otherwise. auditVerdict
// enforces which fields each verdict carries.
func auditorJSONSchema(alternatives []string) string {
	verdicts := []string{"APPROVE", "REVISE"}
	required := []string{"verdict", "rationale", "revised_arguments"}
	var swap string
	if len(alternatives) > 0 {
		verdicts = append(verdicts, "SWAP")
		required = append(required, "swap_to_cluster")
		// A []string of cluster IDs always marshals.
		ids, _ := json.Marshal(alternatives)
		swap = `"swap_to_cluster":{"anyOf":[{"enum":` + string(ids) + `,"type":"string"},{"type":"null"}]},`
	}
	// Two []string values always marshal.
	verdictEnum, _ := json.Marshal(verdicts)
	requiredList, _ := json.Marshal(required)
	return `{"additionalProperties":false,"properties":{"rationale":{"type":"string"},` +
		`"revised_arguments":{"anyOf":[` + authoredArgumentsJSONSchema + `,{"type":"null"}]},` + swap +
		`"verdict":{"enum":` + string(verdictEnum) + `,"type":"string"}},"required":` + string(requiredList) + `,"type":"object"}`
}
