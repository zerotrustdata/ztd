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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// modelProvenanceAttestationPreflightAPIClientTimeout covers the synchronous
// storage attestation probe on the gateway. The default 5s CLI timeout is too
// short while the provenance operator hashes multi-gigabyte Ollama blobs.
const modelProvenanceAttestationPreflightAPIClientTimeout = constants.ModelProvenanceAttestationPreflightTimeout + constants.ModelProvenanceAttestationPreflightClientMargin

type remoteModelProvenanceClient struct {
	client authcmd.APIClient
	mu     sync.Mutex
	cache  map[string]*evalv1.ModelProvenanceAttestationWindow
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
	client, err := authcmd.DefaultAPIClientFactory(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("model provenance: create gateway client: %w", err)
	}
	return &remoteModelProvenanceClient{client: client}, nil
}

func NewCampaignModelProvenanceReader(fileSvc fs.RuntimeFileService, cfg *config.Config) (*evaluation.CampaignModelProvenanceReader, error) {
	remote, err := newModelProvenanceRemote(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	return evaluation.NewCampaignModelProvenanceReaderWithRemote(fileSvc, remote)
}

func preflightModelProvenanceDelivery(fileSvc fs.RuntimeFileService, cfg *config.Config) error {
	if !IsGatewayHealthy() {
		return constants.ErrEvaluationObservationUnavailable
	}
	client, err := authcmd.DefaultAPIClientFactory(fileSvc, cfg)
	if err != nil {
		return fmt.Errorf("model provenance preflight: create gateway client: %w", err)
	}
	body, err := client.Get(constants.APIPaths.InferenceModelProvenanceAttestations + "_preflight")
	if err != nil {
		return fmt.Errorf("model provenance preflight: %w", err)
	}
	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if resp.Status != "ready" {
		return fmt.Errorf("model provenance preflight: unexpected status %q", resp.Status)
	}
	return nil
}

func LoadModelProvenanceAttestation(fileSvc fs.RuntimeFileService, cfg *config.Config, servedModelTag, expectedModelDigest string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if !IsGatewayHealthy() {
		return nil, constants.ErrEvaluationObservationUnavailable
	}
	client, err := api.NewClientWithTimeout(fileSvc, cfg, modelProvenanceAttestationPreflightAPIClientTimeout)
	if err != nil {
		return nil, fmt.Errorf("model provenance attestation preflight: create gateway client: %w", err)
	}
	query := url.Values{}
	query.Set("served_model_tag", servedModelTag)
	query.Set("expected_model_digest", expectedModelDigest)
	path := constants.APIPaths.InferenceModelProvenanceAttestations + "_attest?" + query.Encode()
	body, err := client.Get(path)
	if err != nil {
		return nil, fmt.Errorf("model provenance attestation preflight for %q: %w", servedModelTag, err)
	}
	var resp models.ModelProvenanceAttestResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if resp.Status != "ready" {
		return nil, fmt.Errorf("model provenance attestation preflight for %q: unexpected status %q", servedModelTag, resp.Status)
	}
	if len(resp.Window) == 0 {
		return nil, fmt.Errorf("model provenance attestation preflight for %q: missing attestation window", servedModelTag)
	}
	window := &evalv1.ModelProvenanceAttestationWindow{}
	if err := evalv1.UnmarshalCanonical(resp.Window, window); err != nil {
		return nil, fmt.Errorf("model provenance attestation preflight for %q: decode window: %w", servedModelTag, err)
	}
	return window, nil
}

func PreflightModelProvenanceAttestation(fileSvc fs.RuntimeFileService, cfg *config.Config, servedModelTag, expectedModelDigest string) error {
	return preflightModelProvenanceAttestationContext(context.Background(), fileSvc, cfg, servedModelTag, expectedModelDigest, nil)
}

func preflightModelProvenanceAttestationContext(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, servedModelTag, expectedModelDigest string, progress func(models.ModelProvenancePreflightProgress)) error {
	if !IsGatewayHealthy() {
		return constants.ErrEvaluationObservationUnavailable
	}
	client, err := api.NewClientWithTimeout(fileSvc, cfg, modelProvenanceAttestationPreflightAPIClientTimeout)
	if err != nil {
		return fmt.Errorf("model provenance attestation preflight: create gateway client: %w", err)
	}
	query := url.Values{}
	query.Set("served_model_tag", servedModelTag)
	query.Set("expected_model_digest", expectedModelDigest)
	if progress != nil {
		requestID, err := uuid.NewString()
		if err != nil {
			return fmt.Errorf("model provenance attestation preflight: request id: %w", err)
		}
		query.Set("request_id", requestID)
		streamCtx, stop := context.WithCancel(ctx)
		stream := client.NewSSEClient()
		ready := make(chan struct{})
		stream.SetOnConnect(func() { close(ready) })
		done := make(chan error, 1)
		go func() {
			done <- stream.ConnectOnce(streamCtx, func(eventType, data string) {
				event, ok := sse.DecodeEvent(eventType, data)
				if !ok || event.Type != string(constants.EventModelProvenancePreflightProgress) {
					return
				}
				var update models.ModelProvenancePreflightProgress
				if json.Unmarshal(event.Data, &update) != nil || update.RequestID != requestID {
					return
				}
				progress(update)
			})
		}()
		timer := time.NewTimer(constants.ModelProvenanceCommandAcknowledgementTimeout)
		defer timer.Stop()
		select {
		case <-ready:
			defer func() { stop(); <-done }()
		case err := <-done:
			stop()
			return fmt.Errorf("model provenance preflight: connect progress SSE: %v", err)
		case <-ctx.Done():
			stop()
			<-done
			return ctx.Err()
		case <-timer.C:
			stop()
			<-done
			return fmt.Errorf("model provenance preflight: progress SSE did not connect within %s", constants.ModelProvenanceCommandAcknowledgementTimeout)
		}
	}
	path := constants.APIPaths.InferenceModelProvenanceAttestations + "_attest?" + query.Encode()
	body, err := client.DoRequestContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("model provenance attestation preflight for %q: %w", servedModelTag, err)
	}
	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if resp.Status != "ready" {
		return fmt.Errorf("model provenance attestation preflight for %q: unexpected status %q", servedModelTag, resp.Status)
	}
	return nil
}

// PreflightCampaignModelProvenanceContext streams session-scoped progress before
// dispatch and propagates cancellation to the authoritative attestation request.
func PreflightCampaignModelProvenanceContext(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, bindings []evaluation.CampaignModelBinding, progress func(models.ModelProvenancePreflightProgress)) error {
	if err := preflightModelProvenanceDelivery(fileSvc, cfg); err != nil {
		return err
	}
	for _, binding := range bindings {
		if strings.TrimSpace(binding.ServedModelTag) == "" || strings.TrimSpace(binding.ModelDigest) == "" {
			continue
		}
		if binding.ModelDigest == evaluation.FormationDelegatedRegistryDigestPlaceholder {
			continue
		}
		if err := preflightModelProvenanceAttestationContext(ctx, fileSvc, cfg, binding.ServedModelTag, binding.ModelDigest, progress); err != nil {
			return err
		}
	}
	return nil
}
