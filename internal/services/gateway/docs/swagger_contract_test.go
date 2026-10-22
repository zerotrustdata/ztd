// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package docs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type swaggerSchemaReference struct {
	Ref string `json:"$ref"`
}

type swaggerResponse struct {
	Schema swaggerSchemaReference `json:"schema"`
}

type swaggerOperation struct {
	Parameters []struct {
		Name   string                 `json:"name"`
		Schema swaggerSchemaReference `json:"schema"`
	} `json:"parameters"`
	Responses struct {
		OK swaggerResponse `json:"200"`
	} `json:"responses"`
}

type swaggerContract struct {
	Definitions struct {
		EventReceipt struct {
			Properties struct {
				DeterministicStageEvidence struct {
					Items swaggerSchemaReference `json:"items"`
				} `json:"deterministic_stage_evidence"`
				FinalPersistenceAttestation swaggerSchemaReference `json:"final_persistence_attestation"`
			} `json:"properties"`
		} `json:"operatorv1.EventReceipt"`
		EventReceiptRecord struct {
			Properties struct {
				EventReceipt swaggerSchemaReference `json:"event_receipt"`
			} `json:"properties"`
		} `json:"models.EventReceiptRecord"`
	} `json:"definitions"`
	Paths struct {
		ApprovalVerify struct {
			Post swaggerOperation `json:"post"`
		} `json:"/api/v1/approvals/{txHash}/verify"`
		AuditEventReceipts struct {
			Get swaggerOperation `json:"get"`
		} `json:"/api/v1/audit/event-receipts"`
		AuditEventReceiptsExport struct {
			Get swaggerOperation `json:"get"`
		} `json:"/api/v1/audit/event-receipts/export"`
		GovernanceEnvelopes struct {
			Post swaggerOperation `json:"post"`
		} `json:"/api/v1/governance/envelopes"`
		OperatorsValidate struct {
			Post swaggerOperation `json:"post"`
		} `json:"/api/v1/operators/validate"`
	} `json:"paths"`
}

func TestSwaggerContract_DocumentsCanonicalEventReceiptsAndOperatorValidation(t *testing.T) {
	var contract swaggerContract
	require.NoError(t, json.Unmarshal(SwaggerJSON, &contract))

	responseCases := []struct {
		name      string
		operation swaggerOperation
		wantRef   string
	}{
		{name: "approval canonical event receipt", operation: contract.Paths.ApprovalVerify.Post, wantRef: "#/definitions/operatorv1.EventReceipt"},
		{name: "audit event receipt list wrapper", operation: contract.Paths.AuditEventReceipts.Get, wantRef: "#/definitions/models.AuditEventReceiptsResponse"},
		{name: "audit event receipt export wrapper", operation: contract.Paths.AuditEventReceiptsExport.Get, wantRef: "#/definitions/models.AuditEventReceiptsResponse"},
		{name: "governance canonical event receipt", operation: contract.Paths.GovernanceEnvelopes.Post, wantRef: "#/definitions/operatorv1.EventReceipt"},
		{name: "operator validation response", operation: contract.Paths.OperatorsValidate.Post, wantRef: "#/definitions/models.OperatorSessionValidationResponse"},
	}
	for _, tc := range responseCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantRef, tc.operation.Responses.OK.Schema.Ref)
		})
	}

	require.Len(t, contract.Paths.OperatorsValidate.Post.Parameters, 1)
	assert.Equal(t, "binding", contract.Paths.OperatorsValidate.Post.Parameters[0].Name)
	assert.Equal(t, "#/definitions/models.OperatorSessionValidationRequest", contract.Paths.OperatorsValidate.Post.Parameters[0].Schema.Ref)
	assert.Equal(t, "#/definitions/operatorv1.DeterministicStageEvidence", contract.Definitions.EventReceipt.Properties.DeterministicStageEvidence.Items.Ref)
	assert.Equal(t, "#/definitions/operatorv1.EventReceiptPersistenceAttestation", contract.Definitions.EventReceipt.Properties.FinalPersistenceAttestation.Ref)
	assert.Equal(t, "#/definitions/operatorv1.EventReceipt", contract.Definitions.EventReceiptRecord.Properties.EventReceipt.Ref)
}
