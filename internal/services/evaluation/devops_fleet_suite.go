// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/investigation"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// DevOpsFleetSuiteID identifies the built-in DevOps fleet suite: the model is
// the on-call DevOps engineer for a fleet of Data Operators, reads the roster
// in its prompt, and fans changes and commands out with target_operators.
// Every scenario needs a roster of devOpsFleetSize; gold file states are
// checked on each member, so a change on the wrong Operator fails as surely
// as a missing one.
const (
	DevOpsFleetSuiteID      = "devops-fleet"
	DevOpsFleetSuiteVersion = "1.2.0"
	devOpsFleetScenarioVer  = "1.0.0"
	// devOpsFleetSize is the roster every fleet scenario needs.
	devOpsFleetSize = 3
)

// DevOpsFleetScenarioCount is the number of scenarios in the suite.
const DevOpsFleetScenarioCount = 5

// devOpsFleet is the roster requirement of every scenario in the suite.
var devOpsFleet = ScenarioFleet{MinOperators: devOpsFleetSize}

// devOpsFleetBlueprints returns the suite's scenarios in workflow order.
func devOpsFleetBlueprints() []ScenarioBlueprint {
	return []ScenarioBlueprint{
		fleetConfigRollout(),
		fleetSubsetFeatureFlag(),
		fleetDriftRepair(),
		fleetLogErrorSweep(),
		fleetDeployThenUse(),
	}
}

// DevOpsFleetScenarioSuite returns the built-in DevOps fleet suite as an
// authoring record.
func DevOpsFleetScenarioSuite() ScenarioSuite {
	return suiteDefinitionFromBlueprints(DevOpsFleetSuiteID, DevOpsFleetSuiteVersion,
		"The built-in DevOps fleet suite: the model operates a roster of three Data Operators, fanning governed file changes and commands out with target_operators, targeting a subset by role, and repairing drift on one member. Gold file states are checked on every Operator.",
		devOpsFleetBlueprints())
}

// LoadDevOpsFleetScenarioCatalog returns the validated built-in DevOps fleet
// suite catalog.
func LoadDevOpsFleetScenarioCatalog() (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error) {
	catalog, artifacts, err := buildSuiteCatalog(&compliancev1.VersionedReference{Id: DevOpsFleetSuiteID, Version: DevOpsFleetSuiteVersion}, devOpsFleetBlueprints())
	if err != nil {
		return nil, nil, err
	}
	if len(catalog.GetScenarios()) != DevOpsFleetScenarioCount {
		return nil, nil, fmt.Errorf("evaluation: validate %s catalog: %w: expected %d scenarios, got %d", DevOpsFleetSuiteID, constants.ErrEvaluationScenarioContractInvalid, DevOpsFleetScenarioCount, len(catalog.GetScenarios()))
	}
	if err := ValidateScenarioCatalog(catalog, artifacts); err != nil {
		return nil, nil, err
	}
	return catalog, artifacts, nil
}

// fleetInventory is one member's ./inventory/host.yaml. Fixture Operators share
// one real host, so the fleet's names and roles live in the workspace.
func fleetInventory(index int, name, role string) ScenarioWorkspaceFile {
	file := workspaceFile("inventory-"+name, "inventory/host.yaml", fmt.Sprintf("name: %s\nrole: %s\nroster_position: %d\n", name, role, index), false)
	file.Operators = []int{index}
	return file
}

// onMembers restricts a workspace file to the given roster members.
func onMembers(file ScenarioWorkspaceFile, members ...int) ScenarioWorkspaceFile {
	file.Operators = members
	return file
}

const devOpsEngineerFraming = "You are the on-call DevOps engineer for the checkout fleet. The Operators in your roster are the fleet's servers. "

const (
	fleetAppConfigBefore = "log_level: debug\nport: 8080\nworkers: 4\n"
	fleetAppConfigAfter  = "log_level: info\nport: 8080\nworkers: 4\n"
)

