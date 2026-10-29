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
	"path"
	"slices"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/investigation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// scenarioEvidenceTypes is the closed vocabulary of required evidence types.
// Every type in it is derivable from a digest-bound trace, so none can be
// permanently unavailable (INV-EVAL-CAMP-10).
var scenarioEvidenceTypes = map[string]struct{}{
	"model_inference":   {},
	"semantic_grade":    {},
	"tool_decision":     {},
	"tool_call":         {},
	"governed_action":   {},
	"policy_decision":   {},
	"state_observation": {},
	"recovery":          {},
	"final_response":    {},
}

// scenarioFailure builds a scenario failure carrying its sentinel class. When
// cause is non-nil it is wrapped with %w, so errors.Is and errors.As reach it
// through the returned error's Unwrap chain.
func scenarioFailure(sentinel error, blueprint ScenarioBlueprint, cause error, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	if cause == nil {
		return fmt.Errorf("evaluation: scenario %s: %w: %s", blueprint.ScenarioID, sentinel, message)
	}
	return fmt.Errorf("evaluation: scenario %s: %w: %s: %w", blueprint.ScenarioID, sentinel, message, cause)
}

func scenarioContractError(blueprint ScenarioBlueprint, format string, args ...any) error {
	return scenarioFailure(constants.ErrEvaluationScenarioContractInvalid, blueprint, nil, format, args...)
}

// scenarioContractErrorWithCause is scenarioContractError for a failure that
// has an underlying cause, which stays reachable with errors.Is/errors.As.
func scenarioContractErrorWithCause(blueprint ScenarioBlueprint, cause error, format string, args ...any) error {
	return scenarioFailure(constants.ErrEvaluationScenarioContractInvalid, blueprint, cause, format, args...)
}

func scenarioUnanswerableError(blueprint ScenarioBlueprint, format string, args ...any) error {
	return scenarioFailure(constants.ErrEvaluationPromptUnanswerable, blueprint, nil, format, args...)
}

// scenarioUnanswerableErrorWithCause is scenarioUnanswerableError for a failure
// that has an underlying cause, which stays reachable with errors.Is/errors.As.
func scenarioUnanswerableErrorWithCause(blueprint ScenarioBlueprint, cause error, format string, args ...any) error {
	return scenarioFailure(constants.ErrEvaluationPromptUnanswerable, blueprint, cause, format, args...)
}

// validateScenarioContract fails closed on any breach of the scenario
// contract. Both catalog builders call it, so a bad blueprint cannot
// materialize. Checks run in the order the plan lists them.
func validateScenarioContract(blueprint ScenarioBlueprint, catalog *agentToolCatalog) error {
	if blueprint.TrajectoryPolicy == evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_UNSPECIFIED {
		return scenarioContractError(blueprint, "trajectory policy is unspecified")
	}
	if err := validateSeedCaseTitle(blueprint); err != nil {
		return err
	}
	if err := validateScenarioContext(blueprint, catalog); err != nil {
		return err
	}
	if _, err := investigation.ValidateFileStates(blueprint.Gold.FileState); err != nil {
		return scenarioContractErrorWithCause(blueprint, err, "invalid final file-state objective")
	}
	if probe := blueprint.Gold.GovernedProbe; probe != nil && !models.ValidExecutableName(probe.Command) {
		return scenarioContractError(blueprint, "verification probe must name one executable")
	}
	if err := validateScenarioToolNames(blueprint, catalog); err != nil {
		return err
	}
	if err := validateScenarioToolSets(blueprint); err != nil {
		return err
	}
	if err := validateTrajectoryPolicyShape(blueprint); err != nil {
		return err
	}
	// No executor records judge evidence, so a judge-graded scenario can only
	// ever terminate GRADER_FAILED.
	if blueprint.GradingMethod == evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE || slices.Contains(blueprint.Gold.RequiredEvidenceTypes, "semantic_grade") {
		return scenarioContractError(blueprint, "scenario requires a semantic judge, which no executor provides")
	}
	triageOnly := len(blueprint.EligibleRoles) == 1 && blueprint.EligibleRoles[0] == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE &&
		blueprint.Gold.Players != nil && blueprint.Gold.Players.Triage != nil
	fileStateObjective := blueprint.Gold.GradingContract == ScenarioGradingContractV3 && len(blueprint.Gold.FileState) > 0
	if !contentCheckHasConstraint(blueprint.Gold.ContentCheck) && !triageOnly && !fileStateObjective {
		return scenarioContractError(blueprint, "scenario has no content check, so it cannot fail")
	}
	if blueprint.Gold.ContentCheck != nil {
		if err := validateDiagnosisRubric(blueprint, blueprint.Gold.ContentCheck); err != nil {
			return err
		}
		if err := validateJudgedCriteria(blueprint, blueprint.Gold.ContentCheck); err != nil {
			return err
		}
	}
	if err := validateArgumentValidators(blueprint, catalog); err != nil {
		return err
	}
	if err := validatePromptHint(blueprint, catalog); err != nil {
		return err
	}
	if err := validateGroundedToolArguments(blueprint); err != nil {
		return err
	}
	if err := validateWorkspaceFiles(blueprint); err != nil {
		return err
	}
	if err := validatePlayerExpectations(blueprint); err != nil {
		return err
	}
	if err := validateFollowUps(blueprint); err != nil {
		return err
	}
	if err := validateGoldEvidenceReferences(blueprint); err != nil {
		return err
	}
	return validateRequiredEvidenceTypes(blueprint)
}

