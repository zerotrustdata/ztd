// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type remoteModelProvenanceClient struct {
	client authcmd.APIClient
	stream func() *sse.Client
	mu     sync.Mutex
	cache  map[string]*evalv1.ModelProvenanceAttestationWindow
}

// WatchRecorded reports every attestation-recorded event on the CLI session's
// Gateway SSE stream.
func (c *remoteModelProvenanceClient) WatchRecorded(ctx context.Context, connected func(), recorded func(string)) error {
	if c == nil {
		return fmt.Errorf("model provenance: watch attestations: %w", constants.ErrMissingRequiredField)
	}
	return watchEvidenceWindows(ctx, c.stream, constants.EventModelProvenanceAttestationRecorded, connected, recorded)
}

func (c *remoteModelProvenanceClient) Load(ctx context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if c == nil || c.client == nil || providerAttemptID == "" {
		return nil, constants.ErrNotFound
	}

	c.mu.Lock()
	if window := c.cache[providerAttemptID]; window != nil {
		c.mu.Unlock()
		return window, nil
	}
	c.mu.Unlock()

	body, err := c.client.Get(constants.APIPaths.InferenceModelProvenanceAttestations + providerAttemptID)
	if err != nil {
		return nil, fmt.Errorf("model provenance: gateway read: %w", err)
	}
	var resp models.ModelProvenanceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	window := &evalv1.ModelProvenanceAttestationWindow{}
	if err := evalv1.UnmarshalCanonical(resp.Window, window); err != nil {
		return nil, fmt.Errorf("model provenance: decode window: %w", err)
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*evalv1.ModelProvenanceAttestationWindow)
	}
	c.cache[providerAttemptID] = window
	c.mu.Unlock()
	return window, nil
}

func newModelProvenanceRemote(fileSvc fs.RuntimeFileService, cfg *config.Config) (evaluation.ModelProvenanceRemote, error) {
	if !IsGatewayHealthy() {
		return nil, nil
	}
	client, err := api.NewClient(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("model provenance: create gateway client: %w", err)
	}
	return &remoteModelProvenanceClient{client: client, stream: client.NewSSEClient}, nil
}

// NewCampaignModelProvenanceReader reads the run's sealed attestation windows from
// store. The Gateway remote, when healthy, is used only to seal windows the run
// does not hold yet.
func NewCampaignModelProvenanceReader(fileSvc fs.RuntimeFileService, store *evaluation.Store, cfg *config.Config) (*evaluation.CampaignModelProvenanceReader, error) {
	remote, err := newModelProvenanceRemote(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	return evaluation.NewCampaignModelProvenanceReaderWithRemote(store, remote)
}
