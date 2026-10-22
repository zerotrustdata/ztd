// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evidence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestExportOperationalEvidence_PreservesEventReceiptsPersistenceAndCommitments(t *testing.T) {
	executedAt := time.UnixMilli(1_700_000_001_000).UTC()
	attestation := &operatorv1.EventReceiptPersistenceAttestation{TransactionId: "tx-1", PersistedAtUnixMs: executedAt.Add(time.Second).UnixMilli(), SignerKeyId: "signer-1"}
	eventReceiptBody, err := compliancev1.MarshalCanonical(&operatorv1.EventReceipt{TransactionId: "tx-1", ExecutedAtUnixMs: executedAt.UnixMilli(), FinalPersistenceAttestation: attestation})
	require.NoError(t, err)
	outputDir := testutil.TempDir(t)
	inventory, err := ExportOperationalEvidence(context.Background(), &storage.OperationalEvidenceSnapshot{
		EventReceipts: []storage.OperationalEventReceiptSource{{TransactionID: "tx-1", ExecutedAt: executedAt, Body: eventReceiptBody}},
		Commitments:   []storage.OperationalCommitmentSource{{Sequence: 1, TransactionID: "tx-1", CommittedAt: executedAt.Add(2 * time.Second), Body: []byte(`{"transactionId":"tx-1"}`)}},
	}, OperationalExportRequest{
		ScopeID:              "scope-1",
		AdmissionID:          "source-1",
		SourceKind:           "operator-audit",
		SourceVersion:        "1.0.0",
		SourceScopeID:        "operator-scope-1",
		OwnerRuntimeBoundary: "operator-1",
		RunID:                "run-1",
		VerifierID:           "operational-export",
		VerifierVersion:      "1.0.0",
		AcquisitionBoundary:  "operator-local-export",
		WindowStart:          executedAt.Add(-time.Minute),
		WindowEnd:            executedAt.Add(time.Minute),
		MaxRows:              10,
		OutputDir:            outputDir,
	})
	require.NoError(t, err)
	assert.Equal(t, "source-1", inventory.AdmissionID)
	assert.Equal(t, "operator-scope-1", inventory.SourceScopeID)
	assert.Equal(t, "run-1", inventory.RunID)
	assert.Equal(t, 1, inventory.EventReceiptCount)
	assert.Equal(t, 1, inventory.PersistenceCount)
	assert.Equal(t, 1, inventory.CommitmentCount)
	assert.Len(t, inventory.Artifacts, 3)
	for _, artifact := range inventory.Artifacts {
		body, readErr := os.ReadFile(filepath.Join(outputDir, artifact.RelativePath))
		require.NoError(t, readErr)
		assert.Equal(t, artifact.SHA256, exportDigestHex(body))
	}
	_, err = os.Stat(filepath.Join(outputDir, constants.ComplianceOperationalInventoryFilename))
	assert.NoError(t, err)
}

func TestExportOperationalEvidence_RecordsUnavailablePersistenceWithoutInventingAttestation(t *testing.T) {
	executedAt := time.UnixMilli(1_700_000_001_000).UTC()
	inventory, err := ExportOperationalEvidence(context.Background(), &storage.OperationalEvidenceSnapshot{
		EventReceipts: []storage.OperationalEventReceiptSource{{TransactionID: "tx-1", ExecutedAt: executedAt, Body: []byte(`{"transactionId":"tx-1"}`)}},
	}, OperationalExportRequest{
		ScopeID:              "scope-1",
		AdmissionID:          "source-1",
		SourceKind:           "operator-audit",
		SourceVersion:        "1.0.0",
		SourceScopeID:        "operator-scope-1",
		OwnerRuntimeBoundary: "operator-1",
		RunID:                "run-1",
		VerifierID:           "operational-export",
		VerifierVersion:      "1.0.0",
		AcquisitionBoundary:  "operator-local-export",
		WindowStart:          executedAt.Add(-time.Minute),
		WindowEnd:            executedAt.Add(time.Minute),
		MaxRows:              10,
		OutputDir:            testutil.TempDir(t),
	})
	require.NoError(t, err)
	assert.Zero(t, inventory.PersistenceCount)
	require.Len(t, inventory.Limitations, 1)
	assert.Contains(t, inventory.Limitations[0], "event receipt tx-1 could not yield persistence evidence")
}

func exportDigestHex(body []byte) string {
	return ContentAddress(ArtifactTypeEventReceipt, body)[len(string(ArtifactTypeEventReceipt))+len(":sha256:"):]
}
