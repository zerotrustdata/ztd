// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/investigation"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	GraderID      = "ztd-native-deterministic-grader"
	GraderVersion = "1.18.0"

	// DefaultSuiteID and DefaultSuiteVersion identify the built-in suite every
	// campaign freezes unless it names another one.
	DefaultSuiteID             = "default-suite"
	DefaultSuiteVersion        = "1.28.0"
	SmokeSuiteVersion          = "1.27.0"
	InvestigationsSuiteVersion = "1.26.0"

	// LegacyDefaultSuiteID is the catalog id the built-in suite carried before
	// it was renamed. Campaigns frozen under it (north-star-25@1.0.0) exist on
	// disk, so it stays reserved and is treated as the built-in suite's lineage.
	LegacyDefaultSuiteID = "north-star-25"

	scenarioSyntheticLabel     = "SYNTHETIC - controlled evaluation fixture"
	scenarioInputSchemaVersion = "1.3.0"
	scenarioGoldSchemaVersion  = "1.3.0"
	scenarioInputSchemaRef     = "evaluation-scenario-input@1.3.0"
	scenarioGoldSchemaRef      = "evaluation-scenario-gold@1.3.0"

	// ScenarioWorkspaceToken is the typed template variable a prompt, seed, or
	// argument validator uses for the attempt-scoped fixture workspace root.
	// It is replaced with the absolute root before a prompt is sent or a
	// validator runs; nothing else is templated.
	ScenarioWorkspaceToken = "{{workspace}}"
)

// ScenarioDiagnosisRubric selects a versioned deterministic diagnosis rule
// frozen as part of a scenario's gold content check.
type ScenarioDiagnosisRubric string

const (
	ScenarioDiagnosisRubricCheckoutTimeoutV1 ScenarioDiagnosisRubric = "checkout-timeout-v1"
	ScenarioDiagnosisRubricCheckoutTimeoutV2 ScenarioDiagnosisRubric = "checkout-timeout-v2"
	ScenarioDiagnosisRubricCheckoutTimeoutV3 ScenarioDiagnosisRubric = "checkout-timeout-v3"
	ScenarioDiagnosisRubricCheckoutTimeoutV4 ScenarioDiagnosisRubric = "checkout-timeout-v4"
	ScenarioDiagnosisRubricCheckoutTimeoutV5 ScenarioDiagnosisRubric = "checkout-timeout-v5"
)

// ScenarioGradingContract selects the versioned grading rules frozen with a
// scenario's gold. The empty contract is the original one; it stays so a
// campaign frozen before a later contract regrades exactly as it was graded.
type ScenarioGradingContract string

const (
	// ScenarioGradingContractV2 counts a Lite cell's TriageResult toward its
	// verdict (the classification is the cell's whole task) and renders the
	// content judge's objective from the typed judge.Objective (criterion-v2):
	// the seed conversation, the platform records the model was shown, and
	// every receipted tool result it observed.
	ScenarioGradingContractV2 ScenarioGradingContract = "grading-contract.v2"
	// ScenarioGradingContractV3 inherits v2 and validates Interrogator questions
	// as closed questions rather than rejecting every use of the word "or".
	ScenarioGradingContractV3 ScenarioGradingContract = "grading-contract.v3"
)

// scenarioCatalogFreezeTime is the fixed production and verification time of
// every catalog fixture artifact, so identical suite content always yields
// identical content addresses.
var scenarioCatalogFreezeTime = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// ScenarioInlineContent is synthetic content the scenario expects the model to
// reason over in the same turn. It is rendered as a labelled block beneath the
// user prompt in the outgoing chat message (see renderScenarioMessage); there
// is no separate attachment channel on this path.
type ScenarioInlineContent struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Content string `json:"content"`
}

// ScenarioWorkspaceFile is one fixture file materialized under the
// attempt-scoped workspace on the bound Data Operator before the scored chat
// request is sent (one governed write per file, each with a receipt). RelPath
// is slash-separated and relative to the workspace root. Decoy files exist so
// a search can be wrong. Responder roles retrieve content with tools. Lite
// receives the frozen fixture content as labelled user evidence because it has
// no tool surface.
type ScenarioWorkspaceFile struct {
	Label   string `json:"label"`
	RelPath string `json:"rel_path"`
	Content string `json:"content"`
	Decoy   bool   `json:"decoy,omitempty"`
}

// InvestigationSeedTurn is one prior conversation turn. Sender is "user",
// "primary", or "assistant".
type InvestigationSeedTurn struct {
	Sender  string `json:"sender"`
	Content string `json:"content"`
}

