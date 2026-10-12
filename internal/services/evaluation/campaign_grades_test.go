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

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestGradesEquivalent_ComparesNormalizedFields(t *testing.T) {
	t.Parallel()
	grade := &evalv1.DeterministicGrade{
		GradeId:     "assignment-1:criterion",
		CriterionId: "criterion",
		Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		Score:       1,
		Detail:      "ok",
	}
	assert.True(t, gradesEquivalent([]*evalv1.DeterministicGrade{grade}, []*evalv1.DeterministicGrade{grade}))
	assert.False(t, gradesEquivalent([]*evalv1.DeterministicGrade{grade}, []*evalv1.DeterministicGrade{{
		GradeId:     "assignment-1:criterion",
		CriterionId: "criterion",
		Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		Score:       1,
		Detail:      "ok",
	}}))
}
