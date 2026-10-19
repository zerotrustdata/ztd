// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package testcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

const (
	releaseSmokeApprovalConnectTimeout = 30 * time.Second
	releaseSmokeApprovalCommandTimeout = 3 * time.Minute
)

type releaseSmokeApprovalResult struct {
	transactionHash string
	stdout          string
	stderr          string
	err             error
}

// releaseSmokeCLIApprovalResponder follows the owner CLI's live approval
// stream and runs the real `ztd auth approve --cli` command for pending work.
type releaseSmokeCLIApprovalResponder struct {
	mu       sync.Mutex
	results  []releaseSmokeApprovalResult
	seen     map[string]struct{}
	cancel   context.CancelFunc
	client   *http.Client
	workers  *sync.WaitGroup
	stopOnce sync.Once
}

func (a *releaseSmokeCLIApprovalResponder) Stop() {
	a.stopOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		if a.client != nil {
			a.client.CloseIdleConnections()
		}
		if a.workers != nil {
			a.workers.Wait()
		}
	})
}

func (a *releaseSmokeCLIApprovalResponder) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, result := range a.results {
		if result.err == nil {
			count++
		}
	}
	return count
}

func (a *releaseSmokeCLIApprovalResponder) Errors() []error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var errs []error
	for _, result := range a.results {
		if result.err != nil {
			errs = append(errs, fmt.Errorf("approve transaction %s: %w", result.transactionHash, result.err))
		}
	}
	return errs
}

func (a *releaseSmokeCLIApprovalResponder) record(result releaseSmokeApprovalResult) {
	a.mu.Lock()
	a.results = append(a.results, result)
	a.mu.Unlock()
}

func (a *releaseSmokeCLIApprovalResponder) claim(hash string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.seen[hash]; exists {
		return false
	}
	a.seen[hash] = struct{}{}
	return true
}

// startReleaseSmokeCLIApprovalResponder starts one owner-session SSE stream,
// waits for its initial connection, then reconciles the pending list once and
// after each transactions-changed event. Cleanup cancels and joins every worker.
func startReleaseSmokeCLIApprovalResponder(t *testing.T, r *releaseSmokeRun) *releaseSmokeCLIApprovalResponder {
	t.Helper()
	cfg, err := cliconfig.Load(r.gwRoot)
	require.NoError(t, err)
	fileSvc, err := shared.NewFileSvc(r.gwRoot, slog.Default())
	require.NoError(t, err)
	cfg.Keyring = r.agent.Keyring(fileSvc)
	httpClient, err := auth.BuildMTLSClient(fileSvc, cfg, 0)
	require.NoError(t, err)
	baseURL := fmt.Sprintf("https://127.0.0.1:%d", r.ports.https)
	stream := sse.NewClient(baseURL+constants.APIPaths.SSEStream+"?since_id=0", httpClient)
	stream.SetHeader(constants.HeaderCLISessionID, r.identity.CLISessionID)

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	var readyOnce sync.Once
	wake := make(chan struct{}, 1)
	var workers sync.WaitGroup
	responder := &releaseSmokeCLIApprovalResponder{
		seen:    make(map[string]struct{}),
		cancel:  cancel,
		client:  httpClient,
		workers: &workers,
	}
	workers.Add(2)
	go func() {
		defer workers.Done()
		stream.SetOnConnect(func() { readyOnce.Do(func() { close(ready) }) })
		stream.SetOnDisconnect(func(err error) {
			if ctx.Err() == nil {
				responder.record(releaseSmokeApprovalResult{err: fmt.Errorf("owner SSE stream disconnected: %w", err)})
			}
		})
		stream.Run(ctx, func(eventType, data string) {
			event, ok := sse.DecodeEvent(eventType, data)
			if !ok || event.Type != string(constants.EventPlatformApprovalsChanged) {
				return
			}
			var payload models.ApprovalsChangedPayload
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				slog.Warn("approvals responder failed to unmarshal payload", "error", err, "data", string(event.Data))
				return
			}
			if payload.Subject != models.ApprovalsChangedTransactions && payload.Subject != models.ApprovalsChangedEnrollments {
				return
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		})
	}()
	go func() {
		defer workers.Done()
		select {
		case <-ctx.Done():
			return
		case <-ready:
		}
		responder.reconcile(ctx, r, httpClient, baseURL)
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				responder.reconcile(ctx, r, httpClient, baseURL)
			}
		}
	}()

	t.Cleanup(func() {
		responder.Stop()
	})
	select {
	case <-ready:
		// The reconciliation goroutine also waits on this one-shot signal.
	case <-time.After(releaseSmokeApprovalConnectTimeout):
		responder.Stop()
		t.Fatalf("owner CLI approval SSE did not connect within %s", releaseSmokeApprovalConnectTimeout)
	}
	return responder
}

