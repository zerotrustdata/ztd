// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const capacityTrustedProxyCIDR = "127.0.0.0/8"

const capacityDialMaxAttempts = 16
const capacityDialConcurrency = 64

type capacityMirrorEnv struct {
	helper   *mirrorTestEnv
	baseURL  string
	sourceID string
	client   *http.Client
}

func newCapacityMirrorEnv(t *testing.T, recordCount int) *capacityMirrorEnv {
	t.Helper()
	fileSvc := newProducerFileSvc(t)
	store := openTestPublicMirrorStore(t, fileSvc)
	mirror, err := NewPublicMirrorServer(testutil.NewTestLogger(), store, PublicMirrorConfig{
		TrustedProxyCIDRs: []string{capacityTrustedProxyCIDR},
	})
	require.NoError(t, err)
	mirror.SetIngestAuthToken(mirrorTestIngestToken)

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "capacity-key"
	sourceID := "capacity-source"
	require.NoError(t, mirror.RegisterSourceKey(context.Background(), sourceID, keyID, pub))

	helper := &mirrorTestEnv{
		t:        t,
		mirror:   mirror,
		pub:      pub,
		priv:     priv,
		keyID:    keyID,
		sourceID: sourceID,
		fileSvc:  fileSvc,
		store:    store,
	}
	records := make([]models.PublicFeedRecord, recordCount)
	for index := range records {
		records[index] = helper.makeRecord(int64(index+1), applyProjectionDefaults(models.NewPublicFeedObject(map[string]string{
			"kind":       "catalog_snapshot",
			"dataset_id": fmt.Sprintf("dataset-%d", index%5),
		})))
	}
	batch := helper.buildBatch(records, constants.PublicFeedZeroHashHex)
	status, ingestResp := sendIngestToHandler(t, mirror.IngestHandler(), batch)
	require.Equal(t, http.StatusOK, status)
	require.True(t, ingestResp.Accepted)

	server := httptest.NewServer(mirror.PublicHandler())
	t.Cleanup(server.Close)
	helper.publicServer = server
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2000
	transport.MaxIdleConnsPerHost = 2000
	dialSlots := make(chan struct{}, capacityDialConcurrency)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		select {
		case dialSlots <- struct{}{}:
			defer func() { <-dialSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return capacityDialWithRetry(ctx, network, address)
	}
	client := &http.Client{Transport: transport}
	t.Cleanup(func() { transport.CloseIdleConnections() })
	helper.client = client

	return &capacityMirrorEnv{
		helper:   helper,
		baseURL:  server.URL,
		sourceID: sourceID,
		client:   client,
	}
}

func capacityDialWithRetry(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{}
	var err error
	for attempt := 0; attempt < capacityDialMaxAttempts; attempt++ {
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, network, address)
		if err == nil || !isCapacityConnectionRefusal(err) || attempt+1 == capacityDialMaxAttempts {
			return conn, err
		}

		// Spread retries across a short window so a refused SYN burst does not
		// immediately refill the listener backlog with another synchronized wave.
		delay := 5*time.Millisecond + time.Duration(rand.Intn(11))*time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, err
}

func isCapacityConnectionRefusal(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	// Windows returns the Winsock error value directly. syscall.ECONNREFUSED
	// is a separate, synthesized POSIX-compatible value on Windows.
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.Errno(10061) // WSAECONNREFUSED
}

func sendIngestToHandler(t *testing.T, handler http.Handler, batch models.PublicFeedBatch) (int, models.PublicIngestResponse) {
	t.Helper()
	bodyBytes, err := json.Marshal(models.PublicIngestRequest{Batch: batch})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(bodyBytes))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+mirrorTestIngestToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var ingestResp models.PublicIngestResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &ingestResp))
	return recorder.Code, ingestResp
}

func capacitySyntheticClientAddress(globalIndex int) string {
	return net.IPv4(198, 18, byte(globalIndex/254), byte(globalIndex%254+1)).String()
}

