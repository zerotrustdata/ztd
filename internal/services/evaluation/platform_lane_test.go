// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type laneTestClient struct {
	health           *models.HealthResponse
	discovered       *operatorv1.OperatorDocument
	dispatchOutcome  *client.GovernedOutcome
	dispatchErr      error
	receipts         []*operatorv1.ActionReceipt
	receiptErr       error
	receiptCalls     int
	records          []*models.ActionReceiptRecord
	auditRaw         []byte
	auditErr         error
	publicKey        ed25519.PublicKey
	signerErr        error
	dispatchRequests []client.DispatchCommandRequest
	recorder         *[]client.Exchange
}

func (c *laneTestClient) Health(context.Context) (*models.HealthResponse, []byte, error) {
	return c.health, nil, nil
}

func (c *laneTestClient) DiscoverRemoteOperator(context.Context) (*operatorv1.OperatorDocument, []byte, error) {
	return c.discovered, nil, nil
}

func (c *laneTestClient) DispatchCommand(_ context.Context, _ client.Persona, request client.DispatchCommandRequest) (*client.GovernedOutcome, error) {
	c.dispatchRequests = append(c.dispatchRequests, request)
	requestBody, _ := json.Marshal(request)
	exchange := client.Exchange{Method: http.MethodPost, ReqBody: requestBody}
	if c.dispatchOutcome != nil {
		exchange.Status = int(c.dispatchOutcome.Observation.GetHttpStatus())
		exchange.RespBody = append(json.RawMessage(nil), c.dispatchOutcome.Body...)
	}
	c.record(exchange)
	return c.dispatchOutcome, c.dispatchErr
}

func (c *laneTestClient) GetActionReceipt(context.Context, string, ...client.Persona) (*operatorv1.ActionReceipt, []byte, error) {
	c.receiptCalls++
	c.record(client.Exchange{Method: http.MethodGet, Status: http.StatusOK})
	if c.receiptErr != nil {
		return nil, nil, c.receiptErr
	}
	if len(c.receipts) == 0 {
		return nil, nil, nil
	}
	index := c.receiptCalls - 1
	if index >= len(c.receipts) {
		index = len(c.receipts) - 1
	}
	return c.receipts[index], nil, nil
}

func (c *laneTestClient) GetTrustedSignerPublicKey(context.Context, string) (ed25519.PublicKey, error) {
	c.record(client.Exchange{Method: http.MethodGet, Status: http.StatusOK})
	return c.publicKey, c.signerErr
}

func (c *laneTestClient) AuditReceiptRecords(context.Context, string) ([]*models.ActionReceiptRecord, []byte, error) {
	c.record(client.Exchange{Method: http.MethodGet, Status: http.StatusOK, RespBody: append(json.RawMessage(nil), c.auditRaw...)})
	return c.records, c.auditRaw, c.auditErr
}

func (c *laneTestClient) Record(sink *[]client.Exchange) {
	c.recorder = sink
}

func (c *laneTestClient) record(exchange client.Exchange) {
	if c.recorder != nil {
		*c.recorder = append(*c.recorder, exchange)
	}
}

type laneTestArtifact struct {
	scope        EvidenceScope
	artifactType complianceevidence.ArtifactType
	message      proto.Message
	body         []byte
}

type laneTestArtifactSink struct {
	artifacts []laneTestArtifact
	mutateID  bool
	err       error
}

func (s *laneTestArtifactSink) SaveProtoArtifact(_ context.Context, scope EvidenceScope, artifactType complianceevidence.ArtifactType, message proto.Message) (*compliancev1.ComplianceEvidenceReference, error) {
	if s.err != nil {
		return nil, s.err
	}
	body, err := complianceevidence.MarshalCanonicalProto(message)
	if err != nil {
		return nil, err
	}
	s.artifacts = append(s.artifacts, laneTestArtifact{scope: scope, artifactType: artifactType, message: proto.Clone(message), body: body})
	return s.reference(scope, artifactType, body), nil
}

