// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sqliteutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newTestRuntimeFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewVerboseTestLogger(t))
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	return fileSvc
}
