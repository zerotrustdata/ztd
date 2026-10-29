// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// CampaignInitRequest carries the frozen campaign inputs for controller startup.
type CampaignInitRequest struct {
	CampaignID                 string
	RunID                      string
	Catalog                    *evalv1.EvaluationScenarioCatalog
	Inventory                  *ModelInventoryFreeze
	ScenarioArtifacts          map[string]ScenarioArtifacts
	RepetitionCount            uint32
	HoldContext                bool
	Platform                   PlatformIdentity
	Investigation              *evalv1.EvaluationInvestigation
	InferenceOperatorSessionID string
	DataOperatorSessionID      string
	Deployment                 *evalv1.EvaluationDeploymentIdentity
	Lane                       evalv1.EvaluationLane
	FormationStackSet          *HeterogeneousStackSet
	Governance                 RunGovernance
	// Judge is the campaign's pinned judge; see CampaignCreateRequest.Judge.
	Judge *evalv1.ModelVariant
}

// CampaignRunSummary reports resumable controller state for one run.
type CampaignRunSummary struct {
	Run                *evalv1.EvaluationRun
	ExpectedAssignment uint64
	QueuedCount        uint32
	RunningCount       uint32
	TerminalCount      uint32
	// StoppedCount counts assignments stopped by a cancel before reaching a
	// terminal result. They are included in TerminalCount.
	StoppedCount     uint32
	NextAssignmentID string
}

// CampaignStore is the canonical persistence surface used by the campaign controller.
type CampaignStore interface {
	SaveProtoArtifact(context.Context, EvidenceScope, complianceevidence.ArtifactType, proto.Message) (*compliancev1.ComplianceEvidenceReference, error)
	LoadToolCallingProbeEvidence(context.Context, string, *compliancev1.ComplianceEvidenceReference) (*evalv1.ModelToolCallingProbeEvidence, error)
	SaveCampaignSpec(ctx context.Context, spec *evalv1.EvaluationCampaignSpec) error
	LoadCampaignSpec(ctx context.Context, campaignID string) (*evalv1.EvaluationCampaignSpec, error)
	LoadCampaignRelease(ctx context.Context, campaignID string) (CampaignRelease, error)
	SaveScenarioCatalog(ctx context.Context, campaignID string, catalog *evalv1.EvaluationScenarioCatalog) error
	LoadScenarioCatalog(ctx context.Context, campaignID string) (*evalv1.EvaluationScenarioCatalog, error)
	SaveScenarioArtifacts(ctx context.Context, campaignID string, catalog *evalv1.EvaluationScenarioCatalog, artifacts map[string]ScenarioArtifacts) error
	LoadScenarioArtifacts(ctx context.Context, campaignID string, catalog *evalv1.EvaluationScenarioCatalog) (map[string]ScenarioArtifacts, error)
	SaveRun(ctx context.Context, run *evalv1.EvaluationRun) error
	RunExists(ctx context.Context, runID string) (bool, error)
	LoadRun(ctx context.Context, runID string) (*evalv1.EvaluationRun, error)
	SaveAssignment(ctx context.Context, assignment *evalv1.EvaluationAssignment) error
	LoadAssignment(ctx context.Context, runID, assignmentID string) (*evalv1.EvaluationAssignment, error)
	ListAssignments(ctx context.Context, runID string) ([]*evalv1.EvaluationAssignment, error)
	AssignmentResultExists(ctx context.Context, runID, assignmentID string) (bool, error)
	AssignmentAgentTraceExists(ctx context.Context, runID, assignmentID string) (bool, error)
	LoadAssignmentResults(ctx context.Context, runID string, assignments []*evalv1.EvaluationAssignment) (map[string]*evalv1.EvaluationAssignmentResult, error)
	LoadAssignmentAgentTrace(ctx context.Context, runID, assignmentID string) (*AgentTrace, error)
	LoadAssignmentAgentTraceArtifact(ctx context.Context, runID, assignmentID string) (*AgentTrace, []byte, error)
	SaveAssignmentResult(ctx context.Context, result *evalv1.EvaluationAssignmentResult) error
	LoadAssignmentResult(ctx context.Context, runID, assignmentID string) (*evalv1.EvaluationAssignmentResult, error)
	SaveHeterogeneousStackSet(ctx context.Context, campaignID string, stackSet *HeterogeneousStackSet) error
	LoadHeterogeneousStackSet(ctx context.Context, campaignID string) (*HeterogeneousStackSet, error)
	LoadCampaignVerification(ctx context.Context, runID string) (*evalv1.EvaluationVerificationReport, error)
	ListRunIDs(ctx context.Context) ([]string, error)
}

// CampaignController owns deterministic scheduling, canonical assignment
// persistence, and resumable execution coordination for evaluation model campaigns.
type CampaignController struct {
	store       CampaignStore
	executor    CampaignAssignmentExecutor
	publication *CampaignPublicationCoordinator
	now         func() time.Time
	newID       func(string) string
}

