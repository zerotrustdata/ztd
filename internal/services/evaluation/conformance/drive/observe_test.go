// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
package drive

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestObserveFillsReceiptFromDispatchWhenObservationHasNone(t *testing.T) {
	t.Parallel()
	receipt := &operatorv1.ActionReceipt{TransactionId: "tx-dispatch"}
	o := &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		Dispatch:    &operatorv1.GovernedDispatchResult{Receipt: receipt},
	}

	obs := Observe(o)
	assert.Equal(t, "tx-dispatch", obs.GetReceipt().GetTransactionId())
}

func TestObserveKeepsObservationReceiptOverDispatch(t *testing.T) {
	t.Parallel()
	o := &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{
			Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
			Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-observed"},
		},
		Dispatch: &operatorv1.GovernedDispatchResult{Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-dispatch"}},
	}

	obs := Observe(o)
	assert.Equal(t, "tx-observed", obs.GetReceipt().GetTransactionId())
}

func TestObserveCopiesTypedMessage(t *testing.T) {
	t.Parallel()
	source := &evalv1.ConformanceObservation{
		Kind:        evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		HttpStatus:  500,
		UntypedBody: []byte(`{"error":"internal"}`),
	}
	o := &client.GovernedOutcome{Observation: source}

	obs := Observe(o)
	assert.True(t, proto.Equal(source, obs))

	obs.HttpStatus = 200
	assert.Equal(t, int32(500), source.GetHttpStatus(), "Observe must copy, not alias, the outcome")
}

func TestObserveWithoutDispatchLeavesReceiptEmpty(t *testing.T) {
	t.Parallel()
	o := &client.GovernedOutcome{Observation: &evalv1.ConformanceObservation{
		Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
	}}
	assert.Nil(t, Observe(o).GetReceipt())
}