// validatePlayerExpectations fails closed on player gold that could never
// grade anything or names a label outside the scenario catalog vocabulary, so
// a typo cannot freeze into a scenario that silently accepts or rejects every
// run.
func validatePlayerExpectations(blueprint ScenarioBlueprint) error {
	players := blueprint.Gold.Players
	if players == nil {
		return nil
	}
	if triage := players.Triage; triage != nil {
		if len(triage.Complexity)+len(triage.Intent)+len(triage.Posture) == 0 {
			return scenarioContractError(blueprint, "triage expectation grades no label")
		}
		for _, value := range triage.Complexity {
			if value != constants.TriageComplexitySimple && value != constants.TriageComplexityComplex {
				return scenarioContractError(blueprint, "triage complexity %q is not in the scenario catalog triage vocabulary", value)
			}
		}
		for _, value := range triage.Intent {
			if value != constants.TriageIntentInformation && value != constants.TriageIntentAction && value != constants.TriageIntentUnknown {
				return scenarioContractError(blueprint, "triage intent %q is not in the scenario catalog triage vocabulary", value)
			}
		}
		for _, value := range triage.Posture {
			if value != constants.TriagePostureNormal && value != constants.TriagePostureEscalated && value != constants.TriagePostureAdversarial && value != constants.TriagePostureConfused {
				return scenarioContractError(blueprint, "triage posture %q is not in the scenario catalog triage vocabulary", value)
			}
		}
	}
	if players.Command != nil && !contentCheckHasConstraint(players.Command) {
		return scenarioContractError(blueprint, "command expectation has no constraint")
	}
	if players.Command != nil && players.Command.DiagnosisRubric != "" {
		return scenarioContractError(blueprint, "diagnosis rubric is only supported by scenario content checks")
	}
	if players.Command != nil && (len(players.Command.JudgedCriteria) > 0 || players.Command.RefusalCriterion != "" || len(players.Command.ToolEvidence) > 0 || players.Command.SearchPattern != "") {
		return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationJudgedCriterionInvalid, "judged criteria are only supported by scenario content checks")
	}
	if marshal := players.Marshal; marshal != nil {
		if len(marshal.Risk) == 0 {
			return scenarioContractError(blueprint, "marshal expectation accepts no risk level")
		}
		for _, risk := range marshal.Risk {
			if risk != MarshalRiskLow && risk != MarshalRiskMedium && risk != MarshalRiskHigh {
				return scenarioContractError(blueprint, "marshal risk %q is not in the scenario catalog marshal risk vocabulary", risk)
			}
		}
	}
	if players.Codex != nil && !contentCheckHasConstraint(players.Codex) {
		return scenarioContractError(blueprint, "codex expectation has no constraint")
	}
	if players.Codex != nil && players.Codex.DiagnosisRubric != "" {
		return scenarioContractError(blueprint, "diagnosis rubric is only supported by scenario content checks")
	}
	if players.Codex != nil && (len(players.Codex.JudgedCriteria) > 0 || players.Codex.RefusalCriterion != "" || len(players.Codex.ToolEvidence) > 0 || players.Codex.SearchPattern != "") {
		return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationJudgedCriterionInvalid, "judged criteria are only supported by scenario content checks")
	}
	return nil
}