// InvestigationSeedMemory is the case memory of the seeded investigation.
type InvestigationSeedMemory struct {
	InvestigationSummary     string `json:"investigation_summary,omitempty"`
	CommunicationPreferences string `json:"communication_preferences,omitempty"`
	TechnicalBackground      string `json:"technical_background,omitempty"`
	ResponseStyle            string `json:"response_style,omitempty"`
	ProblemSolvingApproach   string `json:"problem_solving_approach,omitempty"`
	InteractionStyle         string `json:"interaction_style,omitempty"`
}

// InvestigationSeed is the investigation a scored turn runs in. Prior turns
// enter the conversation; case memory is retrieved with read_file by responders
// and injected for Lite. ToolCalls execute before the scored turn and retain
// their actual platform outcomes separately from model-authored calls.
type InvestigationSeed struct {
	CaseTitle       string                   `json:"case_title"`
	CaseDescription string                   `json:"case_description,omitempty"`
	Turns           []InvestigationSeedTurn  `json:"turns,omitempty"`
	CaseMemory      *InvestigationSeedMemory `json:"case_memory,omitempty"`
	ToolCalls       []ScenarioSeedToolCall   `json:"tool_calls,omitempty"`
}

// ScenarioSeedToolCall requests an actual platform observation, never a forged
// tool result. Outcome is success, error, deny, or pre-envelope refusal; a
// mismatch is a setup failure rather than a model failure.
type ScenarioSeedToolCall struct {
	ToolName   string                 `json:"tool_name"`
	Arguments  json.RawMessage        `json:"arguments"`
	Outcome    string                 `json:"outcome"`
	Disclosure ScenarioSeedDisclosure `json:"disclosure,omitempty"`
}

// ScenarioSeedDisclosure says how a seeded governed call reaches the scored
// conversation.
type ScenarioSeedDisclosure string

const (
	// ScenarioSeedDisclosureProse is the original rendering, one user message
	// of labelled prose. It stays so frozen catalogs replay unchanged.
	ScenarioSeedDisclosureProse ScenarioSeedDisclosure = ""
	// ScenarioSeedDisclosurePlatformRecord renders the call as one typed
	// platform record (scenarioPlatformRecord, canonical JSON) in a user
	// message labelled as platform evidence. It never poses as the model's call.
	ScenarioSeedDisclosurePlatformRecord ScenarioSeedDisclosure = "platform_record"
	// ScenarioSeedDisclosureChainOnly keeps the call out of the conversation:
	// it exists only as the signed receipt the executing Operator recorded,
	// which the agent retrieves from the investigation with event_receipt_list.
	// Only an executed call (success or error) leaves that receipt.
	ScenarioSeedDisclosureChainOnly ScenarioSeedDisclosure = "chain_only"
)

// ScenarioScriptedFollowUp defines one frozen scripted user response to a clarification
// question in an investigation episode. Each follow-up has a declared trigger.
type ScenarioScriptedFollowUp struct {
	ID           string     `json:"id"`
	Description  string     `json:"description,omitempty"`
	TriggerTerms [][]string `json:"trigger_terms,omitempty"`
	ResponseText string     `json:"response_text"`
}

// ScenarioInputFixture is the typed prompt/input payload for one scenario. Its
// campaign-frozen canonical bytes are published unredacted as a public proof
// artifact alongside each assignment result that ran it.
type ScenarioInputFixture struct {
	SchemaVersion  string                     `json:"schema_version"`
	ScenarioID     string                     `json:"scenario_id"`
	SyntheticLabel string                     `json:"synthetic_label"`
	UserPrompt     string                     `json:"user_prompt"`
	InlineContext  []ScenarioInlineContent    `json:"inline_context,omitempty"`
	WorkspaceFiles []ScenarioWorkspaceFile    `json:"workspace_files,omitempty"`
	Seed           InvestigationSeed          `json:"seed"`
	FollowUps      []ScenarioScriptedFollowUp `json:"follow_ups,omitempty"`
	Budget         *investigation.Budget      `json:"budget,omitempty"`
}

// ScenarioCriterion describes one responsibility-specific scoring criterion.
type ScenarioCriterion struct {
	CriterionID   string `json:"criterion_id"`
	Description   string `json:"description"`
	Deterministic bool   `json:"deterministic"`
}

