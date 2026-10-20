// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestEpisode_ContinuitySkipsWorkspaceMaterialization_D7(t *testing.T) {
	// First repetition: req.HeldContext is nil -> workspace files are materialized
	f1 := newAgentExecutorFixture(t, []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: "First turn diagnosis"}},
	})
	_, err := f1.execute()
	require.NoError(t, err)
	hasHealth1 := false
	for _, entry := range f1.log {
		if strings.Contains(entry, "status/health.txt") {
			hasHealth1 = true
			break
		}
	}
	assert.True(t, hasHealth1, "initial attempt must materialize workspace files")

	// Second repetition: req.HeldContext is set -> workspace files must NOT be re-materialized
	f2 := newAgentExecutorFixture(t, []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: "Second turn diagnosis"}},
	})
	trace1 := f1.storedTrace()
	f2.req.HeldContext = trace1
	f2.req.Assignment.Repetition = 2

	_, err = f2.execute()
	require.NoError(t, err)

	hasHealth2 := false
	for _, entry := range f2.log {
		if strings.Contains(entry, "status/health.txt") {
			hasHealth2 = true
			break
		}
	}
	assert.False(t, hasHealth2, "continuing held episode context must skip re-materializing workspace files (D7)")
}
