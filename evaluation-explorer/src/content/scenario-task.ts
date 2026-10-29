// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Types, category copy, and lookups for the scenario catalog. The scenario data
// itself lives in scenario-catalog.generated.ts, which is generated from
// internal/services/evaluation/scenario_catalog_definitions.go
// (`make explorer-catalog-generate`); never hand-copy a scenario here.

import type { ModelRole, ScenarioCategory } from '../contract/types';
import { SCENARIO_CATEGORIES } from '../contract/types';
import { ZTD_ARCHITECTURE_DOCS, ZTD_REPO_URL } from './platform';
import { SCENARIO_CATALOG_ID, SCENARIO_CATALOG_VERSION, SCENARIO_TASKS } from './scenario-catalog.generated';

export { SCENARIO_CATALOG_ID, SCENARIO_CATALOG_VERSION, SCENARIO_TASKS };

const CATALOG_SOURCE_PATH = 'internal/services/evaluation/scenario_catalog_definitions.go';

export type ScenarioGradingMethod = 'deterministic' | 'semantic_judge';

/** How the grader reads the model's trajectory for a scenario. */
export type ScenarioTrajectoryPolicy = 'answer' | 'first_choice' | 'guided' | 'governed';

/** Where a required tool argument can be derived from. The value is private. */
export type ScenarioHintSource = 'prompt' | 'seed' | 'workspace' | 'operator_context' | 'model_authored';

/** True when a scenario's expected behavior is direct command execution. */
export function isCapabilityQualificationScenario(expectedTools: readonly string[] | undefined): boolean {
  return expectedTools?.includes('run_shell_command') ?? false;
}

export type ScenarioInlineContent = {
  kind: string;
  label: string;
  content: string;
};

export type ScenarioWorkspaceFile = {
  label: string;
  relPath: string;
  content: string;
  decoy?: boolean;
};

export type ScenarioHintArgument = {
  toolName: string;
  argumentName: string;
  source: ScenarioHintSource;
};

export type ScenarioPromptHint = {
  hintedTools: readonly string[];
  arguments: readonly ScenarioHintArgument[];
};

/** One gold criterion a designated role is graded against. */
export type ScenarioRoleCriterion = {
  id: string;
  description: string;
};

/** The gold criteria for one role the scenario is eligible for. */
export type ScenarioRoleCriteria = {
  role: ModelRole;
  criteria: readonly ScenarioRoleCriterion[];
};

/** A binary statement the pinned judge scores the final reply against. Its
 *  grade is published as `content:<id>`. */
export type ScenarioJudgedCriterion = {
  id: string;
  statement: string;
};

export type ScenarioTaskDefinition = {
  id: string;
  category: ScenarioCategory;
  publicDescription: string;
  gradingMethod: ScenarioGradingMethod;
  trajectoryPolicy: ScenarioTrajectoryPolicy;
  /** Roles the scenario is scored for, in primary, assistant, lite order. */
  eligibleRoles: readonly ModelRole[];
  userPrompt: string;
  inlineContext?: readonly ScenarioInlineContent[];
  workspaceFiles?: readonly ScenarioWorkspaceFile[];
  expectedBehavior: string;
  /** Gold criteria for the eligible roles only, in eligibleRoles order. */
  roleCriteria: readonly ScenarioRoleCriteria[];
  judgedCriteria?: readonly ScenarioJudgedCriterion[];
  /** Id of the judged criterion stating the reply refuses the request. */
  refusalCriterion?: string;
  requiredConcepts: readonly string[];
  allowedTools?: readonly string[];
  expectedTools?: readonly string[];
  forbiddenTools?: readonly string[];
  promptHint?: ScenarioPromptHint;
};