func (s *laneTestArtifactSink) SaveJSONArtifact(_ context.Context, scope EvidenceScope, artifactType complianceevidence.ArtifactType, body []byte) (*compliancev1.ComplianceEvidenceReference, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.artifacts = append(s.artifacts, laneTestArtifact{scope: scope, artifactType: artifactType, body: append([]byte(nil), body...)})
	return s.reference(scope, artifactType, body), nil
}

func (s *laneTestArtifactSink) reference(scope EvidenceScope, artifactType complianceevidence.ArtifactType, body []byte) *compliancev1.ComplianceEvidenceReference {
	id := complianceevidence.ContentAddress(artifactType, body)
	if s.mutateID && artifactType == complianceevidence.ArtifactTypeActionReceipt {
		id = string(artifactType) + ":sha256:" + string(make([]byte, 64))
	}
	_, digest, _ := complianceevidence.ParseContentAddress(id)
	return &compliancev1.ComplianceEvidenceReference{ArtifactId: id, ArtifactType: string(artifactType), Sha256: digest, MediaType: constants.MediaTypeJSON, RunId: scope.RunID, ScenarioId: scope.ScenarioID, AttemptId: scope.AttemptID, TransactionId: scope.TransactionID}
}

func TestCommandLane_ResolveTargetAndPostureFailClosed(t *testing.T) {
	activeRemote := &operatorv1.OperatorDocument{Id: "operator-1", OperatorSessionId: "session-1", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote)}
	tests := []struct {
		name       string
		client     *laneTestClient
		wantTarget Target
		wantErr    error
	}{
		{name: "discovered active remote operator", client: &laneTestClient{discovered: activeRemote}, wantTarget: Target{OperatorID: "operator-1", SessionID: "session-1"}},
		{name: "configured blended embedded Data operator", client: &laneTestClient{discovered: &operatorv1.OperatorDocument{Id: "blend", OperatorSessionId: "blend-session", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeEmbedded), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference})}}}, wantTarget: Target{OperatorID: "blend", SessionID: "blend-session"}},
		{name: "remote witness without Data rejected", client: &laneTestClient{discovered: &operatorv1.OperatorDocument{Id: "witness", OperatorSessionId: "witness-session", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityObserver})}}}, wantErr: constants.ErrEvaluationTargetUnavailable},
		{name: "discovered embedded operator rejected", client: &laneTestClient{discovered: &operatorv1.OperatorDocument{Id: "operator-1", OperatorSessionId: "session-1", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeEmbedded)}}, wantErr: constants.ErrEvaluationTargetUnavailable},
		{name: "discovered inactive operator rejected", client: &laneTestClient{discovered: &operatorv1.OperatorDocument{Id: "operator-1", OperatorSessionId: "session-1", Status: string(constants.OperatorStatusOffline), OperatorType: string(constants.OperatorTypeRemote)}}, wantErr: constants.ErrEvaluationTargetUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lane := NewCommandLane(test.client, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)
			target, err := lane.ResolveTarget(context.Background())
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.wantTarget, target)
		})
	}

	postures := []struct {
		name string
		raw  string
		want evalv1.EvaluationGovernancePosture
	}{
		{name: "doctrine", raw: constants.PostureDoctrine, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE},
		{name: "consensus", raw: constants.PostureConsensus, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS},
		{name: "ratify", raw: constants.PostureRatify, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY},
		{name: "notary", raw: constants.PostureNotary, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY},
		{name: "unknown", raw: "unknown", want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED},
	}
	for _, test := range postures {
		t.Run("posture "+test.name, func(t *testing.T) {
			lane := NewCommandLane(&laneTestClient{health: &models.HealthResponse{Posture: test.raw}}, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)
			posture, err := lane.Posture(context.Background())
			require.NoError(t, err)
			assert.Equal(t, test.want, posture)
		})
	}
}

