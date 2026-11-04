// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The Deterministic cell of a live assignment row: one circle per deterministic
// grade, in grading order. A circle opens to what the grader recorded, and its
// row shows one open at a time.

import type { LiveDeterministicGrade } from '../contract/types';
import { criterionDisplayLabel } from '../views/derived';
import { statusLabel, statusTone } from './UnifiedGradeBadges';

/** Tells a verdict apart without relying on its color. */
const STATUS_SYMBOLS: Record<LiveDeterministicGrade['status'], string> = {
  pass: '✓',
  fail: '×',
  unavailable: '–',
  unsupported: '!',
  invalid_evidence: '!',
};

export function DeterministicGrades({ grades, row, labels }: { grades: LiveDeterministicGrade[]; row: string; labels?: Map<string, string> }) {
  return (
    <div className="deterministic-grades" role="group" aria-label="Deterministic grades">
      {grades.map((grade, index) => {
        const name = `${criterionDisplayLabel(labels, grade.criterion_id)}: ${statusLabel(grade.status)}`;
        return (
          <details key={`${grade.criterion_id}:${index}`} name={`deterministic-grades:${row}`} className="deterministic-grade" data-tone={statusTone(grade.status)}>
            <summary title={name} aria-label={name}>
              <span className="deterministic-grade-circle" aria-hidden="true">{STATUS_SYMBOLS[grade.status]}</span>
            </summary>
            <dl>
              <div><dt>Criterion</dt><dd>{criterionDisplayLabel(labels, grade.criterion_id)}</dd></div>
              <div><dt>Result</dt><dd>{statusLabel(grade.status)}</dd></div>
              {grade.detail ? <div><dt>Detail</dt><dd>{grade.detail}</dd></div> : null}
            </dl>
          </details>
        );
      })}
    </div>
  );
}
