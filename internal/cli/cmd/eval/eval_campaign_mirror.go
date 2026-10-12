// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"time"

	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

func ReconcileVerifiedCampaignMirrorQueue(ctx context.Context, projectRoot string, reconciler *evaluation.CampaignMirrorReconciler, runTimeout time.Duration, force bool, progress evaluation.CampaignMirrorReconcileProgressFunc) (*evaluation.CampaignMirrorReconcileResult, error) {
	return reconciler.ReconcileAllVerifiedRunsFromStore(ctx, runTimeout, true, force, progress)
}