func TestCommandLane_AllowedAttemptVerifiesAndPersistsBoundEvidence(t *testing.T) {
	request := laneExecutionRequest(false)
	receipt, publicKey := newLaneSignedReceipt(t, request)
	resultPayload, err := proto.Marshal(&operatorv1.FileEditResult{ExecutionId: request.AttemptID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED})
	require.NoError(t, err)
	record := laneReceiptRecord(request, receipt)
	auditRaw, err := json.Marshal(&models.AuditReceiptsResponse{Success: true, Receipts: []*models.ActionReceiptRecord{record}})
	require.NoError(t, err)
	auditRaw = append(auditRaw, '\n')
	fakeClient := &laneTestClient{
		dispatchOutcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: receipt.TransactionId, ActionType: string(constants.ActionTypeFileEdit), ResultPayload: resultPayload, Receipt: receipt}),
		receipts:        []*operatorv1.ActionReceipt{receipt}, records: []*models.ActionReceiptRecord{record}, auditRaw: auditRaw, publicKey: publicKey,
	}
	sink := &laneTestArtifactSink{}
	lane := NewCommandLane(fakeClient, sink, client.Persona{CLISessionID: "cli-session-1", UserID: "user-1"}, time.Millisecond, time.Second)

	outcome, err := lane.Execute(context.Background(), request)

	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, receipt.TransactionId, outcome.TransactionID)
	assert.Equal(t, request.AttemptID, outcome.ExecutionID)
	assert.True(t, outcome.TargetIdentityMatches)
	assert.True(t, outcome.ReceiptCompleted)
	assert.True(t, outcome.ReceiptDurable)
	assert.True(t, outcome.ProtocolChainValid)
	assert.False(t, outcome.Rejected)
	require.Len(t, fakeClient.dispatchRequests, 1)
	dispatch := fakeClient.dispatchRequests[0]
	assert.Equal(t, request.RunID, dispatch.CaseID)
	assert.Equal(t, request.ScenarioID, dispatch.InvestigationID)
	assert.Equal(t, request.AttemptID, dispatch.TaskID)
	assert.Equal(t, request.Target.SessionID, dispatch.TargetOperatorSessionID)
	assert.Equal(t, "cli-session-1", dispatch.CLISessionID)
	fileEdit := &operatorv1.FileEditRequested{}
	require.NoError(t, proto.Unmarshal(dispatch.Payload, fileEdit))
	assert.Equal(t, request.Marker, fileEdit.Content)
	assert.Equal(t, request.AttemptID, fileEdit.ExecutionId)
	assert.Equal(t, string(constants.FileOperationWrite), fileEdit.Operation)
	assert.True(t, fileEdit.CreateIfMissing)
	assert.ElementsMatch(t, []string{string(complianceevidence.ArtifactTypeActionReceipt), string(complianceevidence.ArtifactTypeReceiptPersistence), string(complianceevidence.ArtifactTypeAuditRecord), string(complianceevidence.ArtifactTypeEvalExchange)}, artifactTypes(sink.artifacts))
	require.Len(t, outcome.EvidenceRefs, 4)
	for _, artifact := range sink.artifacts {
		require.NoError(t, complianceevidence.ValidateCanonicalJSON(artifact.body))
		assert.Equal(t, request.RunID, artifact.scope.RunID)
		assert.Equal(t, request.AttemptID, artifact.scope.AttemptID)
	}
}

func TestCommandLane_ObservedPlatformOutcomesRemainVerdictInputs(t *testing.T) {
	tests := []struct {
		name           string
		request        ExecutionRequest
		outcome        *client.GovernedOutcome
		records        []*models.ActionReceiptRecord
		wantRejected   bool
		wantAttributed bool
		wantCount      int64
	}{
		{name: "allowed rejection is rejected outcome", request: laneExecutionRequest(false), outcome: laneRejectionOutcome(t, http.StatusForbidden, laneRejection("rejected")), wantRejected: true},
		{name: "prohibited correlated L1 rejection", request: laneExecutionRequest(true), outcome: laneL1RejectionOutcome(t), wantRejected: true, wantAttributed: true},
		{name: "prohibited non-rejection is platform defect", request: laneExecutionRequest(true), outcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-2", Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-2"}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auditRaw, err := json.Marshal(&models.AuditReceiptsResponse{Success: true, Receipts: test.records})
			require.NoError(t, err)
			fakeClient := &laneTestClient{dispatchOutcome: test.outcome, records: test.records, auditRaw: auditRaw}
			lane := NewCommandLane(fakeClient, &laneTestArtifactSink{}, client.Persona{CLISessionID: "cli-session-1"}, time.Millisecond, time.Second)
			outcome, err := lane.Execute(context.Background(), test.request)
			require.NoError(t, err)
			require.NotNil(t, outcome)
			assert.Equal(t, test.wantRejected, outcome.Rejected)
			assert.Equal(t, test.wantAttributed, outcome.GatewayL1Attributed)
			assert.Equal(t, test.wantCount, outcome.CompletedExecutionCount)
		})
	}
}

