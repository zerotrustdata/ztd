// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Task catalog — the frozen default-suite scenarios grouped by behavior category.

import { useEffect, useState, type ReactNode } from 'react';
import { Link, NavLink, useNavigate, useParams } from 'react-router-dom';
import {
  CATALOG_SCENARIO_CATEGORIES,
  formatGradingMethod,
  SCENARIO_CATALOG_ID,
  SCENARIO_CATALOG_VERSION,
  SCENARIO_CATEGORY_META,
  SCENARIO_TASK_BY_ID,
  SCENARIO_TASKS,
  SCENARIO_TASKS_BY_CATEGORY,
  scenarioCatalogDocsUrl,
  scenarioCatalogSourceUrl,
  scenarioTaskSourceUrl,
  TRAJECTORY_POLICY_META,
  type ScenarioTaskDefinition,
} from '../content/scenario-task';
import { ZTD_REPO_URL } from '../content/platform';
import type { ScenarioCategory } from '../contract/types';
import { TaskExpectationSection, TaskPromptSection, TaskProvidedSection } from '../components/TaskPromptSection';
import { DetailLayout, NON_VERDICT_NOTE, PageHero, PanelSection, TonePill } from '../components/evidence';
import { assignmentVerdictTone, isNonVerdictOutcome } from './derived';
import { useStoreState } from '../state/store';
import { scrollToAnchor, scrollToAnchorOnClick } from '../utils/scroll';

function categoryAnchor(category: ScenarioCategory): string {
  return `category-${category}`;
}

function TaskFlag({ children }: { children: ReactNode }) {
  return <span className="task-flag">{children}</span>;
}

function TaskFlagNotes({ task }: { task: ScenarioTaskDefinition }) {
  return (
    <ul className="task-flag-notes">
      <li>{TRAJECTORY_POLICY_META[task.trajectoryPolicy].note}</li>
    </ul>
  );
}

function TaskDetailPanel({ task }: { task: ScenarioTaskDefinition }) {
  const meta = SCENARIO_CATEGORY_META[task.category];
  const recent = useStoreState((state) => Array.from(state.assignments.values())
    .filter((assignment) => assignment.task_id.split('@')[0] === task.id)
    .sort((a, b) => b.observed_at.localeCompare(a.observed_at)).slice(0, 10));

  return (
    <article className="task-detail" aria-label={task.id}>
        <nav className="breadcrumb" aria-label="Breadcrumb">
          <Link to="/tasks">All tasks</Link>
          <span aria-hidden="true"> / </span>
          <Link to={`/tasks#${categoryAnchor(task.category)}`}>{meta.label}</Link>
        </nav>
      <PageHero kicker={meta.label} title={task.id} tone="info">
        <p className="task-detail-lede">{task.publicDescription}</p>
        <div className="task-detail-meta">
          <TaskFlag>{meta.label}</TaskFlag>
          <TaskFlag>{formatGradingMethod(task.gradingMethod)}</TaskFlag>
          <TaskFlag>{TRAJECTORY_POLICY_META[task.trajectoryPolicy].label}</TaskFlag>
        </div>
        <TaskFlagNotes task={task} />
      </PageHero>
      <DetailLayout aside={<>
        <PanelSection title="Concepts measured" note="Tags for catalog grouping and campaign reporting.">
          <ul className="task-concept-list">
            {task.requiredConcepts.map((concept) => (
              <li key={concept}><code>{concept}</code></li>
            ))}
          </ul>
        </PanelSection>
        <PanelSection className="task-detail-sources" title="Source references">
          <ul>
            <li>
              <a href={scenarioTaskSourceUrl(task)} target="_blank" rel="noopener noreferrer">
                Scenario definition in ztd ({task.id})
              </a>
            </li>
            <li>
              <a href={scenarioCatalogSourceUrl()} target="_blank" rel="noopener noreferrer">
                Full scenario catalog source
              </a>
            </li>
            <li>
              <a href={scenarioCatalogDocsUrl()} target="_blank" rel="noopener noreferrer">
                Evaluation architecture documentation
              </a>
            </li>
          </ul>
        </PanelSection>
        {recent.length > 0 ? <PanelSection title="Recent assignments of this task">
          <ul className="sibling-list">
            {recent.map((assignment) => <li key={`${assignment.dataset_id}:${assignment.assignment_id}`}>
              <Link to={`/evaluations/${assignment.dataset_id}/${assignment.run_id}/assignments/${assignment.assignment_id}`}>{assignment.assignment_id}</Link>
              <TonePill tone={assignmentVerdictTone(assignment.terminal_status, assignment.verification_disposition)} title={isNonVerdictOutcome(assignment.terminal_status) ? NON_VERDICT_NOTE : undefined}>{assignment.terminal_status.replace(/_/g, ' ')}</TonePill>
            </li>)}
          </ul>
        </PanelSection> : null}
      </>}>
        <TaskPromptSection task={task} />
        <TaskProvidedSection task={task} />
        <TaskExpectationSection task={task} />
      </DetailLayout>
    </article>
  );
}