func (a *releaseSmokeCLIApprovalResponder) reconcile(ctx context.Context, r *releaseSmokeRun, client *http.Client, baseURL string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+constants.APIPaths.ApprovalsCLIList, nil)
	if err != nil {
		slog.Error("failed to create pending approvals request", "error", err)
		a.record(releaseSmokeApprovalResult{err: err})
		return
	}
	if r.identity.CLISessionID != "" {
		request.Header.Set(constants.HeaderCLISessionID, r.identity.CLISessionID)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("failed to do pending approvals request", "error", err)
			a.record(releaseSmokeApprovalResult{err: fmt.Errorf("list pending approvals: %w", err)})
		}
		return
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		slog.Error("failed to read pending approvals response body", "error", readErr)
		a.record(releaseSmokeApprovalResult{err: fmt.Errorf("read pending approvals: %w", readErr)})
		return
	}
	if closeErr != nil {
		slog.Error("failed to close pending approvals response body", "error", closeErr)
		a.record(releaseSmokeApprovalResult{err: fmt.Errorf("close pending approvals: %w", closeErr)})
		return
	}
	if response.StatusCode != http.StatusOK {
		slog.Error("list pending approvals returned non-200 status", "status", response.StatusCode, "body", string(body))
		a.record(releaseSmokeApprovalResult{err: fmt.Errorf("list pending approvals returned HTTP %d: %s", response.StatusCode, body)})
		return
	}
	var pending models.SuspendedTransactionsResponse
	if err := json.Unmarshal(body, &pending); err != nil {
		slog.Error("failed to unmarshal pending approvals", "error", err, "body", string(body))
		a.record(releaseSmokeApprovalResult{err: fmt.Errorf("decode pending approvals: %w", err)})
		return
	}
	if len(pending.Transactions) > 0 {
		slog.Info("approvals responder reconcile: pending transactions", "count", len(pending.Transactions))
	}
	for _, transaction := range pending.Transactions {
		if transaction.TransactionHash == "" || !a.claim(transaction.TransactionHash) {
			continue
		}
		a.approve(ctx, r, transaction.TransactionHash)
	}
}

func (a *releaseSmokeCLIApprovalResponder) approve(ctx context.Context, r *releaseSmokeRun, hash string) {
	approvalCtx, cancel := context.WithTimeout(ctx, releaseSmokeApprovalCommandTimeout)
	defer cancel()
	args := []string{
		"-e", fmt.Sprintf("127.0.0.1:%d", r.ports.http),
		"-p", strconv.Itoa(r.ports.https),
		"auth", "approve", "--cli", hash,
	}
	cmd := exec.CommandContext(approvalCtx, r.opts.ZtdBin, args...)
	cmd.Dir = r.gwRoot
	cmd.Env = append(cliconfig.WithoutUserEnv(os.Environ()), r.childEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	slog.Info("approving transaction via CLI", "hash", hash)
	err := cmd.Run()
	if err != nil {
		slog.Error("CLI approval failed", "hash", hash, "error", err, "stdout", stdout.String(), "stderr", stderr.String())
	} else {
		slog.Info("CLI approval succeeded", "hash", hash, "stdout", strings.TrimSpace(stdout.String()))
	}
	a.record(releaseSmokeApprovalResult{
		transactionHash: hash,
		stdout:          stdout.String(),
		stderr:          stderr.String(),
		err:             err,
	})
}
