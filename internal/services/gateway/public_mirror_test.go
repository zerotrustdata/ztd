// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// mirrorTestIngestToken is the ingest bearer token every test mirror is
// configured with. The mirror rejects ingest traffic without one.
const mirrorTestIngestToken = "test-ingest-token"

// mirrorTestAuthTransport adds the ingest bearer token to requests that do not
// set Authorization, so fixtures that post ingest traffic authenticate by
// default. Tests that probe missing credentials use a client without it.
type mirrorTestAuthTransport struct {
	base  http.RoundTripper
	token string
}

func (t mirrorTestAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// mirrorTestEnv builds a reference mirror server with a registered source
// key, a test HTTP server, and helper functions for building and sending
// signed batches.
type mirrorTestEnv struct {
	t            *testing.T
	mirror       *PublicMirrorServer
	server       *httptest.Server // ingest listener (IngestHandler)
	publicServer *httptest.Server // anonymous read listener (PublicHandler)
	pub          ed25519.PublicKey
	priv         ed25519.PrivateKey
	keyID        string
	sourceID     string
	client       *http.Client
	fileSvc      fs.RuntimeFileService
	store        *PublicMirrorStore
}

// openTestPublicMirrorStore opens the durable mirror store under fileSvc and
// closes it when the test ends.
func openTestPublicMirrorStore(t *testing.T, fileSvc fs.RuntimeFileService) *PublicMirrorStore {
	t.Helper()
	store, err := OpenPublicMirrorStore(context.Background(), fileSvc, testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// reopenMirror closes the env store and serves a new mirror from a freshly
// reopened store, proving that state survives a restart.
func (e *mirrorTestEnv) reopenMirror() *PublicMirrorServer {
	e.t.Helper()
	require.NoError(e.t, e.store.Close())
	e.store = openTestPublicMirrorStore(e.t, e.fileSvc)
	mirror, err := NewPublicMirrorServer(testutil.NewTestLogger(), e.store, PublicMirrorConfig{})
	require.NoError(e.t, err)
	mirror.SetIngestAuthToken(mirrorTestIngestToken)
	return mirror
}

// sourceSummary returns the in-memory summary for the env source.
func (e *mirrorTestEnv) sourceSummary() *PublicMirrorSourceSummary {
	e.t.Helper()
	e.mirror.mu.RLock()
	defer e.mirror.mu.RUnlock()
	summary, ok := e.mirror.sources[e.sourceID]
	require.True(e.t, ok)
	return summary
}

// assertNoProofState asserts that no proof artifact or manifest was persisted.
func (e *mirrorTestEnv) assertNoProofState() {
	e.t.Helper()
	count, err := e.store.ProofArtifactCount(context.Background())
	require.NoError(e.t, err)
	assert.Zero(e.t, count)
	_, found, err := e.store.ProofManifest(context.Background(), e.sourceID)
	require.NoError(e.t, err)
	assert.False(e.t, found)
}

// retainedBatchCount counts batch rows retained for the env source.
func (e *mirrorTestEnv) retainedBatchCount() int {
	e.t.Helper()
	var count int
	require.NoError(e.t, e.store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM batches WHERE source_id = ?`, e.sourceID).Scan(&count))
	return count
}

func newMirrorTestEnv(t *testing.T) *mirrorTestEnv {
	t.Helper()
	fileSvc := newProducerFileSvc(t)
	store := openTestPublicMirrorStore(t, fileSvc)
	mirror, err := NewPublicMirrorServer(testutil.NewTestLogger(), store, PublicMirrorConfig{})
	require.NoError(t, err)
	mirror.SetIngestAuthToken(mirrorTestIngestToken)
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "test-key-1"
	sourceID := "test-source-1"
	require.NoError(t, mirror.RegisterSourceKey(context.Background(), sourceID, keyID, pub))

	ingestServer, publicServer := newSplitMirrorServers(t, mirror)

	return &mirrorTestEnv{
		t:            t,
		mirror:       mirror,
		server:       ingestServer,
		publicServer: publicServer,
		pub:          pub,
		priv:         priv,
		keyID:        keyID,
		sourceID:     sourceID,
		client:       &http.Client{Transport: mirrorTestAuthTransport{base: ingestServer.Client().Transport, token: mirrorTestIngestToken}},
		fileSvc:      fileSvc,
		store:        store,
	}
}

// newSplitMirrorServers serves the private ingest surface and the anonymous
// public read surface of one mirror on separate listeners, mirroring the
// runtime split. Both servers are closed when the test ends.
func newSplitMirrorServers(t *testing.T, mirror *PublicMirrorServer) (*httptest.Server, *httptest.Server) {
	t.Helper()
	ingestServer := httptest.NewServer(mirror.IngestHandler())
	t.Cleanup(ingestServer.Close)
	publicServer := httptest.NewServer(mirror.PublicHandler())
	t.Cleanup(publicServer.Close)
	return ingestServer, publicServer
}

// buildBatch builds a signed batch with the given records, previous batch
// hash, and sequence range. The batch is signed with the test private key.
func (e *mirrorTestEnv) buildBatch(records []models.PublicFeedRecord, prevHash string) models.PublicFeedBatch {
	e.t.Helper()
	recordHashes := make([]string, len(records))
	for i, r := range records {
		recordHashes[i] = r.RecordHash
	}
	batch := models.PublicFeedBatch{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SchemaVersion:     constants.PublicFeedSchemaVersion,
		SourceID:          e.sourceID,
		FirstSequence:     records[0].Sequence,
		LastSequence:      records[len(records)-1].Sequence,
		PreviousBatchHash: prevHash,
		RecordHashes:      recordHashes,
		GeneratedAt:       time.Now().UTC(),
		SigningKeyID:      e.keyID,
		Records:           records,
	}
	contentHash := computeBatchContentHash(batch)
	batch.ContentHash = contentHash
	contentHashBytes, _ := hex.DecodeString(contentHash)
	sig := ed25519.Sign(e.priv, contentHashBytes)
	batch.Signature = hex.EncodeToString(sig)
	return batch
}

// makeRecord creates a valid public feed record from a projection object.
func (e *mirrorTestEnv) makeRecord(seq int64, proj models.PublicFeedObject) models.PublicFeedRecord {
	return projectionRecordFromObject(e.t, seq, proj)
}

func (e *mirrorTestEnv) makeProofIngestRequest(filename string, content []byte) models.PublicProofIngestRequest {
	e.t.Helper()
	hash := sha256.Sum256(content)
	artifactID := hex.EncodeToString(hash[:])
	generatedAt := time.Now().UTC()
	entry := models.PublicProofCatalogEntry{
		ArtifactID:          artifactID,
		Filename:            filename,
		MediaType:           "application/json",
		ByteSize:            int64(len(content)),
		SHA256:              artifactID,
		Classification:      models.PublicFeedProofClassificationPublicSafe,
		CampaignID:          "c1",
		GeneratedAt:         generatedAt,
		VerificationCommand: "sha256sum " + filename,
		ImmutableURL:        "/proofs/" + artifactID,
	}
	manifest := models.PublicProofManifest{
		SchemaVersion:               constants.PublicProofManifestSchemaVersion,
		CampaignID:                  "c1",
		CampaignRevision:            "rev1",
		VerifiedIndexGenerationHash: artifactID,
		VerificationOK:              true,
		ArtifactCount:               1,
		Artifacts:                   []models.PublicProofCatalogEntry{entry},
		VerifierInstructions:        "verify hashes and signature",
		GeneratedAt:                 generatedAt,
		SigningKeyID:                e.keyID,
	}
	manifest.ProofRootSHA256 = (&PublicPublisherService{}).computeProofRootHash(manifest)
	rootBytes, err := hex.DecodeString(manifest.ProofRootSHA256)
	require.NoError(e.t, err)
	manifest.Signature = hex.EncodeToString(ed25519.Sign(e.priv, rootBytes))
	return models.PublicProofIngestRequest{
		SourceID:  e.sourceID,
		Manifest:  manifest,
		Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: artifactID, Content: content}},
	}
}

// sendIngest sends a batch to the mirror's ingest endpoint and returns the
// response.
func (e *mirrorTestEnv) sendIngest(batch models.PublicFeedBatch) (int, models.PublicIngestResponse) { //nolint:unparam // Keep the HTTP status available for future mirror transport assertions.
	e.t.Helper()
	reqBody := models.PublicIngestRequest{Batch: batch}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(e.t, err)
	resp, err := e.client.Post(e.server.URL+"/ingest", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var ingestResp models.PublicIngestResponse
	require.NoError(e.t, json.NewDecoder(resp.Body).Decode(&ingestResp))
	return resp.StatusCode, ingestResp
}

// getJSON performs a GET request and decodes the JSON response.
func (e *mirrorTestEnv) getJSON(path string, target any) int {
	e.t.Helper()
	resp, err := e.client.Get(e.publicServer.URL + path)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	if target != nil {
		require.NoError(e.t, json.NewDecoder(resp.Body).Decode(target))
	}
	return resp.StatusCode
}

// TestPublicMirror_IngestHandlerRejectsReadRoutes verifies that the private
// ingest listener serves no anonymous read route, even for authenticated GET
// requests.
func TestPublicMirror_IngestHandlerRejectsReadRoutes(t *testing.T) {
	env := newMirrorTestEnv(t)

	for _, path := range []string{"/bootstrap", "/snapshot", "/history", "/stream", "/proof-catalog", "/proof-head", "/proof-segments", "/proofs/unknown-artifact-id"} {
		t.Run(path, func(t *testing.T) {
			request, requestErr := http.NewRequest(http.MethodGet, env.server.URL+path, nil)
			require.NoError(t, requestErr)
			request.Header.Set("Authorization", "Bearer "+mirrorTestIngestToken)
			response, requestErr := env.server.Client().Do(request)
			require.NoError(t, requestErr)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, http.StatusNotFound, response.StatusCode)
			assert.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
		})
	}
}

// TestPublicMirror_PublicHandlerRejectsIngestRoutesWithToken verifies that the
// public read listener mounts no ingest route, so a correctly authenticated
// ingest request is answered as not found rather than admitted.
func TestPublicMirror_PublicHandlerRejectsIngestRoutesWithToken(t *testing.T) {
	env := newMirrorTestEnv(t)

	for _, path := range []string{"/ingest", "/keys/register", "/proof-ingest", "/proof-root?source=" + env.sourceID} {
		t.Run(path, func(t *testing.T) {
			request, requestErr := http.NewRequest(http.MethodPost, env.publicServer.URL+path, strings.NewReader("{}"))
			require.NoError(t, requestErr)
			request.Header.Set("Authorization", "Bearer "+mirrorTestIngestToken)
			response, requestErr := env.publicServer.Client().Do(request)
			require.NoError(t, requestErr)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, http.StatusNotFound, response.StatusCode)
		})
	}
}

func TestPublicMirror_PublicHandlerExposesOnlyAnonymousReadRoutes(t *testing.T) {
	env := newMirrorTestEnv(t)
	server := httptest.NewServer(env.mirror.PublicHandler())
	t.Cleanup(server.Close)

	response, err := server.Client().Get(server.URL + "/bootstrap")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "*", response.Header.Get("Access-Control-Allow-Origin"))

	for _, path := range []string{"/ingest", "/keys/register", "/proof-ingest", "/proof-root"} {
		t.Run(path, func(t *testing.T) {
			request, requestErr := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader("{}"))
			require.NoError(t, requestErr)
			mutationResponse, requestErr := server.Client().Do(request)
			require.NoError(t, requestErr)
			require.NoError(t, mutationResponse.Body.Close())
			assert.Equal(t, http.StatusNotFound, mutationResponse.StatusCode)
			assert.Empty(t, mutationResponse.Header.Get("Access-Control-Allow-Origin"))
		})
	}
}

// ---------------------------------------------------------------------------
// Ingest authentication tests
// ---------------------------------------------------------------------------

// TestMirror_IngestAuth_RejectsMissingToken verifies that the mirror rejects
// ingest requests without a valid bearer token when authentication is
// enabled.
func TestMirror_IngestAuth_RejectsMissingToken(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("secret-token-123")

	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)

	reqBody := models.PublicIngestRequest{Batch: batch}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)
	resp, err := env.server.Client().Post(env.server.URL+"/ingest", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestMirror_IngestAuth_AcceptsWithValidToken verifies that the mirror
// accepts ingest requests with a valid bearer token.
func TestMirror_IngestAuth_AcceptsWithValidToken(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("secret-token-123")

	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)

	reqBody := models.PublicIngestRequest{Batch: batch}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/ingest", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token-123")
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var ingestResp models.PublicIngestResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ingestResp))
	assert.True(t, ingestResp.Accepted)
	assert.Equal(t, int64(1), ingestResp.HighWaterSequence)
}

func TestPublicMirrorClientIDUsesCloudflareAddressFromConfiguredProxy(t *testing.T) {
	fileSvc := newProducerFileSvc(t)
	mirror, err := NewPublicMirrorServer(testutil.NewTestLogger(), openTestPublicMirrorStore(t, fileSvc), PublicMirrorConfig{
		TrustedProxyCIDRs: []string{"172.28.0.1/32"},
	})
	require.NoError(t, err)
	require.NoError(t, mirror.SetAnonymousReadRateLimit(1, time.Minute))

	handler := mirror.PublicHandler()
	request := httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	request.RemoteAddr = "172.28.0.1:32000"
	request.Header.Set("CF-Connecting-IP", "192.0.2.20")
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	assert.NotEqual(t, http.StatusTooManyRequests, first.Code)

	request = httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	request.RemoteAddr = "172.28.0.1:32001"
	request.Header.Set("CF-Connecting-IP", "192.0.2.21")
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request)
	assert.NotEqual(t, http.StatusTooManyRequests, second.Code)

	request = httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	request.RemoteAddr = "198.51.100.4:32002"
	request.Header.Set("CF-Connecting-IP", "192.0.2.22")
	untrusted := httptest.NewRecorder()
	handler.ServeHTTP(untrusted, request)
	assert.NotEqual(t, http.StatusTooManyRequests, untrusted.Code)

	request = httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	request.RemoteAddr = "198.51.100.4:32003"
	request.Header.Set("CF-Connecting-IP", "192.0.2.23")
	untrusted = httptest.NewRecorder()
	handler.ServeHTTP(untrusted, request)
	assert.Equal(t, http.StatusTooManyRequests, untrusted.Code)

	request = httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	request.RemoteAddr = "172.28.0.1:32004"
	request.Header.Add("CF-Connecting-IP", "192.0.2.24")
	request.Header.Add("CF-Connecting-IP", "192.0.2.25")
	assert.Equal(t, "172.28.0.1", mirror.publicMirrorClientID(request))
}

func TestNewPublicMirrorServerRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	fileSvc := newProducerFileSvc(t)
	_, err := NewPublicMirrorServer(testutil.NewTestLogger(), openTestPublicMirrorStore(t, fileSvc), PublicMirrorConfig{
		TrustedProxyCIDRs: []string{"not-a-cidr"},
	})
	assert.ErrorIs(t, err, constants.ErrPublicFeedTrustedProxyConfig)
}

func TestPublicMirrorClientIDRejectsNonUnicastForwardedAddresses(t *testing.T) {
	fileSvc := newProducerFileSvc(t)
	mirror, err := NewPublicMirrorServer(testutil.NewTestLogger(), openTestPublicMirrorStore(t, fileSvc), PublicMirrorConfig{
		TrustedProxyCIDRs: []string{"172.28.0.1/32"},
	})
	require.NoError(t, err)

	for _, forwardedAddress := range []string{"127.0.0.1", "::1", "0.0.0.0", "224.0.0.1"} {
		t.Run(forwardedAddress, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/snapshot", nil)
			request.RemoteAddr = "172.28.0.1:32000"
			request.Header.Set("CF-Connecting-IP", forwardedAddress)
			assert.Equal(t, "172.28.0.1", mirror.publicMirrorClientID(request))
		})
	}
}

func TestMirror_AnonymousReadRateLimitExpiresClientWindowsAndCompactsExpiryQueue(t *testing.T) {
	env := newMirrorTestEnv(t)
	require.NoError(t, env.mirror.SetAnonymousReadRateLimit(1, time.Minute))
	now := time.Now().UTC()
	env.mirror.now = func() time.Time { return now }

	request := httptest.NewRequest(http.MethodGet, "/snapshot?source="+env.sourceID, nil)
	request.RemoteAddr = "198.51.100.10:32000"
	response := httptest.NewRecorder()
	env.mirror.PublicHandler().ServeHTTP(response, request)
	assert.NotEqual(t, http.StatusTooManyRequests, response.Code)

	now = now.Add(time.Minute)
	request = httptest.NewRequest(http.MethodGet, "/snapshot?source="+env.sourceID, nil)
	request.RemoteAddr = "198.51.100.10:32001"
	response = httptest.NewRecorder()
	env.mirror.PublicHandler().ServeHTTP(response, request)
	assert.NotEqual(t, http.StatusTooManyRequests, response.Code)
	assert.Len(t, env.mirror.anonymousRateClients, 1)
	assert.Len(t, env.mirror.anonymousRateExpirations, 1)
	assert.Zero(t, env.mirror.anonymousRateExpiryHead)
}

func TestMirror_AnonymousReadRateLimitRejectsAndResetsPerClientWindow(t *testing.T) {
	env := newMirrorTestEnv(t)
	require.NoError(t, env.mirror.SetAnonymousReadRateLimit(2, time.Minute))
	now := time.Now().UTC()
	env.mirror.now = func() time.Time { return now }

	for range 2 {
		response, err := env.client.Get(env.publicServer.URL + "/snapshot?source=" + env.sourceID)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	response, err := env.client.Get(env.publicServer.URL + "/snapshot?source=" + env.sourceID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, response.StatusCode)
	assert.Equal(t, "60", response.Header.Get("Retry-After"))
	require.NoError(t, response.Body.Close())

	now = now.Add(time.Minute)
	response, err = env.client.Get(env.publicServer.URL + "/snapshot?source=" + env.sourceID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

// ---------------------------------------------------------------------------
// Signature validation tests
// ---------------------------------------------------------------------------

// TestMirror_Signature_RejectsTamperedSignature verifies that the mirror
// rejects a batch with a tampered signature.
func TestMirror_Signature_RejectsTamperedSignature(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)

	// Tamper with the signature.
	sigBytes, _ := hex.DecodeString(batch.Signature)
	sigBytes[0] ^= 0xFF
	batch.Signature = hex.EncodeToString(sigBytes)

	_, resp := env.sendIngest(batch)
	assert.False(t, resp.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionSignatureInvalid, resp.RejectionReason)
}

// TestMirror_Signature_RejectsUnknownKey verifies that the mirror rejects a
// batch signed by an unknown key.
func TestMirror_Signature_RejectsUnknownKey(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	batch.SigningKeyID = "unknown-key"

	_, resp := env.sendIngest(batch)
	assert.False(t, resp.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionUnknownKey, resp.RejectionReason)
}

// TestMirror_Signature_RejectsRevokedKey verifies that the mirror rejects a
// batch signed by a revoked key.
func TestMirror_Signature_RejectsRevokedKey(t *testing.T) {
	env := newMirrorTestEnv(t)
	require.NoError(t, env.mirror.RevokeSourceKey(context.Background(), env.sourceID, env.keyID))

	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)

	_, resp := env.sendIngest(batch)
	assert.False(t, resp.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionRevokedKey, resp.RejectionReason)
}

// ---------------------------------------------------------------------------
// Hash chain validation tests
// ---------------------------------------------------------------------------

// TestMirror_HashChain_RejectsWrongPreviousHash verifies that the mirror
// rejects a batch whose previous_batch_hash does not match the feed chain.
func TestMirror_HashChain_RejectsWrongPreviousHash(t *testing.T) {
	env := newMirrorTestEnv(t)

	// First batch accepted.
	records1 := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	// Second batch with wrong previous hash.
	records2 := []models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch2 := env.buildBatch(records2, "deadbeef00000000000000000000000000000000000000000000000000000000")
	_, resp2 := env.sendIngest(batch2)
	assert.False(t, resp2.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionHashChainMismatch, resp2.RejectionReason)
}

// TestMirror_HashChain_AcceptsCorrectChain verifies that the mirror accepts
// a correctly chained sequence of batches.
func TestMirror_HashChain_AcceptsCorrectChain(t *testing.T) {
	env := newMirrorTestEnv(t)

	// First batch.
	records1 := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	// Second batch chained to first.
	records2 := []models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2"}))}
	batch2 := env.buildBatch(records2, batch1.ContentHash)
	_, resp2 := env.sendIngest(batch2)
	assert.True(t, resp2.Accepted)
	assert.Equal(t, int64(2), resp2.HighWaterSequence)
	assert.Equal(t, batch2.ContentHash, resp2.FeedChainHash)
}

func TestMirror_RestartRecoversAcceptedStateAndContinuesHashChain(t *testing.T) {
	env := newMirrorTestEnv(t)
	records1 := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	mirror2 := env.reopenMirror()
	ingest2, public2 := newSplitMirrorServers(t, mirror2)

	var snapshot models.PublicFeedSnapshot
	response, err := public2.Client().Get(public2.URL + "/snapshot?source=" + env.sourceID)
	require.NoError(t, err)
	require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
	require.NoError(t, response.Body.Close())
	assert.Equal(t, int64(1), snapshot.HighWaterSequence)
	assert.Equal(t, batch1.ContentHash, snapshot.FeedChainHash)

	requestBody, err := json.Marshal(models.PublicIngestRequest{Batch: batch1})
	require.NoError(t, err)
	response, err = env.client.Post(ingest2.URL+"/ingest", "application/json", bytes.NewReader(requestBody))
	require.NoError(t, err)
	var replayResponse models.PublicIngestResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&replayResponse))
	require.NoError(t, response.Body.Close())
	assert.True(t, replayResponse.Accepted)
	_, _, batchCount, recordCount := mirror2.GetSourceState(env.sourceID)
	assert.Equal(t, 1, batchCount)
	assert.Equal(t, 1, recordCount)

	records2 := []models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch2 := env.buildBatch(records2, batch1.ContentHash)
	requestBody, err = json.Marshal(models.PublicIngestRequest{Batch: batch2})
	require.NoError(t, err)
	response, err = env.client.Post(ingest2.URL+"/ingest", "application/json", bytes.NewReader(requestBody))
	require.NoError(t, err)
	var resp2 models.PublicIngestResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&resp2))
	require.NoError(t, response.Body.Close())
	assert.True(t, resp2.Accepted)
	assert.Equal(t, int64(2), resp2.HighWaterSequence)
}

func TestMirror_RetentionPrunesOldestHistoryAndRecoversChain(t *testing.T) {
	env := newMirrorTestEnv(t)
	require.NoError(t, env.mirror.SetMaxRetainedBatches(2))
	previousHash := constants.PublicFeedZeroHashHex
	for sequence := int64(1); sequence <= 3; sequence++ {
		batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(sequence, projectionWithInt64Field("sequence", sequence))}, previousHash)
		_, response := env.sendIngest(batch)
		require.True(t, response.Accepted)
		previousHash = batch.ContentHash
	}

	summary := env.sourceSummary()
	assert.Equal(t, int64(2), summary.RetainedFromSequence)
	assert.Equal(t, int64(3), summary.HighWaterSequence)
	assert.Equal(t, 3, summary.BatchCount)
	assert.Equal(t, 2, env.retainedBatchCount())
	retained, err := env.store.recordsInRange(context.Background(), env.sourceID, 1, 3)
	require.NoError(t, err)
	require.Len(t, retained, 2)
	assert.Equal(t, int64(2), retained[0].Sequence)

	mirror2 := env.reopenMirror()
	require.NoError(t, mirror2.SetMaxRetainedBatches(2))
	batch4 := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(4, projectionWithInt64Field("sequence", 4))}, previousHash)
	_, err = mirror2.validateBatch(context.Background(), batch4)
	require.NoError(t, err)
	require.NoError(t, mirror2.acceptBatch(context.Background(), batch4))

	env.mirror = mirror2
	summary = env.sourceSummary()
	assert.Equal(t, int64(3), summary.RetainedFromSequence)
	assert.Equal(t, 4, summary.BatchCount)
	assert.Equal(t, 2, env.retainedBatchCount())
	retained, err = env.store.recordsInRange(context.Background(), env.sourceID, 1, 4)
	require.NoError(t, err)
	require.Len(t, retained, 2)
	assert.Equal(t, int64(3), retained[0].Sequence)
	assert.Equal(t, int64(4), retained[1].Sequence)
}

func TestMirror_SetMaxRetainedRecordBytesValidates(t *testing.T) {
	env := newMirrorTestEnv(t)
	require.ErrorIs(t, env.mirror.SetMaxRetainedRecordBytes(0), constants.ErrPublicFeedRetentionConfig)
	require.ErrorIs(t, env.mirror.SetMaxRetainedRecordBytes(-10), constants.ErrPublicFeedRetentionConfig)
	assert.NoError(t, env.mirror.SetMaxRetainedRecordBytes(1024))
}

func TestMirror_RetentionPrunesByByteLimitAndRecoversChain(t *testing.T) {
	env := newMirrorTestEnv(t)
	r1 := env.makeRecord(1, projectionWithInt64Field("sequence", 1))
	recordBytesSize := int64(len(r1.RecordBytes))
	require.Positive(t, recordBytesSize)

	// Allow 2 records of bytes (so 3rd batch exceeds byte budget).
	maxBytes := recordBytesSize*2 + 10
	require.NoError(t, env.mirror.SetMaxRetainedBatches(10))
	require.NoError(t, env.mirror.SetMaxRetainedRecordBytes(maxBytes))

	previousHash := constants.PublicFeedZeroHashHex
	for sequence := int64(1); sequence <= 3; sequence++ {
		batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(sequence, projectionWithInt64Field("sequence", sequence))}, previousHash)
		_, response := env.sendIngest(batch)
		require.True(t, response.Accepted)
		previousHash = batch.ContentHash
	}

	summary := env.sourceSummary()
	assert.Equal(t, int64(2), summary.RetainedFromSequence)
	assert.Equal(t, int64(3), summary.HighWaterSequence)
	assert.Equal(t, 3, summary.BatchCount)
	assert.Equal(t, 2, env.retainedBatchCount())
	assert.Equal(t, 2, summary.RetainedRecordCount)
	assert.Equal(t, recordBytesSize*2, summary.RetainedRecordBytes)

	retained, err := env.store.recordsInRange(context.Background(), env.sourceID, 1, 3)
	require.NoError(t, err)
	require.Len(t, retained, 2)
	assert.Equal(t, int64(2), retained[0].Sequence)
	assert.Equal(t, int64(3), retained[1].Sequence)

	// Reopen mirror and verify startup verification passes with RetainedRecordBytes.
	mirror2 := env.reopenMirror()
	require.NoError(t, mirror2.SetMaxRetainedBatches(10))
	require.NoError(t, mirror2.SetMaxRetainedRecordBytes(maxBytes))
	batch4 := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(4, projectionWithInt64Field("sequence", 4))}, previousHash)
	_, err = mirror2.validateBatch(context.Background(), batch4)
	require.NoError(t, err)
	require.NoError(t, mirror2.acceptBatch(context.Background(), batch4))

	env.mirror = mirror2
	summary = env.sourceSummary()
	assert.Equal(t, int64(3), summary.RetainedFromSequence)
	assert.Equal(t, 4, summary.BatchCount)
	assert.Equal(t, 2, env.retainedBatchCount())
	assert.Equal(t, 2, summary.RetainedRecordCount)
	assert.Equal(t, recordBytesSize*2, summary.RetainedRecordBytes)
}

func TestMirror_RetentionPrunesWhicheverLimitHitFirst(t *testing.T) {
	t.Run("batch cap hit first", func(t *testing.T) {
		env := newMirrorTestEnv(t)
		require.NoError(t, env.mirror.SetMaxRetainedBatches(2))
		require.NoError(t, env.mirror.SetMaxRetainedRecordBytes(1000000))
		prevHash := constants.PublicFeedZeroHashHex
		for seq := int64(1); seq <= 3; seq++ {
			batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(seq, projectionWithInt64Field("s", seq))}, prevHash)
			_, resp := env.sendIngest(batch)
			require.True(t, resp.Accepted)
			prevHash = batch.ContentHash
		}
		summary := env.sourceSummary()
		assert.Equal(t, int64(2), summary.RetainedFromSequence)
		assert.Equal(t, 2, env.retainedBatchCount())
	})

	t.Run("byte budget hit first", func(t *testing.T) {
		env := newMirrorTestEnv(t)
		r1 := env.makeRecord(1, projectionWithInt64Field("s", 1))
		rSize := int64(len(r1.RecordBytes))
		require.NoError(t, env.mirror.SetMaxRetainedBatches(100))
		require.NoError(t, env.mirror.SetMaxRetainedRecordBytes(rSize*2+5))
		prevHash := constants.PublicFeedZeroHashHex
		for seq := int64(1); seq <= 3; seq++ {
			batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(seq, projectionWithInt64Field("s", seq))}, prevHash)
			_, resp := env.sendIngest(batch)
			require.True(t, resp.Accepted)
			prevHash = batch.ContentHash
		}
		summary := env.sourceSummary()
		assert.Equal(t, int64(2), summary.RetainedFromSequence)
		assert.Equal(t, 2, env.retainedBatchCount())
	})
}

// TestMirror_RestartRejectsCorruptDurableState verifies every durable
// corruption the store can detect fails closed on reopen.
func TestMirror_RestartRejectsCorruptDurableState(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, env *mirrorTestEnv)
	}{
		{name: "invalid retained window", corrupt: func(t *testing.T, env *mirrorTestEnv) {
			t.Helper()
			_, err := env.store.db.Exec(`UPDATE sources SET retained_from_sequence = 0 WHERE source_id = ?`, env.sourceID)
			require.NoError(t, err)
		}},
		{name: "summary chain hash mismatch", corrupt: func(t *testing.T, env *mirrorTestEnv) {
			t.Helper()
			_, err := env.store.db.Exec(`UPDATE sources SET feed_chain_hash = ? WHERE source_id = ?`, strings.Repeat("ab", 32), env.sourceID)
			require.NoError(t, err)
		}},
		{name: "tampered record bytes", corrupt: func(t *testing.T, env *mirrorTestEnv) {
			t.Helper()
			_, err := env.store.db.Exec(`UPDATE records SET record_bytes = '{}' WHERE source_id = ?`, env.sourceID)
			require.NoError(t, err)
		}},
		{name: "tampered batch signature", corrupt: func(t *testing.T, env *mirrorTestEnv) {
			t.Helper()
			_, err := env.store.db.Exec(`UPDATE batches SET signature = ? WHERE source_id = ?`, strings.Repeat("00", ed25519.SignatureSize), env.sourceID)
			require.NoError(t, err)
		}},
		{name: "missing retained batch", corrupt: func(t *testing.T, env *mirrorTestEnv) {
			t.Helper()
			_, err := env.store.db.Exec(`DELETE FROM batches WHERE source_id = ?`, env.sourceID)
			require.NoError(t, err)
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}, constants.PublicFeedZeroHashHex)
			_, response := env.sendIngest(batch)
			require.True(t, response.Accepted)
			testCase.corrupt(t, env)
			require.NoError(t, env.store.Close())

			_, err := OpenPublicMirrorStore(context.Background(), env.fileSvc, testutil.NewTestLogger())
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
		})
	}
}

func TestMirror_RegisterSourceKeyMakesNewSourceActiveWithoutRemovingArchivedSource(t *testing.T) {
	env := newMirrorTestEnv(t)
	oldBatch := env.buildBatch([]models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"dataset_id": "dataset-old"})),
	}, constants.PublicFeedZeroHashHex)
	_, response := env.sendIngest(oldBatch)
	require.True(t, response.Accepted)

	newPublicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, env.mirror.RegisterSourceKey(context.Background(), "source-new", "key-new", newPublicKey))

	var active models.PublicFeedBootstrap
	assert.Equal(t, http.StatusOK, env.getJSON("/bootstrap", &active))
	assert.Equal(t, "source-new", active.Snapshot.SourceID)
	assert.Zero(t, active.Snapshot.HighWaterSequence)

	var archived models.PublicFeedBootstrap
	assert.Equal(t, http.StatusOK, env.getJSON("/bootstrap?source="+env.sourceID, &archived))
	assert.Equal(t, env.sourceID, archived.Snapshot.SourceID)
	assert.Equal(t, oldBatch.LastSequence, archived.Snapshot.HighWaterSequence)
	assert.Equal(t, oldBatch.ContentHash, archived.Snapshot.FeedChainHash)
}

// ---------------------------------------------------------------------------
// Sequence ordering tests
// ---------------------------------------------------------------------------

// TestMirror_Sequence_RejectsOutOfOrder verifies that the mirror rejects a
// batch whose first sequence is less than or equal to the high-water mark.
func TestMirror_Sequence_RejectsOutOfOrder(t *testing.T) {
	env := newMirrorTestEnv(t)

	// First batch with sequences 1-3.
	records1 := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"})),
		env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2"})),
		env.makeRecord(3, models.NewPublicFeedObject(map[string]string{"v": "3"})),
	}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	// Second batch starting at sequence 2 (overlap).
	records2 := []models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2b"}))}
	batch2 := env.buildBatch(records2, batch1.ContentHash)
	_, resp2 := env.sendIngest(batch2)
	assert.False(t, resp2.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionDuplicateSequence, resp2.RejectionReason)
	assert.Equal(t, batch1.LastSequence, resp2.HighWaterSequence)
	assert.Equal(t, batch1.ContentHash, resp2.FeedChainHash)
}

// TestMirror_Sequence_RejectsFirstBatchNonZeroPrevHash verifies that the
// first batch for a source must carry the zero hash as previous_batch_hash.
func TestMirror_Sequence_RejectsFirstBatchNonZeroPrevHash(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, "abcdef0000000000000000000000000000000000000000000000000000000000")
	_, resp := env.sendIngest(batch)
	assert.False(t, resp.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionHashChainMismatch, resp.RejectionReason)
}

// ---------------------------------------------------------------------------
// Idempotency tests
// ---------------------------------------------------------------------------

// TestMirror_Idempotency_DuplicateBatchAccepted verifies that re-sending an
// already-accepted identical batch returns the durable high-water state.
func TestMirror_Idempotency_DuplicateBatchAccepted(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)

	_, resp1 := env.sendIngest(batch)
	require.True(t, resp1.Accepted)

	// Re-send the same batch.
	_, resp2 := env.sendIngest(batch)
	assert.True(t, resp2.Accepted)
	assert.Equal(t, int64(1), resp2.HighWaterSequence)
	highWater, feedHash, batchCount, recordCount := env.mirror.GetSourceState(env.sourceID)
	assert.Equal(t, int64(1), highWater)
	assert.Equal(t, batch.ContentHash, feedHash)
	assert.Equal(t, 1, batchCount)
	assert.Equal(t, 1, recordCount)
}

func TestMirror_Idempotency_DifferentBatchForAcceptedRangeRejectsEquivocation(t *testing.T) {
	env := newMirrorTestEnv(t)
	batch1 := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}, constants.PublicFeedZeroHashHex)
	_, response := env.sendIngest(batch1)
	require.True(t, response.Accepted)

	batch2 := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "different"}))}, constants.PublicFeedZeroHashHex)
	_, response = env.sendIngest(batch2)
	assert.False(t, response.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionEquivocation, response.RejectionReason)
}

// ---------------------------------------------------------------------------
// Oversized batch tests
// ---------------------------------------------------------------------------

// TestMirror_OversizedBatch_RejectsTooManyRecords verifies that the mirror
// rejects a batch with more than the maximum allowed records.
func TestMirror_OversizedBatch_RejectsTooManyRecords(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := make([]models.PublicFeedRecord, constants.PublicFeedBatchMaxRecords+1)
	for i := range records {
		records[i] = env.makeRecord(int64(i+1), projectionWithInt64Field("i", int64(i)))
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	assert.False(t, resp.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionOversizedBatch, resp.RejectionReason)
}

// ---------------------------------------------------------------------------
// Record hash validation tests
// ---------------------------------------------------------------------------

// TestMirror_RecordHash_RejectsMismatch verifies that the mirror rejects a
// batch where a record hash does not match the computed hash.
func TestMirror_RecordHash_RejectsMismatch(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)

	// Tamper with the record hash.
	batch.Records[0].RecordHash = "deadbeef00000000000000000000000000000000000000000000000000000000"
	batch.RecordHashes[0] = batch.Records[0].RecordHash

	// Recompute content hash and re-sign since we changed record hashes.
	batch.ContentHash = computeBatchContentHash(batch)
	chBytes, _ := hex.DecodeString(batch.ContentHash)
	batch.Signature = hex.EncodeToString(ed25519.Sign(env.priv, chBytes))

	_, resp := env.sendIngest(batch)
	assert.False(t, resp.Accepted)
}

// ---------------------------------------------------------------------------
// Bootstrap endpoint tests
// ---------------------------------------------------------------------------

// TestMirror_Bootstrap_ReturnsSnapshotAndProjections verifies that the
// bootstrap endpoint returns the current snapshot, recent projections, and
// proof catalog summary.
func TestMirror_Bootstrap_ReturnsSnapshotAndProjections(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Ingest a few projection records.
	records := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v1"})),
		env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v2"})),
		env.makeRecord(3, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v3"})),
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	var bootstrap models.PublicFeedBootstrap
	status := env.getJSON("/bootstrap?source="+env.sourceID, &bootstrap)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, constants.PublicFeedProtocolVersion, bootstrap.ProtocolVersion)
	assert.Equal(t, int64(3), bootstrap.Snapshot.HighWaterSequence)
	assert.Equal(t, env.sourceID, bootstrap.Snapshot.SourceID)
	assert.Len(t, bootstrap.RecentProjections, 3)
	assert.Equal(t, models.CampaignFreshnessActive, bootstrap.SourceFreshness)
}

// TestMirror_Bootstrap_EmptySourceReturnsOffline verifies that the bootstrap
// endpoint returns source_offline freshness for an unknown source.
func TestMirror_Bootstrap_EmptySourceReturnsOffline(t *testing.T) {
	env := newMirrorTestEnv(t)
	var bootstrap models.PublicFeedBootstrap
	status := env.getJSON("/bootstrap?source=unknown-source", &bootstrap)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, models.CampaignFreshnessSourceOffline, bootstrap.SourceFreshness)
	assert.Empty(t, bootstrap.RecentProjections)
}

// ---------------------------------------------------------------------------
// Snapshot endpoint tests
// ---------------------------------------------------------------------------

// TestMirror_Snapshot_ReturnsHighWater verifies that the snapshot endpoint
// returns the current high-water sequence and feed-chain hash.
func TestMirror_Snapshot_ReturnsHighWater(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, ingestResp := env.sendIngest(batch)
	require.True(t, ingestResp.Accepted)

	var snap models.PublicFeedSnapshot
	status := env.getJSON("/snapshot?source="+env.sourceID, &snap)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, int64(1), snap.HighWaterSequence)
	assert.Equal(t, batch.ContentHash, snap.FeedChainHash)
	assert.Equal(t, 1, snap.BatchCount)
}

func TestMirror_Snapshot_ReturnsRetainedCheckpoint(t *testing.T) {
	env := newMirrorTestEnv(t)
	first := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}, constants.PublicFeedZeroHashHex)
	_, ingestResp := env.sendIngest(first)
	require.True(t, ingestResp.Accepted)
	second := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2"}))}, first.ContentHash)
	_, ingestResp = env.sendIngest(second)
	require.True(t, ingestResp.Accepted)

	var snap models.PublicFeedSnapshot
	status := env.getJSON("/snapshot?source="+env.sourceID+"&sequence=1", &snap)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, int64(1), snap.HighWaterSequence)
	assert.Equal(t, first.ContentHash, snap.FeedChainHash)
	assert.Equal(t, 1, snap.BatchCount)
}

// ---------------------------------------------------------------------------
// Cursor pagination tests
// ---------------------------------------------------------------------------

// TestMirror_History_CursorPagination verifies that the history endpoint
// paginates records with cursor-based pagination.
func TestMirror_History_CursorPagination(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Ingest 5 records.
	records := make([]models.PublicFeedRecord, 5)
	for i := range records {
		records[i] = env.makeRecord(int64(i+1), projectionWithInt64Field("i", int64(i)))
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	// Page 1 with limit 2.
	var page1 models.PublicFeedCursorPage
	status := env.getJSON("/history?source="+env.sourceID+"&limit=2", &page1)
	assert.Equal(t, http.StatusOK, status)
	assert.Len(t, page1.Items, 2)
	assert.True(t, page1.HasMore)
	assert.Equal(t, "2", page1.Cursor)

	// Page 2 with cursor.
	var page2 models.PublicFeedCursorPage
	status = env.getJSON("/history?source="+env.sourceID+"&cursor=2&limit=2", &page2)
	assert.Equal(t, http.StatusOK, status)
	assert.Len(t, page2.Items, 2)
	assert.True(t, page2.HasMore)

	// Page 3 with cursor.
	var page3 models.PublicFeedCursorPage
	status = env.getJSON("/history?source="+env.sourceID+"&cursor=4&limit=2", &page3)
	assert.Equal(t, http.StatusOK, status)
	assert.Len(t, page3.Items, 1)
	assert.False(t, page3.HasMore)
}

// TestMirror_History_BoundedPageSize verifies that the page size is bounded
// to the maximum.
func TestMirror_History_BoundedPageSize(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Ingest 10 records.
	records := make([]models.PublicFeedRecord, 10)
	for i := range records {
		records[i] = env.makeRecord(int64(i+1), projectionWithInt64Field("i", int64(i)))
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	// Request limit=1000 (should be capped).
	var page models.PublicFeedCursorPage
	status := env.getJSON("/history?source="+env.sourceID+"&limit=1000", &page)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, 500, page.Limit)
}

func TestMirror_JSONReads_CompressGzipResponse(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "title": strings.Repeat("evaluation ", 100)}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, ingestResp := env.sendIngest(batch)
	require.True(t, ingestResp.Accepted)

	getGzip := func(path string) *gzip.Reader {
		req, err := http.NewRequest(http.MethodGet, env.publicServer.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := env.client.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { resp.Body.Close() })
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"), path)
		reader, err := gzip.NewReader(resp.Body)
		require.NoError(t, err, path)
		t.Cleanup(func() { reader.Close() })
		return reader
	}

	var page models.PublicFeedCursorPage
	require.NoError(t, json.NewDecoder(getGzip("/history?source="+env.sourceID)).Decode(&page))
	require.Len(t, page.Items, 1)

	var bootstrap models.PublicFeedBootstrap
	require.NoError(t, json.NewDecoder(getGzip("/bootstrap?source="+env.sourceID)).Decode(&bootstrap))
	require.Len(t, bootstrap.RecentProjections, 1)

	var snapshot models.PublicFeedSnapshot
	require.NoError(t, json.NewDecoder(getGzip("/snapshot?source="+env.sourceID)).Decode(&snapshot))
	assert.Equal(t, int64(1), snapshot.HighWaterSequence)

	var catalog models.PublicProofCatalog
	require.NoError(t, json.NewDecoder(getGzip("/proof-catalog?source="+env.sourceID)).Decode(&catalog))
}

func TestMirror_History_FiltersByRecordKindBeforePagination(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "assignment_result", "dataset_id": "eval-run-1"})),
		env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "eval-run-1"})),
		env.makeRecord(3, models.NewPublicFeedObject(map[string]string{"kind": "assignment_result", "dataset_id": "eval-run-2"})),
		env.makeRecord(4, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "eval-run-2"})),
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	var first models.PublicFeedCursorPage
	status := env.getJSON("/history?source="+env.sourceID+"&kind=catalog_snapshot&limit=1", &first)
	assert.Equal(t, http.StatusOK, status)
	require.Len(t, first.Items, 1)
	datasetID, ok := first.Items[0].StringField("dataset_id")
	require.True(t, ok)
	assert.Equal(t, "eval-run-1", datasetID)
	assert.True(t, first.HasMore)
	assert.Equal(t, "2", first.Cursor)

	var second models.PublicFeedCursorPage
	status = env.getJSON("/history?source="+env.sourceID+"&kind=catalog_snapshot&cursor="+first.Cursor+"&limit=1", &second)
	assert.Equal(t, http.StatusOK, status)
	require.Len(t, second.Items, 1)
	datasetID, ok = second.Items[0].StringField("dataset_id")
	require.True(t, ok)
	assert.Equal(t, "eval-run-2", datasetID)
	assert.False(t, second.HasMore)
}

func TestMirror_History_FiltersByDatasetBeforePagination(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := make([]models.PublicFeedRecord, 0, 41)
	for seq := int64(1); seq <= 40; seq++ {
		records = append(records, env.makeRecord(seq, models.NewPublicFeedObject(map[string]string{
			"kind": "catalog_snapshot", "dataset_id": fmt.Sprintf("ds-live-run-%d", seq%4),
		})))
	}
	records = append(records, env.makeRecord(41, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "ds-live-run-last"})))
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	var found models.PublicFeedCursorPage
	status := env.getJSON("/history?source="+env.sourceID+"&kind=catalog_snapshot&dataset=ds-live-run-last&limit=1", &found)
	assert.Equal(t, http.StatusOK, status)
	require.Len(t, found.Items, 1)
	datasetID, ok := found.Items[0].StringField("dataset_id")
	require.True(t, ok)
	assert.Equal(t, "ds-live-run-last", datasetID)
	assert.False(t, found.HasMore)

	var absent models.PublicFeedCursorPage
	status = env.getJSON("/history?source="+env.sourceID+"&kind=catalog_snapshot&dataset=ds-live-run-missing&limit=1", &absent)
	assert.Equal(t, http.StatusOK, status)
	assert.Empty(t, absent.Items)
	assert.False(t, absent.HasMore)
}

// ---------------------------------------------------------------------------
// CORS tests
// ---------------------------------------------------------------------------

// TestMirror_CORS_AllowsAnonymousReads verifies that anonymous read endpoints
// include CORS headers.
func TestMirror_CORS_AllowsAnonymousReads(t *testing.T) {
	env := newMirrorTestEnv(t)
	resp, err := env.client.Get(env.publicServer.URL + "/snapshot?source=" + env.sourceID)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "GET")
}

// TestMirror_CORS_OptionsReturnsNoContent verifies that OPTIONS preflight
// requests return 204 No Content.
func TestMirror_CORS_OptionsReturnsNoContent(t *testing.T) {
	env := newMirrorTestEnv(t)
	req, err := http.NewRequest(http.MethodOptions, env.publicServer.URL+"/snapshot", nil)
	require.NoError(t, err)
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// TestMirror_CORS_IngestHasNoCORS verifies that the ingest endpoint does not
// include CORS headers (server-to-server only).
func TestMirror_CORS_IngestHasNoCORS(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	reqBody := models.PublicIngestRequest{Batch: batch}
	bodyBytes, _ := json.Marshal(reqBody)
	resp, err := env.client.Post(env.server.URL+"/ingest", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
}

// ---------------------------------------------------------------------------
// SSE replay tests
// ---------------------------------------------------------------------------

// TestMirror_SSE_ReplaysExistingRecords verifies that the SSE stream sends
// existing records on connect.
func TestMirror_SSE_ReplaysExistingRecords(t *testing.T) {
	env := newMirrorTestEnv(t)

	records := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"})),
		env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2"})),
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.publicServer.URL+"/stream?source="+env.sourceID, nil)
	require.NoError(t, err)
	resp2, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()

	scanner := bufio.NewScanner(resp2.Body)
	eventCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventCount++
		}
		// We expect at least a snapshot event + 2 record events.
		if eventCount >= 3 {
			break
		}
	}
	assert.GreaterOrEqual(t, eventCount, 3)
}

func TestMirror_SSE_DeliversFirstBatchToExplicitSourceSubscriber(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.publicServer.URL+"/stream?source="+env.sourceID+"&since_id=0", nil)
	require.NoError(t, err)
	streamResponse, err := env.client.Do(req)
	require.NoError(t, err)
	defer streamResponse.Body.Close()

	batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}, constants.PublicFeedZeroHashHex)
	require.NoError(t, env.mirror.acceptBatch(ctx, batch))

	scanner := bufio.NewScanner(streamResponse.Body)
	receivedRecord := false
	for scanner.Scan() {
		if scanner.Text() == "event: projection" {
			receivedRecord = true
			break
		}
	}
	assert.True(t, receivedRecord)
}

// TestMirror_SSEAllowsConcurrentConnectionsBehindOneProxyUntilGlobalLimit verifies
// that tunnel topology does not collapse independent streams into one client.
func TestMirror_SSEAllowsConcurrentConnectionsBehindOneProxyUntilGlobalLimit(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.maxSSESubscribers = 2
	first, accepted := env.mirror.addSSESubscriber(env.sourceID)
	require.True(t, accepted)
	t.Cleanup(func() { env.mirror.removeSSESubscriber(first) })

	second, accepted := env.mirror.addSSESubscriber(env.sourceID)
	require.True(t, accepted)
	t.Cleanup(func() { env.mirror.removeSSESubscriber(second) })

	_, accepted = env.mirror.addSSESubscriber(env.sourceID)
	assert.False(t, accepted)
}

func TestMirrorSSESubscriber_DropsOldestAndSignalsTruncation(t *testing.T) {
	subscriber := &mirrorSSESubscriber{ch: make(chan models.PublicFeedRecord, 2)}
	subscriber.send(models.PublicFeedRecord{Sequence: 1})
	subscriber.send(models.PublicFeedRecord{Sequence: 2})
	subscriber.send(models.PublicFeedRecord{Sequence: 3})

	assert.Equal(t, int64(2), (<-subscriber.ch).Sequence)
	assert.Equal(t, int64(3), (<-subscriber.ch).Sequence)
	assert.True(t, subscriber.consumeTruncated())
	assert.False(t, subscriber.consumeTruncated())
}

func TestMirror_SSE_ResumesFromSinceID(t *testing.T) {
	env := newMirrorTestEnv(t)

	records := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"})),
		env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2"})),
		env.makeRecord(3, models.NewPublicFeedObject(map[string]string{"v": "3"})),
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Resume from sequence 1 (should get records 2 and 3).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.publicServer.URL+"/stream?source="+env.sourceID+"&since_id=1", nil)
	require.NoError(t, err)
	resp2, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()

	scanner := bufio.NewScanner(resp2.Body)
	dataLines := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			dataLines++
		}
		if dataLines >= 3 {
			break
		}
	}
	// Expect at least: snapshot event + 2 record events = 3 data lines.
	assert.GreaterOrEqual(t, dataLines, 3)
}

func TestMirror_SSE_ReplayTruncatesAtLimit(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetMaxSSEReplayRecords(2)

	records := make([]models.PublicFeedRecord, 4)
	for i := range records {
		records[i] = env.makeRecord(int64(i+1), projectionWithInt64Field("v", int64(i+1)))
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.publicServer.URL+"/stream?source="+env.sourceID, nil)
	require.NoError(t, err)
	resp2, err := env.client.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()

	scanner := bufio.NewScanner(resp2.Body)
	recordEvents := 0
	truncated := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: projection") {
			recordEvents++
		}
		if line == "event: truncated" {
			truncated = true
			break
		}
	}
	assert.Equal(t, 2, recordEvents)
	assert.True(t, truncated)
}

// ---------------------------------------------------------------------------
// Stale source tests
// ---------------------------------------------------------------------------

// TestMirror_StaleSource_ReportsStaleFreshness verifies that elapsed accepted-batch time produces stale freshness.
func TestMirror_StaleSource_ReportsStaleFreshness(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Ingest a batch first.
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	require.NoError(t, env.mirror.SetFreshnessWindows(time.Second, 2*time.Second, 3*time.Second))
	acceptedAt := env.sourceSummary().LastAcceptedAt
	env.mirror.now = func() time.Time { return acceptedAt.Add(2 * time.Second) }

	var snap models.PublicFeedSnapshot
	status := env.getJSON("/snapshot?source="+env.sourceID, &snap)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, models.CampaignFreshnessStale, snap.Freshness)
}

// TestMirror_FreshnessDerivesFromLastAcceptedBatchTime verifies every elapsed-time freshness boundary.
func TestMirror_FreshnessDerivesFromLastAcceptedBatchTime(t *testing.T) {
	env := newMirrorTestEnv(t)
	batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}, constants.PublicFeedZeroHashHex)
	_, response := env.sendIngest(batch)
	require.True(t, response.Accepted)
	require.NoError(t, env.mirror.SetFreshnessWindows(time.Second, 2*time.Second, 3*time.Second))
	acceptedAt := env.sourceSummary().LastAcceptedAt

	testCases := []struct {
		name     string
		age      time.Duration
		expected models.CampaignFreshness
	}{
		{name: "active before delayed window", age: 500 * time.Millisecond, expected: models.CampaignFreshnessActive},
		{name: "delayed after delayed window", age: time.Second, expected: models.CampaignFreshnessDelayed},
		{name: "stale after stale window", age: 2 * time.Second, expected: models.CampaignFreshnessStale},
		{name: "offline after liveness window", age: 3 * time.Second, expected: models.CampaignFreshnessSourceOffline},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env.mirror.now = func() time.Time { return acceptedAt.Add(testCase.age) }
			var snapshot models.PublicFeedSnapshot
			status := env.getJSON("/snapshot?source="+env.sourceID, &snapshot)
			assert.Equal(t, http.StatusOK, status)
			assert.Equal(t, testCase.expected, snapshot.Freshness)
		})
	}
}

func TestMirror_IntentionallyStopped_BootstrapReportsTerminalFreshness(t *testing.T) {
	env := newMirrorTestEnv(t)

	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	require.NoError(t, env.mirror.SetSourceFreshness(context.Background(), env.sourceID, models.CampaignFreshnessIntentionallyStopped))

	var bootstrap models.PublicFeedBootstrap
	status := env.getJSON("/bootstrap?source="+env.sourceID, &bootstrap)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, models.CampaignFreshnessIntentionallyStopped, bootstrap.SourceFreshness)
	assert.Equal(t, models.CampaignFreshnessIntentionallyStopped, bootstrap.Snapshot.Freshness)
}

// ---------------------------------------------------------------------------
// Key rotation tests
// ---------------------------------------------------------------------------

func TestMirror_KeyRegistration_RequiresAuthenticationStrictPayloadAndCurrentKeySignature(t *testing.T) {
	tests := []struct {
		name           string
		authenticate   bool
		mutate         func(*models.PublicKeyRegistrationRequest)
		appendTrailing bool
		expectedStatus int
		expectedKeys   int
	}{
		{name: "missing authentication", expectedStatus: http.StatusUnauthorized, expectedKeys: 1},
		{name: "invalid current key signature", authenticate: true, mutate: func(request *models.PublicKeyRegistrationRequest) {
			request.Signature = strings.Repeat("00", ed25519.SignatureSize)
		}, expectedStatus: http.StatusBadRequest, expectedKeys: 1},
		{name: "mismatched key identifier", authenticate: true, mutate: func(request *models.PublicKeyRegistrationRequest) { request.NewKeyID = "wrong-key" }, expectedStatus: http.StatusBadRequest, expectedKeys: 1},
		{name: "trailing JSON", authenticate: true, appendTrailing: true, expectedStatus: http.StatusBadRequest, expectedKeys: 1},
		{name: "valid signed registration", authenticate: true, expectedStatus: http.StatusOK, expectedKeys: 2},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			env.mirror.SetIngestAuthToken("registration-token")
			newPublicKey, _, err := ed25519.GenerateKey(nil)
			require.NoError(t, err)
			digest := sha256.Sum256(newPublicKey)
			request := models.PublicKeyRegistrationRequest{
				SourceID:     env.sourceID,
				CurrentKeyID: env.keyID,
				NewKeyID:     hex.EncodeToString(digest[:]),
				PublicKey:    hex.EncodeToString(newPublicKey),
			}
			signingBytes, err := publicKeyRegistrationSigningBytes(request)
			require.NoError(t, err)
			request.Signature = hex.EncodeToString(ed25519.Sign(env.priv, signingBytes))
			if testCase.mutate != nil {
				testCase.mutate(&request)
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			if testCase.appendTrailing {
				body = append(body, []byte(`{}`)...)
			}
			httpRequest, err := http.NewRequest(http.MethodPost, env.server.URL+"/keys/register", bytes.NewReader(body))
			require.NoError(t, err)
			if testCase.authenticate {
				httpRequest.Header.Set("Authorization", "Bearer registration-token")
			}
			client := env.client
			if !testCase.authenticate {
				client = env.server.Client()
			}
			response, err := client.Do(httpRequest)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, testCase.expectedStatus, response.StatusCode)
			keys, err := env.store.Keys(context.Background())
			require.NoError(t, err)
			assert.Len(t, keys.Registry, testCase.expectedKeys)
		})
	}
}

// TestMirror_KeyRotation_AcceptsNewKey verifies that the mirror accepts
// batches signed by a newly registered key after the old key is revoked.
func TestMirror_KeyRotation_AcceptsNewKey(t *testing.T) {
	env := newMirrorTestEnv(t)

	// First batch with old key.
	records1 := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	// Generate and register a new key.
	newPub, newPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	newKeyID := "test-key-2"
	require.NoError(t, env.mirror.RegisterSourceKey(context.Background(), env.sourceID, newKeyID, newPub))

	// Revoke old key.
	require.NoError(t, env.mirror.RevokeSourceKey(context.Background(), env.sourceID, env.keyID))

	// Second batch with new key.
	records2 := []models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2"}))}
	batch2 := models.PublicFeedBatch{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SchemaVersion:     constants.PublicFeedSchemaVersion,
		SourceID:          env.sourceID,
		FirstSequence:     2,
		LastSequence:      2,
		PreviousBatchHash: batch1.ContentHash,
		RecordHashes:      []string{records2[0].RecordHash},
		GeneratedAt:       time.Now().UTC(),
		SigningKeyID:      newKeyID,
		Records:           records2,
	}
	batch2.ContentHash = computeBatchContentHash(batch2)
	chBytes, _ := hex.DecodeString(batch2.ContentHash)
	batch2.Signature = hex.EncodeToString(ed25519.Sign(newPriv, chBytes))

	_, resp2 := env.sendIngest(batch2)
	assert.True(t, resp2.Accepted)
	assert.Equal(t, int64(2), resp2.HighWaterSequence)
}

// TestMirror_KeyRotation_OldKeyRejectedAfterRevocation verifies that batches
// signed by the old key are rejected after revocation.
func TestMirror_KeyRotation_OldKeyRejectedAfterRevocation(t *testing.T) {
	env := newMirrorTestEnv(t)

	// First batch with old key.
	records1 := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	// Register new key and revoke old.
	newPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, env.mirror.RegisterSourceKey(context.Background(), env.sourceID, "new-key", newPub))
	require.NoError(t, env.mirror.RevokeSourceKey(context.Background(), env.sourceID, env.keyID))

	// Try to send with old key.
	records2 := []models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"v": "2"}))}
	batch2 := env.buildBatch(records2, batch1.ContentHash)
	_, resp2 := env.sendIngest(batch2)
	assert.False(t, resp2.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionRevokedKey, resp2.RejectionReason)
}

// ---------------------------------------------------------------------------
// Multi-source isolation tests
// ---------------------------------------------------------------------------

// TestMirror_MultiSource_IsolatesSources verifies that the mirror isolates
// state (high-water sequence, feed-chain hash) between multiple sources.
func TestMirror_MultiSource_IsolatesSources(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Register a second source.
	pub2, priv2, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	source2ID := "test-source-2"
	key2ID := "test-key-2"
	require.NoError(t, env.mirror.RegisterSourceKey(context.Background(), source2ID, key2ID, pub2))

	// Ingest to source 1.
	records1 := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"s": "1"}))}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	// Ingest to source 2 (independent sequence space).
	records2Bytes := []byte(`{"schema_version":"1.3.0","kind":"catalog_snapshot","dataset_id":"source-2-dataset","quality_state":"live_in_progress","observed_at":"2026-09-21T00:00:00Z","source_marker":"2"}`)
	records2Hash := sha256.Sum256(records2Bytes)
	records2 := []models.PublicFeedRecord{{
		Sequence:    1,
		RecordType:  models.PublicFeedRecordTypeProjection,
		RecordHash:  hex.EncodeToString(records2Hash[:]),
		RecordBytes: string(records2Bytes),
	}}
	batch2 := models.PublicFeedBatch{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SchemaVersion:     constants.PublicFeedSchemaVersion,
		SourceID:          source2ID,
		FirstSequence:     1,
		LastSequence:      1,
		PreviousBatchHash: constants.PublicFeedZeroHashHex,
		RecordHashes:      []string{records2[0].RecordHash},
		GeneratedAt:       time.Now().UTC(),
		SigningKeyID:      key2ID,
		Records:           records2,
	}
	batch2.ContentHash = computeBatchContentHash(batch2)
	chBytes, _ := hex.DecodeString(batch2.ContentHash)
	batch2.Signature = hex.EncodeToString(ed25519.Sign(priv2, chBytes))

	_, resp2 := env.sendIngest(batch2)
	require.True(t, resp2.Accepted)

	// Source 1 snapshot.
	var snap1 models.PublicFeedSnapshot
	env.getJSON("/snapshot?source="+env.sourceID, &snap1)
	assert.Equal(t, int64(1), snap1.HighWaterSequence)
	assert.Equal(t, batch1.ContentHash, snap1.FeedChainHash)

	// Source 2 snapshot (independent).
	var snap2 models.PublicFeedSnapshot
	env.getJSON("/snapshot?source="+source2ID, &snap2)
	assert.Equal(t, int64(1), snap2.HighWaterSequence)
	assert.Equal(t, batch2.ContentHash, snap2.FeedChainHash)
	assert.NotEqual(t, snap1.FeedChainHash, snap2.FeedChainHash)
}

// ---------------------------------------------------------------------------
// Proof download tests
// ---------------------------------------------------------------------------

func TestMirror_ProofIngest_AuthenticatesValidatesAndAtomicallyStoresPackage(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("proof-token")
	content := []byte(`{"campaign_id":"c1","verification_status":"verified"}`)
	request := env.makeProofIngestRequest("proof.json", content)
	artifactID := request.Artifacts[0].ArtifactID
	requestBody, err := json.Marshal(request)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/proof-ingest", bytes.NewReader(requestBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer proof-token")
	req.Header.Set("Content-Type", "application/json")
	response, err := env.client.Do(req)
	require.NoError(t, err)
	var ingestResponse models.PublicProofIngestResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&ingestResponse))
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.True(t, ingestResponse.Accepted)
	assert.Equal(t, 1, ingestResponse.ArtifactCount)

	response, err = env.client.Get(env.publicServer.URL + "/proofs/" + artifactID)
	require.NoError(t, err)
	stored, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, content, stored)
}

func TestMirror_ProofIngest_AcceptsIncrementalArtifactDelta(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("proof-token")

	first := env.makeProofIngestRequest("proof-a.json", []byte(`{"campaign_id":"c1","artifact":"a"}`))
	firstBody, err := json.Marshal(first)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/proof-ingest", bytes.NewReader(firstBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer proof-token")
	req.Header.Set("Content-Type", "application/json")
	response, err := env.client.Do(req)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)

	secondContent := []byte(`{"campaign_id":"c1","artifact":"b"}`)
	secondHash := sha256.Sum256(secondContent)
	secondID := hex.EncodeToString(secondHash[:])
	secondEntry := models.PublicProofCatalogEntry{
		ArtifactID:          secondID,
		Filename:            "proof-b.json",
		MediaType:           "application/json",
		ByteSize:            int64(len(secondContent)),
		SHA256:              secondID,
		Classification:      models.PublicFeedProofClassificationPublicSafe,
		CampaignID:          "c1",
		GeneratedAt:         time.Now().UTC(),
		VerificationCommand: "sha256sum proof-b.json",
		ImmutableURL:        "/proofs/" + secondID,
	}
	manifest := first.Manifest
	manifest.ArtifactCount = 2
	manifest.Artifacts = append([]models.PublicProofCatalogEntry(nil), first.Manifest.Artifacts[0], secondEntry)
	manifest.ProofRootSHA256 = (&PublicPublisherService{}).computeProofRootHash(manifest)
	rootBytes, err := hex.DecodeString(manifest.ProofRootSHA256)
	require.NoError(t, err)
	manifest.Signature = hex.EncodeToString(ed25519.Sign(env.priv, rootBytes))
	delta := models.PublicProofIngestRequest{
		SourceID:  env.sourceID,
		Manifest:  manifest,
		Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: secondID, Content: secondContent}},
	}
	deltaBody, err := json.Marshal(delta)
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPost, env.server.URL+"/proof-ingest", bytes.NewReader(deltaBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer proof-token")
	req.Header.Set("Content-Type", "application/json")
	response, err = env.client.Do(req)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)

	response, err = env.client.Get(env.publicServer.URL + "/proofs/" + secondID)
	require.NoError(t, err)
	stored, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, secondContent, stored)
}

func TestMirror_ProofIngest_AcceptsManyArtifactDeltaInOneRequest(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("proof-token")

	first := env.makeProofIngestRequest("proof-a.json", []byte(`{"campaign_id":"c1","artifact":"a"}`))
	firstBody, err := json.Marshal(first)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/proof-ingest", bytes.NewReader(firstBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer proof-token")
	req.Header.Set("Content-Type", "application/json")
	response, err := env.client.Do(req)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)

	newEntries := make([]models.PublicProofIngestArtifact, 0, 3)
	newCatalogEntries := append([]models.PublicProofCatalogEntry(nil), first.Manifest.Artifacts[0])
	for i := 0; i < 3; i++ {
		content := []byte(`{"campaign_id":"c1","artifact":"delta-` + string(rune('b'+i)) + `"}`)
		hash := sha256.Sum256(content)
		id := hex.EncodeToString(hash[:])
		newCatalogEntries = append(newCatalogEntries, models.PublicProofCatalogEntry{
			ArtifactID:          id,
			Filename:            "proof-delta-" + string(rune('b'+i)) + ".json",
			MediaType:           "application/json",
			ByteSize:            int64(len(content)),
			SHA256:              id,
			Classification:      models.PublicFeedProofClassificationPublicSafe,
			CampaignID:          "c1",
			GeneratedAt:         time.Now().UTC(),
			VerificationCommand: "sha256sum proof-delta.json",
			ImmutableURL:        "/proofs/" + id,
		})
		newEntries = append(newEntries, models.PublicProofIngestArtifact{ArtifactID: id, Content: content})
	}
	manifest := first.Manifest
	manifest.ArtifactCount = len(newCatalogEntries)
	manifest.Artifacts = newCatalogEntries
	manifest.ProofRootSHA256 = (&PublicPublisherService{}).computeProofRootHash(manifest)
	rootBytes, err := hex.DecodeString(manifest.ProofRootSHA256)
	require.NoError(t, err)
	manifest.Signature = hex.EncodeToString(ed25519.Sign(env.priv, rootBytes))
	delta := models.PublicProofIngestRequest{
		SourceID:  env.sourceID,
		Manifest:  manifest,
		Artifacts: newEntries,
	}
	deltaBody, err := json.Marshal(delta)
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPost, env.server.URL+"/proof-ingest", bytes.NewReader(deltaBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer proof-token")
	req.Header.Set("Content-Type", "application/json")
	response, err = env.client.Do(req)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

func TestMirror_ProofIngest_RejectsCatalogExceedingMaxArtifacts(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("proof-token")
	request := env.makeProofIngestRequest("proof.json", []byte(`{"campaign_id":"c1"}`))
	request.Manifest.ArtifactCount = constants.PublicFeedProofMaxArtifacts + 1
	entries := make([]models.PublicProofCatalogEntry, constants.PublicFeedProofMaxArtifacts+1)
	for i := range entries {
		entries[i] = request.Manifest.Artifacts[0]
	}
	request.Manifest.Artifacts = entries
	request.Artifacts = make([]models.PublicProofIngestArtifact, constants.PublicFeedProofMaxArtifacts+1)
	for i := range request.Artifacts {
		request.Artifacts[i] = models.PublicProofIngestArtifact{
			ArtifactID: request.Manifest.Artifacts[0].ArtifactID,
			Content:    []byte(`{"campaign_id":"c1"}`),
		}
	}
	requestBody, err := json.Marshal(request)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/proof-ingest", bytes.NewReader(requestBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer proof-token")
	req.Header.Set("Content-Type", "application/json")
	response, err := env.client.Do(req)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusBadRequest, response.StatusCode)
	env.assertNoProofState()
}

func TestMirror_ProofIngest_RejectsInvalidPackageWithoutPartialPersistence(t *testing.T) {
	testCases := []struct {
		name     string
		filename string
		content  []byte
		auth     bool
		mutate   func(*models.PublicProofIngestRequest)
	}{
		{name: "missing authentication", filename: "proof.json", content: []byte(`{"campaign_id":"c1"}`), auth: false},
		{name: "path traversal filename", filename: "../proof.json", content: []byte(`{"campaign_id":"c1"}`), auth: true},
		{name: "invalid manifest signature", filename: "proof.json", content: []byte(`{"campaign_id":"c1"}`), auth: true, mutate: func(request *models.PublicProofIngestRequest) {
			request.Manifest.Signature = constants.PublicFeedZeroHashHex
		}},
		{name: "artifact hash substitution", filename: "proof.json", content: []byte(`{"campaign_id":"c1"}`), auth: true, mutate: func(request *models.PublicProofIngestRequest) {
			request.Artifacts[0].Content = []byte(`{"campaign_id":"substituted"}`)
		}},
		{name: "new manifest entry without artifact content", filename: "proof.json", content: []byte(`{"campaign_id":"c1"}`), auth: true, mutate: func(request *models.PublicProofIngestRequest) {
			request.Artifacts = nil
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			env.mirror.SetIngestAuthToken("proof-token")
			request := env.makeProofIngestRequest(testCase.filename, testCase.content)
			if testCase.mutate != nil {
				testCase.mutate(&request)
			}
			requestBody, err := json.Marshal(request)
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, env.server.URL+"/proof-ingest", bytes.NewReader(requestBody))
			require.NoError(t, err)
			if testCase.auth {
				req.Header.Set("Authorization", "Bearer proof-token")
			}
			response, err := env.client.Do(req)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			if testCase.auth {
				assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			} else {
				assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
			}
			env.assertNoProofState()
		})
	}
}

// sendProofIngest posts a proof ingest request with the given bearer token
// and decodes the mirror's response.
func (e *mirrorTestEnv) sendProofIngest(request models.PublicProofIngestRequest, token string) (int, models.PublicProofIngestResponse) {
	e.t.Helper()
	requestBody, err := json.Marshal(request)
	require.NoError(e.t, err)
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/proof-ingest", bytes.NewReader(requestBody))
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := e.client.Do(req)
	require.NoError(e.t, err)
	defer response.Body.Close()
	var ingestResponse models.PublicProofIngestResponse
	require.NoError(e.t, json.NewDecoder(response.Body).Decode(&ingestResponse))
	return response.StatusCode, ingestResponse
}

func TestMirror_ProofIngest_ReportsMissingArtifactIDs(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("proof-token")
	request := env.makeProofIngestRequest("proof.json", []byte(`{"campaign_id":"c1"}`))
	artifacts := request.Artifacts
	request.Artifacts = nil

	status, response := env.sendProofIngest(request, "proof-token")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.False(t, response.Accepted)
	assert.Equal(t, []string{artifacts[0].ArtifactID}, response.MissingArtifactIDs)
	env.assertNoProofState()

	request.Artifacts = artifacts
	status, response = env.sendProofIngest(request, "proof-token")
	assert.Equal(t, http.StatusOK, status)
	assert.True(t, response.Accepted)
	assert.Empty(t, response.MissingArtifactIDs)

	request.Artifacts = nil
	status, response = env.sendProofIngest(request, "proof-token")
	assert.Equal(t, http.StatusOK, status, "a manifest whose artifacts are all stored needs no inline content")
	assert.True(t, response.Accepted)
}

func TestMirror_ProofRoot_AuthenticatesAndReportsStoredRoot(t *testing.T) {
	env := newMirrorTestEnv(t)
	env.mirror.SetIngestAuthToken("proof-token")
	getRoot := func(method, query, token string) (int, models.PublicProofRootResponse) {
		req, err := http.NewRequest(method, env.server.URL+"/proof-root"+query, nil)
		require.NoError(t, err)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		client := env.client
		if token == "" {
			client = env.server.Client()
		}
		response, err := client.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		var root models.PublicProofRootResponse
		if response.StatusCode == http.StatusOK {
			require.NoError(t, json.NewDecoder(response.Body).Decode(&root))
		}
		return response.StatusCode, root
	}
	sourceQuery := "?source=" + env.sourceID

	status, _ := getRoot(http.MethodGet, sourceQuery, "")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = getRoot(http.MethodPost, sourceQuery, "proof-token")
	assert.Equal(t, http.StatusMethodNotAllowed, status)
	status, _ = getRoot(http.MethodGet, "", "proof-token")
	assert.Equal(t, http.StatusBadRequest, status)
	status, _ = getRoot(http.MethodGet, sourceQuery, "proof-token")
	assert.Equal(t, http.StatusNotFound, status)

	request := env.makeProofIngestRequest("proof.json", []byte(`{"campaign_id":"c1"}`))
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), request))
	status, root := getRoot(http.MethodGet, sourceQuery, "proof-token")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, models.PublicProofRootResponse{
		SourceID:        env.sourceID,
		ProofRootSHA256: request.Manifest.ProofRootSHA256,
		ArtifactCount:   1,
	}, root)
}

// TestMirror_ProofDownload_ServesContentAddressedArtifact verifies that the
// mirror serves proof artifacts under content-addressed URLs with safe
// headers.
func TestMirror_ProofDownload_ServesContentAddressedArtifact(t *testing.T) {
	env := newMirrorTestEnv(t)

	content := []byte(`{"proof": "test"}`)
	proofRequest := env.makeProofIngestRequest("proof.json", content)
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), proofRequest))
	artifactID := proofRequest.Artifacts[0].ArtifactID

	resp, err := env.client.Get(env.publicServer.URL + "/proofs/" + artifactID)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Cache-Control"), "immutable")
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "proof.json")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, body)
}

func TestMirror_RestartRecoversProofCatalogArtifactAndKeyRevocation(t *testing.T) {
	env := newMirrorTestEnv(t)
	content := []byte(`{"proof":"durable"}`)
	proofRequest := env.makeProofIngestRequest("proof.json", content)
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), proofRequest))
	artifactID := proofRequest.Artifacts[0].ArtifactID
	require.NoError(t, env.mirror.RevokeSourceKey(context.Background(), env.sourceID, env.keyID))

	mirror2 := env.reopenMirror()
	ingest2, public2 := newSplitMirrorServers(t, mirror2)
	response, err := public2.Client().Get(public2.URL + "/proofs/" + artifactID)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, content, body)

	batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}, constants.PublicFeedZeroHashHex)
	requestBody, err := json.Marshal(models.PublicIngestRequest{Batch: batch})
	require.NoError(t, err)
	response, err = env.client.Post(ingest2.URL+"/ingest", "application/json", bytes.NewReader(requestBody))
	require.NoError(t, err)
	var ingestResponse models.PublicIngestResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&ingestResponse))
	require.NoError(t, response.Body.Close())
	assert.False(t, ingestResponse.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionRevokedKey, ingestResponse.RejectionReason)
}

func TestMirror_RestartRejectsCorruptDurableProofMetadata(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, env *mirrorTestEnv, artifactID string)
	}{
		{name: "entry path traversal", corrupt: func(t *testing.T, env *mirrorTestEnv, _ string) {
			t.Helper()
			_, err := env.store.db.Exec(`UPDATE proof_manifest_entries SET entry_json = json_set(entry_json, '$.filename', '../private.json') WHERE source_id = ?`, env.sourceID)
			require.NoError(t, err)
		}},
		{name: "manifest root mismatch", corrupt: func(t *testing.T, env *mirrorTestEnv, _ string) {
			t.Helper()
			_, err := env.store.db.Exec(`UPDATE proof_manifests SET manifest_json = json_set(manifest_json, '$.proof_root_sha256', ?), proof_root_sha256 = ? WHERE source_id = ?`,
				constants.PublicFeedZeroHashHex, constants.PublicFeedZeroHashHex, env.sourceID)
			require.NoError(t, err)
		}},
		{name: "missing manifest entry", corrupt: func(t *testing.T, env *mirrorTestEnv, _ string) {
			t.Helper()
			_, err := env.store.db.Exec(`DELETE FROM proof_manifest_entries WHERE source_id = ?`, env.sourceID)
			require.NoError(t, err)
		}},
		{name: "substituted artifact file", corrupt: func(t *testing.T, env *mirrorTestEnv, artifactID string) {
			t.Helper()
			require.NoError(t, env.fileSvc.WriteFile(context.Background(), publicMirrorProofArtifactPath(artifactID), []byte(`{"campaign_id":"c2","verification_ok":true}`), constants.PermFilePrivate))
		}},
		{name: "missing artifact file", corrupt: func(t *testing.T, env *mirrorTestEnv, artifactID string) {
			t.Helper()
			require.NoError(t, env.fileSvc.Remove(context.Background(), publicMirrorProofArtifactPath(artifactID)))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			ctx := context.Background()
			request := env.makeProofIngestRequest("proof.json", []byte(`{"campaign_id":"c1","verification_ok":true}`))
			require.NoError(t, env.mirror.storeProofPackage(ctx, request))
			tt.corrupt(t, env, request.Artifacts[0].ArtifactID)
			require.NoError(t, env.store.Close())

			_, err := OpenPublicMirrorStore(ctx, env.fileSvc, testutil.NewTestLogger())

			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
		})
	}
}

// TestMirror_ProofDownload_Returns404ForUnknown verifies that the mirror
// returns 404 for an unknown proof artifact.
func TestMirror_ProofDownload_Returns404ForUnknown(t *testing.T) {
	env := newMirrorTestEnv(t)
	resp, err := env.client.Get(env.server.URL + "/proofs/unknown-artifact-id")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestMirror_ProofCatalog_ReturnsEntries verifies that the proof catalog
// endpoint returns the stored catalog entries.
func TestMirror_ProofCatalog_ReturnsEntries(t *testing.T) {
	env := newMirrorTestEnv(t)

	content := []byte(`{"proof": "test"}`)
	proofRequest := env.makeProofIngestRequest("proof.json", content)
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), proofRequest))
	artifactID := proofRequest.Artifacts[0].ArtifactID

	var catalog models.PublicProofCatalog
	status := env.getJSON("/proof-catalog?source="+env.sourceID, &catalog)
	assert.Equal(t, http.StatusOK, status)
	assert.Len(t, catalog.Entries, 1)
	assert.Equal(t, artifactID, catalog.Entries[0].ArtifactID)
}

// TestMirror_ProofCatalog_ArtifactFilterReturnsOneEntry verifies that the
// artifact parameter narrows the catalog to that entry, returns an empty
// catalog for an unknown digest, and rejects a malformed one.
func TestMirror_ProofCatalog_ArtifactFilterReturnsOneEntry(t *testing.T) {
	env := newMirrorTestEnv(t)
	first := env.makeProofIngestRequest("first.json", []byte(`{"proof":"first"}`))
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), first))
	second := env.makeProofIngestRequest("second.json", []byte(`{"proof":"second"}`))
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), second))
	artifactID := second.Artifacts[0].ArtifactID

	var catalog models.PublicProofCatalog
	require.Equal(t, http.StatusOK, env.getJSON("/proof-catalog?source="+env.sourceID+"&artifact="+artifactID, &catalog))
	require.Len(t, catalog.Entries, 1)
	assert.Equal(t, artifactID, catalog.Entries[0].ArtifactID)
	assert.Equal(t, "second.json", catalog.Entries[0].Filename)

	var unknown models.PublicProofCatalog
	require.Equal(t, http.StatusOK, env.getJSON("/proof-catalog?source="+env.sourceID+"&artifact="+strings.Repeat("0", 64), &unknown))
	assert.Empty(t, unknown.Entries)

	assert.Equal(t, http.StatusBadRequest, env.getJSON("/proof-catalog?source="+env.sourceID+"&artifact=not-a-digest", nil))
	assert.Equal(t, http.StatusBadRequest, env.getJSON("/proof-catalog?source="+env.sourceID+"&artifact=", nil))
}

// TestMirror_ProofCatalog_EmptyReturnsEmptyArray verifies that the proof
// catalog endpoint returns an empty array when no proofs exist.
func TestMirror_ProofCatalog_EmptyReturnsEmptyArray(t *testing.T) {
	env := newMirrorTestEnv(t)
	var catalog models.PublicProofCatalog
	status := env.getJSON("/proof-catalog?source="+env.sourceID, &catalog)
	assert.Equal(t, http.StatusOK, status)
	assert.Empty(t, catalog.Entries)
}

// TestMirror_ProofHead_ReturnsLegacyHeader verifies that a legacy source
// exposes its signing header without duplicating the catalog.
func TestMirror_ProofHead_ReturnsLegacyHeader(t *testing.T) {
	env := newMirrorTestEnv(t)
	proofRequest := env.makeProofIngestRequest("proof.json", []byte(`{"proof":"test"}`))
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), proofRequest))

	var result models.PublicProofHead
	status := env.getJSON("/proof-head?source="+env.sourceID, &result)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "c1", result.CampaignID)
	assert.Equal(t, env.sourceID, result.SourceID)
	assert.Zero(t, result.SegmentIndex)
	assert.Equal(t, proofRequest.Manifest.Signature, result.Signature)
	assert.Equal(t, proofRequest.Manifest.ArtifactCount, result.ArtifactCount)
	assert.Equal(t, proofRequest.Manifest.ProofRootSHA256, result.ProofRootSHA256)
}

func TestMirror_ProofHead_ReadBoundary(t *testing.T) {
	env := newMirrorTestEnv(t)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"no publications", http.MethodGet, "/proof-head", http.StatusNotFound},
		{"unknown source", http.MethodGet, "/proof-head?source=unknown", http.StatusNotFound},
		{"mutation rejected", http.MethodPost, "/proof-head", http.StatusMethodNotAllowed},
		{"removed manifest route", http.MethodGet, "/proof-manifest", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, env.publicServer.URL+tc.path, nil)
			require.NoError(t, err)
			response, err := env.client.Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, tc.status, response.StatusCode)
		})
	}
}

// ---------------------------------------------------------------------------
// Full ingest + read cycle test
// ---------------------------------------------------------------------------

// TestMirror_FullCycle_IngestThenRead verifies the complete cycle: ingest a
// batch, then read it back through bootstrap, snapshot, and history.
func TestMirror_FullCycle_IngestThenRead(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Ingest two batches.
	records1 := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v1"})),
		env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v2"})),
	}
	batch1 := env.buildBatch(records1, constants.PublicFeedZeroHashHex)
	_, resp1 := env.sendIngest(batch1)
	require.True(t, resp1.Accepted)

	records2 := []models.PublicFeedRecord{
		env.makeRecord(3, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v3"})),
	}
	batch2 := env.buildBatch(records2, batch1.ContentHash)
	_, resp2 := env.sendIngest(batch2)
	require.True(t, resp2.Accepted)

	// Snapshot.
	var snap models.PublicFeedSnapshot
	env.getJSON("/snapshot?source="+env.sourceID, &snap)
	assert.Equal(t, int64(3), snap.HighWaterSequence)
	assert.Equal(t, batch2.ContentHash, snap.FeedChainHash)
	assert.Equal(t, 2, snap.BatchCount)

	// Bootstrap.
	var bootstrap models.PublicFeedBootstrap
	env.getJSON("/bootstrap?source="+env.sourceID, &bootstrap)
	assert.Equal(t, int64(3), bootstrap.Snapshot.HighWaterSequence)
	assert.Len(t, bootstrap.RecentProjections, 3)

	// History (all records).
	var page models.PublicFeedCursorPage
	env.getJSON("/history?source="+env.sourceID+"&limit=10", &page)
	assert.Len(t, page.Items, 3)
	assert.False(t, page.HasMore)
}

// ---------------------------------------------------------------------------
// Record type validation tests
// ---------------------------------------------------------------------------

// TestMirror_RecordType_AcceptsEventRecords verifies that the mirror accepts
// canonical explorer live-event records in addition to projections.
func TestMirror_RecordType_AcceptsEventRecords(t *testing.T) {
	env := newMirrorTestEnv(t)

	eventBytes := []byte(`{"schema_version":"1.3.0","kind":"assignment_started","dataset_id":"ds-live-run-1","quality_state":"live_in_progress","observed_at":"2026-09-21T00:00:00Z","event_id":"event-1","run_id":"run-1","lifecycle_status":"running","completed":0,"total":1}`)
	eventHash := sha256.Sum256(eventBytes)
	records := []models.PublicFeedRecord{{
		Sequence:    1,
		RecordType:  models.PublicFeedRecordTypeEvent,
		RecordHash:  hex.EncodeToString(eventHash[:]),
		RecordBytes: string(eventBytes),
	}}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	assert.True(t, resp.Accepted)
}

func TestMirror_RecordType_RejectsUnknownEventKindWithoutAdvancingSequence(t *testing.T) {
	env := newMirrorTestEnv(t)

	eventBytes := []byte(`{"kind":"heartbeat"}`)
	eventHash := sha256.Sum256(eventBytes)
	batch := env.buildBatch([]models.PublicFeedRecord{{
		Sequence:    1,
		RecordType:  models.PublicFeedRecordTypeEvent,
		RecordHash:  hex.EncodeToString(eventHash[:]),
		RecordBytes: string(eventBytes),
	}}, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)

	assert.False(t, resp.Accepted)
	var snapshot models.PublicFeedSnapshot
	env.getJSON("/snapshot?source="+env.sourceID, &snapshot)
	assert.Zero(t, snapshot.HighWaterSequence)
}

// TestMirror_RecordType_AcceptsProofManifestRecords verifies that the mirror
// accepts proof_manifest-type records.
func TestMirror_RecordType_AcceptsProofManifestRecords(t *testing.T) {
	env := newMirrorTestEnv(t)

	manifestBytes, _ := json.Marshal(models.PublicProofManifest{
		SchemaVersion:               constants.PublicProofManifestSchemaVersion,
		ProofRootSHA256:             strings.Repeat("a", 64),
		CampaignID:                  "c1",
		CampaignRevision:            "rev1",
		VerifiedIndexGenerationHash: strings.Repeat("b", 64),
		VerificationOK:              true,
		ArtifactCount:               1,
		Artifacts:                   []models.PublicProofCatalogEntry{{ArtifactID: strings.Repeat("c", 64)}},
		VerifierInstructions:        "verify",
		GeneratedAt:                 time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		SigningKeyID:                "key-1",
		Signature:                   strings.Repeat("d", 128),
	})
	manifestHash := sha256.Sum256(manifestBytes)
	records := []models.PublicFeedRecord{{
		Sequence:    1,
		RecordType:  models.PublicFeedRecordTypeProofManifest,
		RecordHash:  hex.EncodeToString(manifestHash[:]),
		RecordBytes: string(manifestBytes),
	}}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	assert.True(t, resp.Accepted)
}

// TestMirror_RecordType_AcceptsKeyRevocationRecords verifies that the mirror
// accepts key_revocation-type records.
func TestMirror_RecordType_AcceptsKeyRevocationRecords(t *testing.T) {
	env := newMirrorTestEnv(t)
	newPublicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	newKeyDigest := sha256.Sum256(newPublicKey)
	newKeyID := hex.EncodeToString(newKeyDigest[:])
	require.NoError(t, env.mirror.RegisterSourceKey(context.Background(), env.sourceID, newKeyID, newPublicKey))
	revocation := models.PublicKeyRevocationRecord{
		SourceID:     env.sourceID,
		RevokedKeyID: env.keyID,
		RevokedAt:    time.Now().UTC(),
		NewKeyID:     newKeyID,
	}
	signingBytes, err := publicKeyRevocationSigningBytes(revocation)
	require.NoError(t, err)
	revocation.RevocationSignature = hex.EncodeToString(ed25519.Sign(env.priv, signingBytes))
	revBytes, err := json.Marshal(revocation)
	require.NoError(t, err)
	revHash := sha256.Sum256(revBytes)
	records := []models.PublicFeedRecord{{
		Sequence:    1,
		RecordType:  models.PublicFeedRecordTypeKeyRevocation,
		RecordHash:  hex.EncodeToString(revHash[:]),
		RecordBytes: string(revBytes),
	}}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	assert.True(t, resp.Accepted)
	keys, err := env.store.Keys(context.Background())
	require.NoError(t, err)
	assert.True(t, revocation.RevokedAt.Equal(keys.Revoked[env.sourceID+":"+env.keyID]))
}

// ---------------------------------------------------------------------------
// Method not allowed tests
// ---------------------------------------------------------------------------

// TestMirror_MethodNotAllowed_RejectsPOSTOnReadEndpoints verifies that read
// endpoints reject POST requests.
func TestMirror_MethodNotAllowed_RejectsPOSTOnReadEndpoints(t *testing.T) {
	env := newMirrorTestEnv(t)
	for _, path := range []string{"/bootstrap", "/snapshot", "/history", "/proof-catalog"} {
		resp, err := env.client.Post(env.publicServer.URL+path, "application/json", strings.NewReader("{}"))
		require.NoError(t, err)
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "path: %s", path)
		resp.Body.Close()
	}
}

// TestMirror_MethodNotAllowed_RejectsGETOnIngest verifies that the ingest
// endpoint rejects GET requests.
func TestMirror_MethodNotAllowed_RejectsGETOnIngest(t *testing.T) {
	env := newMirrorTestEnv(t)
	resp, err := env.client.Get(env.server.URL + "/ingest")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Empty source tests
// ---------------------------------------------------------------------------

// TestMirror_EmptySource_HistoryReturnsEmpty verifies that the history
// endpoint returns an empty page for a source with no records.
func TestMirror_EmptySource_HistoryReturnsEmpty(t *testing.T) {
	env := newMirrorTestEnv(t)
	var page models.PublicFeedCursorPage
	status := env.getJSON("/history?source="+env.sourceID, &page)
	assert.Equal(t, http.StatusOK, status)
	assert.Empty(t, page.Items)
	assert.False(t, page.HasMore)
}

// TestMirror_EmptySource_SnapshotReturnsZero verifies that the snapshot
// endpoint returns zero high-water for a source with no batches.
func TestMirror_EmptySource_SnapshotReturnsZero(t *testing.T) {
	env := newMirrorTestEnv(t)
	var snap models.PublicFeedSnapshot
	status := env.getJSON("/snapshot?source="+env.sourceID, &snap)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, int64(0), snap.HighWaterSequence)
	assert.Equal(t, constants.PublicFeedZeroHashHex, snap.FeedChainHash)
}

// ---------------------------------------------------------------------------
// Content hash mismatch test
// ---------------------------------------------------------------------------

// TestMirror_ContentHashMismatch_RejectsTamperedContentHash verifies that
// the mirror rejects a batch whose content hash does not match the computed
// hash.
func TestMirror_ContentHashMismatch_RejectsTamperedContentHash(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)

	// Tamper with the content hash.
	batch.ContentHash = "deadbeef00000000000000000000000000000000000000000000000000000000"

	_, resp := env.sendIngest(batch)
	assert.False(t, resp.Accepted)
	assert.Equal(t, models.PublicFeedIngestRejectionHashChainMismatch, resp.RejectionReason)
}

// ---------------------------------------------------------------------------
// Bootstrap with proof catalog summary test
// ---------------------------------------------------------------------------

// TestMirror_Bootstrap_IncludesProofCatalogSummary verifies that the
// bootstrap response includes the proof catalog summary when proofs exist.
func TestMirror_Bootstrap_IncludesProofCatalogSummary(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Ingest a batch.
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"v": "1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	// Store a proof artifact and catalog.
	content := []byte(`{"proof": "test"}`)
	proofRequest := env.makeProofIngestRequest("proof.json", content)
	require.NoError(t, env.mirror.storeProofPackage(context.Background(), proofRequest))

	var bootstrap models.PublicFeedBootstrap
	env.getJSON("/bootstrap?source="+env.sourceID, &bootstrap)
	assert.Equal(t, 1, bootstrap.ProofCatalogSummary.ArtifactCount)
	assert.Equal(t, int64(len(content)), bootstrap.ProofCatalogSummary.TotalByteSize)
	assert.NotNil(t, bootstrap.ProofCatalogSummary.LastGeneratedAt)
}

// ---------------------------------------------------------------------------
// History cursor format test
// ---------------------------------------------------------------------------

// TestMirror_History_CursorIsOpaqueSequence verifies that the cursor is the
// last sequence number and can be used to fetch the next page.
func TestMirror_History_CursorIsOpaqueSequence(t *testing.T) {
	env := newMirrorTestEnv(t)

	// Ingest 10 records.
	records := make([]models.PublicFeedRecord, 10)
	for i := range records {
		records[i] = env.makeRecord(int64(i+1), projectionWithInt64Field("i", int64(i)))
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(batch)
	require.True(t, resp.Accepted)

	// Page 1 with limit 3.
	var page1 models.PublicFeedCursorPage
	env.getJSON("/history?source="+env.sourceID+"&limit=3", &page1)
	assert.Len(t, page1.Items, 3)
	assert.True(t, page1.HasMore)
	cursor := page1.Cursor
	assert.NotEmpty(t, cursor)

	// Page 2 using the cursor.
	var page2 models.PublicFeedCursorPage
	env.getJSON(fmt.Sprintf("/history?source=%s&cursor=%s&limit=3", env.sourceID, cursor), &page2)
	assert.Len(t, page2.Items, 3)

	// Verify no overlap: page2 items should start after page1's last item.
	page1LastSeq, _ := strconv.ParseInt(page1.Cursor, 10, 64)
	var page2FirstSeq int64
	require.NoError(t, json.Unmarshal(page2.Items[0]["sequence"], &page2FirstSeq))
	assert.Greater(t, page2FirstSeq, page1LastSeq)
}

// TestPublicMirrorStore_AssignmentAuditIndexFollowsEventRecords checks that the
// index names each event record's run and assignment, returns them in sequence
// order, and holds no row for a non-event record.
func TestPublicMirrorStore_AssignmentAuditIndexFollowsEventRecords(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	records := []models.PublicFeedRecord{
		liveEventRecord(t, 1, "run-1", "assignment-1", "event-1", 0, 2),
		env.makeRecord(2, projectionWithInt64Field("sequence", 2)),
		liveEventRecord(t, 3, "run-1", "assignment-2", "event-3", 0, 1),
		liveEventRecord(t, 4, "run-1", "assignment-1", "event-4", 1, 2),
	}
	_, resp := env.sendIngest(env.buildBatch(records, constants.PublicFeedZeroHashHex))
	require.True(t, resp.Accepted)

	first, err := env.store.AssignmentEventRecords(ctx, env.sourceID, "run-1", "assignment-1")
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, int64(1), first[0].Sequence)
	assert.Equal(t, int64(4), first[1].Sequence)
	assert.Equal(t, records[0].RecordBytes, first[0].RecordBytes)
	assert.Equal(t, models.PublicFeedRecordTypeEvent, first[0].RecordType)

	second, err := env.store.AssignmentEventRecords(ctx, env.sourceID, "run-1", "assignment-2")
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, int64(3), second[0].Sequence)

	none, err := env.store.AssignmentEventRecords(ctx, env.sourceID, "run-2", "assignment-1")
	require.NoError(t, err)
	assert.Empty(t, none)

	var indexed int
	require.NoError(t, env.store.db.QueryRowWithRetry(ctx, `SELECT COUNT(*) FROM assignment_event_records WHERE source_id = ?`, env.sourceID).Scan(&indexed))
	assert.Equal(t, 3, indexed, "one index row per event record, none for the projection")
}

// TestPublicMirrorStore_AssignmentAuditIndexPrunesWithRecords checks that the
// retention prune removes an index row together with its record.
func TestPublicMirrorStore_AssignmentAuditIndexPrunesWithRecords(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	require.NoError(t, env.mirror.SetMaxRetainedBatches(1))
	first := env.buildBatch([]models.PublicFeedRecord{liveEventRecord(t, 1, "run-1", "assignment-1", "event-1", 0, 2)}, constants.PublicFeedZeroHashHex)
	_, resp := env.sendIngest(first)
	require.True(t, resp.Accepted)
	second := env.buildBatch([]models.PublicFeedRecord{liveEventRecord(t, 2, "run-1", "assignment-1", "event-2", 1, 2)}, first.ContentHash)
	_, resp = env.sendIngest(second)
	require.True(t, resp.Accepted)

	retained, err := env.store.AssignmentEventRecords(ctx, env.sourceID, "run-1", "assignment-1")
	require.NoError(t, err)
	require.Len(t, retained, 1)
	assert.Equal(t, int64(2), retained[0].Sequence)

	var indexed int
	require.NoError(t, env.store.db.QueryRowWithRetry(ctx, `SELECT COUNT(*) FROM assignment_event_records WHERE source_id = ?`, env.sourceID).Scan(&indexed))
	assert.Equal(t, 1, indexed, "the pruned record's index row is removed with it")
}

// TestPublicMirrorStore_AssignmentAuditIndexRejectsUndecodableEvent checks that
// an event body the canonical decoder cannot read fails the store write, so the
// batch is not accepted and nothing is stored. The body passes the mirror's
// protojson validation because its fields are reordered, which the canonical
// form forbids.
func TestPublicMirrorStore_AssignmentAuditIndexRejectsUndecodableEvent(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	body := []byte(`{"kind":"stage_updated","schema_version":"1.9.0","dataset_id":"dataset-1","quality_state":"live_in_progress","observed_at":"2026-10-19T00:00:00Z","event_id":"event-1","run_id":"run-1","assignment_id":"assignment-1","lifecycle_status":"running","completed":0,"total":1}`)
	digest := sha256.Sum256(body)
	record := models.PublicFeedRecord{Sequence: 1, RecordType: models.PublicFeedRecordTypeEvent, RecordHash: hex.EncodeToString(digest[:]), RecordBytes: string(body)}

	t.Run("ingest is refused with a server error and stores nothing", func(t *testing.T) {
		status, resp := env.sendIngest(env.buildBatch([]models.PublicFeedRecord{record}, constants.PublicFeedZeroHashHex))

		assert.False(t, resp.Accepted)
		assert.Equal(t, http.StatusInternalServerError, status, "a store write failure, not a validation rejection")
		stored, err := env.store.recordsInRange(ctx, env.sourceID, 1, 1)
		require.NoError(t, err)
		assert.Empty(t, stored)
		indexed, err := env.store.AssignmentEventRecords(ctx, env.sourceID, "run-1", "assignment-1")
		require.NoError(t, err)
		assert.Empty(t, indexed)
	})

	t.Run("the index write reports the record schema sentinel", func(t *testing.T) {
		err := env.store.write(ctx, "test index write", func(tx *sql.Tx) error {
			return insertAssignmentEventRecords(ctx, tx, env.sourceID, []models.PublicFeedRecord{record})
		})

		require.ErrorIs(t, err, constants.ErrPublicFeedRecordSchemaInvalid)
	})
}

func TestMirror_Bootstrap_ETagAndConditional304(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "eval-run-1"})),
	}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	status, ingestResp := env.sendIngest(batch)
	require.Equal(t, http.StatusOK, status)
	require.True(t, ingestResp.Accepted)

	// First request: 200 OK with ETag
	req, err := http.NewRequest(http.MethodGet, env.publicServer.URL+"/bootstrap?source="+env.sourceID, nil)
	require.NoError(t, err)
	resp, err := env.publicServer.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	etag := resp.Header.Get("ETag")
	assert.NotEmpty(t, etag)
	assert.Equal(t, "public, max-age=0, must-revalidate", resp.Header.Get("Cache-Control"))
	assert.Contains(t, resp.Header.Get("Access-Control-Expose-Headers"), "ETag")

	// Second request with If-None-Match: 304 Not Modified
	req2, err := http.NewRequest(http.MethodGet, env.publicServer.URL+"/bootstrap?source="+env.sourceID, nil)
	require.NoError(t, err)
	req2.Header.Set("If-None-Match", etag)
	req2.Header.Set("Accept-Encoding", "gzip")
	resp2, err := env.publicServer.Client().Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusNotModified, resp2.StatusCode)
	body, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
	assert.Empty(t, resp2.Header.Get("Content-Encoding"), "304 must be returned before a gzip writer is created")
}

func TestMirror_Bootstrap_GzipAndIdentityRepresentationsMatch(t *testing.T) {
	env := newMirrorTestEnv(t)
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "eval-run-1"}))}
	_, ingestResp := env.sendIngest(env.buildBatch(records, constants.PublicFeedZeroHashHex))
	require.True(t, ingestResp.Accepted)

	get := func(acceptEncoding string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, env.publicServer.URL+"/bootstrap?source="+env.sourceID, nil)
		require.NoError(t, err)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		resp, err := env.publicServer.Client().Do(req)
		require.NoError(t, err)
		return resp
	}

	identity := get("identity")
	defer identity.Body.Close()
	identityBody, err := io.ReadAll(identity.Body)
	require.NoError(t, err)
	compressed := get("gzip")
	defer compressed.Body.Close()
	require.Equal(t, http.StatusOK, compressed.StatusCode)
	require.Equal(t, "gzip", compressed.Header.Get("Content-Encoding"))
	assert.Equal(t, identity.Header.Get("ETag"), compressed.Header.Get("ETag"))
	assert.Equal(t, strconv.Itoa(len(identityBody)), identity.Header.Get("Content-Length"))
	assert.Equal(t, strconv.FormatInt(compressed.ContentLength, 10), compressed.Header.Get("Content-Length"))
	gz, err := gzip.NewReader(compressed.Body)
	require.NoError(t, err)
	uncompressedBody, err := io.ReadAll(gz)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	assert.Equal(t, identityBody, uncompressedBody)
}

func TestMirror_Bootstrap_ETagInvalidatesFreshnessAndProofChanges(t *testing.T) {
	t.Run("freshness transition", func(t *testing.T) {
		env := newMirrorTestEnv(t)
		records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "eval-run-1"}))}
		_, ingestResp := env.sendIngest(env.buildBatch(records, constants.PublicFeedZeroHashHex))
		require.True(t, ingestResp.Accepted)
		first := bootstrapResponse(t, env, "")
		firstETag := first.Header.Get("ETag")
		first.Body.Close()
		require.NoError(t, env.mirror.SetSourceFreshness(context.Background(), env.sourceID, models.CampaignFreshnessIntentionallyStopped))
		second := bootstrapResponse(t, env, firstETag)
		defer second.Body.Close()
		require.Equal(t, http.StatusOK, second.StatusCode)
		assert.NotEqual(t, firstETag, second.Header.Get("ETag"))
		var body models.PublicFeedBootstrap
		require.NoError(t, json.NewDecoder(second.Body).Decode(&body))
		assert.Equal(t, models.CampaignFreshnessIntentionallyStopped, body.SourceFreshness)
		assert.Equal(t, models.CampaignFreshnessIntentionallyStopped, body.Snapshot.Freshness)
	})

	t.Run("proof count and size change", func(t *testing.T) {
		env := newMirrorTestEnv(t)
		records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "eval-run-1"}))}
		_, ingestResp := env.sendIngest(env.buildBatch(records, constants.PublicFeedZeroHashHex))
		require.True(t, ingestResp.Accepted)
		content := []byte(`{"proof":"tiny"}`)
		require.NoError(t, env.mirror.storeProofPackage(context.Background(), env.makeProofIngestRequest("proof.json", content)))
		first := bootstrapResponse(t, env, "")
		firstETag := first.Header.Get("ETag")
		first.Body.Close()
		largerContent := []byte(`{"proof":"proof-content-with-substantially-more-bytes"}`)
		require.NoError(t, env.mirror.storeProofPackage(context.Background(), env.makeProofIngestRequest("proof.json", largerContent)))
		second := bootstrapResponse(t, env, firstETag)
		defer second.Body.Close()
		require.Equal(t, http.StatusOK, second.StatusCode)
		assert.NotEqual(t, firstETag, second.Header.Get("ETag"))
		var body models.PublicFeedBootstrap
		require.NoError(t, json.NewDecoder(second.Body).Decode(&body))
		assert.Equal(t, 1, body.ProofCatalogSummary.ArtifactCount)
		assert.Equal(t, int64(len(largerContent)), body.ProofCatalogSummary.TotalByteSize)
		secondETag := second.Header.Get("ETag")
		sameSizeDifferentContent := []byte(`{"proof":"proof-content-with-substantially-more-byteX"}`)
		require.Len(t, sameSizeDifferentContent, len(largerContent))
		require.NoError(t, env.mirror.storeProofPackage(context.Background(), env.makeProofIngestRequest("proof.json", sameSizeDifferentContent)))
		third := bootstrapResponse(t, env, secondETag)
		defer third.Body.Close()
		require.Equal(t, http.StatusOK, third.StatusCode)
		assert.NotEqual(t, secondETag, third.Header.Get("ETag"))
		var finalBody models.PublicFeedBootstrap
		require.NoError(t, json.NewDecoder(third.Body).Decode(&finalBody))
		assert.Equal(t, 1, finalBody.ProofCatalogSummary.ArtifactCount)
		assert.Equal(t, int64(len(largerContent)), finalBody.ProofCatalogSummary.TotalByteSize)
	})
}

func TestPublicMirror_WithGzip_SuppressesEmptyBodyStatusCodes(t *testing.T) {
	m := &PublicMirrorServer{logger: slog.Default()}
	for _, status := range []int{http.StatusNotModified, http.StatusNoContent} {
		handler := m.withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		handler.ServeHTTP(rec, req)

		assert.Equal(t, status, rec.Code)
		assert.Empty(t, rec.Header().Get("Content-Encoding"), "Content-Encoding must not be set on empty-body status %d", status)
		assert.Empty(t, rec.Body.Bytes(), "Body must be empty on empty-body status %d", status)
	}
}

func bootstrapResponse(t *testing.T, env *mirrorTestEnv, etag string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.publicServer.URL+"/bootstrap?source="+env.sourceID, nil)
	require.NoError(t, err)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := env.publicServer.Client().Do(req)
	require.NoError(t, err)
	return resp
}