func NewCampaignController(store CampaignStore, executor CampaignAssignmentExecutor, now func() time.Time, newID func(string) string) *CampaignController {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return prefix }
	}
	return &CampaignController{store: store, executor: executor, now: now, newID: newID}
}

// WithPublication attaches an optional publication coordinator used to emit
// typed public lifecycle projections after canonical state is persisted.
func (c *CampaignController) WithPublication(publication *CampaignPublicationCoordinator) *CampaignController {
	if c != nil {
		c.publication = publication
	}
	return c
}

// CampaignCreateRequest carries the inputs that define one frozen campaign.
type CampaignCreateRequest struct {
	CampaignID        string
	Catalog           *evalv1.EvaluationScenarioCatalog
	Inventory         *ModelInventoryFreeze
	ScenarioArtifacts map[string]ScenarioArtifacts
	RepetitionCount   uint32
	// HoldContext carries each cell's investigation from one repetition into
	// the next. It is frozen only when RepetitionCount > 1.
	HoldContext bool
	// Platform is the build that freezes the campaign; its release is digest-bound.
	Platform          PlatformIdentity
	Investigation     *evalv1.EvaluationInvestigation
	FormationStackSet *HeterogeneousStackSet
	// FormationCatalog is the catalog used to materialize and verify new stacks.
	// Nil selects the built-in defaults; execution uses only the frozen stack set.
	FormationCatalog *ExecutionTopologies
	// Judge is the registry model, resolved by ResolveCampaignJudge, that
	// scores every judged criterion of the campaign. Required.
	Judge *evalv1.ModelVariant
}

// RunStartRequest binds one new run to an existing campaign.
type RunStartRequest struct {
	CampaignID                 string
	RunID                      string
	InferenceOperatorSessionID string
	DataOperatorSessionID      string
	Deployment                 *evalv1.EvaluationDeploymentIdentity
	Lane                       evalv1.EvaluationLane
	ProviderObservationPolicy  evalv1.EvaluationWitnessPolicy
	ModelProvenancePolicy      evalv1.EvaluationWitnessPolicy
	// PlatformRelease is the executing build's release. It must equal the
	// release the campaign recorded when it froze.
	PlatformRelease string
	// Governance is the live Gateway posture and consensus binding, frozen
	// on the run. Every later execution must find the same.
	Governance RunGovernance
}

