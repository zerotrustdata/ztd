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

func TestNewPortService(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	auditStore := storagetest.NewTestAuditStore(t)
	scrubber := mustNewScrubbingSvc(t, logger)

	t.Run("binds every dependency", func(t *testing.T) {

		svc, err := NewPortService(cfg, logger, client, scrubber, auditStore)
		require.NoError(t, err)
		require.NotNil(t, svc)
		assert.Equal(t, cfg, svc.config)
		assert.Equal(t, logger, svc.logger)
		assert.Equal(t, client, svc.client)
		assert.Same(t, scrubber, svc.scrubbing)
		assert.Same(t, auditStore, svc.auditStore)
	})

	tests := []struct {
		name       string
		cfg        *config.Config
		scrubber   *scrubbing.ScrubbingService
		auditStore *storage.SQLAuditStore
		wantErr    error
	}{
		{name: "nil config", scrubber: scrubber, auditStore: auditStore, wantErr: constants.ErrMissingRequiredField},
		{name: "nil scrubber", cfg: cfg, auditStore: auditStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "disabled scrubber", cfg: cfg, scrubber: newEgressScrubber(t, false), auditStore: auditStore, wantErr: constants.ErrEgressScrubberUnavailable},
		{name: "nil audit store", cfg: cfg, scrubber: scrubber, wantErr: constants.ErrAuditStoreRequired},
	}
	for _, tt := range tests { //nolint:paralleltest // mutates package-level state
		t.Run("rejects "+tt.name, func(t *testing.T) {

			svc, err := NewPortService(tt.cfg, logger, client, tt.scrubber, tt.auditStore)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, svc)
		})
	}
}