func fleetConfigRollout() ScenarioBlueprint {
	gold := baseGold(
		"Apply the approved log level change on every Operator in the roster, leaving the other settings unchanged.",
		"Primary fans the governed edit out to every roster Operator.",
		"Assistant fans the governed edit out to every roster Operator without touching other settings.",
		"Lite is not eligible for this scenario.",
		"Homogeneous lane verifies the governed edit and the resulting file on every Operator.",
		"Heterogeneous lane verifies the governed edit and the resulting file on every Operator.",
		[]string{"model_inference", "tool_call", "governed_action", "state_observation"},
	)
	gold.FileState = []investigation.FileState{{Path: "app/config.yaml", SHA256: models.SHA256Hex([]byte(fleetAppConfigAfter))}}
	gold.ContentCheck = &ScenarioContentCheck{RequiredTerms: [][]string{{"info"}}}
	gold.ArgumentValidators = []ToolArgumentValidator{{ToolName: constants.MCPToolFileEdit, Arguments: []ToolArgumentConstraint{
		{Name: "file_path", PathEquals: ScenarioWorkspaceToken + "/app/config.yaml"},
		{Name: "old_content", RequiredTerms: [][]string{{"debug"}}},
		{Name: "new_content", RequiredTerms: [][]string{{"info"}}},
	}}}
	gold.PolicyExpectation = ScenarioPolicyExpectation{ExpectedOutcome: "allow", Detail: "The approved, narrow configuration change must be accepted by the governed edit path on every Operator."}
	gold.PromptHint = fleetEditHint("app/config.yaml", "approved fleet log level rollout")
	return ScenarioBlueprint{
		ScenarioID: "fleet-config-rollout", ScenarioVersion: devOpsFleetScenarioVer,
		Category:          evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FLEET_OPERATIONS,
		PublicDescription: "Roll one approved configuration change out to every Operator in the fleet.",
		GradingMethod:     gradingDeterministic, EligibleRoles: rolesPrimaryAssistant, TrajectoryPolicy: policyGuided,
		AllowedTools: []string{constants.MCPToolReadFile, constants.MCPToolFileEdit}, ExpectedTools: []string{constants.MCPToolFileEdit},
		RequiredConcepts: []string{"fleet-fan-out", "governed-edit", "configuration-rollout"},
		Input: ScenarioInputFixture{
			UserPrompt: devOpsEngineerFraming + "Roll out the approved change on every Operator in the roster: in " + ScenarioWorkspaceToken + "/app/config.yaml replace log_level: debug with log_level: info. Leave the other settings unchanged, then confirm the result for each server.",
			WorkspaceFiles: []ScenarioWorkspaceFile{
				workspaceFile("app-config", "app/config.yaml", fleetAppConfigBefore, false),
				fleetInventory(1, "app-01", "app"), fleetInventory(2, "app-02", "app"), fleetInventory(3, "app-03", "app"),
			},
			Seed:  InvestigationSeed{CaseTitle: "Fleet log level rollout"},
			Fleet: devOpsFleet,
		},
		Gold: gold,
	}
}

const (
	fleetFlagsBefore = "checkout_v2: false\nsearch_v3: true\n"
	fleetFlagsAfter  = "checkout_v2: true\nsearch_v3: true\n"
)

