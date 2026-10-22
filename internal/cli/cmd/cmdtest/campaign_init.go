// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmdtest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// EvaluationTestCampaignInitRequest builds the default-suite smoke campaign request
// used by eval and compliance command tests.
func EvaluationTestCampaignInitRequest(t *testing.T) evaluation.CampaignInitRequest {
	t.Helper()
	catalog, artifacts, err := evaluation.LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := evaluation.MaterializeModelRegistry("smoke-campaign", []*evalv1.ModelVariant{
		{
			VariantId:      "qwen3-4b",
			ServedModelTag: "qwen3:4b",
			ModelDigest:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			ProviderClass:  "ollama",
		},
	})
	require.NoError(t, err)
	return evaluation.CampaignInitRequest{
		CampaignID:                 "smoke-campaign",
		RunID:                      "run-smoke-1",
		Catalog:                    catalog,
		Inventory:                  inventory,
		ScenarioArtifacts:          artifacts,
		RepetitionCount:            1,
		Platform:                   evaluation.PlatformIdentity{Release: "v2.3.0"},
		InferenceOperatorSessionID: "inf-session",
		DataOperatorSessionID:      "data-session",
		Governance: evaluation.RunGovernance{
			Posture:   evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
			Consensus: &evalv1.EvaluationConsensusIdentity{},
		},
	}
}
