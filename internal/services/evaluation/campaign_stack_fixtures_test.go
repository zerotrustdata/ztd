// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"

// testFormationStackSet binds two catalog formations through the production
// materializer. Two stacks exercise cross-stack scheduling and accounting without
// multiplying every scenario by all 1,000 formations. Full catalog coverage is
// checked separately in TestGenerateFormationCatalogStackSet_ReturnsEveryCatalogFormation.
func testFormationStackSet(campaignID string, seed uint64, variants []*evalv1.ModelVariant) (*HeterogeneousStackSet, error) {
	return GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   campaignID,
		Seed:         seed,
		Variants:     variants,
		FormationIDs: []string{"heterogeneous-0000", "heterogeneous-0001"},
	})
}