// CreateCampaign persists the frozen campaign spec, scenario catalog, and
// scenario artifacts. Creating a campaign that already exists with the same
// frozen spec is a no-op. A different spec under the same ID is a conflict.
func (c *CampaignController) CreateCampaign(ctx context.Context, req CampaignCreateRequest) (*evalv1.EvaluationCampaignSpec, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("evaluation: create campaign: %w", constants.ErrMissingRequiredField)
	}
	if req.CampaignID == "" || req.Catalog == nil || req.Inventory == nil {
		return nil, fmt.Errorf("evaluation: create campaign: %w", constants.ErrMissingRequiredField)
	}
	if err := ValidateCampaignJudge(req.Judge); err != nil {
		return nil, fmt.Errorf("evaluation: create campaign %q: %w", req.CampaignID, err)
	}
	spec, err := MaterializeCampaignSpecWithInvestigation(req.CampaignID, req.Catalog, req.Inventory, req.RepetitionCount, req.HoldContext, req.Platform, req.Investigation)
	if err != nil {
		return nil, err
	}
	spec.JudgeVariant = proto.Clone(req.Judge).(*evalv1.ModelVariant)
	if req.FormationStackSet != nil {
		if spec.GetRepetitionCount() != 1 || spec.GetHoldContext() {
			return nil, fmt.Errorf("evaluation: formation campaigns support one repetition: %w", constants.ErrEvaluationFlagsInvalid)
		}
		if err := ValidateFormationStackSetBinding(req.FormationStackSet, req.CampaignID, spec.GetModelRegistry()); err != nil {
			return nil, err
		}
		formationIDs := make([]string, 0, len(req.FormationStackSet.Stacks))
		for _, stack := range req.FormationStackSet.Stacks {
			formationIDs = append(formationIDs, stack.GetStackId())
		}
		expected, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
			CampaignID: req.CampaignID, Variants: spec.GetModelRegistry(), Seed: req.FormationStackSet.Seed, FormationIDs: formationIDs,
			Catalog: req.FormationCatalog,
		})
		if err != nil {
			return nil, err
		}
		if expected.SetDigest != req.FormationStackSet.SetDigest {
			return nil, fmt.Errorf("evaluation: create campaign: formation set differs from catalog: %w", constants.ErrFormationStackMismatch)
		}
		spec.FormationStackSetDigest = req.FormationStackSet.SetDigest
	}
	spec.CampaignDigest, err = ComputeCampaignSpecDigest(spec)
	if err != nil {
		return nil, err
	}
	existing, err := c.store.LoadCampaignSpec(ctx, req.CampaignID)
	switch {
	case err == nil:
		if existing.GetCatalogDigest() != spec.GetCatalogDigest() ||
			existing.GetModelRegistryDigest() != spec.GetModelRegistryDigest() ||
			existing.GetRepetitionCount() != spec.GetRepetitionCount() ||
			existing.GetHoldContext() != spec.GetHoldContext() ||
			existing.GetFormationStackSetDigest() != spec.GetFormationStackSetDigest() ||
			!proto.Equal(existing.GetInvestigation(), spec.GetInvestigation()) ||
			!proto.Equal(existing.GetJudgeVariant(), spec.GetJudgeVariant()) ||
			(existing.GetPlatformRelease() != "" && existing.GetPlatformRelease() != spec.GetPlatformRelease()) {
			return nil, fmt.Errorf("evaluation: create campaign %q: %w", req.CampaignID, constants.ErrEvaluationCampaignConflict)
		}
		if err := ValidateCampaignSpecDigest(existing); err != nil {
			return nil, fmt.Errorf("evaluation: create campaign %q: %w", req.CampaignID, err)
		}
		if req.FormationStackSet != nil {
			frozen, err := c.store.LoadHeterogeneousStackSet(ctx, req.CampaignID)
			if err != nil {
				return nil, err
			}
			if frozen.SetDigest != existing.GetFormationStackSetDigest() {
				return nil, fmt.Errorf("evaluation: create campaign: persisted formation set differs from spec: %w", constants.ErrFormationStackMismatch)
			}
		}
		return existing, nil
	case !isNotFound(err):
		return nil, err
	}
	if req.FormationStackSet == nil && len(spec.GetModelRegistry()) != 1 {
		return nil, fmt.Errorf("evaluation: create campaign: multiple models require frozen formations: %w", constants.ErrEvaluationCampaignSubjectInvalid)
	}
	if err := c.store.SaveScenarioCatalog(ctx, req.CampaignID, req.Catalog); err != nil {
		return nil, err
	}
	if err := c.store.SaveScenarioArtifacts(ctx, req.CampaignID, req.Catalog, req.ScenarioArtifacts); err != nil {
		return nil, err
	}
	if req.FormationStackSet != nil {
		if err := c.store.SaveHeterogeneousStackSet(ctx, req.CampaignID, req.FormationStackSet); err != nil {
			return nil, err
		}
	}
	// Publish the frozen spec only after every artifact it references exists.
	if err := c.store.SaveCampaignSpec(ctx, spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// StartRun persists a new run record bound to an existing campaign.
func (c *CampaignController) StartRun(ctx context.Context, req RunStartRequest) (*evalv1.EvaluationRun, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("evaluation: start run: %w", constants.ErrMissingRequiredField)
	}
	if req.CampaignID == "" || req.RunID == "" || req.PlatformRelease == "" {
		return nil, fmt.Errorf("evaluation: start run: %w", constants.ErrMissingRequiredField)
	}
	if !validWitnessPolicy(req.ProviderObservationPolicy) || !validWitnessPolicy(req.ModelProvenancePolicy) {
		return nil, fmt.Errorf("evaluation: start run witness policies: %w", constants.ErrEvaluationFlagsInvalid)
	}
	spec, err := c.store.LoadCampaignSpec(ctx, req.CampaignID)
	if err != nil {
		return nil, err
	}
	if err := ValidateCampaignSpecDigest(spec); err != nil {
		return nil, fmt.Errorf("evaluation: start run: campaign spec: %w", err)
	}
	// Campaigns frozen before ztd v1.0.0 recorded no release; their asserted tag
	// is not a digest-bound fact, so only a recorded release is enforced.
	if recorded := spec.GetPlatformRelease(); recorded != "" && recorded != req.PlatformRelease {
		return nil, fmt.Errorf("evaluation: start run %q: campaign %q recorded %q, executing build is %q: %w", req.RunID, req.CampaignID, recorded, req.PlatformRelease, constants.ErrEvaluationPlatformReleaseMismatch)
	}
	if err := req.Governance.validate(); err != nil {
		return nil, fmt.Errorf("evaluation: start run %q: %w", req.RunID, err)
	}
	catalog, err := c.store.LoadScenarioCatalog(ctx, req.CampaignID)
	if err != nil {
		return nil, err
	}
	// A catalog frozen before judge grading was removed would terminate every
	// judge-graded cell GRADER_FAILED regardless of the model's answer.
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetGradingMethod() == evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE {
			return nil, fmt.Errorf("evaluation: start run %q: campaign %q scenario %s: %w", req.RunID, req.CampaignID, scenario.GetScenarioId(), constants.ErrEvaluationCatalogRequiresJudge)
		}
	}
	if exists, err := c.store.RunExists(ctx, req.RunID); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("evaluation: start run %q: run already exists", req.RunID)
	}
	lane := req.Lane
	if lane == evalv1.EvaluationLane_EVALUATION_LANE_UNSPECIFIED {
		lane = evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE
	}
	if lane == evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE && len(spec.GetModelRegistry()) != 1 {
		return nil, fmt.Errorf("evaluation: start run %q: campaign %q freezes %d models: %w", req.RunID, req.CampaignID, len(spec.GetModelRegistry()), constants.ErrEvaluationCampaignSubjectInvalid)
	}
	if lane == evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM {
		if spec.GetRepetitionCount() != 1 || spec.GetHoldContext() {
			return nil, fmt.Errorf("evaluation: formation campaigns support one repetition: %w", constants.ErrEvaluationFlagsInvalid)
		}
		set, err := c.store.LoadHeterogeneousStackSet(ctx, req.CampaignID)
		if err != nil {
			return nil, err
		}
		if err := validateCampaignFormationDigest(spec, set); err != nil {
			return nil, err
		}
	} else if spec.GetFormationStackSetDigest() != "" {
		return nil, fmt.Errorf("evaluation: start run: formation campaign requires system lane: %w", constants.ErrEvaluationTargetKindMismatch)
	}
	run := &evalv1.EvaluationRun{
		SchemaVersion:             CampaignSchemaVersion,
		RunId:                     req.RunID,
		SuiteRef:                  catalog.GetCatalogRef(),
		Deployment:                req.Deployment,
		ActivePosture:             req.Governance.Posture,
		Consensus:                 proto.Clone(req.Governance.Consensus).(*evalv1.EvaluationConsensusIdentity),
		Lane:                      lane,
		ProviderObservationPolicy: req.ProviderObservationPolicy,
		ModelProvenancePolicy:     req.ModelProvenancePolicy,
		StartedAt:                 timestamppb.New(c.now().UTC()),
		CampaignBinding: &evalv1.ModelCampaignBinding{
			CampaignId:                 req.CampaignID,
			CampaignDigest:             spec.GetCampaignDigest(),
			CatalogRef:                 catalog.GetCatalogRef(),
			CatalogDigest:              catalog.GetCatalogDigest(),
			ModelRegistryDigest:        spec.GetModelRegistryDigest(),
			InferenceOperatorSessionId: req.InferenceOperatorSessionID,
			DataOperatorSessionId:      req.DataOperatorSessionID,
		},
	}
	if err := c.store.SaveRun(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

func validWitnessPolicy(policy evalv1.EvaluationWitnessPolicy) bool {
	return policy == evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM || policy == evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT
}

// InitializeCampaign creates a campaign and starts its first run.
func (c *CampaignController) InitializeCampaign(ctx context.Context, req CampaignInitRequest) (*evalv1.EvaluationRun, error) {
	if req.RunID == "" {
		return nil, fmt.Errorf("evaluation: initialize campaign: %w", constants.ErrMissingRequiredField)
	}
	if _, err := c.CreateCampaign(ctx, CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		HoldContext:       req.HoldContext,
		Platform:          req.Platform,
		Investigation:     req.Investigation,
		FormationStackSet: req.FormationStackSet,
		Judge:             req.Judge,
	}); err != nil {
		return nil, err
	}
	return c.StartRun(ctx, RunStartRequest{
		CampaignID:                 req.CampaignID,
		RunID:                      req.RunID,
		InferenceOperatorSessionID: req.InferenceOperatorSessionID,
		DataOperatorSessionID:      req.DataOperatorSessionID,
		Deployment:                 req.Deployment,
		Lane:                       req.Lane,
		PlatformRelease:            req.Platform.Release,
		ProviderObservationPolicy:  evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
		ModelProvenancePolicy:      evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
		Governance:                 req.Governance,
	})
}