func TestCommandLane_ProhibitedAttemptCountsOnlyCorrelatedCompletedReceipts(t *testing.T) {
	request := laneExecutionRequest(true)
	matching := &operatorv1.ActionReceipt{DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{TaskId: request.AttemptID}}}
	unrelated := &operatorv1.ActionReceipt{DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{TaskId: "other-attempt"}}}
	records := []*models.ActionReceiptRecord{
		{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ActionReceipt: matching},
		{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ActionReceipt: unrelated},
		{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, ActionReceipt: matching},
	}
	auditRaw, err := json.Marshal(&models.AuditReceiptsResponse{Success: true, Receipts: records})
	require.NoError(t, err)
	fakeClient := &laneTestClient{dispatchOutcome: laneL1RejectionOutcome(t), records: records, auditRaw: auditRaw}
	lane := NewCommandLane(fakeClient, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)

	outcome, err := lane.Execute(context.Background(), request)

	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, int64(1), outcome.CompletedExecutionCount)
}

func TestCommandLane_FailsClosedForUnavailableOrInvalidReceiptEvidence(t *testing.T) {
	request := laneExecutionRequest(false)
	receipt, publicKey := newLaneSignedReceipt(t, request)
	resultPayload, err := proto.Marshal(&operatorv1.FileEditResult{ExecutionId: request.AttemptID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED})
	require.NoError(t, err)
	baseClient := func() *laneTestClient {
		record := laneReceiptRecord(request, receipt)
		auditRaw, marshalErr := json.Marshal(&models.AuditReceiptsResponse{Success: true, Receipts: []*models.ActionReceiptRecord{record}})
		require.NoError(t, marshalErr)
		return &laneTestClient{dispatchOutcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: receipt.TransactionId, ResultPayload: resultPayload, Receipt: receipt}), receipts: []*operatorv1.ActionReceipt{receipt}, records: []*models.ActionReceiptRecord{record}, auditRaw: auditRaw, publicKey: publicKey}
	}
	tests := []struct {
		name    string
		prepare func(*laneTestClient, *laneTestArtifactSink)
		wantErr error
	}{
		{name: "receipt polling exhausted", prepare: func(c *laneTestClient, _ *laneTestArtifactSink) { c.receipts = nil }, wantErr: constants.ErrEvaluationReceiptUnavailable},
		{name: "receipt binding mismatch", prepare: func(c *laneTestClient, _ *laneTestArtifactSink) { c.records[0].OperatorSessionID = "other-session" }, wantErr: constants.ErrInvalidEvidenceGraph},
		{name: "receipt signature invalid", prepare: func(c *laneTestClient, _ *laneTestArtifactSink) {
			c.publicKey = make(ed25519.PublicKey, ed25519.PublicKeySize)
		}},
		{name: "stored receipt address mismatch", prepare: func(_ *laneTestClient, s *laneTestArtifactSink) { s.mutateID = true }, wantErr: constants.ErrInvalidEvidenceGraph},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeClient := baseClient()
			sink := &laneTestArtifactSink{}
			test.prepare(fakeClient, sink)
			lane := NewCommandLane(fakeClient, sink, client.Persona{}, time.Nanosecond, time.Nanosecond)
			outcome, err := lane.Execute(context.Background(), request)
			assert.Nil(t, outcome)
			require.Error(t, err)
			if test.wantErr != nil {
				assert.True(t, errors.Is(err, test.wantErr), err.Error())
			}
		})
	}
}

