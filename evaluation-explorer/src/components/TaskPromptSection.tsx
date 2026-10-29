// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { ReactNode } from 'react';
import {
  formatHintArguments,
  TRAJECTORY_POLICY_META,
  type ScenarioTaskDefinition,
} from '../content/scenario-task';
import type { ModelRole } from '../contract/types';
import { roleLabel } from '../views/derived';

function taskHasToolBoundaries(task: ScenarioTaskDefinition): boolean {
  return Boolean(task.allowedTools?.length || task.expectedTools?.length || task.forbiddenTools?.length);
}

function FoldedSection({
  title,
  fact,
  className,
  label,
  children,
}: {
  title: string;
  fact?: string;
  className?: string;
  label: string;
  children: ReactNode;
}) {
  return (
    <details className={`signal-fold task-detail-section${className ? ` ${className}` : ''}`} aria-label={label}>
      <summary className="signal-fold-summary">
        <h2>{title}</h2>
        {fact ? <span className="signal-fold-fact">{fact}</span> : null}
      </summary>
      <div className="signal-fold-body">{children}</div>
    </details>
  );
}

function gradingMethodNote(task: ScenarioTaskDefinition): string {
  const base =
    task.gradingMethod === 'semantic_judge'
      ? 'An LLM judge scores the interaction against this rubric.'
      : 'Fixed rules over trace evidence decide pass or fail.';
  return taskHasToolBoundaries(task)
    ? `${base} Tool boundaries below refine what counts as a correct tool choice.`
    : base;
}

/** The designated role's gold criterion, stated in one line for a headline. */
export function TaskRoleCriterion({ task, role }: { task: ScenarioTaskDefinition; role: ModelRole }) {
  const criteria = task.roleCriteria.find((entry) => entry.role === role)?.criteria ?? [];
  if (criteria.length === 0) return null;
  return (
    <p className="assignment-role-criterion">
      <strong>{roleLabel(role)} criterion.</strong> {criteria.map((criterion) => criterion.description).join(' ')}
    </p>
  );
}

/** Gold criteria for every role the scenario is scored for. Ineligible roles
 *  are not published, so a role never reads a placeholder as its criterion. */
function TaskRoleCriteria({ task }: { task: ScenarioTaskDefinition }) {
  const entries = task.roleCriteria.filter((entry) => entry.criteria.length > 0);
  if (entries.length === 0) return null;
  return (
    <div className="task-criteria-group" aria-label="Role criteria">
      <h3>Role criteria</h3>
      <ul className="task-criteria-list">
        {entries.flatMap((entry) =>
          entry.criteria.map((criterion) => (
            <li key={`${entry.role}:${criterion.id}`}>
              <strong>{roleLabel(entry.role)}.</strong> {criterion.description}
            </li>
          )),
        )}
      </ul>
    </div>
  );
}

/** The binary requirements the pinned judge scores the final reply against. */
function TaskJudgedCriteria({ task }: { task: ScenarioTaskDefinition }) {
  if (!task.judgedCriteria?.length) return null;
  return (
    <div className="task-criteria-group" aria-label="Judged requirements">
      <h3>Judged requirements</h3>
      <ul className="task-criteria-list">
        {task.judgedCriteria.map((criterion) => (
          <li key={criterion.id}>
            <code>{criterion.id}</code>
            {criterion.id === task.refusalCriterion ? <span className="task-flag">refusal</span> : null}{' '}
            {criterion.statement}
          </li>
        ))}
      </ul>
    </div>
  );
}

export function TaskPromptSection({
  task,
  className,
  fold = false,
}: {
  task: ScenarioTaskDefinition;
  className?: string;
  fold?: boolean;
}) {
  const note = <p className="task-section-note">User prompt sent to the agent.</p>;
  if (fold) {
    return (
      <FoldedSection title="What the model is asked" fact={task.userPrompt} className={`assignment-section-asked${className ? ` ${className}` : ''}`} label="What the model is asked">
        {note}
      </FoldedSection>
    );
  }
  return (
    <section
      className={`task-detail-section assignment-section-asked${className ? ` ${className}` : ''}`}
      aria-label="What the model is asked"
    >
      <h2>What the model is asked</h2>
      {note}
      <blockquote className="task-prompt">{task.userPrompt}</blockquote>
    </section>
  );
}