// CancelRun stops every assignment of a run that has no terminal record: queued
// assignments, and a running assignment whose executor is gone. It returns the
// number of assignments stopped. The caller must have drained any live
// executor first, so a running assignment here is unresolved.
func (c *CampaignController) CancelRun(ctx context.Context, runID string) (int, error) {
	if c == nil || c.store == nil {
		return 0, fmt.Errorf("evaluation: cancel run: %w", constants.ErrMissingRequiredField)
	}
	assignments, err := c.store.ListAssignments(ctx, runID)
	if err != nil {
		return 0, err
	}
	stopped := 0
	for _, assignment := range assignments {
		if exists, err := c.store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId()); err != nil {
			return stopped, err
		} else if exists {
			continue
		}
		switch assignment.GetLifecycleStatus() {
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED,
			evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING:
			assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED
			assignment.CompletedAt = timestamppb.New(c.now().UTC())
			if err := c.store.SaveAssignment(ctx, assignment); err != nil {
				return stopped, err
			}
			stopped++
		}
	}
	return stopped, nil
}

// ScheduleRun materializes and persists the complete assignment matrix of one
// run before any scored call begins.
func (c *CampaignController) ScheduleRun(ctx context.Context, runID string) (int, error) {
	if c == nil || c.store == nil {
		return 0, fmt.Errorf("evaluation: schedule run: %w", constants.ErrMissingRequiredField)
	}
	run, err := c.store.LoadRun(ctx, runID)
	if err != nil {
		return 0, err
	}
	spec, err := c.store.LoadCampaignSpec(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return 0, err
	}
	if err := (RunGovernance{Posture: run.GetActivePosture(), Consensus: run.GetConsensus()}).validate(); err != nil {
		return 0, fmt.Errorf("evaluation: schedule run %s: %w", runID, err)
	}
	if err := ValidateCampaignSpecDigest(spec); err != nil {
		return 0, fmt.Errorf("evaluation: schedule run %s: campaign spec: %w", runID, err)
	}
	if spec.GetCampaignDigest() != run.GetCampaignBinding().GetCampaignDigest() ||
		spec.GetModelRegistryDigest() != run.GetCampaignBinding().GetModelRegistryDigest() ||
		spec.GetCatalogDigest() != run.GetCampaignBinding().GetCatalogDigest() {
		return 0, fmt.Errorf("evaluation: schedule run %s: frozen campaign does not match run binding: %w", runID, constants.ErrChecksumMismatch)
	}
	catalog, err := c.store.LoadScenarioCatalog(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return 0, err
	}
	var assignments []*evalv1.EvaluationAssignment
	switch run.GetLane() {
	case evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM:
		stackSet, err := c.store.LoadHeterogeneousStackSet(ctx, spec.GetCampaignId())
		if err != nil {
			return 0, fmt.Errorf("evaluation: schedule run %s: frozen formations: %w", runID, err)
		}
		if err := validateCampaignFormationDigest(spec, stackSet); err != nil {
			return 0, fmt.Errorf("evaluation: schedule run %s: frozen formations: %w", runID, err)
		}
		assignments, err = BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
			CampaignID: spec.GetCampaignId(), RunID: runID, Catalog: catalog, ActivePosture: run.GetActivePosture(), StackSet: stackSet, QueuedAt: c.now().UTC(),
		})
		if err != nil {
			return 0, err
		}
		if err := ValidateHeterogeneousAssignmentMatrix(catalog, stackSet, assignments, run.GetActivePosture()); err != nil {
			return 0, fmt.Errorf("evaluation: schedule run %s: assignment matrix: %w", runID, err)
		}
	case evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE:
		assignments, err = BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
			CampaignID:      spec.GetCampaignId(),
			RunID:           runID,
			Catalog:         catalog,
			ActivePosture:   run.GetActivePosture(),
			Variants:        spec.GetModelRegistry(),
			RepetitionCount: spec.GetRepetitionCount(),
			QueuedAt:        c.now().UTC(),
		})
		if err != nil {
			return 0, err
		}
		expected := ModelRoleMatrixSize(catalog, uint64(len(spec.GetModelRegistry())), spec.GetRepetitionCount(), run.GetActivePosture())
		if uint64(len(assignments)) != expected {
			return 0, fmt.Errorf("evaluation: schedule run %s: %d model-role assignments, expected %d: %w", runID, len(assignments), expected, constants.ErrEvaluationTargetKindMismatch)
		}
	default:
		return 0, fmt.Errorf("evaluation: schedule run %s: lane %s: %w", runID, run.GetLane(), constants.ErrEvaluationTargetKindMismatch)
	}
	existing, err := c.store.ListAssignments(ctx, runID)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, fmt.Errorf("evaluation: schedule run: assignments already materialized for run %s", runID)
	}
	for _, assignment := range assignments {
		if err := c.store.SaveAssignment(ctx, assignment); err != nil {
			return 0, err
		}
	}
	if err := c.publishQueuedAssignments(ctx, runID, assignments); err != nil {
		return 0, err
	}
	return len(assignments), nil
}

