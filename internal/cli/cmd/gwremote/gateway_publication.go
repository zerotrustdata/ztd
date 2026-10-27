// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
)

// campaignPublicationAPIClientTimeout covers bulk mirror restore work such as
// large proof-catalog flushes while the gateway is signing and ingesting.
const campaignPublicationAPIClientTimeout = 10 * time.Minute

func campaignPublicationAPIClientFactory(fileSvc fs.RuntimeFileService, cfg *config.Config) (authcmd.APIClient, error) {
	return api.NewClientWithTimeout(fileSvc, cfg, campaignPublicationAPIClientTimeout)
}

type remoteGatewayCampaignFeedExporter struct {
	client    authcmd.APIClient
	highWater int64
	mu        sync.Mutex
	exportMu  sync.Mutex
}

func (e *remoteGatewayCampaignFeedExporter) HighWaterSequence(ctx context.Context) (int64, error) {
	e.mu.Lock()
	cached := e.highWater
	e.mu.Unlock()
	if cached > 0 {
		return cached, nil
	}
	return e.fetchGatewayHighWater()
}

func (e *remoteGatewayCampaignFeedExporter) ExportBatch(ctx context.Context, records []evaluation.CampaignPublicFeedRecord) error {
	e.exportMu.Lock()
	defer e.exportMu.Unlock()

	err := e.exportBatchOnce(records)
	if err == nil || !isPublicFeedPublicationRetryable(err) {
		return err
	}
	e.invalidateHighWater()
	nextSequence, err := e.HighWaterSequence(ctx)
	if err != nil {
		return fmt.Errorf("campaign publication: gateway export batch: refresh high water: %w", err)
	}
	nextSequence++
	for index := range records {
		records[index].Sequence = nextSequence + int64(index)
	}
	return e.exportBatchOnce(records)
}

func isPublicFeedPublicationRetryable(err error) bool {
	return isPublicFeedSequenceOutOfOrder(err) || isPublicFeedOutboxPublicationError(err)
}

func isPublicFeedOutboxPublicationError(err error) bool {
	return errors.Is(err, constants.ErrPublicFeedOutboxEquivocation) ||
		errors.Is(err, constants.ErrPublicFeedOutboxCorrupt) ||
		errors.Is(err, constants.ErrPublicFeedHashChainMismatch)
}

func (e *remoteGatewayCampaignFeedExporter) exportBatchOnce(records []evaluation.CampaignPublicFeedRecord) error {
	batch := make([]models.PublicFeedRecord, len(records))
	for index, record := range records {
		recordType := record.RecordType
		if recordType == "" {
			recordType = models.PublicFeedRecordTypeProjection
		}
		batch[index] = models.PublicFeedRecord{
			Sequence:    record.Sequence,
			RecordType:  recordType,
			RecordHash:  record.RecordHash,
			RecordBytes: record.RecordBytes,
		}
	}
	body, err := e.client.Post(constants.APIPaths.PublicFeedBatches, batch)
	if err != nil {
		return fmt.Errorf("campaign publication: gateway export batch: %w", err)
	}
	var resp models.PublicFeedExportBatchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	e.mu.Lock()
	e.highWater = resp.HighWaterSequence
	e.mu.Unlock()
	return nil
}

func (e *remoteGatewayCampaignFeedExporter) fetchGatewayHighWater() (int64, error) {
	body, err := e.client.Get(constants.APIPaths.PublicFeedSnapshot)
	if err != nil {
		if isPublicFeedSnapshotNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("campaign publication: gateway public feed snapshot: %w", err)
	}
	var snapshot models.PublicFeedSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return 0, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	e.mu.Lock()
	if snapshot.HighWaterSequence > e.highWater {
		e.highWater = snapshot.HighWaterSequence
	}
	highWater := e.highWater
	e.mu.Unlock()
	return highWater, nil
}

func (e *remoteGatewayCampaignFeedExporter) invalidateHighWater() {
	e.mu.Lock()
	e.highWater = 0
	e.mu.Unlock()
}

func isPublicFeedSequenceOutOfOrder(err error) bool {
	return errors.Is(err, constants.ErrPublicFeedSequenceOutOfOrder)
}

func isPublicFeedSnapshotNotFound(err error) bool {
	return errors.Is(err, constants.ErrPublicFeedSnapshotNotFound)
}

var defaultMirrorHTTPTransport = httpclient.NewIPv4Transport(nil)

func newMirrorHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: defaultMirrorHTTPTransport,
	}
}