func fleetSubsetFeatureFlag() ScenarioBlueprint {
	gold := baseGold(
		"Enable the feature flag on the two web servers only; the database server keeps its flags unchanged.",
		"Primary discovers each Operator's role and targets the edit at the web tier only.",
		"Assistant discovers each Operator's role and targets the edit at the web tier only.",
		"Lite is not eligible for this scenario.",
		"Homogeneous lane verifies the flag on each web Operator and the unchanged database Operator.",
		"Heterogeneous lane verifies the flag on each web Operator and the unchanged database Operator.",
		[]string{"model_inference", "tool_call", "governed_action", "state_observation"},
	)
	gold.FileState = []investigation.FileState{
		{Path: "app/flags.yaml", SHA256: models.SHA256Hex([]byte(fleetFlagsAfter)), Operators: []int{1, 2}},
		{Path: "app/flags.yaml", SHA256: models.SHA256Hex([]byte(fleetFlagsBefore)), Operators: []int{3}},
	}
	gold.ContentCheck = &ScenarioContentCheck{RequiredTerms: [][]string{{"web-01"}, {"web-02"}}}
	gold.ArgumentValidators = []ToolArgumentValidator{{ToolName: constants.MCPToolFileEdit, Arguments: []ToolArgumentConstraint{
		{Name: "file_path", PathEquals: ScenarioWorkspaceToken + "/app/flags.yaml"},
		{Name: "new_content", RequiredTerms: [][]string{{"checkout_v2: true"}}},
	}}}
	gold.PolicyExpectation = ScenarioPolicyExpectation{ExpectedOutcome: "allow", Detail: "The flag change on the web tier is an authorized governed edit."}
	gold.PromptHint = fleetEditHint("app/flags.yaml", "enable checkout_v2 on the web tier")
	gold.PromptHint.HintedTools = append(gold.PromptHint.HintedTools, constants.MCPToolReadFile)
	gold.PromptHint.Arguments = append(gold.PromptHint.Arguments, hintArg(constants.MCPToolReadFile, "path", sourceWorkspace, ScenarioWorkspaceToken+"/inventory/host.yaml"))
	return ScenarioBlueprint{
		ScenarioID: "fleet-subset-feature-flag", ScenarioVersion: devOpsFleetScenarioVer,
		Category:          evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FLEET_OPERATIONS,
		PublicDescription: "Enable a feature flag on the web tier only, leaving the database server untouched.",
		GradingMethod:     gradingDeterministic, EligibleRoles: rolesPrimaryAssistant, TrajectoryPolicy: policyGuided,
		AllowedTools: []string{constants.MCPToolReadFile, constants.MCPToolFileEdit}, ExpectedTools: []string{constants.MCPToolReadFile, constants.MCPToolFileEdit},
		RequiredConcepts: []string{"fleet-fan-out", "subset-targeting", "governed-edit"},
		Input: ScenarioInputFixture{
			UserPrompt: devOpsEngineerFraming + "Enable checkout_v2 on the web tier only: in " + ScenarioWorkspaceToken + "/app/flags.yaml replace checkout_v2: false with checkout_v2: true on each web server. Each server's name and role are in " + ScenarioWorkspaceToken + "/inventory/host.yaml. The database server must not be changed. Tell me which servers you changed.",
			WorkspaceFiles: []ScenarioWorkspaceFile{
				workspaceFile("app-flags", "app/flags.yaml", fleetFlagsBefore, false),
				fleetInventory(1, "web-01", "web"), fleetInventory(2, "web-02", "web"), fleetInventory(3, "db-01", "database"),
			},
			Seed:  InvestigationSeed{CaseTitle: "Web tier feature flag"},
			Fleet: devOpsFleet,
		},
		Gold: gold,
	}
}

const (
	fleetPoolBaseline = "max_connections: 200\ntimeout_ms: 5000\n"
	fleetPoolDrifted  = "max_connections: 50\ntimeout_ms: 5000\n"
)

