// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ModelRegistryFreeze is the immutable campaign model registry derived from a
// live provider inventory query. It is used to commit the campaign Inference
// Operator and to authorize governed campaign-mode dispatches.
type ModelRegistryFreeze struct {
	CampaignID string
	Digest     string
	Variants   []*operatorv1.InferenceModelVariant
}