// RunSummary returns resumable controller state derived from canonical records.
func (c *CampaignController) RunSummary(ctx context.Context, runID string) (*CampaignRunSummary, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("evaluation: campaign run summary: %w", constants.ErrMissingRequiredField)
	}
	run, err := c.store.LoadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	assignments, err := c.store.ListAssignments(ctx, runID)
	if err != nil {
		return nil, err
	}
	spec, err := c.store.LoadCampaignSpec(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return nil, err
	}
	catalog, err := c.store.LoadScenarioCatalog(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return nil, err
	}
	var expected uint64
	switch run.GetLane() {
	case evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM:
		stackSet, err := c.store.LoadHeterogeneousStackSet(ctx, run.GetCampaignBinding().GetCampaignId())
		if err != nil {
			return nil, err
		}
		expected = FormationMatrixSize(catalog, uint64(len(stackSet.Stacks)), run.GetActivePosture())
	default:
		expected = ModelRoleMatrixSize(catalog, uint64(len(spec.GetModelRegistry())), spec.GetRepetitionCount(), run.GetActivePosture())
	}
	summary := &CampaignRunSummary{
		Run:                run,
		ExpectedAssignment: expected,
	}
	var nextID string
	for _, assignment := range assignments {
		terminal, err := c.store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId())
		if err != nil {
			return nil, err
		}
		if terminal {
			summary.TerminalCount++
			continue
		}
		switch assignment.GetLifecycleStatus() {
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED:
			summary.QueuedCount++
			if nextID == "" {
				nextID = assignment.GetAssignmentId()
			}
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING:
			summary.RunningCount++
			if nextID == "" {
				nextID = assignment.GetAssignmentId()
			}
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED:
			summary.StoppedCount++
			summary.TerminalCount++
		default:
			summary.TerminalCount++
		}
	}
	summary.NextAssignmentID = nextID
	return summary, nil
}

