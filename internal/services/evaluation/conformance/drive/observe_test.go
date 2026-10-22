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

func TestObserveFillsEventReceiptFromDispatchWhenObservationHasNone(t *testing.T) {
	eventReceipt := &operatorv1.EventReceipt{TransactionId: "tx-dispatch"}
	o := &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT},
		Dispatch:    &operatorv1.GovernedDispatchResult{EventReceipt: eventReceipt},
	}

	obs := Observe(o)
	assert.Equal(t, "tx-dispatch", obs.GetEventReceipt().GetTransactionId())
}

func TestObserveKeepsObservationEventReceiptOverDispatch(t *testing.T) {
	o := &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{
			Kind:         evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT,
			EventReceipt: &operatorv1.EventReceipt{TransactionId: "tx-observed"},
		},
		Dispatch: &operatorv1.GovernedDispatchResult{EventReceipt: &operatorv1.EventReceipt{TransactionId: "tx-dispatch"}},
	}

	obs := Observe(o)
	assert.Equal(t, "tx-observed", obs.GetEventReceipt().GetTransactionId())
}

func TestObserveCopiesTypedMessage(t *testing.T) {
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

func TestObserveWithoutDispatchLeavesEventReceiptEmpty(t *testing.T) {
	o := &client.GovernedOutcome{Observation: &evalv1.ConformanceObservation{
		Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
	}}
	assert.Nil(t, Observe(o).GetEventReceipt())
}
