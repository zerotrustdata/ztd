// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDiagnosticFromChildOutput_PrefersErrorRecordCause(t *testing.T) {
	output := `2026-10-12T13:54:23.121-07:00 INFO: Gateway servers starting
  - component: gateway
2026-10-12T13:54:23.124-07:00 ERROR: Gateway service failed
  - component: gateway
  - error: gateway: start public spectator: listen 127.0.0.1:8081: bind: address already in use
2026-10-12T13:54:23.127-07:00 INFO: Gateway mode stopped
  - component: gateway
  - error: unrelated
`
	assert.Equal(t,
		"Gateway service failed: gateway: start public spectator: listen 127.0.0.1:8081: bind: address already in use",
		diagnosticFromChildOutput(output))
}

func TestDiagnosticFromChildOutput_ErrorRecordWithoutCause(t *testing.T) {
	output := "2026-10-12T13:54:23.124-07:00 ERROR: Gateway service failed\n  - component: gateway\n"
	assert.Equal(t, "Gateway service failed", diagnosticFromChildOutput(output))
}

func TestDiagnosticFromChildOutput_PlainStderrFallsBackToLastLine(t *testing.T) {
	assert.Equal(t, "gateway child startup diagnostic", diagnosticFromChildOutput("noise\ngateway child startup diagnostic\n"))
}
