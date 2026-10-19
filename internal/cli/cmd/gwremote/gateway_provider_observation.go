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

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type remoteProviderObservationClient struct {
	client authcmd.APIClient
	stream func() *sse.Client
	mu     sync.Mutex
	cache  map[string]*remoteProviderObservationBundle
}

// WatchRecorded reports every observation-recorded event on the CLI session's
// Gateway SSE stream.
func (c *remoteProviderObservationClient) WatchRecorded(ctx context.Context, connected func(), recorded func(string)) error {
	if c == nil {
		return fmt.Errorf("provider observation: watch observations: %w", constants.ErrMissingRequiredField)
	}
	return watchEvidenceWindows(ctx, c.stream, constants.EventProviderBoundaryObservationRecorded, connected, recorded)
}

type remoteProviderObservationBundle struct {
	window  *evalv1.ProviderBoundaryObservationWindow
	attempt *operatorv1.InferenceProviderAttemptRecord
}

func (c *remoteProviderObservationClient) Load(ctx context.Context, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord, error) {
	if c == nil || c.client == nil || providerAttemptID == "" {
		return nil, nil, constants.ErrNotFound
	}

	c.mu.Lock()
	if bundle := c.cache[providerAttemptID]; bundle != nil {
		c.mu.Unlock()
		return bundle.window, bundle.attempt, nil
	}
	c.mu.Unlock()

	body, err := c.client.Get(constants.APIPaths.InferenceProviderObservations + providerAttemptID)
	if err != nil {
		return nil, nil, fmt.Errorf("provider observation: gateway read: %w", err)
	}
	var resp models.ProviderObservationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	window := &evalv1.ProviderBoundaryObservationWindow{}
	if err := evalv1.UnmarshalCanonical(resp.Window, window); err != nil {
		return nil, nil, fmt.Errorf("provider observation: decode window: %w", err)
	}
	attempt := &operatorv1.InferenceProviderAttemptRecord{}
	if err := protojson.Unmarshal(resp.ProviderAttempt, attempt); err != nil {
		return nil, nil, fmt.Errorf("provider observation: decode attempt: %w", err)
	}

	bundle := &remoteProviderObservationBundle{window: window, attempt: attempt}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*remoteProviderObservationBundle)
	}
	c.cache[providerAttemptID] = bundle
	c.mu.Unlock()
	return bundle.window, bundle.attempt, nil
}

// NewProviderObservationRemote builds the Gateway client that reads provider
// observation windows. Callers decide whether the Gateway is reachable first; see
// NewCampaignWitnessCapture.
func NewProviderObservationRemote(fileSvc fs.RuntimeFileService, cfg *config.Config) (evaluation.ProviderObservationRemote, error) {
	client, err := api.NewClient(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("provider observation: create gateway client: %w", err)
	}
	return &remoteProviderObservationClient{client: client, stream: client.NewSSEClient}, nil
}

// NewCampaignWitnessCapture builds the run's Gateway witness capture over both
// witness remotes. It reports constants.ErrGatewayNotReachable while the Gateway
// is down, when nothing can be fetched.
func NewCampaignWitnessCapture(fileSvc fs.RuntimeFileService, store *evaluation.Store, cfg *config.Config) (*evaluation.CampaignWitnessCapture, error) {
	if !isConfiguredGatewayHealthy(fileSvc, cfg) {
		return nil, fmt.Errorf("campaign witness capture: %w", constants.ErrGatewayNotReachable)
	}
	providers, err := NewProviderObservationRemote(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	provenance, err := newModelProvenanceRemote(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	return evaluation.NewCampaignWitnessCapture(store, providers, provenance)
}

// isConfiguredGatewayHealthy checks the Gateway selected by the CLI config
// through its authenticated API client.
func isConfiguredGatewayHealthy(fileSvc fs.RuntimeFileService, cfg *config.Config) bool {
	client, err := api.NewClient(fileSvc, cfg)
	if err != nil {
		return false
	}
	body, err := client.Get(constants.APIPaths.Health)
	if err != nil {
		return false
	}
	var health models.HealthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		return false
	}
	return health.Posture != ""
}

func PreflightProviderObservationDelivery(fileSvc fs.RuntimeFileService, cfg *config.Config) error {
	if !isConfiguredGatewayHealthy(fileSvc, cfg) {
		return constants.ErrEvaluationObservationUnavailable
	}
	client, err := authcmd.DefaultAPIClientFactory(fileSvc, cfg)
	if err != nil {
		return fmt.Errorf("provider observation preflight: create gateway client: %w", err)
	}
	body, err := client.Get(constants.APIPaths.InferenceProviderObservations + "_preflight")
	if err != nil {
		return fmt.Errorf("provider observation preflight: %w", err)
	}
	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if resp.Status != "ready" {
		return fmt.Errorf("provider observation preflight: unexpected status %q", resp.Status)
	}
	return nil
}
