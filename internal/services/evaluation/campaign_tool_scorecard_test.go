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

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestToolScorecardMetricFromGrade_UsesClosedUnavailableReason(t *testing.T) {
	t.Parallel()
	metric := toolScorecardMetricFromGrade(&evalv1.DeterministicGrade{
		Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE,
	})
	require.NotNil(t, metric)
	assert.Equal(t, "source_unavailable", metric.UnavailableReason)
}