func validateSeedCaseTitle(blueprint ScenarioBlueprint) error {
	title := blueprint.Input.Seed.CaseTitle
	if strings.TrimSpace(title) == "" {
		return scenarioContractError(blueprint, "seed case title is empty")
	}
	lowered := strings.ToLower(title)
	if strings.Contains(lowered, "eval") || strings.Contains(lowered, "{{") {
		return scenarioContractError(blueprint, "seed case title %q must read like a real case and never name the evaluation", title)
	}
	return nil
}

func validateScenarioContext(blueprint ScenarioBlueprint, catalog *agentToolCatalog) error {
	if strings.TrimSpace(blueprint.Input.UserPrompt) == "" {
		return scenarioContractError(blueprint, "user prompt is empty")
	}
	lite := slices.Contains(blueprint.EligibleRoles, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE)
	if lite && (len(blueprint.ExpectedTools) > 0 || blueprint.TribunalCommands) {
		return scenarioContractError(blueprint, "Lite receives injected context and cannot require tool calls")
	}
	responders := slices.Contains(blueprint.EligibleRoles, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY) || slices.Contains(blueprint.EligibleRoles, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT)
	if responders && blueprint.Input.Seed.CaseMemory != nil && !slices.Contains(blueprint.AllowedTools, constants.MCPToolReadFile) {
		return scenarioUnanswerableError(blueprint, "case memory requires read_file for responder retrieval")
	}
	for _, turn := range blueprint.Input.Seed.Turns {
		if !slices.Contains([]string{"user", "primary", "assistant"}, turn.Sender) || strings.TrimSpace(turn.Content) == "" {
			return scenarioContractError(blueprint, "invalid or empty seed conversation turn")
		}
	}
	if budget := blueprint.Input.Budget; budget != nil {
		if budget.ToolTurnLimit < 1 || budget.ToolTurnLimit > investigation.MaxToolTurns || budget.WallClockLimitMS < 1 || budget.WallClockLimitMS > investigation.MaxWallClockMS {
			return scenarioContractError(blueprint, "invalid investigation budget")
		}
	}
	for _, call := range blueprint.Input.Seed.ToolCalls {
		if _, ok := catalog.Tool(call.ToolName); !ok || !json.Valid(call.Arguments) {
			return scenarioContractError(blueprint, "invalid seed tool %q or arguments", call.ToolName)
		}
		if !slices.Contains([]string{scenarioSeedSuccess, scenarioSeedError, scenarioSeedDeny, scenarioSeedRefusal}, call.Outcome) {
			return scenarioContractError(blueprint, "seed tool %s has unspecified outcome", call.ToolName)
		}
		switch call.Disclosure {
		case ScenarioSeedDisclosureProse, ScenarioSeedDisclosurePlatformRecord:
		case ScenarioSeedDisclosureChainOnly:
			// A refused or denied call never reaches L5, so no receipt exists
			// for the agent to retrieve.
			if call.Outcome != scenarioSeedSuccess && call.Outcome != scenarioSeedError {
				return scenarioUnanswerableError(blueprint, "chain-only seed %s must execute to leave a receipt", call.ToolName)
			}
			if responders && !slices.Contains(blueprint.AllowedTools, constants.MCPToolEventReceiptList) {
				return scenarioUnanswerableError(blueprint, "chain-only seed %s requires event_receipt_list for responder retrieval", call.ToolName)
			}
		default:
			return scenarioContractError(blueprint, "seed tool %s has unknown disclosure %q", call.ToolName, call.Disclosure)
		}
	}
	return nil
}

