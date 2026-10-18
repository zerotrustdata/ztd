// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package serve

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// ---------------------------------------------------------------------------
// classifyConfigLoadError — integration with real config.Load
// ---------------------------------------------------------------------------

// TestClassifyConfigLoadError_RealConfigLoadNoPosture_Succeeds verifies that
// the operator no longer requires config posture to start. config.Load with
// an empty posture succeeds (the operator reads posture per-envelope from
// GovernanceEnvelope.Posture at L4 verification time). classifyConfigLoadError
// is never reached on this path.
func TestClassifyConfigLoadError_RealConfigLoadNoPosture_Succeeds(t *testing.T) {
	opts := ServeOperatorOptions{
		Endpoint:   "127.0.0.1",
		WorkingDir: testutil.TempDir(t),
	}
	loadOpts := buildOperatorLoadOptions(opts, opts.Endpoint, opts.WorkingDir)

	cfg, err := config.Load(loadOpts)
	require.NoError(t, err, "config.Load with empty posture should succeed (operator reads posture per-envelope)")
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.Posture, "cfg.Posture should be empty when no posture was supplied")
}
