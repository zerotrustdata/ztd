// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Grade bases, named for the construction sites that state them. The basis is a
// required argument of newDeterministicGrade: there is no default.
const (
	basisObservation = evalv1.GradeBasis_GRADE_BASIS_OBSERVATION
	basisDerived     = evalv1.GradeBasis_GRADE_BASIS_DERIVED
	basisStructural  = evalv1.GradeBasis_GRADE_BASIS_STRUCTURAL
)

func newDeterministicGrade(assignmentID, criterionID string, basis evalv1.GradeBasis, status evalv1.EvaluationVerdictStatus, detail string, score float64) *evalv1.DeterministicGrade {
	return &evalv1.DeterministicGrade{
		GradeId:     assignmentID + ":" + criterionID,
		CriterionId: criterionID,
		Status:      status,
		Score:       score,
		Detail:      detail,
		Basis:       basis,
	}
}

func normalizeGradesForComparison(grades []*evalv1.DeterministicGrade) []*evalv1.DeterministicGrade {
	if len(grades) == 0 {
		return nil
	}
	clones := make([]*evalv1.DeterministicGrade, 0, len(grades))
	for _, grade := range grades {
		if grade == nil {
			continue
		}
		clones = append(clones, &evalv1.DeterministicGrade{
			GradeId:     grade.GetGradeId(),
			CriterionId: grade.GetCriterionId(),
			Status:      grade.GetStatus(),
			Score:       grade.GetScore(),
			Detail:      grade.GetDetail(),
			Basis:       grade.GetBasis(),
		})
	}
	return clones
}

func gradesEquivalent(left, right []*evalv1.DeterministicGrade) bool {
	leftNorm := normalizeGradesForComparison(left)
	rightNorm := normalizeGradesForComparison(right)
	if len(leftNorm) != len(rightNorm) {
		return false
	}
	gradeKey := func(grade *evalv1.DeterministicGrade) string {
		return grade.GetGradeId() + "\x00" + grade.GetCriterionId()
	}
	leftByID := make(map[string]*evalv1.DeterministicGrade, len(leftNorm))
	for _, grade := range leftNorm {
		leftByID[gradeKey(grade)] = grade
	}
	for _, grade := range rightNorm {
		other := leftByID[gradeKey(grade)]
		if other == nil || other.GetStatus() != grade.GetStatus() || other.GetScore() != grade.GetScore() || other.GetDetail() != grade.GetDetail() || other.GetBasis() != grade.GetBasis() {
			return false
		}
	}
	return true
}
