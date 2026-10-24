// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestNewPortService(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	eventsStore := storagetest.NewTestEventsStore(t)
	scrubber := mustNewScrubbingSvc(t, logger)

	t.Run("binds every dependency", func(t *testing.T) {
		t.Parallel()
		svc, err := NewPortService(cfg, logger, client, scrubber, eventsStore)
		require.NoError(t, err)
		require.NotNil(t, svc)
		assert.Equal(t, cfg, svc.config)
		assert.Equal(t, logger, svc.logger)
		assert.Equal(t, client, svc.client)
		assert.Same(t, scrubber, svc.scrubbing)
		assert.Same(t, eventsStore, svc.eventsStore)
	})

	tests := []struct {
		name        string
		cfg         *config.Config
		scrubber    *scrubbing.ScrubbingService
		eventsStore *storage.LocalFirstEventsStore
		wantErr     error
	}{
		{name: "nil config", scrubber: scrubber, eventsStore: eventsStore, wantErr: constants.ErrMissingRequiredField},
		{name: "nil scrubber", cfg: cfg, eventsStore: eventsStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "disabled scrubber", cfg: cfg, scrubber: newEgressScrubber(t, false), eventsStore: eventsStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "nil local-first events store", cfg: cfg, scrubber: scrubber, wantErr: constants.ErrLocalFirstEventsStoreRequired},
	}
	for _, tt := range tests {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()
			svc, err := NewPortService(tt.cfg, logger, client, tt.scrubber, tt.eventsStore)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, svc)
		})
	}
}