// ScenarioRoleCriteria binds scoring criteria to one designated model role.
type ScenarioRoleCriteria struct {
	Role     string              `json:"role"`
	Criteria []ScenarioCriterion `json:"criteria"`
	// RequiredEvidenceTypes overrides the shared evidence requirements for this role.
	RequiredEvidenceTypes []string `json:"required_evidence_types,omitempty"`
}

// ScenarioPipelineCriteria binds scoring criteria to one evaluation lane shape.
type ScenarioPipelineCriteria struct {
	Lane     string              `json:"lane"`
	Criteria []ScenarioCriterion `json:"criteria"`
}

// ScenarioContentCheck is a typed, conjunctive check of the designated role's
// final output. Every set constraint must hold. Term matching is
// case-insensitive and treats whitespace, '-' and '_' as one separator
// (foldContentTerm); each RequiredTerms group must match at least one term.
type ScenarioContentCheck struct {
	ExactToken        string                  `json:"exact_token,omitempty"`
	ExactLabels       []string                `json:"exact_labels,omitempty"`
	LeadingLabel      string                  `json:"leading_label,omitempty"`
	WordCount         int                     `json:"word_count,omitempty"`
	MaxSentences      int                     `json:"max_sentences,omitempty"`
	RequiredTerms     [][]string              `json:"required_terms,omitempty"`
	ForbiddenTerms    []string                `json:"forbidden_terms,omitempty"`
	JSONStringFields  map[string]string       `json:"json_string_fields,omitempty"`
	JSONIntegerFields map[string]int64        `json:"json_integer_fields,omitempty"`
	DiagnosisRubric   ScenarioDiagnosisRubric `json:"diagnosis_rubric,omitempty"`
	// SearchPattern requires successful directory searches to retrieve every
	// fixture line matching this reference regex. Filters and result limits
	// cannot turn an incomplete or empty retrieval into a successful task.
	SearchPattern string `json:"search_pattern,omitempty"`
	// ToolEvidence names receipt-backed tool results included in the content
	// judge's objective. It is frozen with the criteria and replayed offline.
	ToolEvidence []string `json:"tool_evidence,omitempty"`
	// Interrogation requires the whole answer to be the Interrogation Protocol
	// block Sage and Dash are told to emit when context is missing: one
	// <interrogation> element holding exactly three numbered, strictly binary
	// (yes/no) questions and nothing else.
	Interrogation bool `json:"interrogation,omitempty"`
	// JudgedCriteria are binary semantic criteria the pinned campaign judge
	// scores against the final reply, in order. Every criterion must be met.
	// They replace term matching for meaning: a paraphrase that satisfies the
	// statement passes, and a reply that names the terms without meeting the
	// statement fails.
	JudgedCriteria []ScenarioJudgedCriterion `json:"judged_criteria,omitempty"`
	// RefusalCriterion names the judged criterion that states the reply
	// refuses the request; its verdict drives the policy_decision evidence.
	RefusalCriterion string `json:"refusal_criterion,omitempty"`
}

// ScenarioJudgedCriterion is one binary statement about the final reply that
// the pinned judge decides from the frozen objective and the reply alone.
type ScenarioJudgedCriterion struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
}

// ToolArgumentConstraint constrains one named argument of a tool call. Values
// may contain ScenarioWorkspaceToken. Every set constraint must hold.
type ToolArgumentConstraint struct {
	Name string `json:"name"`
	// Equals requires the exact string value.
	Equals string `json:"equals,omitempty"`
	// OneOf requires the string value to be one of the listed values.
	OneOf []string `json:"one_of,omitempty"`
	// PathEquals requires the path, resolved against the operator working
	// directory and cleaned, to equal this path.
	PathEquals string `json:"path_equals,omitempty"`
	// PathUnder requires the resolved path to equal or sit under this path.
	PathUnder string `json:"path_under,omitempty"`
	// RegexMatches and RegexRejects treat the value as a regular expression
	// that must match every RegexMatches sample and no RegexRejects sample, so
	// an escaped-equivalent pattern passes and an over-broad one fails.
	RegexMatches []string `json:"regex_matches,omitempty"`
	RegexRejects []string `json:"regex_rejects,omitempty"`
	// RequiredTerms and ForbiddenTerms apply to free-text values.
	RequiredTerms  [][]string `json:"required_terms,omitempty"`
	ForbiddenTerms []string   `json:"forbidden_terms,omitempty"`
}

// ToolArgumentValidator is the typed argument contract for one tool, keyed by
// tool name.
type ToolArgumentValidator struct {
	ToolName  string                   `json:"tool_name"`
	Arguments []ToolArgumentConstraint `json:"arguments"`
}

