// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes = 64 << 20
	historyLimit     = 500
	maxHistoryPages  = 100
	maxHistoryRounds = 10
	// pollInterval is the pause between one client's poll cycles.
	pollInterval = 200 * time.Millisecond
	modeCold     = "cold"
	modePoll     = "poll"
	pointerPath  = "/pointer.json"
	liveStatus   = "/live/status.json"
)

type options struct {
	target             string
	mode               string
	clients            int
	hold               time.Duration
	requestTimeout     time.Duration
	allowPublic        bool
	syntheticClientIPs bool
	shardIndex         int
	shardCount         int
}

type bootstrapResponse struct {
	Snapshot snapshotResponse `json:"snapshot"`
}

type projectionCursor struct {
	Sequence int64 `json:"sequence"`
}

type historyResponse struct {
	Items   []projectionCursor `json:"items"`
	HasMore bool               `json:"has_more"`
}

type snapshotResponse struct {
	SourceID          string `json:"source_id"`
	HighWaterSequence int64  `json:"high_water_sequence"`
	FeedChainHash     string `json:"feed_chain_hash"`
	BatchCount        int64  `json:"batch_count"`
}

type pointerResponse struct {
	SchemaVersion     string `json:"schema_version"`
	ReadModelSHA256   string `json:"read_model_sha256"`
	HighWaterSequence int64  `json:"high_water_sequence"`
}

type liveStatusResponse struct {
	SchemaVersion string `json:"schema_version"`
}

type requestResult struct {
	Stage   string
	Status  int
	Latency time.Duration
}

type clientResult struct {
	Outcome     string
	Requests    []requestResult
	Established bool
	Survived    bool
}

type latencySummary struct {
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

type summary struct {
	Target             string         `json:"target"`
	Mode               string         `json:"mode"`
	Clients            int            `json:"clients"`
	ShardIndex         int            `json:"shard_index"`
	ShardCount         int            `json:"shard_count"`
	ElapsedSeconds     float64        `json:"elapsed_seconds"`
	Outcomes           map[string]int `json:"outcomes"`
	Statuses           map[string]int `json:"statuses"`
	Latency            latencySummary `json:"latency"`
	ClientsEstablished int            `json:"clients_established"`
	ClientsSurvived    int            `json:"clients_survived"`
}

func main() {
	opts := parseFlags()
	if err := validateOptions(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	result := run(context.Background(), opts)
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("capacity: encode summary: %w", err))
		os.Exit(1)
	}
	fmt.Println(string(encoded))
	if result.Outcomes["complete"] != opts.clients || result.ClientsSurvived != opts.clients {
		os.Exit(1)
	}
}

func parseFlags() options {
	var opts options
	flag.StringVar(&opts.target, "target", "", "public mirror origin")
	flag.StringVar(&opts.mode, "mode", modeCold, "cold or poll")
	flag.IntVar(&opts.clients, "clients", 300, "concurrent client count")
	flag.DurationVar(&opts.hold, "hold", 5*time.Minute, "poll hold duration")
	flag.DurationVar(&opts.requestTimeout, "request-timeout", 30*time.Second, "bounded request timeout")
	flag.BoolVar(&opts.allowPublic, "allow-public", false, "allow a non-loopback target")
	flag.BoolVar(&opts.syntheticClientIPs, "synthetic-client-ips", false, "send distinct test client addresses to a loopback trusted proxy")
	flag.IntVar(&opts.shardIndex, "shard-index", 0, "zero-based shard index for distributed external cold acceptance")
	flag.IntVar(&opts.shardCount, "shard-count", 1, "total shard count for distributed external cold acceptance")
	flag.Parse()
	return opts
}

