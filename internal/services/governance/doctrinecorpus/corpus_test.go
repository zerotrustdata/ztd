// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package doctrinecorpus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestLoad_EmbeddedCorpusDecodes(t *testing.T) {
	t.Parallel()
	corpus, err := Load()
	require.NoError(t, err)
	assert.Len(t, corpus.Entries, 75)
}

func TestAttacks_ReturnsOnlyBlockEntries(t *testing.T) {
	t.Parallel()
	corpus, err := Load()
	require.NoError(t, err)
	attacks := corpus.Attacks()
	require.Len(t, attacks, 52)
	for _, e := range attacks {
		assert.Equal(t, ExpectBlock, e.Expect, "attack %s", e.ID)
	}
}

func TestCategories_EveryAttackCategoryHasAnAttack(t *testing.T) {
	t.Parallel()
	corpus, err := Load()
	require.NoError(t, err)
	covered := make(map[Category]int)
	for _, e := range corpus.Attacks() {
		covered[e.Category]++
	}
	categories := Categories()
	require.Len(t, categories, 5)
	for _, c := range categories {
		assert.NotEqual(t, CategoryBenign, c)
		assert.Positive(t, covered[c], "category %s has no attack", c)
	}
}

func TestCategories_ReturnsCopy(t *testing.T) {
	t.Parallel()
	first := Categories()
	first[0] = CategoryBenign
	assert.NotEqual(t, CategoryBenign, Categories()[0])
}

func TestSHA256_StableAcrossCalls(t *testing.T) {
	t.Parallel()
	first := SHA256()
	require.Len(t, first, 64)
	assert.Equal(t, first, SHA256())
}

func validCorpus() *Corpus {
	return &Corpus{
		Version: "1.0.0",
		Subject: "test",
		Policy:  "test policy",
		Entries: []Entry{
			{ID: "a", Category: CategoryDiskDestroy, Command: "mkfs /dev/sda", Expect: ExpectBlock},
			{ID: "b", Category: CategoryBenign, Command: "ls -la", Expect: ExpectAllow},
		},
	}
}

func TestValidate_AcceptsWellFormedCorpus(t *testing.T) {
	t.Parallel()
	require.NoError(t, validate(validCorpus()))
}

func TestValidate_RejectsInvalidCorpus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(c *Corpus)
	}{
		{
			name: "duplicate id",
			mutate: func(c *Corpus) {
				c.Entries[1].ID = "a"
			},
		},
		{
			name: "unknown category",
			mutate: func(c *Corpus) {
				c.Entries[0].Category = Category("made_up")
			},
		},
		{
			name: "benign entry expecting block",
			mutate: func(c *Corpus) {
				c.Entries[1].Expect = ExpectBlock
			},
		},
		{
			name: "attack entry expecting allow",
			mutate: func(c *Corpus) {
				c.Entries[0].Expect = ExpectAllow
			},
		},
		{
			name: "empty command",
			mutate: func(c *Corpus) {
				c.Entries[0].Command = ""
			},
		},
		{
			name: "missing policy",
			mutate: func(c *Corpus) {
				c.Policy = ""
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			corpus := validCorpus()
			tt.mutate(corpus)
			err := validate(corpus)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrValidationFailed, "got %v", err)
		})
	}
}
