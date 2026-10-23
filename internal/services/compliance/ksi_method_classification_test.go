// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type expectedMethodMetadata struct {
	artifact KSIArtifactIdentity
	boundary KSICollectionBoundary
	verifier KSIVerifierFamily
	property KSIMeasuredProperty
}

var expectedDefaultMethodMetadata = map[string]expectedMethodMetadata{
	"auditEventsExist": {
		artifact: KSIArtifactAuditEvents, boundary: KSICollectionAuditStore,
		verifier: KSIVerifierExistence, property: KSIPropertyPresence,
	},
	"receiptsExist": {
		artifact: KSIArtifactActionReceipts, boundary: KSICollectionAuditStore,
		verifier: KSIVerifierExistence, property: KSIPropertyPresence,
	},
	"receiptsCryptographicallyVerified": {
		artifact: KSIArtifactActionReceipts, boundary: KSICollectionAuditStore,
		verifier: KSIVerifierCryptographic, property: KSIPropertyReceiptPersistenceIntegrity,
	},
	"fileMutationsTracked": {
		artifact: KSIArtifactFileMutations, boundary: KSICollectionAuditStore,
		verifier: KSIVerifierExistence, property: KSIPropertyPresence,
	},
	"ledgerCommitsExist": {
		artifact: KSIArtifactLedgerCommits, boundary: KSICollectionLedgerStore,
		verifier: KSIVerifierExistence, property: KSIPropertyPresence,
	},
	"commitmentChainExists": {
		artifact: KSIArtifactCommitments, boundary: KSICollectionCommitmentStore,
		verifier: KSIVerifierExistence, property: KSIPropertyPresence,
	},
	"commitmentChainIntact": {
		artifact: KSIArtifactCommitments, boundary: KSICollectionCommitmentStore,
		verifier: KSIVerifierStructural, property: KSIPropertyChainLinkage,
	},
	"commitmentsCryptographicallyVerified": {
		artifact: KSIArtifactCommitments, boundary: KSICollectionCommitmentStore,
		verifier: KSIVerifierCryptographic, property: KSIPropertySignatureValidity,
	},
	"ledgerMerkleRootMatchesHead": {
		artifact: KSIArtifactLedgerStateRoot, boundary: KSICollectionLedgerStore,
		verifier: KSIVerifierStateObservation, property: KSIPropertyStateRootMatchesHead,
	},
	"independentStateObserved": {
		artifact: KSIArtifactReceiptStateTransitions, boundary: KSICollectionAuditStore,
		verifier: KSIVerifierStateObservation, property: KSIPropertyStateTransitionBinding,
	},
	"deterministicGraderResultsVerified": {
		artifact: KSIArtifactGraderResults, boundary: KSICollectionEvalResults,
		verifier: KSIVerifierDeterministicGrader, property: KSIPropertyEvidenceContentAddressing,
	},
	"ksiHistoryFreshness": {
		artifact: KSIArtifactHistorySnapshots, boundary: KSICollectionHistoryStore,
		verifier: KSIVerifierHistorical, property: KSIPropertyFreshness,
	},
}

func TestDefaultMethods_EveryMethodHasTypedIndependenceMetadata(t *testing.T) {
	t.Parallel()
	registered := DefaultMethods(EvaluatorDeps{})
	seenNames := make(map[string]bool, len(expectedDefaultMethodMetadata))
	totalRegistered := 0

	for ksiID, methods := range registered {
		independenceKeys := make(map[string]bool, len(methods))
		for _, method := range methods {
			require.NoError(t, method.validate(), "%s/%s", ksiID, method.Name)
			expected, ok := expectedDefaultMethodMetadata[method.Name]
			require.True(t, ok, "unexpected method metadata for %s/%s", ksiID, method.Name)
			assert.Equal(t, expected.artifact, method.ArtifactIdentity)
			assert.Equal(t, expected.boundary, method.CollectionBoundary)
			assert.Equal(t, expected.verifier, method.VerifierFamily)
			assert.Equal(t, expected.property, method.MeasuredProperty)
			if method.Name == "ksiHistoryFreshness" {
				assert.Equal(t, KSIOutcomeStaleEvidence, method.UnsatisfiedOutcome)
			} else {
				assert.Equal(t, KSIOutcomeInvalidEvidence, method.UnsatisfiedOutcome)
			}
			assert.False(t, independenceKeys[method.independenceKey()], "%s contains methods that restate the same fact", ksiID)
			independenceKeys[method.independenceKey()] = true
			seenNames[method.Name] = true
			totalRegistered++
		}
	}

	assert.Equal(t, 20, totalRegistered)
	assert.Len(t, seenNames, len(expectedDefaultMethodMetadata))
}

func TestDefaultMethods_IncludeEvidenceBackedVerifierFamilies(t *testing.T) {
	t.Parallel()
	counts := make(map[KSIVerifierFamily]int)
	for _, methods := range DefaultMethods(EvaluatorDeps{}) {
		for _, method := range methods {
			counts[method.VerifierFamily]++
		}
	}

	assert.Greater(t, counts[KSIVerifierCryptographic], 1)
	assert.Positive(t, counts[KSIVerifierStateObservation])
	assert.Positive(t, counts[KSIVerifierHistorical])
	assert.Positive(t, counts[KSIVerifierDeterministicGrader])
	assert.Positive(t, counts[KSIVerifierExistence])
	assert.Positive(t, counts[KSIVerifierStructural])
}

func TestDefaultMethods_UnregisteredKSIsRemainUnsupported(t *testing.T) {
	t.Parallel()
	catalog := testCatalog()
	registered := DefaultMethods(EvaluatorDeps{})
	unregistered := 0
	for _, ksi := range catalog.KSIs {
		if len(registered[ksi.ID]) == 0 {
			unregistered++
		}
	}

	assert.Positive(t, unregistered)
}
