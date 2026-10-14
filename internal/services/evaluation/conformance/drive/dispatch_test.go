// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
)

// testDispatchTargets gives each executor its own operator session.
func testDispatchTargets() map[cases.Executor]operator.Target {
	return map[cases.Executor]operator.Target{
		cases.ExecutorEmbedded: {OperatorID: "embedded-operator", OperatorSessionID: "embedded-session"},
		cases.ExecutorOutbound: {OperatorID: "outbound-operator", OperatorSessionID: "outbound-session"},
	}
}

// dispatchAttempt builds one attempt for the dispatch driver tests.
func dispatchAttempt(ingress cases.Ingress, fault cases.Fault, action cases.Action) cases.Attempt {
	return cases.Attempt{
		RunID:     "run-1",
		AttemptID: "attempt-1",
		Case:      cases.Case{ID: "dispatch-test", Ingress: ingress, Fault: fault, Action: action, Command: "echo doctrine"},
		TargetAbs: "/tmp/conformance/target.txt",
		ReadAbs:   "/tmp/conformance/read.txt",
		Marker:    cases.Marker("run-1", "attempt-1"),
	}
}
