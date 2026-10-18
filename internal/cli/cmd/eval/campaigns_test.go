// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestParseCampaignInvestigation(t *testing.T) {
	tests := []struct {
		name           string
		mode           string
		dataOperatorID string
		want           *evalv1.EvaluationInvestigation
		wantErr        error
	}{
		{
			name: "fresh default",
			mode: "fresh",
			want: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH},
		},
		{
			name:           "persistent identity",
			mode:           "persistent:checkout-incident",
			dataOperatorID: "operator-stable",
			want: &evalv1.EvaluationInvestigation{
				Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
				InvestigationId: "checkout-incident",
				DataOperatorId:  "operator-stable",
			},
		},
		{name: "persistent ID is required", mode: "persistent:", dataOperatorID: "operator-stable", wantErr: constants.ErrEvaluationFlagsInvalid},
		{name: "persistent Data Operator is required", mode: "persistent:checkout-incident", wantErr: constants.ErrEvaluationFlagsInvalid},
		{name: "Data Operator is invalid for fresh mode", mode: "fresh", dataOperatorID: "operator-stable", wantErr: constants.ErrEvaluationFlagsInvalid},
		{name: "unknown mode is rejected", mode: "hold-context", wantErr: constants.ErrEvaluationFlagsInvalid},
		{name: "reserved harness ID is rejected", mode: "persistent:eval-harness:case", dataOperatorID: "operator-stable", wantErr: constants.ErrEvaluationInvestigationInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCampaignInvestigation(test.mode, test.dataOperatorID)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func threeModelRegistry() []*evalv1.ModelVariant {
	return []*evalv1.ModelVariant{
		{VariantId: "gemma4-e4b", ServedModelTag: "gemma4:e4b", ModelDigest: repeatTestHex('a'), ProviderClass: "ollama", ParameterCount: 4_000_000_000, ModelFamily: "Gemma"},
		{VariantId: "qwen3-4b", ServedModelTag: "qwen3:4b", ModelDigest: repeatTestHex('b'), ProviderClass: "ollama", ParameterCount: 4_000_000_000, ModelFamily: "Qwen"},
		{VariantId: "llama3-8b", ServedModelTag: "llama3:8b", ModelDigest: repeatTestHex('c'), ProviderClass: "ollama", ParameterCount: 8_000_000_000, ModelFamily: "Llama"},
	}
}

func TestExtractCampaignMetricsCurve_EmptyOnNoResults(t *testing.T) {
	curve := extractCampaignMetricsCurve(t.Context(), nil, []string{"run-empty"})
	assert.Empty(t, curve)
}
