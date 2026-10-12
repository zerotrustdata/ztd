// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sqliteutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   []byte
		wantErr bool
		wantNil bool
	}{
		{
			name:    "round trip",
			input:   []byte("hello world, this is some data to compress"),
			wantErr: false,
		},
		{
			name:    "nil input",
			input:   nil,
			wantErr: false,
			wantNil: true,
		},
		{
			name:    "empty input",
			input:   []byte{},
			wantErr: false,
			wantNil: true,
		},
		{
			name:    "large input",
			input:   []byte(strings.Repeat("abcdefghij", 10000)),
			wantErr: false,
		},
		{
			name: "binary data",
			input: func() []byte {
				data := make([]byte, 256)
				for i := range data {
					data[i] = byte(i)
				}
				return data
			}(),
			wantErr: false,
		},
		{
			name:    "single byte",
			input:   []byte{0x42},
			wantErr: false,
		},
		{
			name:    "all zero bytes",
			input:   make([]byte, 1024),
			wantErr: false,
		},
		{
			name:    "space character",
			input:   []byte(" "),
			wantErr: false,
		},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			compressed, err := Compress(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.wantNil {
				assert.Nil(t, compressed)
				return
			}

			assert.NotEmpty(t, compressed)
			assert.NotEqual(t, tt.input, compressed)

			decompressed, err := Decompress(compressed)
			require.NoError(t, err)
			assert.Equal(t, tt.input, decompressed)
		})
	}
}

func TestDecompress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		input           []byte
		wantErr         bool
		wantNil         bool
		wantErrContains string
	}{
		{
			name:    "nil input",
			input:   nil,
			wantErr: false,
			wantNil: true,
		},
		{
			name:    "empty input",
			input:   []byte{},
			wantErr: false,
			wantNil: true,
		},
		{
			name:            "invalid gzip data",
			input:           []byte("this is not gzip data"),
			wantErr:         true,
			wantErrContains: "decompress: gzip reader init",
		},
		{
			name: "truncated data",
			input: func() []byte {
				original := []byte("some data to compress and then truncate")
				compressed, err := Compress(original)
				require.NoError(t, err)
				return compressed[:len(compressed)/2]
			}(),
			wantErr:         true,
			wantErrContains: "decompress",
		},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			decompressed, err := Decompress(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}
			require.NoError(t, err)

			if tt.wantNil {
				assert.Nil(t, decompressed)
				return
			}

			assert.NotNil(t, decompressed)
		})
	}
}

func TestCompress_OutputIsValidGzip(t *testing.T) {
	t.Parallel()
	compressed, err := Compress([]byte("valid gzip test"))
	require.NoError(t, err)

	assert.Equal(t, byte(0x1f), compressed[0], "gzip magic byte 0")
	assert.Equal(t, byte(0x8b), compressed[1], "gzip magic byte 1")
}