func IsGatewayHealthy() bool {
	if GatewayHealthCheck != nil {
		return GatewayHealthCheck()
	}
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", constants.Ports.OperatorHttp)
	client := newMirrorHTTPClient(2 * time.Second)
	resp, err := client.Get(healthURL) //nolint:noctx
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(host), "127.")
	}
}

// ResolvePublicMirrorOrigin returns the public mirror read origin based on explicit
// override, test override, active gateway endpoint, or default loopback.
func ResolvePublicMirrorOrigin(cfg *config.Config, mirrorOriginOverride string) (string, error) {
	if trimmed := strings.TrimSpace(mirrorOriginOverride); trimmed != "" {
		trimmed = strings.TrimRight(trimmed, "/")
		if err := gateway.ValidatePublicMirrorOrigin(trimmed); err != nil {
			return "", err
		}
		return trimmed, nil
	}
	if PublicMirrorOrigin != "" {
		return PublicMirrorOrigin, nil
	}
	if cfg != nil {
		if rawURL := cfg.OperatorHTTPURL(); rawURL != "" {
			if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
				host := parsed.Hostname()
				if !isLoopbackHost(host) {
					return fmt.Sprintf("http://%s:%d", host, constants.PublicSpectatorPublicPort), nil
				}
			}
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", constants.PublicSpectatorPublicPort), nil
}

// PublicMirrorOrigin overrides the default public mirror origin in tests.
var PublicMirrorOrigin string

// publicMirrorOrigin is the single origin every mirror read and the publisher
// status resolve: the override, or the Gateway's default loopback listener.
func publicMirrorOrigin() string {
	if PublicMirrorOrigin != "" {
		return PublicMirrorOrigin
	}
	return fmt.Sprintf("http://127.0.0.1:%d", constants.PublicSpectatorPublicPort)
}

// gatewayHealthCheck overrides isGatewayHealthy in tests.
var GatewayHealthCheck func() bool

type httpCampaignMirrorProbe struct {
	origin string
	client *http.Client
}

func NewHTTPCampaignMirrorProbe(ctx context.Context) evaluation.CampaignMirrorProbe {
	return &httpCampaignMirrorProbe{
		origin: publicMirrorOrigin(),
		client: newMirrorHTTPClient(5 * time.Second),
	}
}

func NewHTTPCampaignMirrorProbeWithOrigin(origin string) (evaluation.CampaignMirrorProbe, error) {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if err := gateway.ValidatePublicMirrorOrigin(origin); err != nil {
		return nil, fmt.Errorf("campaign mirror probe: %w", err)
	}
	return &httpCampaignMirrorProbe{
		origin: origin,
		client: newMirrorHTTPClient(5 * time.Second),
	}, nil
}

// DatasetPresent asks the mirror's active source for one catalog snapshot of
// datasetID. The mirror filters server-side, so the lookup is one request no
// matter how much history the source retains, and withdrawn datasets read as
// absent.
func (p *httpCampaignMirrorProbe) DatasetPresent(ctx context.Context, datasetID string) (bool, error) {
	if p == nil || datasetID == "" {
		return false, fmt.Errorf("campaign mirror probe: dataset id: %w", constants.ErrMissingRequiredField)
	}
	origin := p.origin
	if origin == "" {
		origin = publicMirrorOrigin()
	}
	page, err := fetchPublicMirrorHistoryWithOrigin(ctx, p.client, origin, "", datasetID, "", 1)
	if err != nil {
		return false, fmt.Errorf("campaign mirror probe: %w", err)
	}
	return len(page.Items) > 0, nil
}

// fetchPublicMirrorHistory reads one page of catalog snapshots from the
// mirror's history, optionally narrowed to one source and one dataset.
func fetchPublicMirrorHistory(ctx context.Context, client *http.Client, sourceID, datasetID, cursor string, limit int) (models.PublicFeedCursorPage, error) {
	return fetchPublicMirrorHistoryWithOrigin(ctx, client, publicMirrorOrigin(), sourceID, datasetID, cursor, limit)
}

func fetchPublicMirrorHistoryWithOrigin(ctx context.Context, client *http.Client, origin, sourceID, datasetID, cursor string, limit int) (models.PublicFeedCursorPage, error) {
	if origin == "" {
		origin = publicMirrorOrigin()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/history", nil)
	if err != nil {
		return models.PublicFeedCursorPage{}, err
	}
	query := req.URL.Query()
	if sourceID != "" {
		query.Set("source", sourceID)
	}
	if datasetID != "" {
		query.Set("dataset", datasetID)
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}
	query.Set("kind", "catalog_snapshot")
	req.URL.RawQuery = query.Encode()
	if client == nil {
		client = newMirrorHTTPClient(5 * time.Second)
	}
	resp, err := doPublicMirrorRead(ctx, client, req, "public mirror history")
	if err != nil {
		return models.PublicFeedCursorPage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return models.PublicFeedCursorPage{}, fmt.Errorf("public mirror history: status %d", resp.StatusCode)
	}
	var page models.PublicFeedCursorPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return models.PublicFeedCursorPage{}, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	return page, nil
}

func FetchPublicMirrorBootstrap(ctx context.Context) (models.PublicFeedBootstrap, error) {
	return FetchPublicMirrorBootstrapWithOrigin(ctx, publicMirrorOrigin())
}

func FetchPublicMirrorBootstrapWithOrigin(ctx context.Context, origin string) (models.PublicFeedBootstrap, error) {
	if origin == "" {
		origin = publicMirrorOrigin()
	}
	return fetchPublicMirrorBootstrapWithClientAndOrigin(ctx, newMirrorHTTPClient(5*time.Second), origin)
}

func fetchPublicMirrorBootstrapWithClient(ctx context.Context, client *http.Client) (models.PublicFeedBootstrap, error) {
	return fetchPublicMirrorBootstrapWithClientAndOrigin(ctx, client, publicMirrorOrigin())
}

func fetchPublicMirrorBootstrapWithClientAndOrigin(ctx context.Context, client *http.Client, origin string) (models.PublicFeedBootstrap, error) {
	if origin == "" {
		origin = publicMirrorOrigin()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/bootstrap", nil)
	if err != nil {
		return models.PublicFeedBootstrap{}, err
	}
	resp, err := doPublicMirrorRead(ctx, client, req, "public mirror bootstrap")
	if err != nil {
		return models.PublicFeedBootstrap{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return models.PublicFeedBootstrap{}, fmt.Errorf("public mirror bootstrap: status %d", resp.StatusCode)
	}
	var bootstrap models.PublicFeedBootstrap
	if err := json.NewDecoder(resp.Body).Decode(&bootstrap); err != nil {
		return models.PublicFeedBootstrap{}, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	return bootstrap, nil
}

func doPublicMirrorRead(ctx context.Context, client *http.Client, req *http.Request, operation string) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		_ = resp.Body.Close()
		if attempt == 1 {
			return nil, fmt.Errorf("%s: status %d: %w", operation, resp.StatusCode, constants.ErrPublicFeedRateLimited)
		}
		if err := waitForPublicMirrorRetry(ctx, resp.Header.Get("Retry-After")); err != nil {
			return nil, err
		}
	}
	return nil, constants.ErrPublicFeedMaxRetriesExceeded
}

func waitForPublicMirrorRetry(ctx context.Context, retryAfter string) error {
	delay := time.Second
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	} else if retryAt, err := http.ParseTime(retryAfter); err == nil {
		delay = time.Until(retryAt)
		if delay < 0 {
			delay = 0
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("public mirror rate-limit wait: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func NewCampaignFeedExporter(cmd context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) (evaluation.CampaignFeedExporter, error) {
	if !isConfiguredGatewayHealthy(fileSvc, cfg) {
		return nil, fmt.Errorf("campaign publication: start ztd-gateway with --public-spectator: %w", constants.ErrGatewayUnhealthy)
	}
	client, err := campaignPublicationAPIClientFactory(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("campaign publication: create gateway client: %w", err)
	}
	return &remoteGatewayCampaignFeedExporter{client: client}, nil
}

type remoteGatewayCampaignProofPublisher struct {
	client authcmd.APIClient
}

// IngestAssignmentAuditSlices publishes the assignment audit proofs and returns
// the Gateway's answer for each one, in request order. Each answer's database
// digest is the digest of the database the Gateway built or reused.
func (p *remoteGatewayCampaignProofPublisher) IngestAssignmentAuditSlices(ctx context.Context, inputs []evaluation.AssignmentAuditProofInput, deferMirrorPush bool) ([]models.PublicAssignmentAuditProofPublishResponse, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("campaign publication: publish assignment audit proof: %w", constants.ErrMissingRequiredField)
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	proofs := make([]models.PublicAssignmentAuditProofPublishRequest, len(inputs))
	for index, input := range inputs {
		proofs[index] = models.PublicAssignmentAuditProofPublishRequest{
			CampaignID:       input.CampaignID,
			CampaignRevision: input.CampaignRevision,
			RunID:            input.RunID,
			AssignmentID:     input.AssignmentID,
			IndexDigest:      input.IndexDigest,
			AgentTrace:       input.Artifacts.AgentTrace,
			ScenarioID:       input.Artifacts.ScenarioID,
			ScenarioInput:    input.Artifacts.ScenarioInput,
			ScenarioGold:     input.Artifacts.ScenarioGold,
		}
	}
	path := constants.APIPaths.PublicFeedProofsBatch
	if len(proofs) == 1 {
		path = constants.APIPaths.PublicFeedProofs
	}
	var responseBody []byte
	var err error
	if len(proofs) == 1 {
		proof := proofs[0]
		proof.DeferMirrorPush = deferMirrorPush
		responseBody, err = p.client.Post(path, proof)
	} else {
		responseBody, err = p.client.Post(path, models.PublicAssignmentAuditProofBatchPublishRequest{
			Proofs:          proofs,
			DeferMirrorPush: deferMirrorPush,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("campaign publication: publish assignment audit proof: %w", err)
	}
	var responses []models.PublicAssignmentAuditProofPublishResponse
	if len(proofs) == 1 {
		var response models.PublicAssignmentAuditProofPublishResponse
		if err := json.Unmarshal(responseBody, &response); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
		}
		if !response.Accepted {
			return nil, constants.ErrPublicFeedProofIngestRejected
		}
		responses = []models.PublicAssignmentAuditProofPublishResponse{response}
	} else {
		var response models.PublicAssignmentAuditProofBatchPublishResponse
		if err := json.Unmarshal(responseBody, &response); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
		}
		if !response.Accepted {
			return nil, constants.ErrPublicFeedProofIngestRejected
		}
		responses = response.Proofs
	}
	if err := verifyAssignmentAuditProofDigests(proofs, responses); err != nil {
		return nil, err
	}
	return responses, nil
}

// verifyAssignmentAuditProofDigests checks that the gateway answered every
// request, in request order, with the assignment it published and a database
// digest. The caller receives only answers that pass this check.
func verifyAssignmentAuditProofDigests(proofs []models.PublicAssignmentAuditProofPublishRequest, responses []models.PublicAssignmentAuditProofPublishResponse) error {
	if len(responses) != len(proofs) {
		return fmt.Errorf("%w: gateway returned %d assignment audit proofs for %d requests", constants.ErrInvalidJSONResponse, len(responses), len(proofs))
	}
	for index, proof := range proofs {
		if responses[index].AssignmentID != proof.AssignmentID || responses[index].DatabaseSHA256 == "" {
			return fmt.Errorf("%w: gateway proof %d is for assignment %q without a database digest, want %q", constants.ErrInvalidJSONResponse, index, responses[index].AssignmentID, proof.AssignmentID)
		}
	}
	return nil
}

func (p *remoteGatewayCampaignProofPublisher) PruneRunProofCatalog(ctx context.Context, runID string) error {
	if p == nil || p.client == nil || runID == "" {
		return nil
	}
	responseBody, err := p.client.Post(constants.APIPaths.PublicFeedProofsPrune, models.PublicProofCatalogPruneRequest{RunID: runID})
	if err != nil {
		return fmt.Errorf("campaign publication: prune run proof catalog: %w", err)
	}
	var response models.PublicProofCatalogPruneResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if !response.Accepted {
		return constants.ErrPublicFeedProofIngestRejected
	}
	return nil
}

func (p *remoteGatewayCampaignProofPublisher) FlushProofCatalog(ctx context.Context) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("campaign publication: flush proof catalog: %w", constants.ErrMissingRequiredField)
	}
	responseBody, err := p.client.Post(constants.APIPaths.PublicFeedProofsPush, struct{}{})
	if err != nil {
		return fmt.Errorf("campaign publication: flush proof catalog: %w", err)
	}
	var response models.PublicProofCatalogPushResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if !response.Accepted {
		return constants.ErrPublicFeedProofIngestRejected
	}
	return nil
}

func NewCampaignProofPublisher(cmd context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) (evaluation.CampaignProofPublisher, error) {
	if !isConfiguredGatewayHealthy(fileSvc, cfg) {
		return nil, fmt.Errorf("campaign publication: start ztd-gateway with --public-spectator: %w", constants.ErrGatewayUnhealthy)
	}
	client, err := campaignPublicationAPIClientFactory(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("campaign publication: create gateway proof client: %w", err)
	}
	return &remoteGatewayCampaignProofPublisher{client: client}, nil
}

func isHostPublicFeedConfigured(ctx context.Context, fileSvc fs.RuntimeFileService) bool {
	exists, err := fileSvc.FileExists(ctx, constants.PublicFeedExportConfigPath)
	return err == nil && exists
}

func ShouldPublishViaConfiguredGateway(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) bool {
	if isHostPublicFeedConfigured(ctx, fileSvc) {
		return false
	}
	if cfg != nil {
		return isConfiguredGatewayHealthy(fileSvc, cfg)
	}
	return IsGatewayHealthy()
}

func ShouldPublishViaGateway(ctx context.Context, fileSvc fs.RuntimeFileService) bool {
	return ShouldPublishViaConfiguredGateway(ctx, fileSvc, nil)
}

func GatewayPublisherStatus(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) (models.PublicPublisherStatus, error) {
	return GatewayPublisherStatusWithOrigin(ctx, fileSvc, cfg, "")
}

func GatewayPublisherStatusWithOrigin(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, mirrorOrigin string) (models.PublicPublisherStatus, error) {
	resolvedOrigin, err := ResolvePublicMirrorOrigin(cfg, mirrorOrigin)
	if err != nil {
		return models.PublicPublisherStatus{}, err
	}
	exporter, err := NewCampaignFeedExporter(ctx, fileSvc, cfg)
	if err != nil {
		return models.PublicPublisherStatus{}, err
	}
	bootstrap, err := FetchPublicMirrorBootstrapWithOrigin(ctx, resolvedOrigin)
	if err != nil {
		return models.PublicPublisherStatus{}, err
	}
	highWater, err := exporter.HighWaterSequence(ctx)
	if err != nil {
		return models.PublicPublisherStatus{}, err
	}
	return models.PublicPublisherStatus{
		Enabled:           true,
		MirrorOrigin:      resolvedOrigin,
		SourceID:          bootstrap.Snapshot.SourceID,
		HighWaterSequence: highWater,
		FeedChainHash:     bootstrap.Snapshot.FeedChainHash,
		BatchCount:        bootstrap.Snapshot.BatchCount,
	}, nil
}

func PublishJSONLViaGateway(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, recordsPath string) (int, error) {
	defaults := models.DefaultPublicExportConfig()
	inputs, err := ReadPublicRecordInputs(recordsPath, defaults.BatchMaxRecords, defaults.BatchMaxBytes)
	if err != nil {
		return 0, err
	}
	exporter, err := NewCampaignFeedExporter(ctx, fileSvc, cfg)
	if err != nil {
		return 0, err
	}
	nextSequence, err := exporter.HighWaterSequence(ctx)
	if err != nil {
		return 0, err
	}
	nextSequence++

	published := 0
	for start := 0; start < len(inputs); start += constants.PublicFeedBatchMaxRecords {
		end := start + constants.PublicFeedBatchMaxRecords
		if end > len(inputs) {
			end = len(inputs)
		}
		chunk := inputs[start:end]
		records := make([]evaluation.CampaignPublicFeedRecord, len(chunk))
		for index, input := range chunk {
			digest := sha256.Sum256([]byte(input.RecordBytes))
			records[index] = evaluation.CampaignPublicFeedRecord{
				Sequence:    nextSequence + int64(index),
				RecordHash:  hex.EncodeToString(digest[:]),
				RecordBytes: input.RecordBytes,
			}
		}
		if err := exporter.ExportBatch(ctx, records); err != nil {
			return published, err
		}
		published += len(records)
		nextSequence = records[len(records)-1].Sequence + 1
	}
	return published, nil
}

func ReadPublicRecordInputs(filename string, maxRecords, maxBytes int) ([]models.PublicFeedRecordInput, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("public-feed: open record input: %w", err)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxBytes)
	inputs := make([]models.PublicFeedRecordInput, 0)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var input models.PublicFeedRecordInput
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("public-feed: decode record input: %w", err)
		}
		inputs = append(inputs, input)
		if len(inputs) > maxRecords {
			_ = file.Close()
			return nil, constants.ErrPublicFeedBatchOversized
		}
	}
	if err := scanner.Err(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("public-feed: scan record input: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("public-feed: close record input: %w", err)
	}
	if len(inputs) == 0 {
		return nil, constants.ErrPublicFeedBatchEmpty
	}
	return inputs, nil
}
