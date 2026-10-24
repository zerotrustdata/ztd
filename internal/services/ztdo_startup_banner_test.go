// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestPrintOperatorStartupBanner_LogsIdentityAndCapabilityBoundary(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	tests := []struct {
		name    string
		workDir string
	}{
		{name: "native runtime path", workDir: cfg.WorkDir},
		{name: "Windows path", workDir: `D:\operators\test-op`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg.WorkDir = tt.workDir
			cfg.Endpoint = "gateway.example"
			cfg.HTTPSPort = 8443
			logger, sink := testutil.NewBufferLogger(t)

			printOperatorStartupBanner(cfg, logger)

			text := sink.String()
			assert.Contains(t, text, "Initializing Edge Execution Operator")
			assert.Contains(t, text, "gateway.example:8443")
			// TextHandler quotes the value, escaping Windows path separators.
			assert.Contains(t, text, "fs.read="+strconv.Quote("GRANTED: Scoped to "+cfg.WorkDir))
			assert.Contains(t, text, "Air-gap mode active")
			assert.Contains(t, text, "Awaiting cryptographically signed agentic intents")
		})
	}
}

func TestNewZtdoService_NilTLSConfig(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	_, err := NewZtdoService(cfg, logger, nil, newTestL2Trust(t), nil, nil, newTestPubSubClientFactory())
	assert.ErrorIs(t, err, constants.ErrInternal)
}

func TestNewZtdoService_NilL2Trust(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)

	_, err := NewZtdoService(cfg, testutil.NewTestLogger(), newTestTLSConfig(t), nil, newTestFileSvc(t), nil, newTestPubSubClientFactory())
	assert.ErrorIs(t, err, constants.ErrInternal)
}
