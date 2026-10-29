// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// verifiedRunToolCallingProbes admits only probes whose sealed evidence verifies
// against the run and frozen model registry. An unverified observation cannot
// justify skipping a scored assignment.
func verifiedRunToolCallingProbes(ctx context.Context, store toolCallingProbeStore, run *evalv1.EvaluationRun, spec *evalv1.EvaluationCampaignSpec) (map[string]*evalv1.ModelToolCallingProbe, []string) {
	var failures []string
	probes := make(map[string]*evalv1.ModelToolCallingProbe)
	for _, record := range run.GetToolCallingProbes() {
		var variant *evalv1.ModelVariant
		for _, candidate := range spec.GetModelRegistry() {
			if candidate.GetVariantId() == record.GetVariantId() {
				variant = candidate
				break
			}
		}
		if variant == nil || probes[record.GetVariantId()] != nil {
			failures = append(failures, "tool-calling probe has an unknown or duplicate variant")
			continue
		}
		if err := verifyFrozenToolCallingProbe(ctx, store, run, variant, record); err != nil {
			failures = append(failures, fmt.Sprintf("tool-calling probe %s: %v", record.GetVariantId(), err))
			continue
		}
		probes[record.GetVariantId()] = record
	}
	return probes, failures
}
