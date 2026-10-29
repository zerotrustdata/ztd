// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"

	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ScenarioBlueprint is the authoring record for one frozen scenario.
type ScenarioBlueprint struct {
	ScenarioID        string
	ScenarioVersion   string
	Category          evalv1.EvaluationScenarioCategory
	PublicDescription string
	GradingMethod     evalv1.EvaluationGradingMethod
	AllowedTools      []string
	ExpectedTools     []string
	ForbiddenTools    []string
	RequiredConcepts  []string
	EligibleRoles     []evalv1.ModelCampaignRole
	TrajectoryPolicy  evalv1.EvaluationTrajectoryPolicy
	// TribunalCommands routes run_shell_command through Tribunal authoring.
	TribunalCommands bool
	Input            ScenarioInputFixture
	Gold             ScenarioGoldCriteria
}

// ScenarioArtifacts holds the canonical fixture bodies for one scenario.
type ScenarioArtifacts struct {
	Input ScenarioArtifactPair
	Gold  ScenarioArtifactPair
}

// DefaultSuiteScenarioCount is the number of scenarios in the built-in default
// suite.
const DefaultSuiteScenarioCount = 41

// buildSuiteCatalog materializes one suite's blueprints into a catalog and its
// content-addressed fixture artifacts, preserving authored workflow order. Every
// blueprint is validated against the agent tool registry first, so a blueprint
// that breaks the scenario contract
// cannot materialize. The catalog's identity is ref; the same blueprints under
// another ref yield another catalog digest.
func buildSuiteCatalog(ref *compliancev1.VersionedReference, blueprints []ScenarioBlueprint) (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error) {
	toolCatalog, err := newAgentToolCatalog()
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: build suite %s catalog: %w", ref.GetId(), err)
	}
	artifacts := make(map[string]ScenarioArtifacts, len(blueprints))
	scenarios := make([]*evalv1.EvaluationScenarioDefinition, 0, len(blueprints))
	for _, blueprint := range blueprints {
		scenario, pair, err := materializeScenarioBlueprint(ref.GetId(), blueprint, toolCatalog)
		if err != nil {
			return nil, nil, fmt.Errorf("evaluation: build suite %s catalog: scenario %s: %w", ref.GetId(), blueprint.ScenarioID, err)
		}
		artifacts[blueprint.ScenarioID] = pair
		scenarios = append(scenarios, scenario)
	}
	personas, err := catalogPersonaBindings()
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: build suite %s catalog: %w", ref.GetId(), err)
	}
	catalog := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion:            CampaignSchemaVersion,
		CatalogRef:               ref,
		Scenarios:                scenarios,
		Personas:                 personas,
		ToolCallingProbeRequired: true,
	}
	digest, err := ComputeScenarioCatalogDigest(catalog)
	if err != nil {
		return nil, nil, err
	}
	catalog.CatalogDigest = digest
	return catalog, artifacts, nil
}

// BuildScenarioCatalog materializes the built-in default suite's catalog and
// its fixture artifacts.
func BuildScenarioCatalog() (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error) {
	return buildSuiteCatalog(&compliancev1.VersionedReference{Id: DefaultSuiteID, Version: DefaultSuiteVersion}, scenarioBlueprints())
}

// LoadScenarioCatalog returns the validated built-in default suite catalog.
func LoadScenarioCatalog() (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error) {
	catalog, artifacts, err := BuildScenarioCatalog()
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateDefaultSuiteCatalog(catalog, artifacts); err != nil {
		return nil, nil, err
	}
	return catalog, artifacts, nil
}

// SmokeSuiteID identifies the built-in smoke suite: the discriminative subset
// of the default suite used for Tier 1 screening.
const SmokeSuiteID = "smoke-suite"

// SmokeGateScenarioCount is the number of discriminative scenarios in Tier 1 screening.
const SmokeGateScenarioCount = 10

// SmokeGateScenarioIDs returns the Tier 1 screening scenario IDs. A rollout
// smoke gate passes a model whose smoke run completes and verifies, so the set
// exercises every role, every trajectory policy, and each grading path a full
// run depends on (exact and leading labels, JSON fields, judged criteria,
// history retrieval, tool arguments, guided recovery, governed denial, and
// file state). Within that coverage it keeps the scenarios that best separated
// models in the eval-init campaigns. It returns a fresh slice on every call, so
// a caller cannot mutate the shared smoke selection.
func SmokeGateScenarioIDs() []string {
	return []string{
		"instruction-review-vote-deny",
		"tech-log-parse",
		"verify-contradiction",
		"route-primary-ownership",
		"tool-select-file-read",
		"tool-select-investigation",
		"tool-arg-file-path",
		"recovery-tool-failure",
		"recovery-error-guided-retry",
		"security-policy-deny-delete",
	}
}