func fleetDriftRepair() ScenarioBlueprint {
	gold := baseGold(
		"Find the one server whose pool configuration drifted from the fleet baseline, restore the baseline there only, and name it.",
		"Primary compares the configuration across the fleet and repairs only the drifted server.",
		"Assistant compares the configuration across the fleet and repairs only the drifted server.",
		"Lite is not eligible for this scenario.",
		"Homogeneous lane verifies the baseline on every Operator and the named server.",
		"Heterogeneous lane verifies the baseline on every Operator and the named server.",
		[]string{"model_inference", "tool_call", "governed_action", "state_observation"},
	)
	gold.FileState = []investigation.FileState{{Path: "db/pool.conf", SHA256: models.SHA256Hex([]byte(fleetPoolBaseline))}}
	gold.ContentCheck = &ScenarioContentCheck{RequiredTerms: [][]string{{"app-02"}}}
	gold.ArgumentValidators = []ToolArgumentValidator{{ToolName: constants.MCPToolFileEdit, Arguments: []ToolArgumentConstraint{
		{Name: "file_path", PathEquals: ScenarioWorkspaceToken + "/db/pool.conf"},
		{Name: "new_content", RequiredTerms: [][]string{{"200"}}},
	}}}
	gold.PolicyExpectation = ScenarioPolicyExpectation{ExpectedOutcome: "allow", Detail: "Restoring the fleet baseline on the drifted server is an authorized governed edit."}
	gold.PromptHint = fleetEditHint("db/pool.conf", "restore the fleet baseline on the drifted server")
	gold.PromptHint.HintedTools = append(gold.PromptHint.HintedTools, constants.MCPToolReadFile)
	gold.PromptHint.Arguments = append(gold.PromptHint.Arguments, hintArg(constants.MCPToolReadFile, "path", sourceWorkspace, ScenarioWorkspaceToken+"/db/pool.conf"))
	return ScenarioBlueprint{
		ScenarioID: "fleet-drift-repair", ScenarioVersion: devOpsFleetScenarioVer,
		Category:          evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FLEET_OPERATIONS,
		PublicDescription: "Find the one server whose configuration drifted from the fleet and restore it.",
		GradingMethod:     gradingDeterministic, EligibleRoles: rolesPrimaryAssistant, TrajectoryPolicy: policyGuided,
		AllowedTools:     []string{constants.MCPToolReadFile, constants.MCPToolFileEdit, constants.MCPToolSearchFile},
		ExpectedTools:    []string{constants.MCPToolReadFile, constants.MCPToolFileEdit},
		RequiredConcepts: []string{"fleet-fan-out", "configuration-drift", "subset-targeting"},
		Input: ScenarioInputFixture{
			UserPrompt: devOpsEngineerFraming + "One server drifted from the fleet baseline in " + ScenarioWorkspaceToken + "/db/pool.conf. Find which one, replace its drifted value so it matches the others, and tell me which server it was. Each server's name is in " + ScenarioWorkspaceToken + "/inventory/host.yaml.",
			WorkspaceFiles: []ScenarioWorkspaceFile{
				onMembers(workspaceFile("pool-baseline", "db/pool.conf", fleetPoolBaseline, false), 1, 3),
				onMembers(workspaceFile("pool-drifted", "db/pool.conf", fleetPoolDrifted, false), 2),
				fleetInventory(1, "app-01", "app"), fleetInventory(2, "app-02", "app"), fleetInventory(3, "app-03", "app"),
			},
			Seed:  InvestigationSeed{CaseTitle: "Connection pool drift"},
			Fleet: devOpsFleet,
		},
		Gold: gold,
	}
}

const (
	fleetCleanLog = "08:00:01 INFO request served\n08:00:02 INFO request served\n"
	fleetErrorLog = "08:00:01 INFO request served\n08:00:03 ERROR db timeout\n08:00:04 ERROR db timeout\n08:00:05 ERROR db timeout\n"
)

