// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestMCPNewRejectsNilClient(t *testing.T) {
	t.Parallel()
	_, err := NewMCP(context.Background(), nil, testIdentity().Persona)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCheckNativeToolsFailsWhenToolMissing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		names []string
	}{
		{name: "no tools", names: nil},
		{name: "run_shell_command missing", names: []string{mcpReadTool}},
		{name: "read_file missing", names: []string{mcpShellTool}},
		{name: "other tools do not stand in for it", names: []string{"other", mcpReadTool}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, checkNativeTools(tt.names), constants.ErrConformanceIngressUnsupported)
		})
	}
	require.NoError(t, checkNativeTools([]string{mcpShellTool, mcpReadTool}))
}
