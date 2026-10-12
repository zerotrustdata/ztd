// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newObserverTestFS(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	svc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	return svc
}

func TestRuntimeTargetReader_ObservesPresentAndAbsentControlledTargetState(t *testing.T) {
	observedAt := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	fileSvc := newObserverTestFS(t)
	target := ControlledTargetResource(fileSvc, "run-1")

	t.Run("present target returns raw bytes", func(t *testing.T) {
		require.NoError(t, fileSvc.WriteFile(context.Background(), ControlledTargetDirectory()+"/"+filepath.Base(target), []byte("marker\n"), constants.PermFilePrivate))
		observer := newRuntimeTargetReader(fileSvc, func() time.Time { return observedAt })

		state, err := observer.Observe(context.Background(), target)

		require.NoError(t, err)
		require.NotNil(t, state)
		assert.True(t, state.Present)
		assert.Equal(t, []byte("marker\n"), state.Content)
		assert.Equal(t, observedAt, state.ObservedAt)
	})

	t.Run("absent target returns typed absence", func(t *testing.T) {
		absent := ControlledTargetResource(fileSvc, "run-absent")
		observer := newRuntimeTargetReader(fileSvc, func() time.Time { return observedAt })

		state, err := observer.Observe(context.Background(), absent)

		require.NoError(t, err)
		require.NotNil(t, state)
		assert.False(t, state.Present)
		assert.Nil(t, state.Content)
		assert.Equal(t, observedAt, state.ObservedAt)
	})
}

func TestRuntimeTargetReader_FailsClosedForInvalidTargetAndReadFailure(t *testing.T) {
	fileSvc := newObserverTestFS(t)
	ctx := context.Background()
	// A directory at the controlled target path cannot be read as a file.
	unreadable := ControlledTargetResource(fileSvc, "run-dir")
	require.NoError(t, os.MkdirAll(unreadable, constants.PermDirPrivate))

	tests := []struct {
		name   string
		target string
	}{
		{name: "relative target is rejected", target: constants.TestEvaluationTargetFilename},
		{name: "target outside controlled directory is rejected", target: filepath.Join(constants.TestPathVarLibDataDir, constants.TestEvaluationTargetFilename)},
		{name: "target directly under runtime root is rejected", target: fileSvc.Resolve(constants.TestEvaluationTargetFilename)},
		{name: "traversal out of controlled directory is rejected", target: filepath.Join(fileSvc.Resolve(ControlledTargetDirectory()), "..", constants.TestEvaluationTargetFilename)},
		{name: "unreadable controlled target is unavailable", target: unreadable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := newRuntimeTargetReader(fileSvc, time.Now).Observe(ctx, test.target)

			assert.Nil(t, state)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
		})
	}
}

func TestRuntimeTargetReader_RequiresDependencies(t *testing.T) {
	state, err := newRuntimeTargetReader(nil, time.Now).Observe(context.Background(), ControlledTargetResource(newObserverTestFS(t), "run-nil"))

	assert.Nil(t, state)
	assert.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
}