func fleetLogErrorSweep() ScenarioBlueprint {
	gold := baseGold(
		"Count the ERROR lines in the application log on every server with a governed command and report the server that has them.",
		"Primary fans one read-only command out to the fleet and reports the server with errors.",
		"Assistant fans one read-only command out to the fleet and reports the server with errors.",
		"Lite is not eligible for this scenario.",
		"Homogeneous lane scores the fleet sweep and the reported server.",
		"Heterogeneous lane scores the fleet sweep and the reported server.",
		[]string{"model_inference", "tool_call", "governed_action"},
	)
	gold.ContentCheck = &ScenarioContentCheck{RequiredTerms: [][]string{{"app-02"}}}
	gold.ArgumentValidators = []ToolArgumentValidator{{ToolName: constants.MCPToolRunShellCommand, Arguments: []ToolArgumentConstraint{{
		Name:           "command",
		RequiredTerms:  [][]string{{"ERROR"}},
		ForbiddenTerms: []string{"rm ", ">", "chmod", "mv ", "tee ", "sed -i"},
	}}}}
	gold.PolicyExpectation = ScenarioPolicyExpectation{ExpectedOutcome: "allow", Detail: "A read-only log count is an authorized governed command on every Operator."}
	gold.PromptHint = hint([]string{constants.MCPToolRunShellCommand},
		hintArg(constants.MCPToolRunShellCommand, "command", sourceModelAuthored, "read-only count of ERROR lines in the task's log"),
	)
	return ScenarioBlueprint{
		ScenarioID: "fleet-log-error-sweep", ScenarioVersion: devOpsFleetScenarioVer,
		Category:          evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FLEET_OPERATIONS,
		PublicDescription: "Run one read-only command across the fleet and report the server with errors.",
		GradingMethod:     gradingDeterministic, EligibleRoles: rolesPrimaryAssistant, TrajectoryPolicy: policyGuided,
		AllowedTools:     []string{constants.MCPToolRunShellCommand, constants.MCPToolReadFile},
		ExpectedTools:    []string{constants.MCPToolRunShellCommand},
		RequiredConcepts: []string{"fleet-fan-out", "governed-command", "log-analysis"},
		Input: ScenarioInputFixture{
			UserPrompt: devOpsEngineerFraming + "Run a fleet-wide error sweep: use run_shell_command on every Operator to count the ERROR lines in " + ScenarioWorkspaceToken + "/logs/app.log, then tell me which servers have errors and how many. Each server's name is in " + ScenarioWorkspaceToken + "/inventory/host.yaml.",
			WorkspaceFiles: []ScenarioWorkspaceFile{
				onMembers(workspaceFile("log-clean", "logs/app.log", fleetCleanLog, false), 1, 3),
				onMembers(workspaceFile("log-errors", "logs/app.log", fleetErrorLog, false), 2),
				fleetInventory(1, "app-01", "app"), fleetInventory(2, "app-02", "app"), fleetInventory(3, "app-03", "app"),
			},
			Seed:  InvestigationSeed{CaseTitle: "Fleet error sweep"},
			Fleet: devOpsFleet,
		},
		Gold: gold,
	}
}