// ResumeNextAssignment returns the next queued assignment that lacks a
// terminal persisted result. An unresolved running assignment fails closed;
// completed assignments are never re-selected.
func (c *CampaignController) ResumeNextAssignment(ctx context.Context, runID string) (*evalv1.EvaluationAssignment, bool, error) {
	if c == nil || c.store == nil {
		return nil, false, fmt.Errorf("evaluation: resume next assignment: %w", constants.ErrMissingRequiredField)
	}
	assignments, err := c.store.ListAssignments(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	for _, assignment := range assignments {
		exists, err := c.store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId())
		if err != nil {
			return nil, false, err
		}
		if exists {
			continue
		}
		switch assignment.GetLifecycleStatus() {
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED:
			return assignment, true, nil
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING:
			return nil, false, fmt.Errorf("evaluation: resume next assignment %s: %w", assignment.GetAssignmentId(), constants.ErrEvaluationAssignmentUnresolved)
		}
	}
	return nil, false, nil
}

// ExecuteNextAssignment resumes one assignment through the configured executor,
// persists the terminal result, and updates assignment lifecycle state. A
// publication failure after the result is persisted returns the result with an
// ErrEvaluationPublicationFailed error, so the caller can continue the run.
func (c *CampaignController) ExecuteNextAssignment(ctx context.Context, runID string, binding CampaignExecutionBinding, artifacts map[string]ScenarioArtifacts) (*evalv1.EvaluationAssignmentResult, bool, error) {
	if c == nil || c.executor == nil {
		return nil, false, fmt.Errorf("evaluation: execute next assignment: executor: %w", constants.ErrMissingRequiredField)
	}
	assignment, ok, err := c.ResumeNextAssignment(ctx, runID)
	if err != nil || !ok {
		return nil, ok, err
	}
	run, err := c.store.LoadRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	campaignSpec, err := c.store.LoadCampaignSpec(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return nil, false, err
	}
	if err := ValidateCampaignSpecDigest(campaignSpec); err != nil {
		return nil, false, fmt.Errorf("evaluation: execute next assignment: campaign spec: %w", err)
	}
	if campaignSpec.GetCampaignDigest() != run.GetCampaignBinding().GetCampaignDigest() ||
		campaignSpec.GetModelRegistryDigest() != run.GetCampaignBinding().GetModelRegistryDigest() ||
		campaignSpec.GetCatalogDigest() != run.GetCampaignBinding().GetCatalogDigest() {
		return nil, false, fmt.Errorf("evaluation: execute next assignment: campaign spec does not match run binding: %w", constants.ErrChecksumMismatch)
	}
	if binding.ModelRegistryDigest != campaignSpec.GetModelRegistryDigest() {
		return nil, false, fmt.Errorf("evaluation: execute next assignment: execution binding registry does not match frozen campaign: %w", constants.ErrChecksumMismatch)
	}
	var formationStackSet *HeterogeneousStackSet
	if run.GetLane() == evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM {
		formationStackSet, err = c.store.LoadHeterogeneousStackSet(ctx, campaignSpec.GetCampaignId())
		if err != nil {
			return nil, false, fmt.Errorf("evaluation: execute next assignment: frozen formations: %w", err)
		}
		if err := validateCampaignFormationDigest(campaignSpec, formationStackSet); err != nil {
			return nil, false, fmt.Errorf("evaluation: execute next assignment: frozen formations: %w", err)
		}
		stack, err := HeterogeneousStackFromAssignment(assignment)
		if err != nil {
			return nil, false, fmt.Errorf("evaluation: execute next assignment: formation assignment: %w", err)
		}
		if !formationStackSetContains(formationStackSet, stack) {
			return nil, false, fmt.Errorf("evaluation: execute next assignment: assignment formation is not in frozen set: %w", constants.ErrFormationStackMismatch)
		}
	}
	if exists, err := c.store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId()); err != nil {
		return nil, false, err
	} else if exists {
		return nil, false, nil
	}
	assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING
	assignment.StartedAt = timestamppb.New(c.now().UTC())
	if err := c.store.SaveAssignment(ctx, assignment); err != nil {
		return nil, false, err
	}
	// The assignment is RUNNING once saved; a start publication failure must not
	// strand it there, so it is carried to the terminal result instead.
	startPublishErr := c.publishAssignmentLifecycle(ctx, assignment)
	if startPublishErr == nil && !IsHeterogeneousAssignment(assignment) {
		startPublishErr = c.publishAssignmentInvocationLiveEvents(ctx, assignment)
	}
	if startPublishErr != nil {
		startPublishErr = fmt.Errorf("%w: assignment %s start: %w", constants.ErrEvaluationPublicationFailed, assignment.GetAssignmentId(), startPublishErr)
	}
	execReq, err := c.frozenAssignmentRequest(ctx, campaignSpec, binding, formationStackSet, artifacts, assignment, c.newID("attempt"))
	if err != nil {
		return nil, false, fmt.Errorf("evaluation: execute next assignment: %w", err)
	}
	var result *evalv1.EvaluationAssignmentResult
	result, err = c.toolCallingGate(ctx, run, execReq)
	if err == nil && result == nil {
		execReq.HeldContext, err = c.heldAssignmentContext(ctx, run.GetCampaignBinding().GetCampaignId(), assignment)
	}
	if err == nil && result == nil {
		result, err = c.executor.ExecuteAssignment(ctx, execReq)
	}
	if err != nil {
		if !isRecoverableAssignmentExecutionError(err) {
			return nil, false, fmt.Errorf("evaluation: execute next assignment: %w", err)
		}
		failureResult, buildErr := c.buildFailureAssignmentResult(ctx, execReq, err)
		if buildErr != nil {
			return nil, false, fmt.Errorf("evaluation: execute next assignment: %w", buildErr)
		}
		return persistedTerminalResult(failureResult, startPublishErr, c.persistTerminalAssignment(ctx, assignment, failureResult))
	}
	if result == nil {
		return nil, false, fmt.Errorf("evaluation: execute next assignment: executor returned nil result: %w", constants.ErrEvaluationAssignmentExecutionFailed)
	}
	return persistedTerminalResult(result, startPublishErr, c.persistTerminalAssignment(ctx, assignment, result))
}

