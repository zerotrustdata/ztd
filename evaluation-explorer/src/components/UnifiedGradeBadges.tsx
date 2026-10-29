// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Per-criterion grade badges. Each badge expands to the published scenario
// criterion it graded: method, whether it is required, and the explanation.

import type { AssignmentResult, PublicScenarioCriterion } from '../contract/types';
import { DetailRow } from './shared';
import { assignmentGradeChips } from '../views/derived';

function statusTone(status: string): string {
  if (status === 'pass') return 'ok';
  if (status === 'fail') return 'critical';
  if (status === 'unavailable') return 'info';
  if (status === 'unsupported' || status === 'invalid_evidence') return 'warn';
  return 'neutral';
}

function statusLabel(status: string): string {
  return status.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
}

export function UnifiedGradeBadges({ assignment }: { assignment: AssignmentResult }) {
  const gradeChips = assignmentGradeChips(assignment);
  const scenarioCriteria = assignment.scenario_summary?.criteria ?? [];

  // Map criterion_id to PublicScenarioCriterion for quick lookup
  const criterionMap = new Map<string, PublicScenarioCriterion>(
    scenarioCriteria.map((c) => [c.criterion_id, c])
  );

  return (
    <div className="unified-grade-badges" aria-label="Grade badges">
      {gradeChips.map((grade) => {
        const criterion = criterionMap.get(grade.criterion_id);
        const tone = statusTone(grade.status);
        const face = (
          <>
            <span className="badge-label">{grade.label}</span>
            <span className="badge-status" aria-label={statusLabel(grade.status)}>
              {statusLabel(grade.status)}
            </span>
            {grade.explanation ? <span className="badge-why">{grade.explanation}</span> : null}
          </>
        );
        if (!criterion && !grade.detail) {
          return <span key={grade.criterion_id} className={`grade-badge-summary grade-badge-static tone-${tone}`}>{face}</span>;
        }

        return (
          <details
            key={grade.criterion_id}
            className={`grade-badge-details grade-${grade.status}`}
          >
            <summary className={`grade-badge-summary tone-${tone}`}>{face}</summary>
            <div className="grade-badge-details-panel">
              {criterion ? (
                <>
                  <DetailRow label="Grading method">
                    {criterion.grading_method === 'deterministic' ? 'Deterministic check' : 'Semantic judge'}
                  </DetailRow>
                  <DetailRow label="Required">{criterion.required ? 'Yes' : 'No'}</DetailRow>
                  {criterion.public_description ? (
                    <DetailRow label="Description">{criterion.public_description}</DetailRow>
                  ) : null}
                </>
              ) : null}
              {grade.detail ? (
                <DetailRow label="Grader detail"><span className="grade-detail">{grade.detail}</span></DetailRow>
              ) : null}
            </div>
          </details>
        );
      })}
    </div>
  );
}