func validateOptions(opts options) error {
	if opts.target == "" {
		return fmt.Errorf("capacity: --target is required")
	}
	parsed, err := url.Parse(opts.target)
	if err != nil {
		return fmt.Errorf("capacity: parse target: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("capacity: target scheme must be http or https")
	}
	if parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("capacity: target must be a bare origin")
	}
	isLoopback := loopbackHost(parsed.Hostname())
	if !opts.allowPublic && !isLoopback {
		return fmt.Errorf("capacity: public target requires --allow-public")
	}
	if opts.syntheticClientIPs && !isLoopback {
		return fmt.Errorf("capacity: synthetic client addresses require a loopback target")
	}
	if opts.mode != modeCold && opts.mode != modePoll {
		return fmt.Errorf("capacity: --mode must be cold or poll")
	}
	if opts.clients <= 0 || opts.hold <= 0 || opts.requestTimeout <= 0 {
		return fmt.Errorf("capacity: clients and durations must be positive")
	}
	if opts.shardCount <= 0 {
		return fmt.Errorf("capacity: shard-count must be positive")
	}
	if opts.shardIndex < 0 || opts.shardIndex >= opts.shardCount {
		return fmt.Errorf("capacity: shard-index must satisfy 0 <= shard-index < shard-count")
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func syntheticClientAddress(shardIndex, shardCount, clientIndex int) string {
	globalIndex := shardIndex*10_000 + clientIndex
	if shardCount > 1 {
		globalIndex = shardIndex*10_000 + clientIndex
	}
	return net.IPv4(198, 18, byte(globalIndex/254), byte(globalIndex%254+1)).String()
}

func run(ctx context.Context, opts options) summary {
	origin := strings.TrimSuffix(opts.target, "/")
	started := time.Now()
	results := make(chan clientResult, opts.clients)
	var wg sync.WaitGroup
	wg.Add(opts.clients)
	for index := range opts.clients {
		clientIP := ""
		if opts.syntheticClientIPs {
			clientIP = syntheticClientAddress(opts.shardIndex, opts.shardCount, index)
		}
		go func() {
			defer wg.Done()
			if opts.mode == modePoll {
				results <- pollHold(ctx, opts, newClient(opts.requestTimeout), origin, clientIP)
				return
			}
			results <- runColdClient(ctx, opts, origin, clientIP)
		}()
	}
	wg.Wait()
	close(results)

	all := make([]clientResult, 0, opts.clients)
	for result := range results {
		all = append(all, result)
	}
	return summarize(opts, time.Since(started), all)
}

func runColdClient(ctx context.Context, opts options, origin, clientIP string) clientResult {
	client := newClient(opts.requestTimeout)
	bootstrap, requestInfo, err := fetchBootstrap(ctx, client, origin, clientIP)
	result := clientResult{Requests: []requestResult{requestInfo}}
	if err != nil || requestInfo.Status != http.StatusOK {
		result.Outcome = "bootstrap"
		return result
	}
	source := bootstrap.Snapshot.SourceID
	cursor := bootstrap.Snapshot.HighWaterSequence
	for range maxHistoryRounds {
		for range maxHistoryPages {
			page, requestInfo, pageErr := fetchHistory(ctx, client, origin, source, cursor, clientIP)
			result.Requests = append(result.Requests, requestInfo)
			if pageErr != nil || requestInfo.Status != http.StatusOK {
				result.Outcome = "history"
				return result
			}
			if len(page.Items) > 0 {
				cursor = page.Items[len(page.Items)-1].Sequence
			}
			if !page.HasMore {
				break
			}
			if len(page.Items) == 0 {
				result.Outcome = "history_stalled"
				return result
			}
		}
		snapshot, requestInfo, snapshotErr := fetchSnapshot(ctx, client, origin, source, clientIP)
		result.Requests = append(result.Requests, requestInfo)
		if snapshotErr != nil || requestInfo.Status != http.StatusOK {
			result.Outcome = "snapshot"
			return result
		}
		if snapshot.HighWaterSequence == cursor {
			pollResult := pollHold(ctx, opts, client, origin, clientIP)
			result.Requests = append(result.Requests, pollResult.Requests...)
			result.Established = pollResult.Established
			result.Survived = pollResult.Survived
			result.Outcome = pollResult.Outcome
			return result
		}
	}
	result.Outcome = "sequence_divergence"
	return result
}

// pollHold polls pointer.json and live/status.json until opts.hold has elapsed.
// The client is established once its first cycle succeeds, and survives when
// every cycle through the hold succeeds. The first failed request ends the
// client with an outcome naming the endpoint.
func pollHold(ctx context.Context, opts options, client *http.Client, origin, clientIP string) clientResult {
	deadline := time.Now().Add(opts.hold)
	var result clientResult
	for cycle := 0; ; cycle++ {
		var pointer pointerResponse
		pointerRequest, pointerErr := getJSON(ctx, client, endpoint(origin, pointerPath, nil), "pointer", clientIP, &pointer)
		result.Requests = append(result.Requests, pointerRequest)
		if pointerErr != nil || pointerRequest.Status != http.StatusOK {
			result.Outcome = "pointer"
			return result
		}
		var status liveStatusResponse
		statusRequest, statusErr := getJSON(ctx, client, endpoint(origin, liveStatus, nil), "live_status", clientIP, &status)
		result.Requests = append(result.Requests, statusRequest)
		if statusErr != nil || statusRequest.Status != http.StatusOK {
			result.Outcome = "live_status"
			return result
		}
		if cycle == 0 {
			result.Established = true
		}
		if !time.Now().Before(deadline) {
			result.Outcome = "complete"
			result.Survived = true
			return result
		}
		select {
		case <-ctx.Done():
			result.Outcome = "poll_cancelled"
			return result
		case <-time.After(pollInterval):
		}
	}
}

func fetchBootstrap(ctx context.Context, client *http.Client, origin, clientIP string) (bootstrapResponse, requestResult, error) {
	var value bootstrapResponse
	result, err := getJSON(ctx, client, endpoint(origin, "/bootstrap", nil), "bootstrap", clientIP, &value)
	return value, result, err
}

func fetchHistory(ctx context.Context, client *http.Client, origin, source string, cursor int64, clientIP string) (historyResponse, requestResult, error) {
	var value historyResponse
	query := url.Values{"source": {source}, "cursor": {strconv.FormatInt(cursor, 10)}, "limit": {strconv.Itoa(historyLimit)}}
	result, err := getJSON(ctx, client, endpoint(origin, "/history", query), "history", clientIP, &value)
	return value, result, err
}

func fetchSnapshot(ctx context.Context, client *http.Client, origin, source, clientIP string) (snapshotResponse, requestResult, error) {
	var value snapshotResponse
	result, err := getJSON(ctx, client, endpoint(origin, "/snapshot", url.Values{"source": {source}}), "snapshot", clientIP, &value)
	return value, result, err
}

type mirrorResponse interface {
	bootstrapResponse | historyResponse | snapshotResponse | pointerResponse | liveStatusResponse
}

func getJSON[T mirrorResponse](ctx context.Context, client *http.Client, address, stage, clientIP string, value *T) (requestResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return requestResult{Stage: stage}, fmt.Errorf("%s request: %w", stage, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ztd-public-capacity/1.0")
	if clientIP != "" {
		request.Header.Set("CF-Connecting-IP", clientIP)
	}
	started := time.Now()
	response, err := client.Do(request)
	latency := time.Since(started)
	if err != nil {
		return requestResult{Stage: stage, Latency: latency}, fmt.Errorf("%s request: %w", stage, err)
	}
	defer response.Body.Close()
	result := requestResult{Stage: stage, Status: response.StatusCode, Latency: latency}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return result, nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(value); err != nil {
		return result, fmt.Errorf("%s response: %w", stage, err)
	}
	return result, nil
}

func endpoint(origin, path string, query url.Values) string {
	address := strings.TrimSuffix(origin, "/") + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	return address
}

func newClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 4
	transport.MaxIdleConnsPerHost = 4
	transport.IdleConnTimeout = timeout
	transport.ResponseHeaderTimeout = timeout
	transport.TLSHandshakeTimeout = timeout
	return &http.Client{Transport: transport}
}

func summarize(opts options, elapsed time.Duration, results []clientResult) summary {
	outcomes := make(map[string]int)
	statuses := make(map[string]int)
	latencies := make([]time.Duration, 0)
	established := 0
	survived := 0
	for _, result := range results {
		outcomes[result.Outcome]++
		if result.Established {
			established++
		}
		if result.Survived {
			survived++
		}
		for _, request := range result.Requests {
			statuses[request.Stage+":"+strconv.Itoa(request.Status)]++
			latencies = append(latencies, request.Latency)
		}
	}
	return summary{
		Target:             opts.target,
		Mode:               opts.mode,
		Clients:            opts.clients,
		ShardIndex:         opts.shardIndex,
		ShardCount:         opts.shardCount,
		ElapsedSeconds:     elapsed.Seconds(),
		Outcomes:           outcomes,
		Statuses:           statuses,
		Latency:            summarizeLatency(latencies),
		ClientsEstablished: established,
		ClientsSurvived:    survived,
	}
}

func summarizeLatency(values []time.Duration) latencySummary {
	if len(values) == 0 {
		return latencySummary{}
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	milliseconds := func(index int) float64 { return float64(values[index]) / float64(time.Millisecond) }
	percentile := func(value float64) float64 {
		index := int(value * float64(len(values)))
		if index >= len(values) {
			index = len(values) - 1
		}
		return milliseconds(index)
	}
	middle := len(values) / 2
	median := milliseconds(middle)
	if len(values)%2 == 0 {
		median = (milliseconds(middle-1) + milliseconds(middle)) / 2
	}
	return latencySummary{P50: median, P95: percentile(0.95), P99: percentile(0.99), Max: milliseconds(len(values) - 1)}
}
