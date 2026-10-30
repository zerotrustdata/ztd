// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestProviderResidencyProto_RoundTripsEveryResidentModel(t *testing.T) {
	t.Parallel()
	residency := ProviderResidency{Models: []ProviderResidencyModel{{Name: "qwen3:0.6b"}, {Name: "hf.co/LiquidAI/LFM2.5-1.2B-Instruct-GGUF:Q4_K_M"}}}

	decoded, err := ProviderResidencyFromProto(ProviderResidencyToProto(residency))

	require.NoError(t, err)
	assert.Equal(t, residency, decoded)
	assert.Equal(t, []string{"qwen3:0.6b", "hf.co/LiquidAI/LFM2.5-1.2B-Instruct-GGUF:Q4_K_M"}, decoded.Names())
}

func TestProviderResidencyFromProto_EmptyResidencyIsNotAnError(t *testing.T) {
	t.Parallel()

	decoded, err := ProviderResidencyFromProto(&operatorv1.OllamaModelResidencyResult{})

	require.NoError(t, err)
	assert.Empty(t, decoded.Models)
	assert.Empty(t, decoded.Names())
}

func TestProviderResidencyFromProto_FailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		result  *operatorv1.OllamaModelResidencyResult
		wantErr error
	}{
		{name: "missing result", wantErr: constants.ErrMissingRequiredField},
		{
			name:    "resident model without a name",
			result:  &operatorv1.OllamaModelResidencyResult{Models: []*operatorv1.OllamaModelResidencyModel{{Name: "qwen3:0.6b"}, {}}},
			wantErr: constants.ErrInferenceProviderResponseInvalid,
		},
		{
			name:    "nil resident model",
			result:  &operatorv1.OllamaModelResidencyResult{Models: []*operatorv1.OllamaModelResidencyModel{nil}},
			wantErr: constants.ErrInferenceProviderResponseInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := ProviderResidencyFromProto(test.result)

			require.ErrorIs(t, err, test.wantErr, "an unnamed model cannot prove absence from residency")
			assert.Empty(t, decoded.Models)
		})
	}
}
