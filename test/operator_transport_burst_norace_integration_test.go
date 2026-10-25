// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration && !race

package tests

import (
	"testing"
	"time"
)

// The smoke contract enrolls 100 Operators at once, approved in one owner
// decision.
const (
	burstOperators         = 100
	burstHeartbeatInterval = 10 * time.Second
	burstTimeout           = 10 * time.Minute
)

// requireBurstBuild keeps the larger smoke cohort out of coverage runs;
// make test-operator-burst runs this size without instrumentation.
func requireBurstBuild(t *testing.T) {
	t.Helper()
	if testing.CoverMode() != "" {
		t.Skip("the 100-Operator smoke requires an uninstrumented build; run make test-operator-burst")
	}
}