func validateScenarioToolNames(blueprint ScenarioBlueprint, catalog *agentToolCatalog) error {
	names := make([]string, 0, len(blueprint.AllowedTools)+len(blueprint.ExpectedTools)+len(blueprint.ForbiddenTools))
	names = append(names, blueprint.AllowedTools...)
	names = append(names, blueprint.ExpectedTools...)
	names = append(names, blueprint.ForbiddenTools...)
	if blueprint.Gold.PromptHint != nil {
		names = append(names, blueprint.Gold.PromptHint.HintedTools...)
	}
	for _, validator := range blueprint.Gold.ArgumentValidators {
		names = append(names, validator.ToolName)
	}
	for _, name := range names {
		if _, ok := catalog.Tool(name); !ok {
			return scenarioContractError(blueprint, "tool %q is not in the agent tool catalog", name)
		}
	}
	return nil
}

func validateScenarioToolSets(blueprint ScenarioBlueprint) error {
	allowed := stringSet(blueprint.AllowedTools)
	if blueprint.TribunalCommands {
		if _, ok := allowed[constants.MCPToolRunShellCommand]; !ok {
			return scenarioContractError(blueprint, "tribunal commands require allowed tool %q", constants.MCPToolRunShellCommand)
		}
	}
	for _, name := range blueprint.ExpectedTools {
		if _, ok := allowed[name]; !ok {
			return scenarioContractError(blueprint, "expected tool %q is not in AllowedTools", name)
		}
	}
	for _, name := range blueprint.ForbiddenTools {
		if _, ok := allowed[name]; ok {
			return scenarioContractError(blueprint, "tool %q is both allowed and forbidden", name)
		}
	}
	return nil
}

func validateTrajectoryPolicyShape(blueprint ScenarioBlueprint) error {
	hint := blueprint.Gold.PromptHint
	switch blueprint.TrajectoryPolicy {
	case evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER:
		if len(blueprint.ExpectedTools) > 0 || hint != nil {
			return scenarioContractError(blueprint, "ANSWER scenarios declare no expected tools or prompt hint")
		}
	case evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_FIRST_CHOICE, evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_GUIDED:
		if len(blueprint.ExpectedTools) == 0 {
			return scenarioContractError(blueprint, "%s scenarios need expected tools", blueprint.TrajectoryPolicy)
		}
		if hint == nil {
			return scenarioContractError(blueprint, "%s scenarios need a prompt hint", blueprint.TrajectoryPolicy)
		}
		expected := stringSet(blueprint.ExpectedTools)
		for _, name := range hint.HintedTools {
			if _, ok := expected[name]; !ok {
				return scenarioContractError(blueprint, "hinted tool %q is not an expected tool", name)
			}
		}
	case evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_GOVERNED:
		if len(blueprint.ForbiddenTools) == 0 || len(blueprint.ExpectedTools) > 0 {
			return scenarioContractError(blueprint, "GOVERNED scenarios need forbidden tools and no expected tools")
		}
	default:
		return scenarioContractError(blueprint, "unknown trajectory policy %s", blueprint.TrajectoryPolicy)
	}
	return nil
}

func contentCheckHasConstraint(check *ScenarioContentCheck) bool {
	if check == nil {
		return false
	}
	return check.ExactToken != "" || len(check.ExactLabels) > 0 || check.LeadingLabel != "" || check.WordCount > 0 ||
		check.MaxSentences > 0 || len(check.RequiredTerms) > 0 || len(check.ForbiddenTerms) > 0 ||
		len(check.JSONStringFields) > 0 || len(check.JSONIntegerFields) > 0 || check.Interrogation || check.DiagnosisRubric != "" ||
		len(check.JudgedCriteria) > 0 || check.SearchPattern != ""
}