func (env *capacityMirrorEnv) coldLifecycle(ctx context.Context, clientIP string) (outcome string, rateLimited bool) {
	origin := env.baseURL

	bootstrap, status, err := capacityGetJSON[bootstrapCapacityResponse](ctx, env.client, origin+"/bootstrap", clientIP)
	if err != nil || status != http.StatusOK {
		return fmt.Sprintf("bootstrap (status=%d, error=%v)", status, err), status == http.StatusTooManyRequests
	}
	source := bootstrap.Snapshot.SourceID
	if source == "" {
		source = env.sourceID
	}
	cursor := bootstrap.Snapshot.HighWaterSequence
	for range 10 {
		for range 100 {
			history, status, err := capacityGetJSON[historyCapacityResponse](ctx, env.client, capacityEndpoint(origin, "/history", url.Values{
				"source": {source},
				"cursor": {strconv.FormatInt(cursor, 10)},
				"limit":  {"500"},
			}), clientIP)
			if err != nil || status != http.StatusOK {
				return fmt.Sprintf("history (status=%d, error=%v)", status, err), status == http.StatusTooManyRequests
			}
			if len(history.Items) > 0 {
				cursor = history.Items[len(history.Items)-1].Sequence
			}
			if !history.HasMore {
				break
			}
			if len(history.Items) == 0 {
				return "history_stalled", false
			}
		}
		snapshot, status, err := capacityGetJSON[snapshotCapacityResponse](ctx, env.client, capacityEndpoint(origin, "/snapshot", url.Values{"source": {source}}), clientIP)
		if err != nil || status != http.StatusOK {
			return fmt.Sprintf("snapshot (status=%d, error=%v)", status, err), status == http.StatusTooManyRequests
		}
		if snapshot.HighWaterSequence == cursor {
			survived, status, outcome := env.pollLive(ctx, clientIP, 200*time.Millisecond)
			if !survived && outcome != "complete" {
				return outcome, status == http.StatusTooManyRequests
			}
			return outcome, status == http.StatusTooManyRequests
		}
	}
	return "sequence_divergence", false
}

// capacityPollInterval is the pause between one client's poll cycles.
const capacityPollInterval = 200 * time.Millisecond

// pollLive polls pointer.json and live/status.json from one client until hold
// has elapsed. The first non-200 response or transport failure ends the poll
// with that status and an outcome naming the endpoint.
func (env *capacityMirrorEnv) pollLive(ctx context.Context, clientIP string, hold time.Duration) (survived bool, status int, outcome string) {
	deadline := time.Now().Add(hold)
	for {
		if _, status, err := capacityGetJSON[models.PublicFeedPointer](ctx, env.client, env.baseURL+"/"+constants.PublicLayoutPointerKey, clientIP); err != nil || status != http.StatusOK {
			return false, status, fmt.Sprintf("pointer (status=%d, error=%v)", status, err)
		}
		if _, status, err := capacityGetJSON[models.PublicLiveStatus](ctx, env.client, env.baseURL+"/"+constants.PublicLayoutLiveStatusKey, clientIP); err != nil || status != http.StatusOK {
			return false, status, fmt.Sprintf("live_status (status=%d, error=%v)", status, err)
		}
		if !time.Now().Before(deadline) {
			return true, http.StatusOK, "complete"
		}
		select {
		case <-ctx.Done():
			return false, 0, "poll_cancelled"
		case <-time.After(capacityPollInterval):
		}
	}
}

type bootstrapCapacityResponse struct {
	Snapshot snapshotCapacityResponse `json:"snapshot"`
}

type historyCapacityResponse struct {
	Items   []projectionCapacityCursor `json:"items"`
	HasMore bool                       `json:"has_more"`
}

type snapshotCapacityResponse struct {
	SourceID          string `json:"source_id"`
	HighWaterSequence int64  `json:"high_water_sequence"`
}

type projectionCapacityCursor struct {
	Sequence int64 `json:"sequence"`
}

func capacityEndpoint(origin, path string, query url.Values) string {
	address := strings.TrimSuffix(origin, "/") + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	return address
}

func capacityGetJSON[T any](ctx context.Context, client *http.Client, address, clientIP string) (T, int, error) {
	var value T
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return value, 0, err
	}
	request.Header.Set("Accept", "application/json")
	if clientIP != "" {
		request.Header.Set("CF-Connecting-IP", clientIP)
	}
	response, err := client.Do(request)
	if err != nil {
		return value, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return value, response.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&value); err != nil {
		return value, response.StatusCode, err
	}
	return value, response.StatusCode, nil
}

