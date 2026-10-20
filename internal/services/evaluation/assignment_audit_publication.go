// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	AssignmentAuditSliceKind = "assignment_audit_slice"
	AssignmentAgentTraceKind = "assignment_agent_trace"
	ScenarioInputFixtureKind = "scenario_input_fixture"
	ScenarioGoldCriteriaKind = "scenario_gold_criteria"
	assignmentAuditSchemaRef = "ztd.eval.v1.PublicAssignmentAuditSlice"
	assignmentAgentTraceRef  = "ztd.eval.v1.PublicAssignmentAgentTrace"
	scenarioInputFixtureRef  = "ztd.eval.v1.PublicScenarioInputFixture"
	scenarioGoldCriteriaRef  = "ztd.eval.v1.PublicScenarioGoldCriteria"
)

// AssignmentAuditProofInput is one assignment's audit proof request. The Gateway
// builds the assignment database from the signed event records it stores, so
// the input carries no database bytes and no key. The identity fields, the
// campaign revision, and the index digest are all required.
type AssignmentAuditProofInput struct {
	CampaignID       string
	CampaignRevision string
	RunID            string
	AssignmentID     string
	IndexDigest      string
	Artifacts        AssignmentAuditProofArtifacts
}

// AssignmentAuditProofArtifacts are the bodies published beside an assignment's
// database: its sealed agent trace for agent assignments, and its scenario's
// frozen input fixture and gold criteria. Each digest is the SHA-256 of the body
// sent.
type AssignmentAuditProofArtifacts struct {
	AgentTrace          []byte
	AgentTraceSHA256    string
	ScenarioID          string
	ScenarioInput       []byte
	ScenarioInputSHA256 string
	ScenarioGold        []byte
	ScenarioGoldSHA256  string
}

// CampaignProofPublisher ingests assignment audit proofs into the public proof
// catalog. Nil publishers skip proof publication. IngestAssignmentAuditSlices
// answers with one response per input, in input order; each response's
// DatabaseSHA256 is the digest of the database the Gateway built or reused.
type CampaignProofPublisher interface {
	IngestAssignmentAuditSlices(ctx context.Context, inputs []AssignmentAuditProofInput, deferMirrorPush bool) ([]models.PublicAssignmentAuditProofPublishResponse, error)
	PruneRunProofCatalog(ctx context.Context, runID string) error
	FlushProofCatalog(ctx context.Context) error
}

// AssignmentAuditProofIdempotencyKey returns the publication idempotency key for
// one assignment audit proof package.
func AssignmentAuditProofIdempotencyKey(runID, assignmentID string) string {
	return runID + ":" + assignmentID + ":audit-proof"
}

// validate rejects an input the Gateway would refuse. An empty campaign revision
// or index digest is an error; it is never replaced by another field's value.
func (i AssignmentAuditProofInput) validate() error {
	switch {
	case i.CampaignID == "" || i.RunID == "" || i.AssignmentID == "":
		return fmt.Errorf("evaluation: assignment audit proof: identity: %w", constants.ErrMissingRequiredField)
	case i.CampaignRevision == "":
		return fmt.Errorf("evaluation: assignment audit proof %s: campaign revision: %w", i.AssignmentID, constants.ErrMissingRequiredField)
	case i.IndexDigest == "":
		return fmt.Errorf("evaluation: assignment audit proof %s: index digest: %w", i.AssignmentID, constants.ErrMissingRequiredField)
	}
	return nil
}

// hasAssignmentAuditEvents reports whether an assignment published any live
// event record. The Gateway builds the audit database from the records it
// stored, so an assignment with none has no database to publish.
func hasAssignmentAuditEvents(requests []campaignFeedPublishRequest) bool {
	for _, request := range requests {
		if request.RecordType != "" && request.RecordType != models.PublicFeedRecordTypeEvent {
			continue
		}
		if len(request.Body) > 0 {
			return true
		}
	}
	return false
}

