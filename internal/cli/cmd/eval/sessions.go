// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

// resolveOperatorTopology resolves the selected Inference session, which must
// serve every class in hostedClasses, and the Data fleet the selection names.
func resolveOperatorTopology(cmd *cobra.Command, deps nativeEvalDeps, fleet evaluation.DataFleetSelection, hostedClasses []string) (evaluation.CampaignOperatorTopology, error) {
	gatewayDeps := deps.gatewayDeps()
	cfg, _, authContext, err := gatewayEvalEnvironment(cmd, gatewayDeps)
	if err != nil {
		return evaluation.CampaignOperatorTopology{}, err
	}
	operators, err := evalListOperators(cmd, gatewayDeps, cfg, authContext)
	if err != nil {
		return evaluation.CampaignOperatorTopology{}, err
	}
	return evaluation.ResolveCampaignOperatorTopology(operators, "", fleet, hostedClasses)
}
