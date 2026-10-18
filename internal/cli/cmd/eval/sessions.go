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

// resolveOperatorTopology asks the Gateway for active operator sessions and
// resolves the Inference and Data runtimes a dispatching command targets. Only
// commands that dispatch work call it.
func resolveOperatorTopology(cmd *cobra.Command, deps nativeEvalDeps) (evaluation.CampaignOperatorTopology, error) {
	return resolveOperatorTopologyForDataOperator(cmd, deps, "")
}

// resolveOperatorTopologyForDataOperator resolves the selected Inference
// session and either the default Data Operator or a campaign's frozen stable
// Data Operator identity.
func resolveOperatorTopologyForDataOperator(cmd *cobra.Command, deps nativeEvalDeps, dataOperatorID string) (evaluation.CampaignOperatorTopology, error) {
	gatewayDeps := deps.gatewayDeps()
	cfg, _, authContext, err := gatewayEvalEnvironment(cmd, gatewayDeps)
	if err != nil {
		return evaluation.CampaignOperatorTopology{}, err
	}
	operators, err := evalListOperators(cmd, gatewayDeps, cfg, authContext)
	if err != nil {
		return evaluation.CampaignOperatorTopology{}, err
	}
	return evaluation.ResolveCampaignOperatorTopologyForDataOperator(operators, "", dataOperatorID)
}