// assignmentAuditProofInput builds the proof request for one terminal
// assignment from its live event requests. It reports false when there is
// nothing to publish: no proof publisher is attached, or the assignment
// published no live events.
func (c *CampaignPublicationCoordinator) assignmentAuditProofInput(
	ctx context.Context,
	assignment *evalv1.EvaluationAssignment,
	result *evalv1.EvaluationAssignmentResult,
	fixtures map[string]ScenarioArtifacts,
	liveRequests []campaignFeedPublishRequest,
) (AssignmentAuditProofInput, bool, error) {
	if c == nil || c.proofPublisher == nil || assignment == nil || result == nil || !hasAssignmentAuditEvents(liveRequests) {
		return AssignmentAuditProofInput{}, false, nil
	}
	trace, traceSHA256, err := loadAssignmentAgentTraceArtifact(ctx, c.store, result)
	if err != nil {
		return AssignmentAuditProofInput{}, false, err
	}
	artifacts := AssignmentAuditProofArtifacts{AgentTrace: trace, AgentTraceSHA256: traceSHA256}
	if err := artifacts.attachScenarioFixture(assignment, fixtures); err != nil {
		return AssignmentAuditProofInput{}, false, err
	}
	spec, err := c.store.LoadCampaignSpec(ctx, assignment.GetCampaignId())
	if err != nil {
		return AssignmentAuditProofInput{}, false, fmt.Errorf("evaluation: build assignment audit proof: load campaign spec: %w", err)
	}
	input := AssignmentAuditProofInput{
		CampaignID:       assignment.GetCampaignId(),
		CampaignRevision: spec.GetCampaignDigest(),
		RunID:            assignment.GetRunId(),
		AssignmentID:     assignment.GetAssignmentId(),
		IndexDigest:      result.GetResultDigest(),
		Artifacts:        artifacts,
	}
	if err := input.validate(); err != nil {
		return AssignmentAuditProofInput{}, false, err
	}
	return input, true, nil
}

// ingestAssignmentAuditProofs sends the inputs to the proof publisher in one
// request and records each answer. The database digest is the Gateway's: the
// host never derives one. Callers build result records only after this returns,
// so every record binds the digest the Gateway answered. The mirror push is
// deferred to FlushProofCatalog when the coordinator defers it. The result is
// the published artifacts keyed by assignment ID.
func (c *CampaignPublicationCoordinator) ingestAssignmentAuditProofs(ctx context.Context, inputs []AssignmentAuditProofInput) (map[string]CampaignPublishedProofArtifacts, error) {
	published := make(map[string]CampaignPublishedProofArtifacts, len(inputs))
	if len(inputs) == 0 {
		return published, nil
	}
	if c == nil || c.proofPublisher == nil {
		return nil, fmt.Errorf("evaluation: ingest assignment audit proofs: %w", constants.ErrMissingRequiredField)
	}
	answers, err := c.proofPublisher.IngestAssignmentAuditSlices(ctx, inputs, c.deferProofMirrorPush)
	if err != nil {
		return nil, fmt.Errorf("evaluation: ingest assignment audit proofs: %w", err)
	}
	if c.deferProofMirrorPush {
		c.pendingProofMirrorPush = true
	}
	if len(answers) != len(inputs) {
		return nil, fmt.Errorf("evaluation: ingest assignment audit proofs: %d answers for %d proofs: %w", len(answers), len(inputs), constants.ErrEvidenceArtifactMalformed)
	}
	runID := inputs[0].RunID
	for index, input := range inputs {
		answer := answers[index]
		if answer.AssignmentID != input.AssignmentID || answer.DatabaseSHA256 == "" {
			return nil, fmt.Errorf("evaluation: ingest assignment audit proofs: answer %d lacks the database digest of assignment %s: %w", index, input.AssignmentID, constants.ErrEvidenceArtifactMalformed)
		}
		if input.RunID != runID {
			return nil, fmt.Errorf("evaluation: ingest assignment audit proofs: proof %d spans runs %s and %s: %w", index, runID, input.RunID, constants.ErrEvidenceScopeMismatch)
		}
		published[input.AssignmentID] = CampaignPublishedProofArtifacts{
			DatabaseSHA256:      answer.DatabaseSHA256,
			AgentTraceSHA256:    input.Artifacts.AgentTraceSHA256,
			ScenarioInputSHA256: input.Artifacts.ScenarioInputSHA256,
			ScenarioGoldSHA256:  input.Artifacts.ScenarioGoldSHA256,
		}
	}
	if err := c.recordPublishedProofArtifacts(ctx, runID, published); err != nil {
		return nil, err
	}
	return published, nil
}

// ingestAssignmentAuditBindings ingests one terminal assignment's audit proof and
// returns the evidence bindings its result record cites. It returns nil when the
// assignment has no audit proof to publish.
func (c *CampaignPublicationCoordinator) ingestAssignmentAuditBindings(
	ctx context.Context,
	assignment *evalv1.EvaluationAssignment,
	result *evalv1.EvaluationAssignmentResult,
	fixtures map[string]ScenarioArtifacts,
	liveRequests []campaignFeedPublishRequest,
) ([]*evalv1.PublicEvidenceBinding, error) {
	input, ok, err := c.assignmentAuditProofInput(ctx, assignment, result, fixtures, liveRequests)
	if err != nil || !ok {
		return nil, err
	}
	published, err := c.ingestAssignmentAuditProofs(ctx, []AssignmentAuditProofInput{input})
	if err != nil {
		return nil, err
	}
	return AssignmentAuditEvidenceBindingsFromHashes(published[input.AssignmentID]), nil
}

