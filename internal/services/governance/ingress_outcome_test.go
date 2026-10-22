// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func marshalIngressFailure(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := protojson.Marshal(msg)
	require.NoError(t, err)
	return body
}

func TestDecodeIngressFailure(t *testing.T) {
	signedRejection := &operatorv1.GovernanceRejection{
		Error: "rejected",
		EventReceipt: &operatorv1.EventReceipt{
			TransactionId: "tx-1",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:   operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		},
	}
	refusal := &operatorv1.IngressRefusal{
		Code:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED,
		Error: "method not allowed",
	}

	tests := []struct {
		name          string
		body          []byte
		wantRejection bool
		wantRefusal   operatorv1.IngressRefusalCode
		wantUntyped   bool
	}{
		{
			name:          "signed rejection decodes as GovernanceRejection",
			body:          marshalIngressFailure(t, signedRejection),
			wantRejection: true,
		},
		{
			name:        "typed refusal decodes as IngressRefusal",
			body:        marshalIngressFailure(t, refusal),
			wantRefusal: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED,
		},
		{
			name:        "refusal with UNSPECIFIED code is untyped",
			body:        []byte(`{"error":"no code"}`),
			wantUntyped: true,
		},
		{
			name:        "rejection without an event receipt is untyped",
			body:        []byte(`{"error":"dispatch: no delivery"}`),
			wantUntyped: true,
		},
		{
			name:        "unknown field is untyped under strict decoding",
			body:        []byte(`{"code":"INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED","error":"x","future_field":{"a":1}}`),
			wantUntyped: true,
		},
		{
			name:        "rejection with an unknown field is untyped",
			body:        []byte(`{"error":"x","event_receipt":{"transaction_id":"tx-1"},"future_field":1}`),
			wantUntyped: true,
		},
		{
			name:        "plain text is untyped",
			body:        []byte("gateway unavailable"),
			wantUntyped: true,
		},
		{
			name:        "empty body is untyped",
			body:        nil,
			wantUntyped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejection, gotRefusal, err := DecodeIngressFailure(tt.body)
			if tt.wantUntyped {
				assert.ErrorIs(t, err, constants.ErrIngressResponseUntyped)
				assert.Nil(t, rejection)
				assert.Nil(t, gotRefusal)
				return
			}
			require.NoError(t, err)
			if tt.wantRejection {
				require.NotNil(t, rejection)
				assert.Nil(t, gotRefusal)
				assert.Equal(t, "tx-1", rejection.GetEventReceipt().GetTransactionId())
				return
			}
			require.NotNil(t, gotRefusal)
			assert.Nil(t, rejection)
			assert.Equal(t, tt.wantRefusal, gotRefusal.GetCode())
		})
	}
}