// validateJudgedCriteria fails closed on a judged criterion the judge could not
// decide or the grader could not attribute: an ID that is not kebab-case or is
// repeated, an empty statement, or a refusal criterion that names no criterion.
// A scenario that grades a policy decision must judge refusal, because a
// refusal is otherwise unobservable and would read as "did not refuse".
func validateJudgedCriteria(blueprint ScenarioBlueprint, check *ScenarioContentCheck) error {
	refusalRequired := slices.Contains(blueprint.Gold.RequiredEvidenceTypes, "policy_decision")
	for _, role := range blueprint.Gold.RoleCriteria {
		refusalRequired = refusalRequired || slices.Contains(role.RequiredEvidenceTypes, "policy_decision")
	}
	if check == nil {
		if refusalRequired {
			return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationRefusalCriterionRequired, "scenario has no content check")
		}
		return nil
	}
	seenTools := make(map[string]struct{}, len(check.ToolEvidence))
	for _, tool := range check.ToolEvidence {
		if _, duplicate := seenTools[tool]; duplicate || !slices.Contains(blueprint.AllowedTools, tool) || len(check.JudgedCriteria) == 0 {
			return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationJudgedCriterionInvalid, "tool evidence %q must be unique, allowed, and used by judged criteria", tool)
		}
		seenTools[tool] = struct{}{}
	}
	if check.SearchPattern != "" {
		matches, err := fixtureSearchMatches(blueprint.Input, ScenarioWorkspace{}, check.SearchPattern)
		if err != nil {
			return scenarioContractErrorWithCause(blueprint, err, "invalid search evidence contract")
		}
		if len(matches) == 0 || !slices.Contains(blueprint.ExpectedTools, constants.MCPToolSearchDirectory) ||
			len(check.JudgedCriteria) == 0 || !slices.Contains(check.ToolEvidence, constants.MCPToolSearchDirectory) {
			return scenarioContractError(blueprint, "search evidence requires a directory search, matching fixture lines, and tool-grounded judged criteria")
		}
	}
	seen := make(map[string]struct{}, len(check.JudgedCriteria))
	for _, criterion := range check.JudgedCriteria {
		if !investigation.ValidID(criterion.ID) {
			return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationJudgedCriterionInvalid, "criterion ID %q is not lowercase kebab-case", criterion.ID)
		}
		if _, dup := seen[criterion.ID]; dup {
			return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationJudgedCriterionInvalid, "criterion %q is repeated", criterion.ID)
		}
		seen[criterion.ID] = struct{}{}
		if strings.TrimSpace(criterion.Statement) == "" {
			return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationJudgedCriterionInvalid, "criterion %q has no statement", criterion.ID)
		}
	}
	if check.RefusalCriterion != "" {
		if _, ok := seen[check.RefusalCriterion]; !ok {
			return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationRefusalCriterionRequired, "refusal criterion %q is not a judged criterion", check.RefusalCriterion)
		}
	} else if refusalRequired {
		return scenarioContractErrorWithCause(blueprint, constants.ErrEvaluationRefusalCriterionRequired, "no refusal criterion is declared")
	}
	return nil
}

func validateDiagnosisRubric(blueprint ScenarioBlueprint, check *ScenarioContentCheck) error {
	if check == nil {
		return nil
	}
	switch check.DiagnosisRubric {
	case "", ScenarioDiagnosisRubricCheckoutTimeoutV1, ScenarioDiagnosisRubricCheckoutTimeoutV2, ScenarioDiagnosisRubricCheckoutTimeoutV3, ScenarioDiagnosisRubricCheckoutTimeoutV4, ScenarioDiagnosisRubricCheckoutTimeoutV5:
		return nil
	}
	return scenarioContractError(blueprint, "unknown diagnosis rubric %q", check.DiagnosisRubric)
}

func validateArgumentValidators(blueprint ScenarioBlueprint, catalog *agentToolCatalog) error {
	for _, validator := range blueprint.Gold.ArgumentValidators {
		tool, ok := catalog.Tool(validator.ToolName)
		if !ok {
			return scenarioContractError(blueprint, "validator for %q constrains unknown tool", validator.ToolName)
		}
		arguments := tool.Arguments()
		known := stringSet(arguments)
		for _, constraint := range validator.Arguments {
			if _, ok := known[constraint.Name]; !ok {
				return scenarioContractError(blueprint, "validator for %q constrains unknown argument %q", validator.ToolName, constraint.Name)
			}
			hasRegex := len(constraint.RegexMatches) > 0 || len(constraint.RegexRejects) > 0
			if hasRegex && (len(constraint.RegexMatches) == 0 || len(constraint.RegexRejects) == 0) {
				return scenarioContractError(blueprint, "regex constraint on %q.%q needs both match and reject samples", validator.ToolName, constraint.Name)
			}
		}
	}
	return nil
}