// smokeSuiteBlueprints selects the smoke scenarios from the default suite.
func smokeSuiteBlueprints() []ScenarioBlueprint {
	ids := SmokeGateScenarioIDs()
	smokeIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		smokeIDs[id] = struct{}{}
	}
	blueprints := make([]ScenarioBlueprint, 0, len(ids))
	for _, blueprint := range scenarioBlueprints() {
		if _, ok := smokeIDs[blueprint.ScenarioID]; ok {
			blueprints = append(blueprints, blueprint)
		}
	}
	return blueprints
}

// BuildSmokeGateScenarioCatalog materializes the screening catalog
// and its content-addressed fixture artifacts.
func BuildSmokeGateScenarioCatalog() (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error) {
	return buildSuiteCatalog(&compliancev1.VersionedReference{Id: SmokeSuiteID, Version: SmokeSuiteVersion}, smokeSuiteBlueprints())
}

// LoadSmokeGateScenarioCatalog returns the validated built-in smoke suite catalog.
func LoadSmokeGateScenarioCatalog() (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error) {
	catalog, artifacts, err := BuildSmokeGateScenarioCatalog()
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateSmokeGateScenarioCatalog(catalog, artifacts); err != nil {
		return nil, nil, err
	}
	return catalog, artifacts, nil
}

// ValidateSmokeGateScenarioCatalog verifies the built-in smoke suite.
func ValidateSmokeGateScenarioCatalog(catalog *evalv1.EvaluationScenarioCatalog, artifacts map[string]ScenarioArtifacts) error {
	if catalog == nil || catalog.GetCatalogRef() == nil {
		return fmt.Errorf("evaluation: validate smoke scenario catalog: %w", constants.ErrMissingRequiredField)
	}
	if catalog.GetCatalogRef().GetId() != SmokeSuiteID || catalog.GetCatalogRef().GetVersion() != SmokeSuiteVersion {
		return fmt.Errorf("evaluation: validate smoke scenario catalog: catalog identity: %w", constants.ErrEvidenceScopeMismatch)
	}
	if len(catalog.GetScenarios()) != SmokeGateScenarioCount {
		return fmt.Errorf("evaluation: validate smoke scenario catalog: %w: expected %d scenarios, got %d", constants.ErrEvaluationScenarioContractInvalid, SmokeGateScenarioCount, len(catalog.GetScenarios()))
	}
	return ValidateScenarioCatalog(catalog, artifacts)
}

// ValidateDefaultSuiteCatalog verifies the built-in default suite: its
// identity, its scenario count, and its per-category population.
func ValidateDefaultSuiteCatalog(catalog *evalv1.EvaluationScenarioCatalog, artifacts map[string]ScenarioArtifacts) error {
	if catalog == nil || catalog.GetCatalogRef() == nil {
		return fmt.Errorf("evaluation: validate scenario catalog: %w", constants.ErrMissingRequiredField)
	}
	if catalog.GetCatalogRef().GetId() != DefaultSuiteID || catalog.GetCatalogRef().GetVersion() != DefaultSuiteVersion {
		return fmt.Errorf("evaluation: validate scenario catalog: catalog identity: %w", constants.ErrEvidenceScopeMismatch)
	}
	if len(catalog.GetScenarios()) != DefaultSuiteScenarioCount {
		return fmt.Errorf("evaluation: validate scenario catalog: %w: expected %d scenarios, got %d", constants.ErrEvaluationScenarioContractInvalid, DefaultSuiteScenarioCount, len(catalog.GetScenarios()))
	}
	if err := ValidateScenarioCatalog(catalog, artifacts); err != nil {
		return err
	}
	return validateDefaultSuiteCategoryCounts(catalog.GetScenarios())
}

