// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestPublicMirrorReadModelRetention(t *testing.T) {
	t.Parallel()
	t.Run("expires beyond the pointer cache", func(t *testing.T) {
		now := time.Now()
		mirror := &PublicMirrorServer{now: func() time.Time { return now }}
		require.NoError(t, mirror.retainLayoutReadModel("source", "digest", []byte("body")))
		mirror.pruneLayoutReadModels(now.Add(30 * time.Second))
		require.Len(t, mirror.layout.readModels, 1)
		mirror.pruneLayoutReadModels(now.Add(publicMirrorReadModelRetention))
		assert.Empty(t, mirror.layout.readModels)
	})
	t.Run("bounds entries and retains refreshed objects", func(t *testing.T) {
		mirror := &PublicMirrorServer{now: time.Now}
		for i := range publicMirrorReadModelMaxEntries {
			require.NoError(t, mirror.retainLayoutReadModel("source", fmt.Sprint(i), []byte("body")))
		}
		require.NoError(t, mirror.retainLayoutReadModel("source", "0", []byte("body")))
		require.NoError(t, mirror.retainLayoutReadModel("source", "new", []byte("body")))
		require.Len(t, mirror.layout.readModels, publicMirrorReadModelMaxEntries)
		assert.Equal(t, "2", mirror.layout.readModels[0].digest)
		assert.Equal(t, "0", mirror.layout.readModels[publicMirrorReadModelMaxEntries-2].digest)
	})
	t.Run("bounds compressed bytes across sources", func(t *testing.T) {
		mirror := &PublicMirrorServer{now: time.Now}
		body := make([]byte, publicMirrorReadModelMaxBytes/2+1)
		require.NoError(t, mirror.retainLayoutReadModel("source-a", "first", body))
		require.NoError(t, mirror.retainLayoutReadModel("source-b", "second", body))
		require.Len(t, mirror.layout.readModels, 1)
		assert.Equal(t, "second", mirror.layout.readModels[0].digest)
	})
	t.Run("refuses an object that cannot be retained", func(t *testing.T) {
		mirror := &PublicMirrorServer{now: time.Now}
		err := mirror.retainLayoutReadModel("source", "digest", make([]byte, publicMirrorReadModelMaxBytes+1))
		require.ErrorIs(t, err, constants.ErrEvidenceArtifactTooLarge)
		assert.Empty(t, mirror.layout.readModels)
	})
}