// validateGroundedToolArguments grades concrete hint values against their
// argument rules without executing tools. A search objective also supplies a
// reference regex when the model derives its pattern from the workspace.
func validateGroundedToolArguments(blueprint ScenarioBlueprint) error {
	// Derive a representative workspace so template tokens are rendered as
	// they are during grading. This performs no filesystem I/O.
	ws, err := NewScenarioWorkspace("/", "catalog-validation", blueprint.ScenarioID)
	if err != nil {
		return scenarioContractErrorWithCause(blueprint, err, "derive argument-validation workspace")
	}
	for _, validator := range blueprint.Gold.ArgumentValidators {
		for _, constraint := range validator.Arguments {
			var references []string
			if hint := blueprint.Gold.PromptHint; hint != nil {
				for _, argument := range hintArgumentsFor(hint, validator.ToolName, constraint.Name) {
					switch argument.Source {
					case sourcePrompt, sourceSeed, sourceWorkspace:
						references = append(references, argument.Value)
					}
				}
			}
			if len(constraint.RegexMatches)+len(constraint.RegexRejects) > 0 && len(references) == 0 {
				if check := blueprint.Gold.ContentCheck; validator.ToolName == constants.MCPToolSearchDirectory && constraint.Name == "pattern" && check != nil && check.SearchPattern != "" {
					references = append(references, check.SearchPattern)
				} else {
					return scenarioContractError(blueprint, "regex constraint on %q.%q needs a concrete prompt, seed, workspace, or search-objective reference value", validator.ToolName, constraint.Name)
				}
			}
			for _, reference := range references {
				if ok, failure := validateToolArgument(ws.Render(reference), constraint, ws); !ok {
					return scenarioContractError(blueprint, "reference value for %q.%q fails argument grading: %s", validator.ToolName, failure.FailedArg, failure.Rule)
				}
			}
		}
	}
	return nil
}

// validatePromptHint enforces INV-EVAL-CAMP-11: every required argument of
// every hinted tool is derivable from where the hint says it is.
func validatePromptHint(blueprint ScenarioBlueprint, catalog *agentToolCatalog) error {
	hint := blueprint.Gold.PromptHint
	if hint == nil {
		return nil
	}
	hinted := stringSet(hint.HintedTools)
	for _, argument := range hint.Arguments {
		if _, ok := hinted[argument.ToolName]; !ok {
			return scenarioContractError(blueprint, "hint argument %q names tool %q that is not hinted", argument.Name, argument.ToolName)
		}
	}
	for _, toolName := range hint.HintedTools {
		tool, ok := catalog.Tool(toolName)
		if !ok {
			return scenarioContractError(blueprint, "hint names unknown tool %q", toolName)
		}
		requiredArguments := tool.RequiredArguments()
		for _, required := range requiredArguments {
			matches := hintArgumentsFor(hint, toolName, required)
			if len(matches) != 1 {
				return scenarioUnanswerableError(blueprint, "required argument %q of %q has %d hint sources, want exactly 1", required, toolName, len(matches))
			}
			if err := validateHintArgumentSource(blueprint, matches[0]); err != nil {
				return err
			}
		}
	}
	return nil
}

func hintArgumentsFor(hint *ScenarioPromptHint, toolName, argumentName string) []ScenarioHintArgument {
	var matches []ScenarioHintArgument
	for _, argument := range hint.Arguments {
		if argument.ToolName == toolName && argument.Name == argumentName {
			matches = append(matches, argument)
		}
	}
	return matches
}