// ValidateScenarioCatalog verifies the structure of any suite's frozen
// catalog: identity, schema, digest, and every scenario's fixture binding. It
// puts no constraint on how many scenarios a suite has or how they are spread
// across categories; the built-in suites add those gates themselves.
func ValidateScenarioCatalog(catalog *evalv1.EvaluationScenarioCatalog, artifacts map[string]ScenarioArtifacts) error {
	if catalog == nil || catalog.GetCatalogRef() == nil {
		return fmt.Errorf("evaluation: validate scenario catalog: %w", constants.ErrMissingRequiredField)
	}
	if catalog.GetCatalogRef().GetId() == "" || catalog.GetCatalogRef().GetVersion() == "" {
		return fmt.Errorf("evaluation: validate scenario catalog: %w: catalog identity", constants.ErrMissingRequiredField)
	}
	if catalog.GetSchemaVersion() != CampaignSchemaVersion {
		return fmt.Errorf("evaluation: validate scenario catalog: %w", constants.ErrEvidenceSchemaMismatch)
	}
	if err := ValidateScenarioCatalogDigest(catalog); err != nil {
		return err
	}
	if len(catalog.GetScenarios()) == 0 {
		return fmt.Errorf("evaluation: validate scenario catalog: %w: a suite needs at least one scenario", constants.ErrEvaluationScenarioContractInvalid)
	}
	seen := make(map[string]struct{}, len(catalog.GetScenarios()))
	for _, scenario := range catalog.GetScenarios() {
		if scenario == nil || scenario.GetScenarioId() == "" || scenario.GetScenarioVersion() == "" {
			return fmt.Errorf("evaluation: validate scenario catalog: %w", constants.ErrMissingRequiredField)
		}
		key := scenario.GetScenarioId() + "@" + scenario.GetScenarioVersion()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("evaluation: validate scenario catalog: %w: duplicate scenario %s", constants.ErrEvaluationScenarioContractInvalid, key)
		}
		seen[key] = struct{}{}
		if scenario.GetCategory() == evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_UNSPECIFIED {
			return fmt.Errorf("evaluation: validate scenario catalog: %w: scenario %s has unspecified category", constants.ErrEvaluationScenarioContractInvalid, scenario.GetScenarioId())
		}
		if scenario.GetPublicDescription() == "" {
			return fmt.Errorf("evaluation: validate scenario catalog: %w: scenario %s missing public description", constants.ErrEvaluationScenarioContractInvalid, scenario.GetScenarioId())
		}
		if scenario.GetGradingMethod() == evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_UNSPECIFIED {
			return fmt.Errorf("evaluation: validate scenario catalog: %w: scenario %s has unspecified grading method", constants.ErrEvaluationScenarioContractInvalid, scenario.GetScenarioId())
		}
		if err := validatePublicScenarioCriteria(scenario); err != nil {
			return fmt.Errorf("evaluation: validate scenario catalog: %w", err)
		}
		if scenario.GetInputFixtureRef() == nil || scenario.GetGoldCriteriaRef() == nil {
			return fmt.Errorf("evaluation: validate scenario catalog: %w: scenario %s missing fixture references", constants.ErrMissingRequiredField, scenario.GetScenarioId())
		}
		if _, err := scenarioEligibleRoles(scenario); err != nil {
			return fmt.Errorf("evaluation: validate scenario catalog: %w", err)
		}
		pair, ok := artifacts[scenario.GetScenarioId()]
		if !ok {
			return fmt.Errorf("evaluation: validate scenario catalog: %w: missing artifacts for scenario %s", constants.ErrMissingRequiredField, scenario.GetScenarioId())
		}
		if err := validateScenarioArtifactBinding(scenario.GetInputFixtureRef(), pair.Input); err != nil {
			return fmt.Errorf("evaluation: validate scenario catalog: scenario %s input fixture: %w", scenario.GetScenarioId(), err)
		}
		if err := validateScenarioArtifactBinding(scenario.GetGoldCriteriaRef(), pair.Gold); err != nil {
			return fmt.Errorf("evaluation: validate scenario catalog: scenario %s gold criteria: %w", scenario.GetScenarioId(), err)
		}
	}
	return nil
}

func materializeScenarioBlueprint(suiteID string, blueprint ScenarioBlueprint, catalog *agentToolCatalog) (*evalv1.EvaluationScenarioDefinition, ScenarioArtifacts, error) {
	if err := validateScenarioContract(blueprint, catalog); err != nil {
		return nil, ScenarioArtifacts{}, err
	}
	input := blueprint.Input
	input.ScenarioID = blueprint.ScenarioID
	input.SyntheticLabel = scenarioSyntheticLabel
	inputArtifact, err := buildScenarioInputArtifact(suiteID, input)
	if err != nil {
		return nil, ScenarioArtifacts{}, err
	}
	gold := blueprint.Gold
	gold.ScenarioID = blueprint.ScenarioID
	goldArtifact, err := buildScenarioGoldArtifact(suiteID, gold)
	if err != nil {
		return nil, ScenarioArtifacts{}, err
	}
	publicCriteria, err := publicCriteriaForBlueprint(blueprint)
	if err != nil {
		return nil, ScenarioArtifacts{}, err
	}
	return &evalv1.EvaluationScenarioDefinition{
		ScenarioId:                blueprint.ScenarioID,
		ScenarioVersion:           blueprint.ScenarioVersion,
		Category:                  blueprint.Category,
		PublicDescription:         blueprint.PublicDescription,
		GradingMethod:             blueprint.GradingMethod,
		AllowedTools:              append([]string(nil), blueprint.AllowedTools...),
		ExpectedTools:             append([]string(nil), blueprint.ExpectedTools...),
		ForbiddenTools:            append([]string(nil), blueprint.ForbiddenTools...),
		RequiredConcepts:          append([]string(nil), blueprint.RequiredConcepts...),
		EligibleRoles:             append([]evalv1.ModelCampaignRole(nil), blueprint.EligibleRoles...),
		InputFixtureRef:           inputArtifact.Reference,
		GoldCriteriaRef:           goldArtifact.Reference,
		TrajectoryPolicy:          blueprint.TrajectoryPolicy,
		TribunalCommands:          blueprint.TribunalCommands,
		ExcludeWhenL2Enforced:     scenarioRequiresL2(blueprint),
		PromptHint:                publicPromptHint(blueprint.Gold.PromptHint),
		PublicCriteria:            publicCriteria,
		PublicToolScoreDimensions: publicToolScoreDimensionsForBlueprint(blueprint),
	}, ScenarioArtifacts{Input: inputArtifact, Gold: goldArtifact}, nil
}

