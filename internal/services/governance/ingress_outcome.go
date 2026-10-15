// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// DecodeIngressFailure decodes a failure body from any governed ingress (HTTP body
// or JSON-RPC error.data). Exactly one return is non-nil on success. Decoding is
// strict protojson (no DiscardUnknown).
func DecodeIngressFailure(body []byte) (*operatorv1.GovernanceRejection, *operatorv1.IngressRefusal, error) {
	rejection := &operatorv1.GovernanceRejection{}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(body, rejection); err == nil && rejection.GetReceipt() != nil {
		return rejection, nil, nil
	}
	refusal := &operatorv1.IngressRefusal{}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(body, refusal); err == nil &&
		refusal.GetCode() != operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED {
		return nil, refusal, nil
	}
	return nil, nil, fmt.Errorf("decode ingress failure: %w", constants.ErrIngressResponseUntyped)
}
