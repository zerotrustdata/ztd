// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewBufferLogger_RecordsMessageAndAttributes(t *testing.T) {
	t.Parallel()

	logger, sink := NewBufferLogger(t)
	logger.Info("operator ready", "operator_id", "op-1")

	text := sink.String()
	assert.Contains(t, text, "operator ready")
	assert.Contains(t, text, "op-1")
}