export const SCENARIO_CATEGORY_COPY: Record<ScenarioCategory, { label: string; blurb: string }> = {
  instruction_adherence: {
    label: 'Instruction adherence',
    blurb: 'Follow constraints, formats, and stop conditions',
  },
  tool_selection: {
    label: 'Tool selection',
    blurb: 'Pick the right tool for the intent',
  },
  tool_arguments: {
    label: 'Tool arguments',
    blurb: 'Populate schemas and semantic arguments correctly',
  },
  technical_analysis: {
    label: 'Technical analysis',
    blurb: 'Interpret host and log evidence accurately',
  },
  routing_delegation: {
    label: 'Routing & delegation',
    blurb: 'Route work to the correct role or escalate',
  },
  verification: {
    label: 'Verification',
    blurb: 'Validate outputs before committing',
  },
  security_policy: {
    label: 'Security & policy',
    blurb: 'Respect authorization and data-handling policy',
  },
  recovery: {
    label: 'Recovery',
    blurb: 'Recover from tool or execution failures',
  },
  final_response: {
    label: 'Final response',
    blurb: 'Synthesize a correct user-facing answer',
  },
  containment: {
    label: 'Containment',
    blurb: 'Act only through the governed boundary, never around it',
  },
};

export const SCENARIO_TASK_BY_ID = new Map(SCENARIO_TASKS.map((task) => [task.id, task]));

export const SCENARIO_TASKS_BY_CATEGORY = SCENARIO_CATEGORIES.reduce(
  (groups, category) => {
    groups.set(
      category,
      SCENARIO_TASKS.filter((task) => task.category === category),
    );
    return groups;
  },
  new Map<ScenarioCategory, ScenarioTaskDefinition[]>(),
);

/** Categories the published catalog has scenarios for, in contract order. A
 *  category the contract names but the catalog does not yet exercise is not
 *  listed as an empty section. */
export const CATALOG_SCENARIO_CATEGORIES: readonly ScenarioCategory[] = SCENARIO_CATEGORIES.filter(
  (category) => (SCENARIO_TASKS_BY_CATEGORY.get(category)?.length ?? 0) > 0,
);

/** Category label, blurb, and the scenario count taken from the catalog. */
export const SCENARIO_CATEGORY_META: Record<
  ScenarioCategory,
  { label: string; blurb: string; count: number }
> = Object.fromEntries(
  SCENARIO_CATEGORIES.map((category) => [
    category,
    { ...SCENARIO_CATEGORY_COPY[category], count: SCENARIO_TASKS_BY_CATEGORY.get(category)?.length ?? 0 },
  ]),
) as Record<ScenarioCategory, { label: string; blurb: string; count: number }>;

export function scenarioTaskSourceUrl(_task: ScenarioTaskDefinition): string {
  return scenarioCatalogSourceUrl();
}

export function scenarioCatalogSourceUrl(): string {
  return `${ZTD_REPO_URL}/blob/main/${CATALOG_SOURCE_PATH}`;
}

export function scenarioCatalogDocsUrl(): string {
  return ZTD_ARCHITECTURE_DOCS.evals;
}

export function formatGradingMethod(method: ScenarioGradingMethod): string {
  return method === 'semantic_judge' ? 'Semantic judge' : 'Deterministic';
}

export const TRAJECTORY_POLICY_META: Record<ScenarioTrajectoryPolicy, { label: string; note: string }> = {
  answer: {
    label: 'Direct answer',
    note: 'The answer is graded; an unneeded tool call is recorded but does not fail the scenario.',
  },
  first_choice: {
    label: 'First-choice tool',
    note: 'The model must call the expected tool with correct arguments.',
  },
  guided: {
    label: 'Guided tool use',
    note: 'The model may recover from a tool error by acting on the guidance it is shown; repeating the same failing call fails.',
  },
  governed: {
    label: 'Governed denial',
    note: 'Passing means not circumventing a policy denial and explaining the refusal.',
  },
};

export const HINT_SOURCE_LABEL: Record<ScenarioHintSource, string> = {
  prompt: 'prompt',
  seed: 'seed',
  workspace: 'workspace',
  operator_context: 'operator context',
  model_authored: 'model',
};

export function formatHintArguments(hint: ScenarioPromptHint): string {
  return hint.arguments
    .map((argument) => `${argument.toolName}.${argument.argumentName} ← ${HINT_SOURCE_LABEL[argument.source]}`)
    .join(', ');
}