// ScenarioHintArgument says where one required argument of a hinted tool comes
// from. Value is the literal the prompt or seed must contain for PROMPT and
// SEED sources, and is private (it never reaches the public projection).
type ScenarioHintArgument struct {
	ToolName string                              `json:"tool_name"`
	Name     string                              `json:"name"`
	Source   evalv1.EvaluationHintArgumentSource `json:"source"`
	Value    string                              `json:"value,omitempty"`
}

// ScenarioPromptHint records which tools the prompt hints at and where each
// required argument of each hinted tool is derivable from.
type ScenarioPromptHint struct {
	HintedTools []string               `json:"hinted_tools"`
	Arguments   []ScenarioHintArgument `json:"arguments"`
}

// ScenarioPolicyExpectation records the expected governed policy outcome.
type ScenarioPolicyExpectation struct {
	ExpectedOutcome string `json:"expected_outcome"`
	Detail          string `json:"detail"`
}

// ScenarioEscalationExpectation records whether escalation is expected.
type ScenarioEscalationExpectation struct {
	Expected bool   `json:"expected"`
	FromRole string `json:"from_role,omitempty"`
	ToRole   string `json:"to_role,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// recoveryKindMissingResource marks a recovery scenario whose tool call fails by
// design (its target does not exist) and whose answer must explain the failure.
const recoveryKindMissingResource = "missing_resource"

// recoveryKindErrorGuidedRetry supplies the earlier failure in the scenario's
// seed. The scored call must successfully apply the frozen argument constraints.
const recoveryKindErrorGuidedRetry = "error_guided_retry"

// ScenarioRecoveryExpectation records whether recovery behavior is expected.
type ScenarioRecoveryExpectation struct {
	Expected bool   `json:"expected"`
	Kind     string `json:"kind,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// ScenarioGoldEvidenceReference defines one authoritative evidence reference
// (receipt or observation) that a correct investigation should retrieve or
// produce. Kept strictly out of every model-visible input.
type ScenarioGoldEvidenceReference struct {
	ReferenceID     string `json:"reference_id"`
	Description     string `json:"description,omitempty"`
	ActionType      string `json:"action_type,omitempty"`
	TargetResource  string `json:"target_resource,omitempty"`
	CommandPattern  string `json:"command_pattern,omitempty"`
	ExpectedContent string `json:"expected_content,omitempty"`
}

// ScenarioGoldCriteria is the typed gold standard for one scenario. Its
// campaign-frozen canonical bytes are published unredacted as a public proof
// artifact alongside each assignment result graded against it.
type ScenarioGoldCriteria struct {
	SchemaVersion          string                          `json:"schema_version"`
	ScenarioID             string                          `json:"scenario_id"`
	ExpectedBehavior       string                          `json:"expected_behavior"`
	RoleCriteria           []ScenarioRoleCriteria          `json:"role_criteria"`
	PipelineCriteria       []ScenarioPipelineCriteria      `json:"pipeline_criteria"`
	ContentCheck           *ScenarioContentCheck           `json:"content_check,omitempty"`
	ArgumentValidators     []ToolArgumentValidator         `json:"argument_validators,omitempty"`
	FileState              []investigation.FileState       `json:"file_state,omitempty"`
	GovernedProbe          *investigation.GovernedProbe    `json:"governed_probe,omitempty"`
	Players                *ScenarioPlayerExpectations     `json:"players,omitempty"`
	PromptHint             *ScenarioPromptHint             `json:"prompt_hint,omitempty"`
	PolicyExpectation      ScenarioPolicyExpectation       `json:"policy_expectation"`
	EscalationExpectation  ScenarioEscalationExpectation   `json:"escalation_expectation"`
	RecoveryExpectation    ScenarioRecoveryExpectation     `json:"recovery_expectation"`
	RequiredEvidenceTypes  []string                        `json:"required_evidence_types"`
	GoldEvidenceReferences []ScenarioGoldEvidenceReference `json:"gold_evidence_references,omitempty"`
	// RequireReceiptIDActionCitation freezes whether the prompt asks the agent
	// to cite a retrieved receipt by ID and action. Nil is reserved for gold
	// authored before this field existed and preserves its historical grading.
	RequireReceiptIDActionCitation *bool                   `json:"require_receipt_id_action_citation,omitempty"`
	GradingContract                ScenarioGradingContract `json:"grading_contract,omitempty"`
}

// requiresReceiptIDActionCitation preserves the old expected-tool rule only
// for gold that predates the explicit citation contract. New gold records true
// or false based on its prompt.
func (gold *ScenarioGoldCriteria) requiresReceiptIDActionCitation(expectedReceiptList bool) bool {
	if gold.RequireReceiptIDActionCitation != nil {
		return *gold.RequireReceiptIDActionCitation
	}
	return expectedReceiptList
}

// gradingContractV2 reports whether the gold was frozen under
// ScenarioGradingContractV2.
func (gold *ScenarioGoldCriteria) gradingContractV2() bool {
	return gold.GradingContract == ScenarioGradingContractV2 || gold.GradingContract == ScenarioGradingContractV3
}

func (gold *ScenarioGoldCriteria) closedInterrogationContract() bool {
	return gold.GradingContract == ScenarioGradingContractV3
}

func (gold *ScenarioGoldCriteria) requiredEvidenceForRole(role string) []string {
	for _, criteria := range gold.RoleCriteria {
		if criteria.Role == role && len(criteria.RequiredEvidenceTypes) > 0 {
			return criteria.RequiredEvidenceTypes
		}
	}
	return gold.RequiredEvidenceTypes
}

// ScenarioArtifactPair holds canonical fixture bytes and their evidence reference.
type ScenarioArtifactPair struct {
	Body      []byte
	Reference *compliancev1.ComplianceEvidenceReference
}

func marshalScenarioFixture(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("evaluation: marshal scenario fixture: %w", err)
	}
	if err := complianceevidence.ValidateCanonicalJSON(body); err != nil {
		return nil, fmt.Errorf("evaluation: marshal scenario fixture: %w", err)
	}
	return body, nil
}