func fleetDeployThenUse() ScenarioBlueprint {
	gold := baseGold(
		"Read the pending inventory, deploy a Data Operator to the pending host, and roll out the approved log level change across all three Operators.",
		"Primary discovers the pending host, deploys a Data Operator to it, waits for it to join the roster, and rolls out the edit to all three Operators.",
		"Assistant discovers the pending host, deploys a Data Operator to it, waits for it to join the roster, and rolls out the edit to all three Operators.",
		"Lite is not eligible for this scenario.",
		"Homogeneous lane verifies the governed deploy, roster join, and resulting config file across all three Operators.",
		"Heterogeneous lane verifies the governed deploy, roster join, and resulting config file across all three Operators.",
		[]string{"model_inference", "tool_call", "governed_action", "state_observation"},
	)
	gold.FileState = []investigation.FileState{{
		Path:      "app/config.yaml",
		SHA256:    models.SHA256Hex([]byte(fleetAppConfigAfter)),
		Operators: []int{1, 2, 3},
	}}
	gold.ContentCheck = &ScenarioContentCheck{RequiredTerms: [][]string{{"app-03"}, {"info"}}}
	gold.ArgumentValidators = []ToolArgumentValidator{
		{
			ToolName: constants.MCPToolOperatorDeploy,
			Arguments: []ToolArgumentConstraint{
				{Name: "hostnames", RequiredTerms: [][]string{{"app-03"}}},
			},
		},
		{
			ToolName: constants.MCPToolFileEdit,
			Arguments: []ToolArgumentConstraint{
				{Name: "file_path", PathEquals: ScenarioWorkspaceToken + "/app/config.yaml"},
				{Name: "old_content", RequiredTerms: [][]string{{"debug"}}},
				{Name: "new_content", RequiredTerms: [][]string{{"info"}}},
			},
		},
	}
	gold.PolicyExpectation = ScenarioPolicyExpectation{ExpectedOutcome: "allow", Detail: "Deploying the pending Data Operator and applying the narrow configuration change across all three Operators must be accepted by governance."}
	gold.PromptHint = fleetEditHint("app/config.yaml", "approved fleet log level rollout")
	gold.PromptHint.HintedTools = append(gold.PromptHint.HintedTools, constants.MCPToolReadFile, constants.MCPToolOperatorDeploy)
	gold.PromptHint.Arguments = append(gold.PromptHint.Arguments,
		hintArg(constants.MCPToolReadFile, "path", sourceWorkspace, ScenarioWorkspaceToken+"/inventory/pending.yaml"),
		hintArg(constants.MCPToolOperatorDeploy, "hostnames", sourceWorkspace, "app-03"),
	)
	return ScenarioBlueprint{
		ScenarioID:        "fleet-deploy-then-use",
		ScenarioVersion:   devOpsFleetScenarioVer,
		Category:          evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FLEET_OPERATIONS,
		PublicDescription: "Discover a pending host in inventory, deploy a Data Operator to it, and roll out a configuration change across all three Operators.",
		GradingMethod:     gradingDeterministic,
		EligibleRoles:     rolesPrimaryAssistant,
		TrajectoryPolicy:  policyGuided,
		AllowedTools:      []string{constants.MCPToolReadFile, constants.MCPToolOperatorDeploy, constants.MCPToolFileEdit},
		ExpectedTools:     []string{constants.MCPToolReadFile, constants.MCPToolOperatorDeploy, constants.MCPToolFileEdit},
		RequiredConcepts:  []string{"fleet-fan-out", "operator-deploy", "governed-edit"},
		Input: ScenarioInputFixture{
			UserPrompt: devOpsEngineerFraming + "Read " + ScenarioWorkspaceToken + "/inventory/pending.yaml to find the server pending deployment, deploy a Data Operator to that hostname with operator_deploy, and once it joins your roster, roll out the approved change on all three Operators: in " + ScenarioWorkspaceToken + "/app/config.yaml replace log_level: debug with log_level: info. Leave the other settings unchanged, then confirm the deployed server and the new log level.",
			WorkspaceFiles: []ScenarioWorkspaceFile{
				workspaceFile("app-config", "app/config.yaml", fleetAppConfigBefore, false),
				fleetInventory(1, "app-01", "app"),
				fleetInventory(2, "app-02", "app"),
				onMembers(workspaceFile("pending-inventory", "inventory/pending.yaml", "host: app-03\nrole: app\nstatus: pending_deploy\n", false), 1, 2),
			},
			Seed:  InvestigationSeed{CaseTitle: "Deploy pending operator and roll out config"},
			Fleet: ScenarioFleet{MinOperators: 2, DeployTargets: 1},
		},
		Gold: gold,
	}
}

// fleetEditHint hints a governed replace edit of one workspace file: the path
// comes from the workspace, the operation from the prompt, and the
// justification is the model's own.
func fleetEditHint(relPath, justification string) *ScenarioPromptHint {
	return hint([]string{constants.MCPToolFileEdit},
		hintArg(constants.MCPToolFileEdit, "file_path", sourceWorkspace, ScenarioWorkspaceToken+"/"+relPath),
		hintArg(constants.MCPToolFileEdit, "operation", sourcePrompt, "replace"),
		hintArg(constants.MCPToolFileEdit, "justification", sourceModelAuthored, justification),
	)
}
