// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const doctrineCorpusPath = "testdata/doctrine_corpus.json"

const (
	corpusExpectBlock = "block"
	corpusExpectAllow = "allow"
)

// doctrineCorpus is the versioned adversarial corpus for L1 EXECUTE_BASH screening.
type doctrineCorpus struct {
	Version string                `json:"version"`
	Policy  string                `json:"policy"`
	Entries []doctrineCorpusEntry `json:"entries"`
}

type doctrineCorpusEntry struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Command  string `json:"command"`
	Expect   string `json:"expect"`
}

func loadDoctrineCorpus(t *testing.T) doctrineCorpus {
	t.Helper()
	raw, err := os.ReadFile(doctrineCorpusPath)
	require.NoError(t, err, "read doctrine corpus")
	var corpus doctrineCorpus
	require.NoError(t, json.Unmarshal(raw, &corpus), "parse doctrine corpus")
	require.NotEmpty(t, corpus.Policy, "corpus must document its policy")
	require.NotEmpty(t, corpus.Entries, "corpus must not be empty")
	return corpus
}

func TestL1DoctrineCorpus_WellFormed(t *testing.T) {
	t.Parallel()
	corpus := loadDoctrineCorpus(t)

	seen := make(map[string]struct{}, len(corpus.Entries))
	for _, e := range corpus.Entries {
		require.NotEmpty(t, e.ID, "entry id")
		require.NotContains(t, seen, e.ID, "duplicate corpus id")
		seen[e.ID] = struct{}{}
		require.NotEmpty(t, e.Command, "entry %s command", e.ID)
		require.Contains(t, []string{corpusExpectBlock, corpusExpectAllow}, e.Expect, "entry %s expect", e.ID)
		require.NotEmpty(t, e.Category, "entry %s category", e.ID)
		if e.Category == "benign" {
			require.Equal(t, corpusExpectAllow, e.Expect, "benign entry %s must expect allow", e.ID)
		} else {
			require.Equal(t, corpusExpectBlock, e.Expect, "malicious entry %s must expect block", e.ID)
		}
	}
}

// TestL1DoctrineCorpus_ExecuteBashScreening runs every corpus command through the
// built-in L1 doctrine as an EXECUTE_BASH (CommandRequested) payload and reports
// every miss (block expected, allowed) and every false positive (allow expected, blocked).
func TestL1DoctrineCorpus_ExecuteBashScreening(t *testing.T) {
	t.Parallel()
	corpus := loadDoctrineCorpus(t)
	doctrine := NewL1Doctrine()

	var misses, falsePositives []string
	perCategory := map[string]int{}
	for _, e := range corpus.Entries {
		perCategory[e.Category]++
		violations := doctrine.ValidatePayload(&operatorv1.CommandRequested{Command: e.Command})
		blocked := len(violations) > 0
		switch {
		case e.Expect == corpusExpectBlock && !blocked:
			misses = append(misses, fmt.Sprintf("  MISS [%s] %s: %q", e.Category, e.ID, e.Command))
		case e.Expect == corpusExpectAllow && blocked:
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