// persistedTerminalResult reports a persisted result as executed even when its
// publication failed; only a persistence failure withholds it.
func persistedTerminalResult(result *evalv1.EvaluationAssignmentResult, startPublishErr, persistErr error) (*evalv1.EvaluationAssignmentResult, bool, error) {
	if persistErr != nil && !errors.Is(persistErr, constants.ErrEvaluationPublicationFailed) {
		return nil, false, persistErr
	}
	return result, true, errors.Join(startPublishErr, persistErr)
}

// frozenAssignmentRequest builds the request one assignment executes and is
// graded under from its campaign's frozen spec, scenario catalog and artifacts.
func (c *CampaignController) frozenAssignmentRequest(ctx context.Context, spec *evalv1.EvaluationCampaignSpec, binding CampaignExecutionBinding, formationStackSet *HeterogeneousStackSet, artifacts map[string]ScenarioArtifacts, assignment *evalv1.EvaluationAssignment, attemptID string) (AssignmentExecutionRequest, error) {
	artifact, found := artifacts[assignment.GetScenarioId()]
	if !found {
		return AssignmentExecutionRequest{}, fmt.Errorf("missing scenario artifacts for %s: %w", assignment.GetScenarioId(), constants.ErrMissingRequiredField)
	}
	var scenarioInput ScenarioInputFixture
	if err := json.Unmarshal(artifact.Input.Body, &scenarioInput); err != nil {
		return AssignmentExecutionRequest{}, fmt.Errorf("decode scenario input: %w", err)
	}
	var scenarioGold ScenarioGoldCriteria
	if err := json.Unmarshal(artifact.Gold.Body, &scenarioGold); err != nil {
		return AssignmentExecutionRequest{}, fmt.Errorf("decode scenario gold: %w", err)
	}
	catalog, err := c.store.LoadScenarioCatalog(ctx, spec.GetCampaignId())
	if err != nil {
		return AssignmentExecutionRequest{}, err
	}
	gradingMethod, err := scenarioGradingMethodForAssignment(catalog, assignment)
	if err != nil {
		return AssignmentExecutionRequest{}, err
	}
	scenarioTools, err := scenarioToolsForAssignment(catalog, assignment)
	if err != nil {
		return AssignmentExecutionRequest{}, err
	}
	requiredConcepts, err := scenarioRequiredConceptsForAssignment(catalog, assignment)
	if err != nil {
		return AssignmentExecutionRequest{}, err
	}
	req := AssignmentExecutionRequest{
		Assignment:        assignment,
		FormationStackSet: formationStackSet,
		Investigation:     spec.GetInvestigation(),
		AttemptID:         attemptID,
		ScenarioInput:     scenarioInput,
		ScenarioGold:      scenarioGold,
		ScenarioTools:     scenarioTools,
		RequiredConcepts:  requiredConcepts,
		GradingMethod:     gradingMethod,
		Binding:           binding,
	}
	req.Binding.ModelRegistryDigest = spec.GetModelRegistryDigest()
	req.Binding.ModelVariants = spec.GetModelRegistry()
	req.Binding.ModelRegistry = InferenceVariantsFromEvalRegistry(spec.GetModelRegistry())
	req.Binding.FormationStackSetDigest = spec.GetFormationStackSetDigest()
	req.Binding.Personas = catalog.GetPersonas()
	req.Binding.ToolCallingProbeRequired = catalog.GetToolCallingProbeRequired()
	req.Binding.JudgeVariant = spec.GetJudgeVariant()
	return req, nil
}