// scenarioRequiresL2 derives frozen posture eligibility from the action
// classification used by governance.L2Enforced. Required state-changing tools
// and expected successful/error seed actions are excluded under L2-enforcing
// postures. Expected denials and refusals remain scoreable.
func scenarioRequiresL2(blueprint ScenarioBlueprint) bool {
	for _, tool := range blueprint.ExpectedTools {
		if agentToolChangesState(tool) {
			return true
		}
	}
	for _, call := range blueprint.Input.Seed.ToolCalls {
		if call.Outcome != scenarioSeedDeny && call.Outcome != scenarioSeedRefusal &&
			agentToolChangesState(call.ToolName) {
			return true
		}
	}
	return false
}

// agentToolChangesState reports whether a call of tool changes state: a
// governed action tool by its own action (FILE_EDIT and RESTORE_FILE change
// state for every target), any other native tool as an MCP_CALL target. An
// unknown classification fails closed as a state change.
func agentToolChangesState(tool string) bool {
	action, governed, err := agentToolActionType(tool)
	if err != nil {
		return true
	}
	if governed != nil {
		return action.IsStateChange()
	}
	return action.ChangesState(tool)
}

// publicPromptHint projects the private hint to its public form. Values are
// never copied: they stay in the private gold artifact (INV-EVAL-EVID-04).
func publicPromptHint(private *ScenarioPromptHint) *evalv1.PromptHint {
	if private == nil {
		return nil
	}
	public := &evalv1.PromptHint{HintedTools: append([]string(nil), private.HintedTools...)}
	for _, argument := range private.Arguments {
		public.Arguments = append(public.Arguments, &evalv1.PromptHintArgument{
			ToolName:     argument.ToolName,
			ArgumentName: argument.Name,
			Source:       argument.Source,
		})
	}
	return public
}

func validateScenarioArtifactBinding(reference *compliancev1.ComplianceEvidenceReference, artifact ScenarioArtifactPair) error {
	if reference == nil || artifact.Reference == nil {
		return fmt.Errorf("%w: missing reference", constants.ErrMissingRequiredField)
	}
	if reference.GetArtifactId() != artifact.Reference.GetArtifactId() {
		return fmt.Errorf("artifact ID: %w", constants.ErrEvidenceScopeMismatch)
	}
	if reference.GetSha256() != artifact.Reference.GetSha256() {
		return fmt.Errorf("artifact digest: %w", constants.ErrChecksumMismatch)
	}
	if !contentMatchesReference(artifact.Body, artifact.Reference) {
		return fmt.Errorf("artifact body: %w", constants.ErrChecksumMismatch)
	}
	return nil
}

func validateDefaultSuiteCategoryCounts(scenarios []*evalv1.EvaluationScenarioDefinition) error {
	expected := map[evalv1.EvaluationScenarioCategory]int{
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE: 4,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TOOL_SELECTION:        4,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TOOL_ARGUMENT:         3,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TECHNICAL_ANALYSIS:    6,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_ROUTING_DELEGATION:    3,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_VERIFICATION:          4,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_SECURITY_POLICY:       11,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_RECOVERY:              5,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FINAL_RESPONSE:        1,
	}
	counts := make(map[evalv1.EvaluationScenarioCategory]int, len(expected))
	for _, scenario := range scenarios {
		counts[scenario.GetCategory()]++
	}
	for category, want := range expected {
		if counts[category] != want {
			return fmt.Errorf("evaluation: validate scenario catalog: %w: category %s expected %d scenarios, got %d", constants.ErrEvaluationScenarioContractInvalid, category.String(), want, counts[category])
		}
	}
	return nil
}
