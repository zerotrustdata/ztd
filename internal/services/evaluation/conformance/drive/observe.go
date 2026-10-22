// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Observe converts one typed harness outcome into a conformance observation.
// It copies the typed message; it never inspects body text. The event receipt of a
// dispatch success is filled from the dispatch result when the observation has
// none. Every driver calls Observe, so no driver converts outcomes itself.
// o must come from a call that returned no error.
func Observe(o *client.GovernedOutcome) *evalv1.ConformanceObservation {
	obs := proto.Clone(o.Observation).(*evalv1.ConformanceObservation)
	if obs.GetEventReceipt() == nil && o.Dispatch.GetEventReceipt() != nil {
		obs.EventReceipt = proto.Clone(o.Dispatch.GetEventReceipt()).(*operatorv1.EventReceipt)
	}
	return obs
}
