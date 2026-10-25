// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCampaignQueueBuildBatchPlan(t *testing.T) {
	queue := &CampaignQueue{
		Models: []CampaignQueueModel{
			{VariantID: "granite3-3-2b", Status: QueueStatusVerified},
			{VariantID: "skipme", Status: QueueStatusSkipped},
			{VariantID: "qwen3-4b", Status: QueueStatusFailed},
			{VariantID: "gemma3-4b", Status: QueueStatusPending},
			{VariantID: "llama3-8b", Status: QueueStatusPending},
		},
	}

	tests := []struct {
		name string
		req  CampaignQueueBatchPlanRequest
		want []string
	}{
		{name: "skip verified drops verified skipped and failed", req: CampaignQueueBatchPlanRequest{SkipVerified: true}, want: []string{"gemma3-4b", "llama3-8b"}},
		{name: "keep verified still drops skipped and failed", req: CampaignQueueBatchPlanRequest{}, want: []string{"granite3-3-2b", "gemma3-4b", "llama3-8b"}},
		{name: "until bounds the batch after failed entries are excluded", req: CampaignQueueBatchPlanRequest{SkipVerified: true, Until: 1}, want: []string{"gemma3-4b"}},
		{name: "until larger than the plan", req: CampaignQueueBatchPlanRequest{SkipVerified: true, Until: 10}, want: []string{"gemma3-4b", "llama3-8b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, entry := range queue.BuildBatchPlan(test.req) {
				got = append(got, entry.VariantID)
			}
			assert.Equal(t, test.want, got)
		})
	}
}

func TestCampaignQueueBuildBatchPlan_SkipVerifiedKeepsStaleVerifiedEntries(t *testing.T) {
	queue := &CampaignQueue{
		Models: []CampaignQueueModel{
			{VariantID: "current", Status: QueueStatusVerified},
			{VariantID: "stale", Status: QueueStatusVerified},
			{VariantID: "pending", Status: QueueStatusPending},
		},
	}
	isStale := func(entry CampaignQueueModel) bool { return entry.VariantID == "stale" }

	var got []string
	for _, entry := range queue.BuildBatchPlan(CampaignQueueBatchPlanRequest{SkipVerified: true, VerifiedIsStale: isStale}) {
		got = append(got, entry.VariantID)
	}

	assert.Equal(t, []string{"stale", "pending"}, got)
}
