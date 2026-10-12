// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

const (
	// heterogeneousPipelineDescription describes the heterogeneous-pipeline
	// criterion: all three roles completed and each role received prior role
	// outputs as handoff evidence.
	heterogeneousPipelineDescription = "Heterogeneous lane executes all three roles (Lite → Assistant → Primary) with verified handoff of prior role outputs at each step."
)
