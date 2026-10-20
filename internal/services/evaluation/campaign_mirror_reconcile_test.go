// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCampaignMirrorReconcileRunTimeout_ScalesWithAssignmentCount(t *testing.T) {
	base := 5 * time.Minute
	assert.Equal(t, base, CampaignMirrorReconcileRunTimeout(base, 0))
	assert.Equal(t, 7*time.Minute, CampaignMirrorReconcileRunTimeout(base, 60))
	assert.Equal(t, CampaignMirrorRunTimeoutMax, CampaignMirrorReconcileRunTimeout(base, 10_000))
}
