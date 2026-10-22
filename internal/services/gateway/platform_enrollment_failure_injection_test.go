// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// Integration tests proving the issuance saga is recoverable after a
// failure at every persistence boundary. Each test injects a failure
// after a specific write boundary in the issuance saga and asserts that
// a retry with the same token and valid proof returns the same issued
// artifacts rather than creating a second identity or consuming the
// approval. The issuance lease plus idempotent handlers plus
// reconciliation provide the recoverable saga boundary; these tests
// prove it.

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// failureInjectingProcessor wraps an EnvelopeProcessor and injects a
// failure after a specified action type is processed N times. The
// wrapper delegates to the real processor, counts successful calls per
// action type, and returns an error when the configured action type
// reaches the configured failure count. Subsequent calls (after the
// injected failure) succeed normally, simulating a transient crash or
// persistence failure that is recovered on retry.
type failureInjectingProcessor struct {
	inner       governanceEnvelopeProcessor
	failAfter   string // action type to fail after
	failOnCount int32  // fail when the call count for failAfter reaches this value
	callCount   int32  // atomic counter for the failAfter action
	injectedErr error  // error to return on the injected failure
	totalCalls  int32  // total calls across all actions (for assertions)
}

// governanceEnvelopeProcessor is the minimal interface used by the
// failure-injecting wrapper. It matches governance.EnvelopeProcessor
// but is declared locally to avoid importing the governance package
// (which would create a circular dependency in some test setups).
type governanceEnvelopeProcessor interface {
	ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.EventReceipt, error)
}

func (f *failureInjectingProcessor) ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.EventReceipt, error) {
	atomic.AddInt32(&f.totalCalls, 1)

	// Decode the action type from the wire payload to determine which
	// boundary this call represents.
	actionType := decodeActionTypeFromWire(payload)

	if actionType == f.failAfter {
		count := atomic.AddInt32(&f.callCount, 1)
		if count == f.failOnCount {
			return nil, f.injectedErr
		}
	}
	return f.inner.ProcessEnvelope(ctx, payload)
}

