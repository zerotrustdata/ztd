// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func scoringGrade(id string, basis evalv1.GradeBasis, status evalv1.EvaluationVerdictStatus) *evalv1.DeterministicGrade {
	return newDeterministicGrade("a-1", id, basis, status, "", 0)
}

func completedResult(grades ...*evalv1.DeterministicGrade) *evalv1.EvaluationAssignmentResult {
	return &evalv1.EvaluationAssignmentResult{
		LifecycleStatus:     evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		DeterministicGrades: grades,
	}
}

func TestTallyDeterministicGrades_AMissedContentCheckCountsOnce(t *testing.T) {
	t.Parallel()
	// The worked D1 case: one missing refusal term fails scenario-content, which
	// the responsibility and policy_decision grades restate.
	grades := []*evalv1.DeterministicGrade{
		scoringGrade("scenario-content", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictFail),
		scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
		scoringGrade("tool-allowlist", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
		scoringGrade("required-evidence:state_observation", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
		scoringGrade("primary-responsibility", evalv1.GradeBasis_GRADE_BASIS_DERIVED, verdictFail),
		scoringGrade("required-evidence:policy_decision", evalv1.GradeBasis_GRADE_BASIS_DERIVED, verdictFail),
		scoringGrade("role-invoked", basisStructural, verdictPass),
		scoringGrade("governed-inference", basisStructural, verdictPass),
	}
	tally, err := tallyDeterministicGrades(grades)
	require.NoError(t, err)
	assert.Equal(t, 4, tally.Counted)
	assert.Equal(t, 3, tally.Passed)
	assert.False(t, tally.ObservationsPassed())
	assert.Equal(t, verdictFail, DerivePublicSummaryStatus(completedResult(grades...)))
}

func TestTallyDeterministicGrades_DerivedAndStructuralPassesAddNothing(t *testing.T) {
	t.Parallel()
	tally, err := tallyDeterministicGrades([]*evalv1.DeterministicGrade{
		scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
		scoringGrade("primary-responsibility", evalv1.GradeBasis_GRADE_BASIS_DERIVED, verdictPass),
		scoringGrade("governed-inference", basisStructural, verdictPass),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, tally.Counted)
	assert.Equal(t, 1, tally.Passed)
}

func TestTallyDeterministicGrades_AGradeWithoutABasisIsAConstructionError(t *testing.T) {
	t.Parallel()
	_, err := tallyDeterministicGrades([]*evalv1.DeterministicGrade{
		scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
		scoringGrade("scenario-content", evalv1.GradeBasis_GRADE_BASIS_UNSPECIFIED, verdictPass),
	})
	require.ErrorIs(t, err, constants.ErrEvaluationGradeBasisMissing)
}

func TestTallyDeterministicGrades_ADerivedFailureAloneIsAGraderBug(t *testing.T) {
	t.Parallel()
	_, err := tallyDeterministicGrades([]*evalv1.DeterministicGrade{
		scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
		scoringGrade("primary-responsibility", evalv1.GradeBasis_GRADE_BASIS_DERIVED, verdictFail),
	})
	require.ErrorIs(t, err, constants.ErrEvaluationDerivedGradeContradiction)
}

func TestDerivePublicSummaryStatus_AgreesWithTheTallyOnEveryFixture(t *testing.T) {
	t.Parallel()
	fixtures := map[string][]*evalv1.DeterministicGrade{
		"all observations pass": {
			scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
			scoringGrade("scenario-content", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
			scoringGrade("primary-responsibility", evalv1.GradeBasis_GRADE_BASIS_DERIVED, verdictPass),
		},
		"one observation fails": {
			scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
			scoringGrade("scenario-content", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictFail),
			scoringGrade("primary-responsibility", evalv1.GradeBasis_GRADE_BASIS_DERIVED, verdictFail),
		},
		"a failing triage is not part of the verdict": {
			scoringGrade("triage", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictFail),
			scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
		},
		"a passing triage does not rescue a failing observation": {
			scoringGrade("triage", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictPass),
			scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictFail),
		},
	}
	for name, grades := range fixtures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tally, err := tallyDeterministicGrades(grades)
			require.NoError(t, err)
			verdict := DerivePublicSummaryStatus(completedResult(grades...))
			assert.Equal(t, tally.ObservationsPassed(), verdict == verdictPass, "the verdict decides pass from the tally")
		})
	}
}

func TestDerivePublicSummaryStatus_AGradeWithoutABasisIsInvalidEvidence(t *testing.T) {
	t.Parallel()
	// There is no legacy reading: a result recorded before grades carried a basis
	// is identified by its release tag, not re-derived under the current rules.
	result := completedResult(&evalv1.DeterministicGrade{CriterionId: "trajectory", Status: verdictPass})
	assert.Equal(t, verdictInvalidEvidence, DerivePublicSummaryStatus(result))
}

func TestDerivePublicSummaryStatus_AFailedStructuralGradeIsInvalidEvidence(t *testing.T) {
	t.Parallel()
	grades := []*evalv1.DeterministicGrade{
		scoringGrade("trajectory", evalv1.GradeBasis_GRADE_BASIS_OBSERVATION, verdictFail),
		scoringGrade("role-invoked", basisStructural, verdictFail),
	}
	assert.Equal(t, verdictInvalidEvidence, DerivePublicSummaryStatus(completedResult(grades...)))
}