export function TaskProvidedSection({
  task,
  className,
  fold = false,
}: {
  task: ScenarioTaskDefinition;
  className?: string;
  fold?: boolean;
}) {
  const hasInlineContext = Boolean(task.inlineContext && task.inlineContext.length > 0);
  const hasWorkspaceFiles = Boolean(task.workspaceFiles && task.workspaceFiles.length > 0);
  const hasAttachments = hasInlineContext || hasWorkspaceFiles;

  const providedFact = hasAttachments
    ? `${(task.inlineContext?.length ?? 0) + (task.workspaceFiles?.length ?? 0)} attached`
    : 'Prompt only';
  const head = (
    <p className="task-section-note">
      Context, synthetic content, and tool boundaries supplied with the task.
    </p>
  );
  const body = (
    <>
      {head}

      {hasInlineContext ? (
        <div className="task-attachments-list">
          {task.inlineContext!.map((att, idx) => (
            <div key={`${att.label}-${idx}`} className="task-attachment-item">
              <div className="task-attachment-header">
                <span className="task-attachment-label">{att.label}</span>
                <span className="task-attachment-kind">{att.kind}</span>
              </div>
              <pre className="task-attachment-content"><code>{att.content}</code></pre>
            </div>
          ))}
        </div>
      ) : null}

      {hasWorkspaceFiles ? (
        <div className="task-attachments-list">
          {task.workspaceFiles!.map((file, idx) => (
            <div key={`${file.relPath}-${idx}`} className="task-attachment-item">
              <div className="task-attachment-header">
                <span className="task-attachment-label">{file.label}</span>
                <span className="task-attachment-kind">{file.decoy ? 'decoy file' : 'workspace file'}</span>
                <span className="task-attachment-path">{file.relPath}</span>
              </div>
              <pre className="task-attachment-content"><code>{file.content}</code></pre>
            </div>
          ))}
        </div>
      ) : null}

      {!hasAttachments ? (
        <p className="task-provided-none">No synthetic content provided (raw prompt only).</p>
      ) : null}

      <p className="task-system-context">
        The model is always offered the full production tool set. The scenario&apos;s allowed tools
        are graded, not hidden.
      </p>

      {task.promptHint ? (
        <div className="task-provided-tools">
          <strong>Prompt hint:</strong>{' '}
          <span>
            {task.promptHint.hintedTools.join(', ')} ({formatHintArguments(task.promptHint)})
          </span>
        </div>
      ) : null}
    </>
  );
  if (fold) {
    return (
      <FoldedSection title="What the model was provided" fact={providedFact} className={`assignment-section-provided${className ? ` ${className}` : ''}`} label="What the model was provided">
        {body}
      </FoldedSection>
    );
  }
  return (
    <section
      className={`task-detail-section assignment-section-provided${className ? ` ${className}` : ''}`}
      aria-label="What the model was provided"
    >
      <h2>What the model was provided</h2>
      {body}
    </section>
  );
}

export function TaskExpectationSection({
  task,
  className,
  fold = false,
  showRoleCriteria = true,
}: {
  task: ScenarioTaskDefinition;
  className?: string;
  fold?: boolean;
  /** An assignment states its designated role's criterion in its headline
   *  (TaskRoleCriterion), so it lists the judged requirements only. */
  showRoleCriteria?: boolean;
}) {
  const criteriaBody = (
    <>
      <p className="task-section-note">{gradingMethodNote(task)}</p>
      <p>{task.expectedBehavior}</p>
      {showRoleCriteria ? <TaskRoleCriteria task={task} /> : null}
      <TaskJudgedCriteria task={task} />
      <p className="task-section-note">
        <strong>{TRAJECTORY_POLICY_META[task.trajectoryPolicy].label}.</strong>{' '}
        {TRAJECTORY_POLICY_META[task.trajectoryPolicy].note}
      </p>
    </>
  );
  const boundaryCount = (task.allowedTools?.length ?? 0) + (task.expectedTools?.length ?? 0) + (task.forbiddenTools?.length ?? 0);
  const boundaries = taskHasToolBoundaries(task) ? (
    fold ? (
      <FoldedSection title="Tool boundaries" fact={`${boundaryCount} named`} label="Tool boundaries">
        <p className="task-section-note">
          The model is offered every production tool. Allowed tools are what the scenario permits.
          Forbidden tools fail the scenario if the model calls them.
        </p>
        <ToolBoundaryLists task={task} />
      </FoldedSection>
    ) : (
      <section className="task-detail-section" aria-label="Tool boundaries">
        <h2>Tool boundaries</h2>
        <p className="task-section-note">
          The model is offered every production tool. <strong>Allowed</strong> tools are what the
          scenario permits: calling any other tool and having it succeed fails the scenario.{' '}
          <strong>Forbidden</strong> tools are the ones the scenario must not see used. No tool
          is both.
        </p>
        <ToolBoundaryLists task={task} />
      </section>
    )
  ) : null;
  return (
    <>
      {fold ? (
        <FoldedSection title="Pass criteria" fact={TRAJECTORY_POLICY_META[task.trajectoryPolicy].label} className={className} label="What the model is expected to do">
          {criteriaBody}
        </FoldedSection>
      ) : (
        <section className={`task-detail-section assignment-section-expected${className ? ` ${className}` : ''}`} aria-label="What the model is expected to do">
          <h2>Pass criteria</h2>
          {criteriaBody}
        </section>
      )}
      {boundaries}
    </>
  );
}

function ToolBoundaryLists({ task }: { task: ScenarioTaskDefinition }) {
  return (
    <div className="task-tool-grid">
      {task.allowedTools?.length ? (
        <div>
          <h3>Allowed tools</h3>
          <ul>{task.allowedTools.map((tool) => <li key={tool}><code>{tool}</code></li>)}</ul>
        </div>
      ) : null}
      {task.expectedTools?.length ? (
        <div>
          <h3>Expected tools</h3>
          <ul>{task.expectedTools.map((tool) => <li key={tool}><code>{tool}</code></li>)}</ul>
        </div>
      ) : null}
      {task.forbiddenTools?.length ? (
        <div>
          <h3>Forbidden tools</h3>
          <ul>{task.forbiddenTools.map((tool) => <li key={tool}><code>{tool}</code></li>)}</ul>
        </div>
      ) : null}
    </div>
  );
}