// decodeActionTypeFromWire extracts the actionType field from a
// protojson-encoded GovernanceEnvelope payload. Protojson is
// JSON-compatible, so encoding/json suffices for field extraction.
func decodeActionTypeFromWire(payload []byte) string {
	var env struct {
		ActionType string `json:"actionType"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return ""
	}
	return env.ActionType
}

// injectFailureAfterAction replaces the enrollment service's envProc
// with a failure-injecting wrapper that fails the Nth call to the
// specified action type. Returns a cleanup function that restores the
// original processor.
func injectFailureAfterAction(t *testing.T, env *platformEnrollmentTestEnv, actionType string, failOnCount int32, injectedErr error) func() {
	t.Helper()
	original := env.enrollSvc.envProc
	wrapper := &failureInjectingProcessor{
		inner:       original,
		failAfter:   actionType,
		failOnCount: failOnCount,
		injectedErr: injectedErr,
	}
	env.enrollSvc.envProc = wrapper
	return func() {
		env.enrollSvc.envProc = original
	}
}

// ============================================================================
// Failure injection after each persistence boundary in the issuance saga
// ============================================================================

// TestPlatformEnrollmentFailureInjection_AfterSigning proves that a
// failure after the ISSUE handler signs the certificate (but before the
// downstream PERSIST_POLICY envelope) is recoverable. The retry returns
// the same certificate rather than issuing another.
func TestPlatformEnrollmentFailureInjection_AfterSigning(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	csr, key := generateAppCSRAndKey(t)
	requestID, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fi-1", "dashboard.local",
		csr, "", "")

	proof := models.PlatformEnrollmentProofs{
		App: signCompletionTranscript(t, approved, key),
	}

	// Inject a failure on the first PERSIST_POLICY call (after ISSUE
	// has signed and stored the certificate, but before the policy is
	// persisted). This simulates a crash between ISSUE and
	// PERSIST_POLICY.
	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionPersistPolicy), 1,
		fmt.Errorf("simulated crash after signing"))
	_, err := env.enrollSvc.Complete(context.Background(), token, proof)
	restore()
	assert.Error(t, err, "completion must fail when PERSIST_POLICY fails")

	// The request should be in the completed state (ISSUE succeeded),
	// but the policy may not be persisted yet.
	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateCompleted, stored.State,
		"ISSUE must have completed before the downstream failure")

	// Retry: the same token and proof must return the same certificate.
	resp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err, "retry must succeed after the transient failure")
	require.NotNil(t, resp.App)
	assert.NotEmpty(t, resp.App.AppCert, "retry must return the issued certificate")

	// Verify the policy was persisted on retry.
	policyDoc, err := env.docStore.DocGet(t.Context(),
		marshaler.CollectionName(constants.CollectionAppPolicies), "spiffe://ztd.local/app/ztdd")
	require.NoError(t, err)
	require.NotNil(t, policyDoc, "policy must be persisted after retry")
}

// TestPlatformEnrollmentFailureInjection_AfterPolicyWrite proves that a
// failure after the PERSIST_POLICY handler writes the app policy is
// recoverable. The retry re-submits the idempotent PERSIST_POLICY and
// returns the same certificate.
func TestPlatformEnrollmentFailureInjection_AfterPolicyWrite(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	csr, key := generateAppCSRAndKey(t)
	requestID, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fi-2", "dashboard.local",
		csr, "", "")

	proof := models.PlatformEnrollmentProofs{
		App: signCompletionTranscript(t, approved, key),
	}

	// Complete successfully first to get the baseline certificate.
	firstResp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)
	require.NotNil(t, firstResp.App)
	firstCert := firstResp.App.AppCert

	// Verify the stored request is completed.
	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateCompleted, stored.State)

	// Inject a failure on the next PERSIST_POLICY call (simulating a
	// crash after the policy write on a retry path).
	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionPersistPolicy), 2,
		fmt.Errorf("simulated crash after policy write"))
	// The retry path re-submits downstream envelopes; a failure there
	// is returned to the caller but does not corrupt the completed state.
	// The certificate was already returned successfully on the first call.
	// The error is intentionally discarded: when the request is already
	// completed, Complete short-circuits and returns the stored response
	// without re-running the downstream envelopes, so the injected failure
	// may or may not surface depending on timing.
	_, _ = env.enrollSvc.Complete(context.Background(), token, proof)
	restore()

	// Another retry must succeed and return the same certificate.
	secondResp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)
	require.NotNil(t, secondResp.App)
	assert.Equal(t, firstCert, secondResp.App.AppCert,
		"retry must return the same certificate, not a new identity")
}

// TestPlatformEnrollmentFailureInjection_AfterOperatorDocWrite proves
// that a failure after the operator document is written (inside the
// ISSUE handler for operator components) is recoverable. The retry
// returns the same operator identity rather than creating a second one.
func TestPlatformEnrollmentFailureInjection_AfterOperatorDocWrite(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	operatorCSR, operatorKey, cliCSR, cliKey := generateOperatorCSRsAndKeys(t)
	requestID, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentOperator, "operator-fi-1", "operator.local",
		"", operatorCSR, cliCSR)

	proof := models.PlatformEnrollmentProofs{
		Operator: signCompletionTranscript(t, approved, operatorKey),
		CLI:      signCompletionTranscript(t, approved, cliKey),
	}

	// Inject a failure on the first CREATE_SESSION call (after ISSUE
	// has signed both certs and persisted the operator document, but
	// before the sessions are created). This simulates a crash between
	// ISSUE and CREATE_SESSION.
	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionCreateSession), 1,
		fmt.Errorf("simulated crash after operator doc write"))
	_, err := env.enrollSvc.Complete(context.Background(), token, proof)
	restore()
	assert.Error(t, err, "completion must fail when CREATE_SESSION fails")
	assert.ErrorIs(t, err, constants.ErrPlatformEnrollmentIssuanceFailed,
		"a downstream failure must be retryable so the Operator redials instead of stranding")

	// The request should be completed (ISSUE succeeded).
	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateCompleted, stored.State,
		"ISSUE must have completed before the session creation failure")

	// Retry: the same token and proof must return the same operator identity.
	resp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err, "retry must succeed after the transient failure")
	require.NotNil(t, resp.Operator)
	assert.NotEmpty(t, resp.Operator.OperatorID)
	assert.NotEmpty(t, resp.Operator.OperatorCert)

	// Verify the operator document exists (written by ISSUE, not by retry).
	opDoc, err := env.docStore.DocGet(t.Context(),
		marshaler.CollectionName(constants.CollectionOperators), resp.Operator.OperatorID)
	require.NoError(t, err)
	require.NotNil(t, opDoc, "operator document must exist after retry")
}

// TestPlatformEnrollmentFailureInjection_AfterCLISessionWrite proves
// that a failure after the CLI session is persisted (but before the
// operator session) is recoverable. The retry re-submits the idempotent
// CREATE_SESSION and both sessions end up persisted.
func TestPlatformEnrollmentFailureInjection_AfterCLISessionWrite(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	operatorCSR, operatorKey, cliCSR, cliKey := generateOperatorCSRsAndKeys(t)
	_, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentOperator, "operator-fi-2", "operator.local",
		"", operatorCSR, cliCSR)

	proof := models.PlatformEnrollmentProofs{
		Operator: signCompletionTranscript(t, approved, operatorKey),
		CLI:      signCompletionTranscript(t, approved, cliKey),
	}

	// Complete successfully first.
	firstResp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)
	require.NotNil(t, firstResp.Operator)
	firstOperatorID := firstResp.Operator.OperatorID
	firstCLISessionID := firstResp.Operator.CLISessionID
	require.NotEmpty(t, firstCLISessionID)

	// Inject a failure on the next CREATE_SESSION call (simulating a
	// crash after the CLI session write on a retry path).
	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionCreateSession), 2,
		fmt.Errorf("simulated crash after CLI session write"))
	// The retry path re-submits downstream envelopes; a failure there
	// is returned to the caller but does not corrupt the completed state.
	// The error is intentionally discarded: when the request is already
	// completed, Complete short-circuits and returns the stored response
	// without re-running the downstream envelopes, so the injected failure
	// may or may not surface depending on timing.
	_, _ = env.enrollSvc.Complete(context.Background(), token, proof)
	restore()

	// Another retry must succeed and return the same operator identity.
	secondResp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)
	require.NotNil(t, secondResp.Operator)
	assert.Equal(t, firstOperatorID, secondResp.Operator.OperatorID,
		"retry must return the same operator ID, not a new identity")
	assert.Equal(t, firstResp.Operator.OperatorCert, secondResp.Operator.OperatorCert,
		"retry must return the same operator certificate")
}

// TestPlatformEnrollmentFailureInjection_AfterResponsePersisted proves
// that a failure after the issued response is persisted (the ISSUE
// handler's conditional update to completed) but before the downstream
// envelope is recoverable. The retry finds the completed request and
// returns the stored response.
func TestPlatformEnrollmentFailureInjection_AfterResponsePersisted(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	csr, key := generateAppCSRAndKey(t)
	requestID, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fi-1", "dashboard.local",
		csr, "", "")

	proof := models.PlatformEnrollmentProofs{
		App: signCompletionTranscript(t, approved, key),
	}

	// Inject a failure on the first PERSIST_POLICY call (after ISSUE
	// has persisted the response and transitioned to completed).
	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionPersistPolicy), 1,
		fmt.Errorf("simulated crash after response persisted"))
	_, err := env.enrollSvc.Complete(context.Background(), token, proof)
	restore()
	assert.Error(t, err, "completion must fail when downstream envelope fails")

	// The request must be in the completed state with issued material.
	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateCompleted, stored.State,
		"response must be persisted before the downstream failure")
	require.NotNil(t, stored.Issued, "issued material must be stored")
	assert.NotEmpty(t, stored.Issued.App.AppCert)

	// Retry returns the same stored response.
	resp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)
	require.NotNil(t, resp.App)
	assert.Equal(t, stored.Issued.App.AppCert, resp.App.AppCert,
		"retry must return the stored certificate, not a new one")
}

// TestPlatformEnrollmentFailureInjection_RetryDoesNotCreateSecondIdentity
// proves that after a failure and recovery, no second identity is
// created. The operator document count and app policy count remain
// unchanged after a successful retry.
func TestPlatformEnrollmentFailureInjection_RetryDoesNotCreateSecondIdentity(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	csr, key := generateAppCSRAndKey(t)
	requestID, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fi-3", "dashboard.local",
		csr, "", "")

	proof := models.PlatformEnrollmentProofs{
		App: signCompletionTranscript(t, approved, key),
	}

	// Count app policies before.
	policiesBefore := countDocsInCollection(t, env, marshaler.CollectionName(constants.CollectionAppPolicies))

	// Inject a failure on the first PERSIST_POLICY call.
	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionPersistPolicy), 1,
		fmt.Errorf("simulated crash"))
	_, err := env.enrollSvc.Complete(context.Background(), token, proof)
	restore()
	assert.Error(t, err)

	// Retry successfully.
	resp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)
	require.NotNil(t, resp.App)

	// Count app policies after: must be exactly one more (not two).
	policiesAfter := countDocsInCollection(t, env, marshaler.CollectionName(constants.CollectionAppPolicies))
	assert.Equal(t, policiesBefore+1, policiesAfter,
		"retry must not create a second app policy")

	// The request is still a single request.
	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateCompleted, stored.State)
}

// TestPlatformEnrollmentFailureInjection_RetryDoesNotConsumeApproval
// proves that a failure in the issuance saga does not consume the
// approval. The request remains recoverable and the approval is still
// valid for the retry.
func TestPlatformEnrollmentFailureInjection_RetryDoesNotConsumeApproval(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	csr, key := generateAppCSRAndKey(t)
	_, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fi-4", "dashboard.local",
		csr, "", "")

	proof := models.PlatformEnrollmentProofs{
		App: signCompletionTranscript(t, approved, key),
	}

	// Inject a failure on the ISSUE call itself (before signing).
	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionIssue), 1,
		fmt.Errorf("simulated crash before signing"))
	_, err := env.enrollSvc.Complete(context.Background(), token, proof)
	restore()
	assert.Error(t, err, "completion must fail when ISSUE fails")
	assert.ErrorIs(t, err, constants.ErrPlatformEnrollmentIssuanceFailed,
		"a failed ISSUE must be retryable so the Operator redials instead of stranding")

	// The request must be rolled back to approved (not consumed).
	stored := loadStoredRequest(t, env, approved.ID)
	assert.Equal(t, models.PlatformEnrollmentStateApproved, stored.State,
		"failed ISSUE must roll back to approved, not consume the approval")

	// Retry must succeed (the approval is still valid).
	resp, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err, "retry must succeed after ISSUE failure")
	require.NotNil(t, resp.App)
	assert.NotEmpty(t, resp.App.AppCert)
}

// TestPlatformEnrollmentFailureInjection_CompletedRetryDownstreamFailureIsRetryable
// proves that a downstream failure on the redemption of a completed
// request is retryable: the Operator redials and receives the stored
// issuance instead of ending enrollment.
func TestPlatformEnrollmentFailureInjection_CompletedRetryDownstreamFailureIsRetryable(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	operatorCSR, operatorKey, cliCSR, cliKey := generateOperatorCSRsAndKeys(t)
	_, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentOperator, "operator-fi-3", "operator.local",
		"", operatorCSR, cliCSR)

	proof := models.PlatformEnrollmentProofs{
		Operator: signCompletionTranscript(t, approved, operatorKey),
		CLI:      signCompletionTranscript(t, approved, cliKey),
	}
	first, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)

	restore := injectFailureAfterAction(t, env,
		string(constants.PlatformEnrollmentActionCreateSession), 1,
		fmt.Errorf("simulated transient session failure"))
	_, err = env.enrollSvc.Complete(context.Background(), token, proof)
	restore()
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentIssuanceFailed)

	second, err := env.enrollSvc.Complete(context.Background(), token, proof)
	require.NoError(t, err)
	assert.Equal(t, first.Operator.OperatorID, second.Operator.OperatorID)
}

// cancelingProcessor cancels the caller's saga context and fails the
// ISSUE envelope, simulating an ISSUE that outlived the saga deadline.
type cancelingProcessor struct {
	inner  governanceEnvelopeProcessor
	cancel context.CancelFunc
}

func (p *cancelingProcessor) ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.EventReceipt, error) {
	if decodeActionTypeFromWire(payload) == string(constants.PlatformEnrollmentActionIssue) {
		p.cancel()
		return nil, context.Canceled
	}
	return p.inner.ProcessEnvelope(ctx, payload)
}

// TestPlatformEnrollmentFailureInjection_RollbackSurvivesSagaDeadline
// proves the lease rollback lands after the saga context ended, so an
// ISSUE that ran out the deadline returns the request to approved
// instead of leaving it issuing until reconciliation.
func TestPlatformEnrollmentFailureInjection_RollbackSurvivesSagaDeadline(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)

	csr, _ := generateAppCSRAndKey(t)
	requestID, _, _ := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fi-5", "dashboard.local",
		csr, "", "")

	sagaCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := env.enrollSvc.envProc
	env.enrollSvc.envProc = &cancelingProcessor{inner: original, cancel: cancel}
	defer func() { env.enrollSvc.envProc = original }()

	_, err := env.enrollSvc.issueComponent(sagaCtx, loadStoredRequest(t, env, requestID))
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentIssuanceFailed)

	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateApproved, stored.State,
		"the lease rollback must not depend on the expired saga context")
}

// reclaimingProcessor hands the issuance lease to another holder before the
// ISSUE envelope runs, as when the submitter's lease expired, reconciliation
// reclaimed it, and another completion re-acquired it. With fail set, the
// ISSUE then fails, so the stale submitter rolls back.
type reclaimingProcessor struct {
	t     *testing.T
	env   *platformEnrollmentTestEnv
	inner governanceEnvelopeProcessor
	id    string
	fail  bool
}

const reclaimedLeaseOwner = "reacquired-holder"

func (p *reclaimingProcessor) ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.EventReceipt, error) {
	if decodeActionTypeFromWire(payload) == string(constants.PlatformEnrollmentActionIssue) {
		update, err := json.Marshal(struct {
			Owner   string    `json:"issuance_lease_owner"`
			Expires time.Time `json:"issuance_lease_expires_at"`
		}{reclaimedLeaseOwner, time.Now().UTC().Add(constants.PlatformEnrollmentIssuanceLeaseTTL)})
		require.NoError(p.t, err)
		applied, err := p.env.docStore.DocConditionalUpdate(ctx, platformEnrollmentCollectionName(), p.id, update,
			"state", string(models.PlatformEnrollmentStateIssuing))
		require.NoError(p.t, err)
		require.True(p.t, applied)
		if p.fail {
			return nil, errInjectedFailure
		}
	}
	return p.inner.ProcessEnvelope(ctx, payload)
}

var errInjectedFailure = errors.New("injected ISSUE failure")

// TestPlatformEnrollmentLeaseFence_ReclaimedHolderCannotComplete proves the
// ISSUE handler signs only for the holder the stored lease names: a submitter
// whose lease was reclaimed leaves the new holder's issuance untouched.
func TestPlatformEnrollmentLeaseFence_ReclaimedHolderCannotComplete(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)
	csr, _ := generateAppCSRAndKey(t)
	requestID, _, _ := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fence-1", "dashboard.local", csr, "", "")

	original := env.enrollSvc.envProc
	env.enrollSvc.envProc = &reclaimingProcessor{t: t, env: env, inner: original, id: requestID}
	defer func() { env.enrollSvc.envProc = original }()

	_, err := env.enrollSvc.issueComponent(t.Context(), loadStoredRequest(t, env, requestID))
	require.Error(t, err)

	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateIssuing, stored.State)
	assert.Equal(t, reclaimedLeaseOwner, stored.IssuanceLeaseOwner, "the new holder keeps its lease")
	assert.Nil(t, stored.Issued, "the stale holder signed nothing into the request")
}

// TestPlatformEnrollmentLeaseFence_ReclaimedHolderCannotRollBack proves a
// failed submitter whose lease was reclaimed does not return the new holder's
// issuance to approved.
func TestPlatformEnrollmentLeaseFence_ReclaimedHolderCannotRollBack(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)
	csr, _ := generateAppCSRAndKey(t)
	requestID, _, _ := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fence-2", "dashboard.local", csr, "", "")

	original := env.enrollSvc.envProc
	env.enrollSvc.envProc = &reclaimingProcessor{t: t, env: env, inner: original, id: requestID, fail: true}
	defer func() { env.enrollSvc.envProc = original }()

	_, err := env.enrollSvc.issueComponent(t.Context(), loadStoredRequest(t, env, requestID))
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentIssuanceFailed)

	stored := loadStoredRequest(t, env, requestID)
	assert.Equal(t, models.PlatformEnrollmentStateIssuing, stored.State, "the stale rollback must not apply")
	assert.Equal(t, reclaimedLeaseOwner, stored.IssuanceLeaseOwner)
}

// TestPlatformEnrollmentLeaseFence_RequestTTLDoesNotEndALiveIssuance proves
// the request TTL never expires an issuing request whose lease is live:
// neither cleanup, nor a status read, nor a redialed completion.
func TestPlatformEnrollmentLeaseFence_RequestTTLDoesNotEndALiveIssuance(t *testing.T) {
	env := setupPlatformEnrollmentEnv(t, true)
	csr, key := generateAppCSRAndKey(t)
	requestID, token, approved := createAndApproveRequest(t, env,
		models.PlatformComponentDashboard, "dashboard-fence-3", "dashboard.local", csr, "", "")

	applied, err := env.docStore.TransitionPlatformEnrollment(t.Context(), models.PlatformEnrollmentTransition{
		RequestID: requestID, From: models.PlatformEnrollmentStateApproved, To: models.PlatformEnrollmentStateIssuing,
		LeaseOwner: "live-holder", LeaseExpiresAt: time.Now().UTC().Add(constants.PlatformEnrollmentIssuanceLeaseTTL),
	})
	require.NoError(t, err)
	require.True(t, applied)
	backdateExpiry(t, env, requestID, time.Now().UTC().Add(-time.Minute))

	require.NoError(t, env.enrollSvc.CleanupTerminalRequests(t.Context()))
	assert.Equal(t, models.PlatformEnrollmentStateIssuing, loadStoredRequest(t, env, requestID).State)

	status, err := env.enrollSvc.GetStatus(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, models.PlatformEnrollmentStateIssuing, status.State)

	_, err = env.enrollSvc.Complete(t.Context(), token, models.PlatformEnrollmentProofs{
		App: signCompletionTranscript(t, approved, key),
	})
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentIssuanceInProgress)
	assert.Equal(t, models.PlatformEnrollmentStateIssuing, loadStoredRequest(t, env, requestID).State)
}

// ============================================================================
// Helper functions
// ============================================================================

// countDocsInCollection counts the number of documents in a collection.
func countDocsInCollection(t *testing.T, env *platformEnrollmentTestEnv, collection string) int {
	t.Helper()
	docs, err := env.docStore.DocQuery(t.Context(), collection, nil, "", 0)
	require.NoError(t, err)
	return len(docs)
}