func validateHintArgumentSource(blueprint ScenarioBlueprint, argument ScenarioHintArgument) error {
	switch argument.Source {
	case evalv1.EvaluationHintArgumentSource_EVALUATION_HINT_ARGUMENT_SOURCE_PROMPT:
		if argument.Value == "" || !strings.Contains(blueprint.Input.UserPrompt, argument.Value) {
			return scenarioUnanswerableError(blueprint, "%q.%q is sourced from the prompt but the prompt does not contain %q", argument.ToolName, argument.Name, argument.Value)
		}
	case evalv1.EvaluationHintArgumentSource_EVALUATION_HINT_ARGUMENT_SOURCE_SEED:
		if argument.Value == "" || !seedContains(blueprint.Input.Seed, argument.Value) {
			return scenarioUnanswerableError(blueprint, "%q.%q is sourced from the seed but the seed does not contain %q", argument.ToolName, argument.Name, argument.Value)
		}
	case evalv1.EvaluationHintArgumentSource_EVALUATION_HINT_ARGUMENT_SOURCE_WORKSPACE:
		if !strings.Contains(blueprint.Input.UserPrompt, ScenarioWorkspaceToken) {
			return scenarioUnanswerableError(blueprint, "%q.%q is sourced from the workspace but the prompt never names it", argument.ToolName, argument.Name)
		}
		if argument.Value == "" {
			return scenarioUnanswerableError(blueprint, "%q.%q is sourced from the workspace but names no workspace path", argument.ToolName, argument.Name)
		}
		if !strings.HasPrefix(argument.Value, ScenarioWorkspaceToken) {
			return scenarioUnanswerableError(blueprint, "%q.%q workspace value %q is not under the workspace", argument.ToolName, argument.Name, argument.Value)
		}
	case evalv1.EvaluationHintArgumentSource_EVALUATION_HINT_ARGUMENT_SOURCE_OPERATOR_CONTEXT:
		if argument.Name == "target_operators" {
			return nil
		}
		if argument.ToolName == constants.MCPToolRestoreFile && argument.Name == "commit_hash" && argument.Value == "ledger_hash_before" &&
			(slices.Contains(blueprint.AllowedTools, constants.MCPToolFileHistory) || slices.Contains(blueprint.AllowedTools, constants.MCPToolFileDiff)) {
			for _, call := range blueprint.Input.Seed.ToolCalls {
				if call.ToolName == constants.MCPToolFileEdit && call.Disclosure == ScenarioSeedDisclosureChainOnly && call.Outcome == scenarioSeedSuccess {
					return nil
				}
			}
		}
		return scenarioUnanswerableError(blueprint, "%q.%q cannot come from operator context", argument.ToolName, argument.Name)
	case evalv1.EvaluationHintArgumentSource_EVALUATION_HINT_ARGUMENT_SOURCE_MODEL_AUTHORED:
		allowed := false
		switch {
		case argument.Name == "justification", argument.Name == "request":
			allowed = true
		case argument.ToolName == constants.MCPToolRunShellCommand && argument.Name == "command":
			allowed = strings.TrimSpace(argument.Value) != ""
		case argument.ToolName == constants.MCPToolSearchDirectory && argument.Name == "pattern":
			allowed = strings.TrimSpace(argument.Value) != ""
		}
		if !allowed {
			return scenarioUnanswerableError(blueprint, "%q.%q cannot be authored by the model", argument.ToolName, argument.Name)
		}
	default:
		return scenarioUnanswerableError(blueprint, "%q.%q has an unspecified hint source", argument.ToolName, argument.Name)
	}
	return nil
}

func seedContains(seed InvestigationSeed, value string) bool {
	for _, turn := range seed.Turns {
		if strings.Contains(turn.Content, value) {
			return true
		}
	}
	return false
}

func validateWorkspaceFiles(blueprint ScenarioBlueprint) error {
	files := blueprint.Input.WorkspaceFiles
	seen := make(map[string]struct{}, len(files))
	real := 0
	if blueprint.Input.Seed.CaseMemory != nil {
		real++ // materializeCaseMemory writes the typed notes before the scored turn
	}
	for _, file := range files {
		if err := validateWorkspaceRelPath(file.RelPath); err != nil {
			return scenarioContractErrorWithCause(blueprint, err, "workspace file %q is not a clean relative path", file.RelPath)
		}
		if _, dup := seen[file.RelPath]; dup {
			return scenarioContractError(blueprint, "duplicate workspace file %q", file.RelPath)
		}
		seen[file.RelPath] = struct{}{}
		if file.RelPath == scenarioCaseMemoryFile && blueprint.Input.Seed.CaseMemory != nil {
			return scenarioContractError(blueprint, "workspace file collides with the seeded case notes")
		}
		if !file.Decoy {
			real++
		}
	}
	if real > 0 || blueprint.ScenarioID == "recovery-tool-failure" {
		return nil
	}
	if strings.Contains(blueprint.Input.UserPrompt, ScenarioWorkspaceToken) || validatorsUseWorkspace(blueprint.Gold.ArgumentValidators) {
		return scenarioContractError(blueprint, "the prompt or validators use the workspace but it has no non-decoy file")
	}
	return nil
}

