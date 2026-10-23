// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func loadDoctrineCorpus(t *testing.T) *doctrinecorpus.Corpus {
	t.Helper()
	corpus, err := doctrinecorpus.Load()
	require.NoError(t, err, "load doctrine corpus")
	return corpus
}

func TestL1DoctrineCorpus_WellFormed(t *testing.T) {
	t.Parallel()
	corpus := loadDoctrineCorpus(t)

	benign := 0
	for _, e := range corpus.Entries {
		if e.Category == doctrinecorpus.CategoryBenign {
			benign++
		}
	}
	require.Len(t, corpus.Entries, 75, "corpus entry count")
	require.Len(t, corpus.Attacks(), 52, "corpus attack count")
	require.Equal(t, 23, benign, "corpus benign count")
}

// TestL1DoctrineCorpus_ExecuteBashScreening runs every corpus command through the
// built-in L1 doctrine as an EXECUTE_BASH (CommandRequested) payload and reports
// every miss (block expected, allowed) and every false positive (allow expected, blocked).
func TestL1DoctrineCorpus_ExecuteBashScreening(t *testing.T) {
	t.Parallel()
	corpus := loadDoctrineCorpus(t)
	doctrine := NewL1Doctrine()

	var misses, falsePositives []string
	perCategory := map[doctrinecorpus.Category]int{}
	for _, e := range corpus.Entries {
		perCategory[e.Category]++
		violations := doctrine.ValidatePayload(&operatorv1.CommandRequested{Command: e.Command})
		blocked := len(violations) > 0
		switch {
		case e.Expect == doctrinecorpus.ExpectBlock && !blocked:
			misses = append(misses, fmt.Sprintf("  MISS [%s] %s: %q", e.Category, e.ID, e.Command))
		case e.Expect == doctrinecorpus.ExpectAllow && blocked:
			falsePositives = append(falsePositives, fmt.Sprintf("  FALSE POSITIVE [%s] %s: %q -> %s",
				e.Category, e.ID, e.Command, strings.Join(violations, "; ")))
		}
	}

	t.Logf("corpus entries per category: %v", perCategory)
	if len(misses) > 0 || len(falsePositives) > 0 {
		t.Errorf("doctrine corpus %s: %d miss(es), %d false positive(s)\n%s\n%s",
			corpus.Version, len(misses), len(falsePositives),
			strings.Join(misses, "\n"), strings.Join(falsePositives, "\n"))
	}
}