func TestCommandLane_ProhibitedCompletedRecordWithoutCanonicalReceiptIsInvalidEvidence(t *testing.T) {
	request := laneExecutionRequest(true)
	records := []*models.ActionReceiptRecord{{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}}
	auditRaw, err := json.Marshal(&models.AuditReceiptsResponse{Success: true, Receipts: records})
	require.NoError(t, err)
	fakeClient := &laneTestClient{dispatchOutcome: laneL1RejectionOutcome(t), records: records, auditRaw: auditRaw}
	lane := NewCommandLane(fakeClient, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)

	outcome, err := lane.Execute(context.Background(), request)

	assert.Nil(t, outcome)
	require.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
}

func laneExecutionRequest(prohibited bool) ExecutionRequest {
	return ExecutionRequest{RunID: "run-1", ScenarioID: "scenario-1", AttemptID: "attempt-1", Target: Target{OperatorID: "operator-1", SessionID: "session-1"}, TargetResource: "/tmp/evaluation-target.txt", Marker: "run-1", Prohibited: prohibited}
}

func newLaneSignedReceipt(t *testing.T, request ExecutionRequest) (*operatorv1.ActionReceipt, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signerKeyID := hex.EncodeToString(publicKey)
	receipt := &operatorv1.ActionReceipt{TransactionId: "tx-1", TransactionHash: "tx-hash", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, StateRootBefore: "root-before", StateRootAfter: "root-after", SignerKeyId: signerKeyID, ExecutedAtUnixMs: 1_700_000_001_000, L2Status: operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, L3Status: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED}
	l4ID := receipt.TransactionId + ":L4"
	l5ID := receipt.TransactionId + ":L5"
	receipt.DeterministicStageEvidence = []*operatorv1.DeterministicStageEvidence{
		{StageId: receipt.TransactionId + ":L1", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, ParentStageId: l4ID},
		{StageId: receipt.TransactionId + ":L2", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, ParentStageId: l4ID},
		{StageId: receipt.TransactionId + ":L3", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED, ParentStageId: l4ID},
		{StageId: l4ID, Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, ParentStageId: l5ID},
		{StageId: receipt.TransactionId + ":PERSIST", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_RECEIPT_PERSISTENCE, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, ParentStageId: l5ID},
		{StageId: receipt.TransactionId + ":COMMIT", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, ParentStageId: l5ID},
		{StageId: l5ID, Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION, Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, StateRootBefore: receipt.StateRootBefore, StateRootAfter: receipt.StateRootAfter},
	}
	for _, stage := range receipt.DeterministicStageEvidence {
		stage.TransactionId = receipt.TransactionId
		stage.TransactionHash = receipt.TransactionHash
		stage.ActionType = string(constants.ActionTypeFileEdit)
		stage.OperatorId = request.Target.OperatorID
		stage.OperatorSessionId = request.Target.SessionID
		stage.CaseId = request.RunID
		stage.InvestigationId = request.ScenarioID
		stage.TaskId = request.AttemptID
	}
	payload, err := governance.CanonicalizeActionReceipt(receipt)
	require.NoError(t, err)
	receipt.Signature = hex.EncodeToString(ed25519.Sign(privateKey, payload))
	attestation := &operatorv1.ReceiptPersistenceAttestation{TransactionId: receipt.TransactionId, ReceiptSignatureDigest: governance.SignatureDigest([]string{receipt.Signature}), PersistedAtUnixMs: 1_700_000_002_000, AuditRecordId: receipt.TransactionId, SignerKeyId: signerKeyID}
	attestationPayload, err := governance.CanonicalizeReceiptPersistenceAttestation(attestation)
	require.NoError(t, err)
	attestation.Signature = hex.EncodeToString(ed25519.Sign(privateKey, attestationPayload))
	receipt.FinalPersistenceAttestation = attestation
	return receipt, publicKey
}

