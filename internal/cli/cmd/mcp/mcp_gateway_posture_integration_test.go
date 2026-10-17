// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package mcp

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestStartGatewayIfNeeded_RejectsInvalidPostureBeforeAnyWork(t *testing.T) {
	_, err := startGatewayIfNeeded(func(string, *slog.Logger) (fs.RuntimeFileService, error) {
		t.Fatal("file service must not be built for an invalid posture")
		return nil, nil
	}, "strict")
	require.ErrorIs(t, err, constants.ErrInvalidPosture)
}