func TestPublicSpectatorColdLoad_ThreeHundredDistinctClientsReconcile(t *testing.T) {
	env := newCapacityMirrorEnv(t, 60)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const clients = 300
	results := make(chan string, clients)
	var rateLimited atomic.Int32
	var wg sync.WaitGroup
	wg.Add(clients)
	for index := range clients {
		go func(clientIndex int) {
			defer wg.Done()
			outcome, limited := env.coldLifecycle(ctx, capacitySyntheticClientAddress(clientIndex))
			results <- outcome
			if limited {
				rateLimited.Add(1)
			}
		}(index)
	}
	wg.Wait()
	close(results)

	outcomes := make(map[string]int)
	for outcome := range results {
		outcomes[outcome]++
	}
	assert.Zero(t, rateLimited.Load(), "distinct client identities must not share one rate window")
	assert.Equal(t, clients, outcomes["complete"], "outcomes: %v", outcomes)
}

func TestPublicSpectatorPublicationDeliversDuringPollHold(t *testing.T) {
	env := newCapacityMirrorEnv(t, 5)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	const pollers = 32
	hold := 1 * time.Second
	var delivered atomic.Int32
	var wg, attached sync.WaitGroup
	wg.Add(pollers)
	attached.Add(pollers)
	for index := range pollers {
		go func(clientIndex int) {
			defer wg.Done()
			// A successful first poll means the client is attached. A failed
			// attempt also releases the wait; it just never delivers.
			var attachedOnce sync.Once
			markAttached := func() { attachedOnce.Do(attached.Done) }
			defer markAttached()
			clientIP := capacitySyntheticClientAddress(clientIndex)
			deadline := time.Now().Add(hold)
			for {
				status, code, err := capacityGetJSON[models.PublicLiveStatus](ctx, env.client, env.baseURL+"/"+constants.PublicLayoutLiveStatusKey, clientIP)
				if err != nil || code != http.StatusOK {
					return
				}
				markAttached()
				for _, record := range status.Tail {
					if strings.Contains(record.RecordBytes, "dataset-publish") {
						delivered.Add(1)
						return
					}
				}
				if !time.Now().Before(deadline) {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(capacityPollInterval):
				}
			}
		}(index)
	}

	attached.Wait()
	var snapshot models.PublicFeedSnapshot
	require.Equal(t, http.StatusOK, env.helper.getJSON("/snapshot?source="+env.sourceID, &snapshot))
	publishRecord := env.helper.makeRecord(6, applyProjectionDefaults(models.NewPublicFeedObject(map[string]string{
		"kind":       "catalog_snapshot",
		"dataset_id": "dataset-publish",
	})))
	batch := env.helper.buildBatch([]models.PublicFeedRecord{publishRecord}, snapshot.FeedChainHash)
	status, ingestResp := sendIngestToHandler(t, env.helper.mirror.IngestHandler(), batch)
	require.Equal(t, http.StatusOK, status)
	require.True(t, ingestResp.Accepted)

	wg.Wait()
	assert.GreaterOrEqual(t, int(delivered.Load()), pollers/2, "polling clients should read the published record during hold")
}

// pollHoldClients is the number of distinct clients held in the poll-hold test.
const pollHoldClients = 1000

func TestPublicSpectatorPollHold_ThousandDistinctClientsSurvive(t *testing.T) {
	env := newCapacityMirrorEnv(t, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const clients = pollHoldClients
	hold := 300 * time.Millisecond
	results := make(chan bool, clients)
	errors := make(chan string, clients)
	var wg sync.WaitGroup
	wg.Add(clients)
	for index := range clients {
		go func(clientIndex int) {
			defer wg.Done()
			survived, status, outcome := env.pollLive(ctx, capacitySyntheticClientAddress(clientIndex), hold)
			results <- survived && status == http.StatusOK
			if !survived || status != http.StatusOK {
				errors <- fmt.Sprintf("status=%d, %s", status, outcome)
			}
		}(index)
	}
	wg.Wait()
	close(results)
	close(errors)

	survived := 0
	for ok := range results {
		if ok {
			survived++
		}
	}
	outcomes := make(map[string]int)
	for outcome := range errors {
		outcomes[outcome]++
	}
	assert.Equal(t, clients, survived, "outcomes: %v", outcomes)
}
