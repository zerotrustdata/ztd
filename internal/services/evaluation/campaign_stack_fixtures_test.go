// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"

// testFormationStackSet creates test data through the production catalog
// materializer so fixtures obey the same frozen formation contract.
func testFormationStackSet(campaignID string, seed uint64, variants []*evalv1.ModelVariant) (*HeterogeneousStackSet, error) {
	return GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID: campaignID,
		Seed:       seed,
		Variants:   variants,
	})
}