// catchUpAuditProofs returns the audit proof artifacts, keyed by assignment ID,
// for every assignment whose result record catch-up still publishes. A stored
// proof is reused without contacting the Gateway only while its agent trace and
// scenario fixture are unchanged. Every other proof is ingested in one request
// before any result record is built.
func (c *CampaignPublicationCoordinator) catchUpAuditProofs(
	ctx context.Context,
	runID string,
	assignments []*evalv1.EvaluationAssignment,
	results map[string]*evalv1.EvaluationAssignmentResult,
	fixtures map[string]ScenarioArtifacts,
	publicationState *CampaignPublicationState,
) (map[string]CampaignPublishedProofArtifacts, error) {
	proofs := make(map[string]CampaignPublishedProofArtifacts, len(assignments))
	pending := make([]*evalv1.EvaluationAssignment, 0, len(assignments))
	for _, assignment := range assignments {
		if publicationState != nil && containsString(publicationState.PublishedIdempotency, AssignmentResultIdempotencyKey(runID, assignment.GetAssignmentId())) {
			continue
		}
		pending = append(pending, assignment)
	}
	// The Gateway builds the audit proof from the live event records it
	// retains, so every pending assignment's events are delivered before any
	// proof is reused or requested.
	liveRequests, err := c.publishAssignmentAuditEvents(ctx, runID, pending, results)
	if err != nil {
		return nil, err
	}
	inputs := make([]AssignmentAuditProofInput, 0, len(pending))
	for _, assignment := range pending {
		assignmentID := assignment.GetAssignmentId()
		result := results[assignmentID]
		if result == nil {
			continue
		}
		if publicationState != nil && containsString(publicationState.PublishedIdempotency, AssignmentAuditProofIdempotencyKey(runID, assignmentID)) {
			if stored, ok := publicationState.PublishedProofArtifacts[assignmentID]; ok {
				// Reuse the published package only when it already carries the
				// current agent trace and scenario fixture; otherwise ingest so
				// both are published and bound.
				_, traceSHA256, err := loadAssignmentAgentTraceArtifact(ctx, c.store, result)
				if err != nil {
					return nil, err
				}
				fixture := fixtures[assignment.GetScenarioId()]
				if stored.DatabaseSHA256 != "" && stored.AgentTraceSHA256 == traceSHA256 && stored.ScenarioInputSHA256 != "" &&
					stored.ScenarioInputSHA256 == fixture.Input.Reference.GetSha256() && stored.ScenarioGoldSHA256 == fixture.Gold.Reference.GetSha256() {
					proofs[assignmentID] = stored
					continue
				}
			}
		}
		input, ok, err := c.assignmentAuditProofInput(ctx, assignment, result, fixtures, liveRequests[assignmentID])
		if err != nil {
			return nil, err
		}
		if ok {
			inputs = append(inputs, input)
		}
	}
	ingested, err := c.ingestAssignmentAuditProofs(ctx, inputs)
	if err != nil {
		return nil, err
	}
	for assignmentID, artifacts := range ingested {
		proofs[assignmentID] = artifacts
	}
	return proofs, nil
}

// republishAuditProofs ingests the audit proof of every terminal assignment for
// the verified republish. The request is the one each assignment was published
// with, so the Gateway reuses its catalogued database and answers with the digest
// the republished result records bind. Nothing is rebuilt on the host.
func (c *CampaignPublicationCoordinator) republishAuditProofs(
	ctx context.Context,
	assignments []*evalv1.EvaluationAssignment,
	results map[string]*evalv1.EvaluationAssignmentResult,
	fixtures map[string]ScenarioArtifacts,
) (map[string]CampaignPublishedProofArtifacts, error) {
	if len(assignments) == 0 {
		return map[string]CampaignPublishedProofArtifacts{}, nil
	}
	liveRequests, err := c.publishAssignmentAuditEvents(ctx, assignments[0].GetRunId(), assignments, results)
	if err != nil {
		return nil, err
	}
	inputs := make([]AssignmentAuditProofInput, 0, len(assignments))
	for _, assignment := range assignments {
		result := results[assignment.GetAssignmentId()]
		if result == nil {
			continue
		}
		input, ok, err := c.assignmentAuditProofInput(ctx, assignment, result, fixtures, liveRequests[assignment.GetAssignmentId()])
		if err != nil {
			return nil, err
		}
		if ok {
			inputs = append(inputs, input)
		}
	}
	return c.ingestAssignmentAuditProofs(ctx, inputs)
}

