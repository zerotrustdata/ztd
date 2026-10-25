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
	"slices"
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

// releaseSmokeApprovalDecision is the owner's answer to one pending transaction.
type releaseSmokeApprovalDecision int

const (
	// releaseSmokeLeavePending answers nothing yet; the transaction is offered
	// again on the next reconciliation.
	releaseSmokeLeavePending releaseSmokeApprovalDecision = iota
	// releaseSmokeApprove runs `ztd auth approve --cli`.
	releaseSmokeApprove
	// releaseSmokeDeny runs `ztd auth approve --deny`.
	releaseSmokeDeny
)

// releaseSmokeCLIApprovalResponder follows the owner CLI's live approval
// stream and runs the real `ztd auth approve` command for pending work.
type releaseSmokeCLIApprovalResponder struct {
	mu      sync.Mutex
	results []releaseSmokeApprovalResult
	seen    map[string]struct{}
	// decide, when set, answers each pending transaction; without it every
	// transaction is approved.
	decide   func(models.SuspendedTxResponse) releaseSmokeApprovalDecision
	r        *releaseSmokeRun
	baseURL  string
	wake     chan struct{}
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

// Decide hands every later answer to decide, then reconciles the pending list
// so transactions already waiting are answered by it too.
func (a *releaseSmokeCLIApprovalResponder) Decide(decide func(models.SuspendedTxResponse) releaseSmokeApprovalDecision) {
	a.mu.Lock()
	a.decide = decide
	a.mu.Unlock()
	a.Reconcile()
}

// Reconcile asks for one more pass over the pending list, for a decider whose
// answers changed without an approvals event.
func (a *releaseSmokeCLIApprovalResponder) Reconcile() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// claim answers transaction once: it returns the decision for a transaction not
// yet answered and marks it answered, unless the decision leaves it pending.
func (a *releaseSmokeCLIApprovalResponder) claim(transaction models.SuspendedTxResponse) releaseSmokeApprovalDecision {
	a.mu.Lock()
	defer a.mu.Unlock()
	hash := transaction.TransactionHash
	if _, exists := a.seen[hash]; exists {
		return releaseSmokeLeavePending
	}
	decision := releaseSmokeApprove
	if a.decide != nil {
		decision = a.decide(transaction)
	}
	if decision != releaseSmokeLeavePending {
		a.seen[hash] = struct{}{}
	}
	return decision
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
		r:       r,
		baseURL: baseURL,
		wake:    wake,
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
		responder.Reconcile()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				// Approving a command can suspend its Tribunal review's
				// governed inference. Keep the stream responsive so that
				// nested transaction can receive its own CLI approval.
				workers.Add(1)
				go func() {
					defer workers.Done()
					responder.reconcile(ctx)
				}()
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

// Pending lists the owner's pending transactions, oldest first.
func (a *releaseSmokeCLIApprovalResponder) Pending(ctx context.Context) ([]models.SuspendedTxResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+constants.APIPaths.ApprovalsCLIList, nil)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	if a.r.identity.CLISessionID != "" {
		request.Header.Set(constants.HeaderCLISessionID, a.r.identity.CLISessionID)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read pending approvals: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close pending approvals: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list pending approvals returned HTTP %d: %s", response.StatusCode, body)
	}
	var pending models.SuspendedTransactionsResponse
	if err := json.Unmarshal(body, &pending); err != nil {
		return nil, fmt.Errorf("decode pending approvals: %w", err)
	}
	slices.SortStableFunc(pending.Transactions, func(x, y models.SuspendedTxResponse) int { return x.CreatedAt.Compare(y.CreatedAt) })
	return pending.Transactions, nil
}

// reconcile answers the pending transactions in the order they were created.
func (a *releaseSmokeCLIApprovalResponder) reconcile(ctx context.Context) {
	pending, err := a.Pending(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("approvals responder failed to list pending approvals", "error", err)
			a.record(releaseSmokeApprovalResult{err: err})
		}
		return
	}
	if len(pending) > 0 {
		slog.Info("approvals responder reconcile: pending transactions", "count", len(pending))
	}
	for _, transaction := range pending {
		if transaction.TransactionHash == "" {
			continue
		}
		switch a.claim(transaction) {
		case releaseSmokeApprove:
			a.answer(ctx, transaction.TransactionHash, "--cli")
		case releaseSmokeDeny:
			a.answer(ctx, transaction.TransactionHash, "--deny")
		}
	}
}

// answer runs `ztd auth approve <flag> <hash>` as the owner CLI.
func (a *releaseSmokeCLIApprovalResponder) answer(ctx context.Context, hash, flag string) {
	r := a.r
	approvalCtx, cancel := context.WithTimeout(ctx, releaseSmokeApprovalCommandTimeout)
	defer cancel()
	args := []string{
		"-e", fmt.Sprintf("127.0.0.1:%d", r.ports.http),
		"-p", strconv.Itoa(r.ports.https),
		"auth", "approve", flag, hash,
	}
	cmd := exec.CommandContext(approvalCtx, r.opts.ZtdBin, args...)
	cmd.Dir = r.gwRoot
	cmd.Env = append(cliconfig.WithoutUserEnv(os.Environ()), r.childEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	slog.Info("answering transaction via CLI", "hash", hash, "flag", flag)
	// The exit status is authoritative: a command that prints the success marker
	// and then exits non-zero has failed, so it must not be counted as answered.
	err := cmd.Run()
	if err != nil {
		slog.Error("CLI answer failed", "hash", hash, "flag", flag, "error", err, "stdout", stdout.String(), "stderr", stderr.String())
	} else {
		slog.Info("CLI answer succeeded", "hash", hash, "flag", flag, "stdout", strings.TrimSpace(stdout.String()))
	}
	a.record(releaseSmokeApprovalResult{
		transactionHash: hash,
		stdout:          stdout.String(),
		stderr:          stderr.String(),
		err:             err,
	})
}
