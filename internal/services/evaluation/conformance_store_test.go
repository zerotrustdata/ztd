// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func newConformanceRecord(runID string) *evalv1.ConformanceRunRecord {
	return &evalv1.ConformanceRunRecord{
		RunId:        runID,
		SuiteId:      "governance-conformance",
		SuiteVersion: "1.0.0",
		Posture:      evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Anchors:      []*evalv1.ConformanceAnchor{{RuntimeId: "gateway", KeyId: "k1", PublicKeyHex: "00"}},
		CanaryRoot:   "eval/targets/conformance-" + runID,
		Cases:        []*evalv1.ConformanceCaseEvidence{{CaseId: "c-1", AttemptId: runID + "-0001"}},
	}
}

func TestConformanceStoreRoundTripKeepsCanonicalBytesAndDigest(t *testing.T) {
	ctx := context.Background()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	store := NewStore(files)
	record := newConformanceRecord("run-roundtrip")

	require.NoError(t, store.SaveConformanceRecord(ctx, record))

	want, err := evalv1.MarshalCanonical(record)
	require.NoError(t, err)
	raw, digestHex, err := store.LoadConformanceRecordRaw(ctx, "run-roundtrip")
	require.NoError(t, err)
	assert.Equal(t, want, raw)

	sum := sha256.Sum256(raw)
	assert.Equal(t, hex.EncodeToString(sum[:]), digestHex)
}

func TestConformanceStoreRefusesToOverwriteRecord(t *testing.T) {
	ctx := context.Background()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	store := NewStore(files)
	record := newConformanceRecord("run-twice")

	require.NoError(t, store.SaveConformanceRecord(ctx, record))
	err = store.SaveConformanceRecord(ctx, record)

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationReportPersistFailed)
}

func TestConformanceStoreRejectsInvalidRunID(t *testing.T) {
	ctx := context.Background()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	store := NewStore(files)

	for _, runID := range []string{"", "../escape", "nested/run"} {
		err := store.SaveConformanceRecord(ctx, newConformanceRecord(runID))
		assert.ErrorIsf(t, err, constants.ErrEvaluationReportPersistFailed, "save run id %q", runID)
	}
}
