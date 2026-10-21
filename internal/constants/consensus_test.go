// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseConsensusProvider(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    ConsensusProvider
		wantErr error
	}{
		{name: "empty is application", value: "", want: ConsensusProviderApplication},
		{name: "application", value: "application", want: ConsensusProviderApplication},
		{name: "tribunal", value: "tribunal", want: ConsensusProviderTribunal},
		{name: "unknown provider", value: "bogus", wantErr: ErrConsensusProviderInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConsensusProvider(tc.value)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
