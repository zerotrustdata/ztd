// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package run

import (
	"context"
	"time"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Root is one executing runtime's file access, as the runner sees it.
type Root struct {
	Files     fs.RuntimeFileService     // the executor's runtime root
	Dir       string                    // absolute path of that root (to build absolute paths for drivers)
	RuntimeID string                    // oracle.GatewayRuntimeID for embedded; the outbound operator id otherwise
	Bound     *evalv1.ConformanceAnchor // outbound only: the key bound at enrollment (oracle.BoundAnchor); nil for embedded
}

// Config is everything one conformance run needs from its caller.
type Config struct {
	RunID   string
	Drivers map[cases.Ingress]cases.Driver
	// Unavailable explicitly declares optional ingress coverage that cannot run.
	Unavailable map[cases.Ingress]evalv1.ConformanceObservationKind
	Sentinel    cases.Sentinel
	Roots       map[cases.Executor]Root
	Posture     func(ctx context.Context) (evalv1.EvaluationGovernancePosture, error) // from the Gateway health
	Recorder    func(sink *[]client.Exchange)                                         // client.Record
	Attended    bool
	Now         func() time.Time
}