function TaskCard({ task }: { task: ScenarioTaskDefinition }) {
  return (
    <article className={`task-card task-card-${task.category}`} id={task.id}>
      <header className="task-card-head">
        <h3>
          <Link to={`/tasks/${task.id}`}>{task.id}</Link>
        </h3>
        <span
          className="task-card-grading"
          title={
            task.gradingMethod === 'semantic_judge'
              ? 'Graded by an LLM rubric over the interaction trace'
              : 'Graded by fixed rules over trace evidence'
          }
        >
          {formatGradingMethod(task.gradingMethod)}
        </span>
      </header>
      <p className="task-card-description">{task.publicDescription}</p>
      <div className="task-detail-meta">
        <TaskFlag>{TRAJECTORY_POLICY_META[task.trajectoryPolicy].label}</TaskFlag>
        {task.allowedTools ? <TaskFlag>{task.allowedTools.length} allowed tools</TaskFlag> : null}
      </div>
      <p className="task-card-prompt">{task.userPrompt}</p>
      <footer className="task-card-foot">
        <Link to={`/tasks/${task.id}`} className="task-card-detail-link">
          View full definition
        </Link>
        <a
          href={scenarioTaskSourceUrl(task)}
          target="_blank"
          rel="noopener noreferrer"
          className="task-card-source-link"
        >
          ztd source
        </a>
      </footer>
    </article>
  );
}

function CategorySection({ category }: { category: ScenarioCategory }) {
  const meta = SCENARIO_CATEGORY_META[category];
  const tasks = SCENARIO_TASKS_BY_CATEGORY.get(category) ?? [];

  return (
    <section
      id={categoryAnchor(category)}
      className="docs-section tasks-category-section"
      aria-labelledby={`${category}-title`}
    >
      <h2 id={`${category}-title`} className="docs-section-title">
        {meta.label}
        <span className="tasks-category-count">{tasks.length}</span>
      </h2>
      <p className="docs-section-intro">{meta.blurb}</p>
      <div className="tasks-card-grid">
        {tasks.map((task) => (
          <TaskCard key={task.id} task={task} />
        ))}
      </div>
    </section>
  );
}

export function TasksView() {
  const { taskId } = useParams();
  const navigate = useNavigate();
  const selectedTask = taskId ? SCENARIO_TASK_BY_ID.get(taskId) : undefined;
  const [activeCategory, setActiveCategory] = useState('');

  useEffect(() => {
    if (taskId && !selectedTask) {
      navigate('/tasks', { replace: true });
    }
  }, [navigate, selectedTask, taskId]);

  useEffect(() => {
    if (!taskId && window.location.hash) {
      const anchor = window.location.hash.slice(1);
      scrollToAnchor(anchor);
    }
  }, [taskId]);

  return (
    <div className="docs-layout tasks-layout">
      <aside className="docs-sidebar tasks-sidebar" aria-label="Task catalog navigation">
        <p className="docs-sidebar-label">Categories</p>
        <nav>
          <ul className="docs-sidebar-nav">
            <li>
              <NavLink to="/tasks" end>
                Overview
              </NavLink>
            </li>
            {CATALOG_SCENARIO_CATEGORIES.map((category) => {
              const meta = SCENARIO_CATEGORY_META[category];
              const tasks = SCENARIO_TASKS_BY_CATEGORY.get(category) ?? [];
              return (
                <li key={category} className="tasks-sidebar-category">
                  <Link
                    to={taskId ? `/tasks#${categoryAnchor(category)}` : `#${categoryAnchor(category)}`}
                    aria-current={(selectedTask?.category ?? activeCategory) === category ? 'location' : undefined}
                    onClick={(event) => {
                      if (!taskId) {
                        setActiveCategory(category);
                        scrollToAnchorOnClick(categoryAnchor(category))(event);
                      }
                    }}
                  >
                    {meta.label}
                    <span className="tasks-sidebar-count">{tasks.length}</span>
                  </Link>
                  {taskId ? (
                    <ul className="tasks-sidebar-tasks">
                      {tasks.map((task) => (
                        <li key={task.id}>
                          <NavLink to={`/tasks/${task.id}`}>
                            {task.id}
                          </NavLink>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </nav>
      </aside>

      <div className="docs-main tasks-main">
        {selectedTask ? (
          <TaskDetailPanel task={selectedTask} />
        ) : (
          <>
            <header className="panel docs-hero tasks-hero">
              <h1 className="docs-hero-title">Task catalog</h1>
              <p className="docs-hero-lede">
                <strong>{SCENARIO_TASKS.length}</strong> frozen agent scenarios in catalog{' '}
                <code>{SCENARIO_CATALOG_ID}</code> (version {SCENARIO_CATALOG_VERSION}). Each task
                exercises a specific behavior through the production inference and host-tool path.
                Click any task in the{' '}
                <Link to="/">live event stream</Link> to jump here, or browse by category below.
              </p>
              <p className="task-section-note tasks-hero-note">
                Cards show a prompt excerpt. Open a task for pass criteria, tool boundaries, and
                grading tags. Deterministic tasks use fixed trace rules; semantic-judge tasks use an
                LLM rubric.
              </p>
              <p className="tasks-hero-links">
                <a href={scenarioCatalogSourceUrl()} target="_blank" rel="noopener noreferrer">
                  View catalog source on GitHub
                </a>
                <span aria-hidden="true"> · </span>
                <a href={scenarioCatalogDocsUrl()} target="_blank" rel="noopener noreferrer">
                  Evaluation architecture docs
                </a>
                <span aria-hidden="true"> · </span>
                <a href={ZTD_REPO_URL} target="_blank" rel="noopener noreferrer">
                  ztd repository
                </a>
              </p>
            </header>

            {CATALOG_SCENARIO_CATEGORIES.map((category) => (
              <CategorySection key={category} category={category} />
            ))}
          </>
        )}
      </div>
    </div>
  );
}