func scenarioGradingMethodForAssignment(catalog *evalv1.EvaluationScenarioCatalog, assignment *evalv1.EvaluationAssignment) (evalv1.EvaluationGradingMethod, error) {
	if catalog == nil || assignment == nil || assignment.GetScenarioId() == "" {
		return evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_UNSPECIFIED, fmt.Errorf("evaluation: scenario grading method lookup: %w", constants.ErrMissingRequiredField)
	}
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == assignment.GetScenarioId() {
			return scenario.GetGradingMethod(), nil
		}
	}
	return evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_UNSPECIFIED, fmt.Errorf("evaluation: scenario grading method lookup: unknown scenario %s", assignment.GetScenarioId())
}

func scenarioRequiredConceptsForAssignment(catalog *evalv1.EvaluationScenarioCatalog, assignment *evalv1.EvaluationAssignment) ([]string, error) {
	if catalog == nil || assignment == nil || assignment.GetScenarioId() == "" {
		return nil, fmt.Errorf("evaluation: scenario concept lookup: %w", constants.ErrMissingRequiredField)
	}
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == assignment.GetScenarioId() {
			return append([]string(nil), scenario.GetRequiredConcepts()...), nil
		}
	}
	return nil, fmt.Errorf("evaluation: scenario concept lookup: unknown scenario %s", assignment.GetScenarioId())
}

func scenarioToolsForAssignment(catalog *evalv1.EvaluationScenarioCatalog, assignment *evalv1.EvaluationAssignment) (ScenarioToolExpectations, error) {
	if catalog == nil || assignment == nil || assignment.GetScenarioId() == "" {
		return ScenarioToolExpectations{}, fmt.Errorf("evaluation: scenario tool lookup: %w", constants.ErrMissingRequiredField)
	}
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == assignment.GetScenarioId() {
			tools := ScenarioToolExpectations{
				AllowedTools:     append([]string(nil), scenario.GetAllowedTools()...),
				ExpectedTools:    append([]string(nil), scenario.GetExpectedTools()...),
				ForbiddenTools:   append([]string(nil), scenario.GetForbiddenTools()...),
				TrajectoryPolicy: scenario.GetTrajectoryPolicy(),
				TribunalCommands: scenario.GetTribunalCommands(),
			}
			role, err := AssignmentDesignatedRole(assignment)
			if err != nil {
				return ScenarioToolExpectations{}, err
			}
			if role == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE {
				tools.ContextOnly = true
			}
			return tools, nil
		}
	}
	return ScenarioToolExpectations{}, fmt.Errorf("evaluation: scenario tool lookup: unknown scenario %s", assignment.GetScenarioId())
}

func (c *CampaignController) publishQueuedAssignments(ctx context.Context, runID string, assignments []*evalv1.EvaluationAssignment) error {
	if c == nil || c.publication == nil {
		return nil
	}
	run, err := c.store.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	catalog, err := c.store.LoadScenarioCatalog(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return err
	}
	if err := c.publication.PublishAssignmentLifecycles(ctx, runID, assignments, catalog); err != nil {
		return err
	}
	_, err = c.publication.PublishRunAggregates(ctx, runID, c.now().UTC())
	return err
}

func (c *CampaignController) publishAssignmentInvocationLiveEvents(ctx context.Context, assignment *evalv1.EvaluationAssignment) error {
	if c == nil || c.publication == nil || assignment == nil {
		return nil
	}
	return c.publication.PublishAssignmentInvocationLiveEvents(ctx, assignment)
}

func (c *CampaignController) publishAssignmentLifecycle(ctx context.Context, assignment *evalv1.EvaluationAssignment) error {
	if c == nil || c.publication == nil || assignment == nil {
		return nil
	}
	run, err := c.store.LoadRun(ctx, assignment.GetRunId())
	if err != nil {
		return err
	}
	catalog, err := c.store.LoadScenarioCatalog(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return err
	}
	category, err := ScenarioCategoryForAssignment(catalog, assignment)
	if err != nil {
		return err
	}
	return c.publication.PublishAssignmentLifecycle(ctx, assignment, category, assignmentLifecycleObservedAt(assignment))
}

func (c *CampaignController) publishAssignmentTerminal(ctx context.Context, assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) error {
	if c == nil || c.publication == nil || assignment == nil || result == nil {
		return nil
	}
	if err := c.publication.PublishAssignmentLiveEvents(ctx, assignment, result); err != nil {
		return err
	}
	if err := c.publishAssignmentLifecycle(ctx, assignment); err != nil {
		return err
	}
	run, err := c.store.LoadRun(ctx, assignment.GetRunId())
	if err != nil {
		return err
	}
	catalog, err := c.store.LoadScenarioCatalog(ctx, run.GetCampaignBinding().GetCampaignId())
	if err != nil {
		return err
	}
	category, err := ScenarioCategoryForAssignment(catalog, assignment)
	if err != nil {
		return err
	}
	return c.publication.PublishAssignmentResult(ctx, assignment, result, category, "unverified")
}