func validatorsUseWorkspace(validators []ToolArgumentValidator) bool {
	for _, validator := range validators {
		for _, constraint := range validator.Arguments {
			if strings.Contains(constraint.PathEquals, ScenarioWorkspaceToken) || strings.Contains(constraint.PathUnder, ScenarioWorkspaceToken) || strings.Contains(constraint.Equals, ScenarioWorkspaceToken) {
				return true
			}
		}
	}
	return false
}

func validateRequiredEvidenceTypes(blueprint ScenarioBlueprint) error {
	if slices.Contains(blueprint.EligibleRoles, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE) {
		for _, evidenceType := range blueprint.Gold.requiredEvidenceForRole("lite") {
			if slices.Contains([]string{"tool_call", "tool_decision", "governed_action"}, evidenceType) {
				return scenarioContractError(blueprint, "Lite cannot require tool evidence %s", evidenceType)
			}
		}
	}
	requirements := [][]string{blueprint.Gold.RequiredEvidenceTypes}
	for _, role := range blueprint.Gold.RoleCriteria {
		if slices.Contains(role.RequiredEvidenceTypes, "semantic_grade") {
			return scenarioContractError(blueprint, "role %s requires a semantic judge, which no executor provides", role.Role)
		}
		requirements = append(requirements, role.RequiredEvidenceTypes)
	}
	for _, evidenceTypes := range requirements {
		for _, evidenceType := range evidenceTypes {
			if _, ok := scenarioEvidenceTypes[evidenceType]; !ok {
				return scenarioContractError(blueprint, "required evidence type %q is not in the closed vocabulary", evidenceType)
			}
		}
	}
	return nil
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func validateWorkspaceRelPath(relPath string) error {
	if relPath == "" || path.IsAbs(relPath) || path.Clean(relPath) != relPath || relPath == "." || strings.HasPrefix(relPath, "../") || strings.Contains(relPath, "\x00") {
		return fmt.Errorf("evaluation: workspace file path %q must be a clean relative path inside the workspace: %w", relPath, constants.ErrPathValidation)
	}
	return nil
}

func validateFollowUps(blueprint ScenarioBlueprint) error {
	seen := make(map[string]bool, len(blueprint.Input.FollowUps))
	for _, fu := range blueprint.Input.FollowUps {
		if fu.ID == "" {
			return scenarioContractError(blueprint, "scripted follow-up has empty ID")
		}
		if fu.ResponseText == "" {
			return scenarioContractError(blueprint, "scripted follow-up %q has empty response_text", fu.ID)
		}
		if seen[fu.ID] {
			return scenarioContractError(blueprint, "duplicate scripted follow-up ID %q", fu.ID)
		}
		seen[fu.ID] = true
	}
	return nil
}

func validateGoldEvidenceReferences(blueprint ScenarioBlueprint) error {
	seen := make(map[string]bool, len(blueprint.Gold.GoldEvidenceReferences))
	for _, ref := range blueprint.Gold.GoldEvidenceReferences {
		if strings.TrimSpace(ref.ReferenceID) == "" {
			return scenarioContractError(blueprint, "gold evidence reference has empty reference_id")
		}
		if ref.ReferenceID != strings.TrimSpace(ref.ReferenceID) {
			return scenarioContractError(blueprint, "gold evidence reference %q has surrounding whitespace", ref.ReferenceID)
		}
		if seen[ref.ReferenceID] {
			return scenarioContractError(blueprint, "duplicate gold evidence reference ID %q", ref.ReferenceID)
		}
		seen[ref.ReferenceID] = true
		if ref.ActionType == "" && ref.TargetResource == "" && ref.CommandPattern == "" && ref.ExpectedContent == "" {
			return scenarioContractError(blueprint, "gold evidence reference %q must specify at least one criterion (action_type, target_resource, command_pattern, expected_content)", ref.ReferenceID)
		}
	}
	return nil
}
