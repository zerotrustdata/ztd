// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestApprovalPageURL(t *testing.T) {
	cfg := &config.Config{}
	got := ApprovalPageURL(cfg, "tx-abc")
	assert.True(t, strings.HasPrefix(got, cfg.OperatorPublicURL()))
	assert.True(t, strings.HasSuffix(got, constants.APIPaths.ApprovePagePrefix+"tx-abc"))
}