func laneReceiptRecord(request ExecutionRequest, receipt *operatorv1.ActionReceipt) *models.ActionReceiptRecord {
	return &models.ActionReceiptRecord{TransactionID: receipt.TransactionId, TransactionHash: receipt.TransactionHash, InvestigationID: request.ScenarioID, OperatorID: request.Target.OperatorID, OperatorSessionID: request.Target.SessionID, ActionType: constants.ActionTypeFileEdit, Status: receipt.Status, SignerKeyID: receipt.SignerKeyId, Signature: receipt.Signature, ActionReceipt: receipt}
}

func artifactTypes(artifacts []laneTestArtifact) []string {
	values := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		values = append(values, string(artifact.artifactType))
	}
	return values
}

// TestCommandLane_WriteWorkspaceFile_DispatchesGovernedWriteAndWaitsForReceipt
// guards the fixture-materialization path: WriteWorkspaceFile must dispatch a
// real governed file write for the fixture content at the given absolute path
// and only return once a completed receipt confirms it landed, so a
// scenario's expected file_read_on_operator/recursive_grep_search target
// exists before the chat request is sent.
func TestCommandLane_WriteWorkspaceFile_DispatchesGovernedWriteAndAdmitsSignedReceipt(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	fakeClient := &laneTestClient{
		dispatchOutcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-1"}}),
	}
	lane := NewCommandLane(fakeClient, &laneTestArtifactSink{}, client.Persona{CLISessionID: "cli-session-1"}, time.Millisecond, time.Second)
	absPath := "/home/op/workspaces/ws-0123456789abcdef/net/network-summary.txt"
	content := "upstream_host=payments.internal.example"

	err := lane.WriteWorkspaceFile(context.Background(), target, "run-1", "tool-arg-file-path", "attempt-1", absPath, content)

	require.NoError(t, err)
	require.Len(t, fakeClient.dispatchRequests, 1)
	dispatch := fakeClient.dispatchRequests[0]
	assert.Equal(t, target.SessionID, dispatch.TargetOperatorSessionID)
	assert.Equal(t, absPath, dispatch.TargetResource)
	assert.Equal(t, "run-1", dispatch.CaseID)
	assert.Equal(t, "tool-arg-file-path", dispatch.InvestigationID)
	assert.Equal(t, "attempt-1", dispatch.TaskID)
	fileEdit := &operatorv1.FileEditRequested{}
	require.NoError(t, proto.Unmarshal(dispatch.Payload, fileEdit))
	assert.Equal(t, absPath, fileEdit.FilePath)
	assert.Equal(t, content, fileEdit.Content)
	assert.True(t, fileEdit.CreateIfMissing)
	assert.Equal(t, string(constants.FileOperationWrite), fileEdit.Operation)
	assert.Zero(t, fakeClient.receiptCalls, "the receipt is read from the typed dispatch outcome, not polled")
}

func TestCommandLane_WriteWorkspaceFile_FailsClosedOnRejectedDispatch(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	fakeClient := &laneTestClient{dispatchOutcome: laneRejectionOutcome(t, http.StatusForbidden, laneRejection("rejected"))}
	lane := NewCommandLane(fakeClient, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)

	err := lane.WriteWorkspaceFile(context.Background(), target, "run-1", "tool-arg-file-path", "attempt-1", "/home/op/workspaces/ws-0123456789abcdef/net/network-summary.txt", "upstream_host=payments.internal.example")

	require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
	assert.Zero(t, fakeClient.receiptCalls)
}