func buildScenarioArtifactReference(suiteID string, artifactType complianceevidence.ArtifactType, schemaRef, scenarioID string, body []byte) (*compliancev1.ComplianceEvidenceReference, error) {
	if suiteID == "" || scenarioID == "" || len(body) == 0 {
		return nil, fmt.Errorf("evaluation: build scenario artifact reference: %w", constants.ErrMissingRequiredField)
	}
	artifactID := complianceevidence.ContentAddress(artifactType, body)
	_, digest, ok := complianceevidence.ParseContentAddress(artifactID)
	if !ok {
		return nil, fmt.Errorf("evaluation: build scenario artifact reference: content address: %w", constants.ErrEvidenceArtifactMalformed)
	}
	freeze := timestamppb.New(scenarioCatalogFreezeTime)
	return &compliancev1.ComplianceEvidenceReference{
		ArtifactId:         artifactID,
		ArtifactType:       string(artifactType),
		Sha256:             digest,
		MediaType:          constants.MediaTypeJSON,
		SchemaRef:          schemaRef,
		ProducerIdentity:   "ztd-eval-catalog",
		ProducedAt:         freeze,
		ScopeId:            suiteID,
		RunId:              "catalog",
		ScenarioId:         scenarioID,
		VerificationStatus: "verified",
		VerifierId:         GraderID,
		VerifierVersion:    GraderVersion,
		VerifiedAt:         freeze,
	}, nil
}

func buildScenarioInputArtifact(suiteID string, input ScenarioInputFixture) (ScenarioArtifactPair, error) {
	input.SchemaVersion = scenarioInputSchemaVersion
	body, err := marshalScenarioFixture(input)
	if err != nil {
		return ScenarioArtifactPair{}, err
	}
	ref, err := buildScenarioArtifactReference(suiteID, complianceevidence.ArtifactTypeEvaluationScenarioInput, scenarioInputSchemaRef, input.ScenarioID, body)
	if err != nil {
		return ScenarioArtifactPair{}, err
	}
	return ScenarioArtifactPair{Body: body, Reference: ref}, nil
}

func buildScenarioGoldArtifact(suiteID string, gold ScenarioGoldCriteria) (ScenarioArtifactPair, error) {
	gold.SchemaVersion = scenarioGoldSchemaVersion
	body, err := marshalScenarioFixture(gold)
	if err != nil {
		return ScenarioArtifactPair{}, err
	}
	ref, err := buildScenarioArtifactReference(suiteID, complianceevidence.ArtifactTypeEvaluationScenarioGold, scenarioGoldSchemaRef, gold.ScenarioID, body)
	if err != nil {
		return ScenarioArtifactPair{}, err
	}
	return ScenarioArtifactPair{Body: body, Reference: ref}, nil
}
