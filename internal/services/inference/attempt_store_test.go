// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestAttemptStore_BeginCompleteAndRejectDuplicate(t *testing.T) {
	t.Parallel()
	store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	require.NoError(t, err)

	ctx := context.Background()
	record := &operatorv1.InferenceProviderAttemptRecord{
		ProviderAttemptId:   "attempt-1",
		TransactionId:       "tx-1",
		RetryCount:          1,
		RetryClassification: models.ClassifyRetry(1),
	}
	require.NoError(t, store.Begin(ctx, record))
	require.NoError(t, store.Complete(ctx, "attempt-1", "digest-1"))

	stored, err := store.Get(ctx, "attempt-1")
	require.NoError(t, err)
	assert.Equal(t, operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED, stored.GetStatus())
	assert.Equal(t, "digest-1", stored.GetResultDigest())

	err = store.Begin(ctx, record)
	assert.ErrorIs(t, err, constants.ErrInferenceProviderAttemptConflict)
}

func TestAttemptStore_ImportPersistsSynthesizedRemoteRecord(t *testing.T) {
	t.Parallel()
	store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	require.NoError(t, err)
	ctx := context.Background()
	record := &operatorv1.InferenceProviderAttemptRecord{
		ProviderAttemptId: "attempt-imported",
		Status:            operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED,
		StartedAtUnixMs:   1_700_000_000_000,
		CompletedAtUnixMs: 1_700_000_001_000,
	}

	require.NoError(t, store.Import(ctx, record))
	require.NoError(t, store.Import(ctx, record))
	stored, err := store.Get(ctx, record.GetProviderAttemptId())
	require.NoError(t, err)
	assert.Equal(t, record, stored)
}

func TestAttemptStore_FailMarksTerminalState(t *testing.T) {
	t.Parallel()
	store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, store.Begin(ctx, &operatorv1.InferenceProviderAttemptRecord{
		ProviderAttemptId: "attempt-2",
		TransactionId:     "tx-2",
	}))
	require.NoError(t, store.Fail(ctx, "attempt-2", AttemptFailureBackendUnavailable))

	stored, err := store.Get(ctx, "attempt-2")
	require.NoError(t, err)
	assert.Equal(t, operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_FAILED, stored.GetStatus())
	assert.Equal(t, "backend_unavailable", stored.GetFailureSummary())
}

func TestAttemptStore_RejectsPathHostileAttemptID(t *testing.T) {
	t.Parallel()
	for _, attemptID := range []string{"../escape", "a/b", `a\b`, "..", "a\x00b"} {
		t.Run(attemptID, func(t *testing.T) {
			t.Parallel()
			store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
			require.NoError(t, err)
			ctx := context.Background()

			err = store.Begin(ctx, &operatorv1.InferenceProviderAttemptRecord{
				ProviderAttemptId: attemptID,
				TransactionId:     "tx-hostile",
			})
			assert.ErrorIs(t, err, constants.ErrInferenceProviderAttemptIDInvalid)
			_, err = store.Get(ctx, attemptID)
			assert.ErrorIs(t, err, constants.ErrInferenceProviderAttemptIDInvalid)
		})
	}
}

func TestAttemptStore_FailRejectsUnknownClass(t *testing.T) {
	t.Parallel()
	store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, testutil.TempDir(t)))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, store.Begin(ctx, &operatorv1.InferenceProviderAttemptRecord{ProviderAttemptId: "attempt-3", TransactionId: "tx-3"}))
	err = store.Fail(ctx, "attempt-3", AttemptFailureClass("raw error text echoing the prompt"))
	require.ErrorIs(t, err, constants.ErrInferenceAttemptFailureClassInvalid)
	stored, err := store.Get(ctx, "attempt-3")
	require.NoError(t, err)
	assert.Equal(t, operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_IN_PROGRESS, stored.GetStatus())
}

func TestClassifyAttemptFailure_NeverCarriesErrorText(t *testing.T) {
	t.Parallel()
	cases := map[error]AttemptFailureClass{
		fmt.Errorf("x: %w: model %q", constants.ErrInferenceModelNotFound, "secret-model"): AttemptFailureModelNotFound,
		fmt.Errorf("x: %w", constants.ErrInferenceBackendTimeout):                          AttemptFailureBackendTimeout,
		fmt.Errorf("x: %w", constants.ErrInferenceProviderResponseInvalid):                 AttemptFailureResponseInvalid,
		context.Canceled: AttemptFailureCanceled,
		errors.New("provider echoed: SECRET-PROMPT-MATERIAL"): AttemptFailureInternal,
	}
	for err, want := range cases {
		got := ClassifyAttemptFailure(err)
		assert.Equal(t, want, got)
		assert.True(t, got.valid())
		assert.NotContains(t, string(got), "SECRET")
	}
}

// The attempt store holds identifiers, status, digests and sizes only. Provider
// bytes are content: they are bound by digest and never written to disk.
func TestAttemptStore_RawResponseIsBoundByDigestAndNeverWritten(t *testing.T) {
	t.Parallel()
	root := testutil.TempDir(t)
	store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, root))
	require.NoError(t, err)
	ctx := context.Background()
	const marker = "PLAINTEXT-MARKER-model-output-7f3a"
	raw := []byte(`{"message":{"content":"` + marker + `"},"done":true}` + "\n")
	require.NoError(t, store.Begin(ctx, &operatorv1.InferenceProviderAttemptRecord{ProviderAttemptId: "attempt-4", TransactionId: "tx-4"}))
	require.NoError(t, store.RecordRawResponse(ctx, "attempt-4", raw))
	require.NoError(t, store.Fail(ctx, "attempt-4", ClassifyAttemptFailure(errors.New(marker))))

	stored, err := store.Get(ctx, "attempt-4")
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	assert.Equal(t, hex.EncodeToString(sum[:]), stored.GetRawResponseDigest())
	assert.Equal(t, uint64(len(raw)), stored.GetRawResponseSizeBytes())
	assert.Equal(t, "internal_error", stored.GetFailureSummary())

	assertNoFileContains(t, root, marker)
	var attemptFiles []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.Contains(path, "attempts") {
			attemptFiles = append(attemptFiles, filepath.Base(path))
		}
		return nil
	}))
	assert.Equal(t, []string{"attempt-4.json"}, attemptFiles)
}

// assertNoFileContains fails when any file under root contains needle.
func assertNoFileContains(t *testing.T, root, needle string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		assert.NotContains(t, string(data), needle, "plaintext marker found in %s", path)
		return nil
	}))
}