// publishAssignmentAuditEvents builds the live event requests of every terminal
// assignment in one run and exports them to the Gateway in one feed export
// before any audit proof is requested. The Gateway indexes only the event
// records it was sent, so an execution that ran without publication never
// delivered them and the deferred publish must. Records already published are
// skipped by their idempotency keys. The result is keyed by assignment ID.
func (c *CampaignPublicationCoordinator) publishAssignmentAuditEvents(
	ctx context.Context,
	runID string,
	assignments []*evalv1.EvaluationAssignment,
	results map[string]*evalv1.EvaluationAssignmentResult,
) (map[string][]campaignFeedPublishRequest, error) {
	byAssignment := make(map[string][]campaignFeedPublishRequest, len(assignments))
	var all []campaignFeedPublishRequest
	for _, assignment := range assignments {
		result := results[assignment.GetAssignmentId()]
		if result == nil {
			continue
		}
		requests, err := c.buildAssignmentLiveEventPublishRequests(ctx, assignment, result)
		if err != nil {
			return nil, err
		}
		byAssignment[assignment.GetAssignmentId()] = requests
		all = append(all, requests...)
	}
	if _, err := c.exportFeedRecords(ctx, runID, all); err != nil {
		return nil, err
	}
	return byAssignment, nil
}

// AssignmentAuditEvidenceBindingsFromHashes returns public evidence bindings
// from the content-addressed proof hashes the Gateway answered. The agent trace
// binding is present only for assignments that persisted a trace, and the
// scenario fixture bindings only when both frozen fixture bodies were published.
// Without a database digest there is no audit proof to bind.
func AssignmentAuditEvidenceBindingsFromHashes(hashes CampaignPublishedProofArtifacts) []*evalv1.PublicEvidenceBinding {
	if hashes.DatabaseSHA256 == "" {
		return nil
	}
	bindings := []*evalv1.PublicEvidenceBinding{
		{Sha256: hashes.DatabaseSHA256, SchemaRef: assignmentAuditSchemaRef, Kind: AssignmentAuditSliceKind},
	}
	if hashes.AgentTraceSHA256 != "" {
		bindings = append(bindings, &evalv1.PublicEvidenceBinding{Sha256: hashes.AgentTraceSHA256, SchemaRef: assignmentAgentTraceRef, Kind: AssignmentAgentTraceKind})
	}
	if hashes.ScenarioInputSHA256 != "" && hashes.ScenarioGoldSHA256 != "" {
		bindings = append(bindings,
			&evalv1.PublicEvidenceBinding{Sha256: hashes.ScenarioInputSHA256, SchemaRef: scenarioInputFixtureRef, Kind: ScenarioInputFixtureKind},
			&evalv1.PublicEvidenceBinding{Sha256: hashes.ScenarioGoldSHA256, SchemaRef: scenarioGoldCriteriaRef, Kind: ScenarioGoldCriteriaKind},
		)
	}
	return bindings
}

// attachScenarioFixture adds the assignment scenario's frozen input fixture and
// gold criteria bodies to the package. Both bodies must still hash to the
// catalog references they were frozen under.
func (a *AssignmentAuditProofArtifacts) attachScenarioFixture(assignment *evalv1.EvaluationAssignment, fixtures map[string]ScenarioArtifacts) error {
	pair, ok := fixtures[assignment.GetScenarioId()]
	if !ok || !contentMatchesReference(pair.Input.Body, pair.Input.Reference) || !contentMatchesReference(pair.Gold.Body, pair.Gold.Reference) {
		return fmt.Errorf("evaluation: publish scenario fixture %s: %w", assignment.GetScenarioId(), constants.ErrEvidenceArtifactMalformed)
	}
	a.ScenarioID = assignment.GetScenarioId()
	a.ScenarioInput, a.ScenarioInputSHA256 = pair.Input.Body, pair.Input.Reference.GetSha256()
	a.ScenarioGold, a.ScenarioGoldSHA256 = pair.Gold.Body, pair.Gold.Reference.GetSha256()
	return nil
}

// loadAssignmentAgentTraceArtifact returns the exact persisted trace body and
// its SHA-256 for public proof publication. A missing trace is allowed only
// when the result does not reference one; a referenced trace must hash to the
// result's evidence ref.
func loadAssignmentAgentTraceArtifact(ctx context.Context, store CampaignStore, result *evalv1.EvaluationAssignmentResult) ([]byte, string, error) {
	referenced, referencedSHA256 := false, ""
	for _, ref := range result.GetEvidenceRefs() {
		if ref.GetArtifactType() == string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace) {
			referenced, referencedSHA256 = true, ref.GetSha256()
			break
		}
	}
	_, body, err := store.LoadAssignmentAgentTraceArtifact(ctx, result.GetRunId(), result.GetAssignmentId())
	if errors.Is(err, constants.ErrNotFound) && !referenced {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("evaluation: publish assignment agent trace: %w", err)
	}
	digest := hashBytes(body)
	if referenced && referencedSHA256 != digest {
		return nil, "", fmt.Errorf("evaluation: publish assignment agent trace: %w", constants.ErrEvidenceArtifactMalformed)
	}
	return body, digest, nil
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
