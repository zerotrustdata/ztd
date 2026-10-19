// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestParseCAPool_RejectsEmptyAndCertlessBundles(t *testing.T) {
	tests := []struct {
		name     string
		caBundle []byte
		wantErr  error
	}{
		{name: "empty bundle", caBundle: []byte(""), wantErr: constants.ErrEmptyTrustBundle},
		{name: "invalid PEM (no valid certs)", caBundle: []byte("not a certificate"), wantErr: constants.ErrCAParseFailed},
		{name: "whitespace only", caBundle: []byte("   \n\t  "), wantErr: constants.ErrCAParseFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, err := parseCAPool(tt.caBundle)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, pool)
		})
	}
}

func TestTruncateBody(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "empty body",
			input:    []byte(""),
			expected: "",
		},
		{
			name:     "short body under limit",
			input:    []byte("hello world"),
			expected: "hello world",
		},
		{
			name:     "body exactly at limit (512)",
			input:    []byte(strings.Repeat("a", 512)),
			expected: strings.Repeat("a", 512),
		},
		{
			name:     "body over limit",
			input:    []byte(strings.Repeat("b", 600)),
			expected: strings.Repeat("b", 512) + "...(truncated)",
		},
		{
			name:     "body with special characters",
			input:    []byte("hello\nworld\t\r"),
			expected: "hello\nworld\t\r",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateBody(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMaxResponseBytesConstant(t *testing.T) {
	// Verify the constant has the expected value (1 MiB)
	assert.Equal(t, 1<<20, maxResponseBytes)
}

func TestDefaultClientTimeoutConstant(t *testing.T) {
	// Verify the constant has the expected value (30 seconds)
	assert.Equal(t, 30*time.Second, defaultClientTimeout)
}

// TestDecodeJSON covers the single typed decode path used by all E2EClient
// response handlers. It runs as a Tier 1 unit test with no network or platform
// dependency.
func TestDecodeJSON(t *testing.T) {
	type widget struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	tests := []struct {
		name          string
		body          []byte
		label         string
		expected      widget
		expectError   bool
		errorContains string
	}{
		{
			name:  "valid JSON decodes into typed value",
			body:  []byte(`{"name":"alpha","count":7}`),
			label: "widget",
			expected: widget{
				Name:  "alpha",
				Count: 7,
			},
			expectError: false,
		},
		{
			name:          "malformed JSON returns wrapped error",
			body:          []byte(`{invalid json`),
			label:         "widget",
			expectError:   true,
			errorContains: "decode widget",
		},
		{
			name:          "empty body returns wrapped error",
			body:          []byte(``),
			label:         "widget",
			expectError:   true,
			errorContains: "decode widget",
		},
		{
			name:          "null body returns wrapped error",
			body:          nil,
			label:         "widget",
			expectError:   true,
			errorContains: "decode widget",
		},
		{
			name:          "type mismatch returns wrapped error",
			body:          []byte(`{"name":"alpha","count":"not-a-number"}`),
			label:         "widget",
			expectError:   true,
			errorContains: "decode widget",
		},
		{
			name:  "extra fields ignored by tolerant decode",
			body:  []byte(`{"name":"alpha","count":7,"extra":"ignored"}`),
			label: "widget",
			expected: widget{
				Name:  "alpha",
				Count: 7,
			},
			expectError: false,
		},
		{
			name:          "label appears in error message",
			body:          []byte(`{`),
			label:         "health response",
			expectError:   true,
			errorContains: "decode health response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := decodeJSON[widget](tt.body, tt.label)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
