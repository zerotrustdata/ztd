// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestQuickstartCampaignID_IsAStablePathElementPerReleaseAndSubject(t *testing.T) {
	cases := map[string][2]string{
		"quickstart-v1-5-0-qwen3-5-2b":      {"v1.5.0", "qwen3.5:2b"},
		"quickstart-v1-5-0-formation-lite":  {"v1.5.0", "formation-lite"},
		"quickstart-hf-co-org-model-q4-k-m": {"", "hf.co/Org/Model:Q4_K_M"},
	}
	for want, in := range cases {
		got := quickstartCampaignID(in[0], in[1])
		assert.Equal(t, want, got)
		require.NoError(t, complianceevidence.ValidatePathElement(got), got)
	}
	assert.NotEqual(t, quickstartCampaignID("v1.5.0", "gemma3:1b"), quickstartCampaignID("v1.6.0", "gemma3:1b"), "a new release freezes a new campaign")
}

func TestQuickstartSpecSuffix_ChangesWithModelDigestOrJudgeOnly(t *testing.T) {
	registry := []*evalv1.ModelVariant{
		{ServedModelTag: "granite4:1b-h", ModelDigest: "aaa"},
		{ServedModelTag: "llama3.2:1b", ModelDigest: "bbb"},
	}
	judge := &evalv1.ModelVariant{ServedModelTag: "granite4:1b-h", ModelDigest: "aaa"}
	base := quickstartSpecSuffix(registry, []string{"granite4:1b-h"}, judge)
	assert.Len(t, base, 8)
	assert.Equal(t, base, quickstartSpecSuffix(registry, []string{"granite4:1b-h"}, judge), "the same spec reruns on the same campaign")

	refrozen := []*evalv1.ModelVariant{{ServedModelTag: "granite4:1b-h", ModelDigest: "ccc"}, registry[1]}
	assert.NotEqual(t, base, quickstartSpecSuffix(refrozen, []string{"granite4:1b-h"}, judge), "a re-frozen digest is a new campaign")
	assert.NotEqual(t, base, quickstartSpecSuffix(registry, []string{"granite4:1b-h"}, registry[1]), "a different judge is a new campaign")
	assert.NotEqual(t, base, quickstartSpecSuffix(registry, []string{"granite4:1b-h", "llama3.2:1b"}, judge), "a different formation is a new campaign")
}

func TestQuickstartSummary_PointsAtTraceAndNextSteps(t *testing.T) {
	var out bytes.Buffer
	writeQuickstartSummary(&out, quickstartResultJSON{RunID: "run-1", Subject: "granite4:1b-h", SubjectModels: []string{"granite4:1b-h"}, Lane: campaignLaneModelRole, Executed: 1, Passed: 1, Verified: true, Outcomes: []string{"route-primary-ownership: COMPLETED verdict=PASS"}})
	text := out.String()
	assert.Contains(t, text, "Platform  PASS  governed run verified offline")
	assert.Contains(t, text, "Model     PASS  1/1 cells passed (granite4:1b-h)")
	assert.Contains(t, text, "ztd eval campaigns create my-screen granite4:1b-h --suite smoke-suite --judge llama3.2:1b")
	assert.Contains(t, text, "ztd eval runs start my-screen --publish=false")
	assert.Contains(t, text, "route-primary-ownership: COMPLETED verdict=PASS")
	assert.Contains(t, text, "Run       run-1, not published")
	assert.Contains(t, text, "ztd eval runs trace run-1 --scenario route-primary-ownership")
	assert.Contains(t, text, "--formation formation-lite")

	out.Reset()
	writeQuickstartSummary(&out, quickstartResultJSON{RunID: "run-2", Subject: "formation-lite", SubjectModels: []string{"granite4:1b-h", "llama3.2:1b", "gemma3:1b"}, Lane: campaignLaneSystem, Executed: 1})
	text = out.String()
	assert.Contains(t, text, "Platform  FAIL  evidence did not verify")
	assert.Contains(t, text, "Model     FAIL  0/1 cells passed (formation-lite)")
	assert.Contains(t, text, "--formations formation-lite --suite smoke-suite --judge granite3.3:2b")
	assert.NotContains(t, text, "ztd eval quickstart --formation")
}
