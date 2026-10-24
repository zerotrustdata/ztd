// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package scrubbing

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestRegexScrubber_Name(t *testing.T) {
	t.Parallel()
	s := &RegexScrubber{name: "test-scrubber"}
	assert.Equal(t, "test-scrubber", s.Name())
}

func TestRegexScrubber_Scrub(t *testing.T) {
	t.Parallel()
	upper := strings.ToUpper

	t.Run("whole match without value group", func(t *testing.T) {
		t.Parallel()
		s := newRegexScrubber("id", regexp.MustCompile(`id-\d+`))
		assert.Equal(t, "a ID-1 b ID-22", s.Scrub("a id-1 b id-22", upper))
	})

	t.Run("only the value group", func(t *testing.T) {
		t.Parallel()
		s := newRegexScrubber("kv", regexp.MustCompile(`key=(?P<value>\w+)`))
		assert.Equal(t, "key=ABC, key=DEF", s.Scrub("key=abc, key=def", upper))
	})

	t.Run("no match returns input", func(t *testing.T) {
		t.Parallel()
		s := newRegexScrubber("id", regexp.MustCompile(`id-\d+`))
		assert.Equal(t, "nothing here", s.Scrub("nothing here", upper))
	})
}

func TestTokenizeSensitivePairs(t *testing.T) {
	t.Parallel()
	service := newTestService(t, DefaultConfig())
	mark := func(v string) string { return "<" + v + ">" }

	tests := []struct {
		input, expected string
	}{
		{"user=svc-deploy", "user=svc-deploy"},
		{"password=abc user=svc", "password=<abc> user=svc"},
		{`password="a b c"`, `password="<a b c>"`},
		{"Authorization: Bearer abc def", "Authorization: <Bearer abc def>"},
		{`{"token": "x", "id": "y"}`, `{"token": "<x>", "id": "y"}`},
		{`{'secret':'z'}`, `{'secret':'<z>'}`},
		{"auth://host", "auth://host"},
		{"password={{UEI_3f9a0c12be47_3}}", "password=<{{UEI_3f9a0c12be47_3}}>"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, service.tokenizeSensitivePairs(tt.input, mark), "input: %s", tt.input)
	}
}

func TestTokenize_KeepsExistingTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t, DefaultConfig())

	inner, err := service.GetTokenForValue(ctx, "abc")
	require.NoError(t, err)
	assert.Equal(t, inner, service.tokenize(ctx, inner))

	result := service.tokenize(ctx, "  Bearer "+inner)
	assert.Regexp(t, `^  \{\{UEI_[0-9a-f]{12}_\d+\}\} `+regexp.QuoteMeta(inner)+`$`, result)
	assert.Equal(t, "  Bearer abc", mustRehydrateText(t, service, result))
}

func TestTokenKeymapHash_Empty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, newTestService(t, nil).TokenKeymapHash())
}

func TestTokenKeymapHash_WithTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t, lenientConfig())

	_, err := service.GetTokenForValue(ctx, "secret123")
	require.NoError(t, err)
	_, err = service.GetTokenForValue(ctx, "sk-abc123")
	require.NoError(t, err)
	hash1 := service.TokenKeymapHash()
	assert.NotEmpty(t, hash1)

	_, err = service.GetTokenForValue(ctx, "xyz789")
	require.NoError(t, err)
	hash2 := service.TokenKeymapHash()
	assert.NotEmpty(t, hash2)
	assert.NotEqual(t, hash1, hash2)
}

func TestTokenKeymapHash_Deterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Two separate stores holding the same owner namespace mint identical keymaps.
	newService := func() *ScrubbingService {
		store := storagetest.NewTestTokenStore()
		require.NoError(t, store.KVSet(ctx, constants.ScrubbingTokenOwnerKey, "3f9a0c12be47", 0))
		service, err := NewScrubbingService(ctx, lenientConfig(), testutil.NewTestLogger(), store)
		require.NoError(t, err)
		return service
	}
	service1, service2 := newService(), newService()

	_, err := service1.GetTokenForValue(ctx, "secret123")
	require.NoError(t, err)
	_, err = service2.GetTokenForValue(ctx, "secret123")
	require.NoError(t, err)

	assert.Equal(t, service1.TokenKeymapHash(), service2.TokenKeymapHash())
}