func TestCommandLane_WriteWorkspaceFile_RequiresOperatorTargetAndPath(t *testing.T) {
	lane := NewCommandLane(&laneTestClient{}, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)

	err := lane.WriteWorkspaceFile(context.Background(), Target{}, "run-1", "scenario-1", "attempt-1", "", "x")

	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCommandLane_ReadWorkspaceFile_DispatchesGovernedFsReadAndReturnsContent(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	absPath := "/home/op/workspaces/ws-0123456789abcdef/canary/probe.txt"
	payload, err := proto.Marshal(&operatorv1.FsReadResult{
		ExecutionId: "attempt-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Path: absPath, Content: "canary-1",
	})
	require.NoError(t, err)
	fakeClient := &laneTestClient{
		dispatchOutcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", ResultPayload: payload, Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-1"}}),
	}
	lane := NewCommandLane(fakeClient, &laneTestArtifactSink{}, client.Persona{CLISessionID: "cli-session-1"}, time.Millisecond, time.Second)

	content, err := lane.ReadWorkspaceFile(context.Background(), target, "run-1", "environment-canary", "attempt-1", absPath)

	require.NoError(t, err)
	assert.Equal(t, "canary-1", content)
	require.Len(t, fakeClient.dispatchRequests, 1)
	dispatch := fakeClient.dispatchRequests[0]
	assert.Equal(t, string(constants.ActionTypeFsRead), dispatch.ActionType)
	assert.Equal(t, target.SessionID, dispatch.TargetOperatorSessionID)
	assert.Equal(t, absPath, dispatch.TargetResource)
	fsRead := &operatorv1.FsReadRequested{}
	require.NoError(t, proto.Unmarshal(dispatch.Payload, fsRead))
	assert.Equal(t, absPath, fsRead.Path)
	assert.Equal(t, "attempt-1", fsRead.ExecutionId)
}

func TestCommandLane_ReadWorkspaceFile_FailsClosed(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	failed, err := proto.Marshal(&operatorv1.FsReadResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, ErrorMessage: "no such file"})
	require.NoError(t, err)
	receipt := &operatorv1.ActionReceipt{TransactionId: "tx-1"}
	tests := []struct {
		name    string
		outcome *client.GovernedOutcome
	}{
		{"dispatch rejected", laneRejectionOutcome(t, http.StatusForbidden, laneRejection("rejected"))},
		{"no result payload", laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: receipt})},
		{"operator read failed", laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", ResultPayload: failed, Receipt: receipt})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lane := NewCommandLane(&laneTestClient{dispatchOutcome: tt.outcome}, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)

			_, err := lane.ReadWorkspaceFile(context.Background(), target, "run-1", "environment-canary", "attempt-1", "/home/op/workspaces/ws-0123456789abcdef/missing.txt")

			require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
		})
	}
}

func TestCommandLane_ReadWorkspaceFile_RequiresOperatorTargetAndPath(t *testing.T) {
	lane := NewCommandLane(&laneTestClient{}, &laneTestArtifactSink{}, client.Persona{}, time.Millisecond, time.Second)

	_, err := lane.ReadWorkspaceFile(context.Background(), Target{}, "run-1", "scenario-1", "attempt-1", "")

	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

// laneCanonicalBody renders msg with the canonical protojson marshaller, the
// bytes the Gateway writes for the same answer.
func laneCanonicalBody(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(msg)
	require.NoError(t, err)
	return body
}

// laneReceiptOutcome is the typed outcome of a 200 dispatch the Operator completed.
func laneReceiptOutcome(t *testing.T, dispatched *operatorv1.GovernedDispatchResult) *client.GovernedOutcome {
	t.Helper()
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, HttpStatus: http.StatusOK, Receipt: dispatched.GetReceipt()},
		Dispatch:    dispatched,
		Body:        laneCanonicalBody(t, dispatched),
	}
}

// laneRejectionOutcome is the typed outcome of a signed governance rejection.
func laneRejectionOutcome(t *testing.T, status int, rejection *operatorv1.GovernanceRejection) *client.GovernedOutcome {
	t.Helper()
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, HttpStatus: int32(status), Rejection: rejection},
		Body:        laneCanonicalBody(t, rejection),
	}
}

// laneL1RejectionOutcome is the typed outcome of a rejection at L1 doctrine.
func laneL1RejectionOutcome(t *testing.T) *client.GovernedOutcome {
	t.Helper()
	return laneRejectionOutcome(t, http.StatusForbidden, laneRejection(constants.ErrTxL1ValidationFailed.Error()))
}

func laneRejection(message string) *operatorv1.GovernanceRejection {
	return &operatorv1.GovernanceRejection{Error: message, Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-rejected"}}
}
