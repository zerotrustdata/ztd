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
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// AttemptStore persists durable operator-local provider-attempt records. A
// record holds identifiers, status, timestamps, digests, sizes and a fixed
// failure category only. Provider bytes and inference results are content and
// are never written here: the executing Operator's encrypted local-first events store owns
// them, and the record binds them by digest.
type AttemptStore interface {
	Begin(ctx context.Context, record *operatorv1.InferenceProviderAttemptRecord) error
	Import(ctx context.Context, record *operatorv1.InferenceProviderAttemptRecord) error
	Complete(ctx context.Context, providerAttemptID, resultDigest string) error
	Fail(ctx context.Context, providerAttemptID string, class AttemptFailureClass) error
	Get(ctx context.Context, providerAttemptID string) (*operatorv1.InferenceProviderAttemptRecord, error)
	// RecordRawResponse binds the digest and size of the raw provider byte
	// stream into the attempt record. The bytes themselves are not persisted.
	RecordRawResponse(ctx context.Context, providerAttemptID string, response []byte) error
}

// AttemptFailureClass is the fixed category stored in a failed attempt record.
// It replaces free-form error text, which can echo prompt or provider content.
type AttemptFailureClass string

const (
	AttemptFailureBackendUnavailable AttemptFailureClass = "backend_unavailable"
	AttemptFailureBackendTimeout     AttemptFailureClass = "backend_timeout"
	AttemptFailureModelNotFound      AttemptFailureClass = "model_not_found"
	AttemptFailureCapabilityRejected AttemptFailureClass = "capability_unsupported"
	AttemptFailureContextOverflow    AttemptFailureClass = "context_overflow"
	AttemptFailureResponseInvalid    AttemptFailureClass = "provider_response_invalid"
	AttemptFailureStreamError        AttemptFailureClass = "provider_stream_error"
	AttemptFailureGenerateFailed     AttemptFailureClass = "generation_failed"
	AttemptFailureCanceled           AttemptFailureClass = "canceled"
	AttemptFailureInternal           AttemptFailureClass = "internal_error"
)

// ClassifyAttemptFailure maps an inference error to its fixed failure category.
// Unrecognized errors are AttemptFailureInternal; their text is never stored.
func ClassifyAttemptFailure(err error) AttemptFailureClass {
	switch {
	case errors.Is(err, constants.ErrInferenceBackendTimeout):
		return AttemptFailureBackendTimeout
	case errors.Is(err, constants.ErrInferenceBackendUnavailable):
		return AttemptFailureBackendUnavailable
	case errors.Is(err, constants.ErrInferenceModelNotFound):
		return AttemptFailureModelNotFound
	case errors.Is(err, constants.ErrInferenceDecisionUnsupported), errors.Is(err, constants.ErrInferenceHostedNotAllowed), errors.Is(err, constants.ErrInferenceDecisionRequestInvalid), errors.Is(err, constants.ErrInferenceToolsUnsupported), errors.Is(err, constants.ErrInferenceCapabilityUnsupported):
		return AttemptFailureCapabilityRejected
	case errors.Is(err, constants.ErrInferenceContextOverflow):
		return AttemptFailureContextOverflow
	case errors.Is(err, constants.ErrInferenceProviderResponseInvalid), errors.Is(err, constants.ErrInferenceDecisionResponseInvalid):
		return AttemptFailureResponseInvalid
	case errors.Is(err, constants.ErrInferenceProviderStreamError):
		return AttemptFailureStreamError
	case errors.Is(err, constants.ErrInferenceGenerateFailed):
		return AttemptFailureGenerateFailed
	case errors.Is(err, constants.ErrInferenceCanceled), errors.Is(err, constants.ErrInferenceCallerDisconnected), errors.Is(err, context.Canceled):
		return AttemptFailureCanceled
	default:
		return AttemptFailureInternal
	}
}

func (c AttemptFailureClass) valid() bool {
	switch c {
	case AttemptFailureBackendUnavailable, AttemptFailureBackendTimeout, AttemptFailureModelNotFound,
		AttemptFailureCapabilityRejected, AttemptFailureContextOverflow, AttemptFailureResponseInvalid,
		AttemptFailureStreamError, AttemptFailureGenerateFailed, AttemptFailureCanceled, AttemptFailureInternal:
		return true
	default:
		return false
	}
}

type fileAttemptStore struct {
	fileSvc fs.RuntimeFileService
}

// NewAttemptStore constructs a durable provider-attempt store under
// data/inference/attempts within the runtime directory.
func NewAttemptStore(fileSvc fs.RuntimeFileService) (AttemptStore, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("inference attempt store: %w", constants.ErrMissingRequiredField)
	}
	return &fileAttemptStore{fileSvc: fileSvc}, nil
}

func (s *fileAttemptStore) attemptsDir() string {
	return filepath.Join(constants.DataDirname, constants.InferenceDirname, constants.InferenceAttemptsDirname)
}

// RecordRawResponse keeps only the SHA-256 and byte count of the provider
// stream. Decoding or canonicalization must never change what is hashed.
func (s *fileAttemptStore) RecordRawResponse(ctx context.Context, providerAttemptID string, response []byte) error {
	record, err := s.loadRequired(ctx, providerAttemptID)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(response)
	record.RawResponseDigest = hex.EncodeToString(sum[:])
	record.RawResponseSizeBytes = uint64(len(response))
	return s.write(ctx, record)
}

func (s *fileAttemptStore) recordPath(providerAttemptID string) (string, error) {
	if err := models.ValidateProviderAttemptID(providerAttemptID); err != nil {
		return "", fmt.Errorf("inference attempt store: %w", err)
	}
	return filepath.Join(s.attemptsDir(), providerAttemptID+constants.FileExtJSON), nil
}

func (s *fileAttemptStore) Begin(ctx context.Context, record *operatorv1.InferenceProviderAttemptRecord) error {
	if record == nil || record.GetProviderAttemptId() == "" || record.GetTransactionId() == "" {
		return fmt.Errorf("inference attempt store: %w", constants.ErrMissingRequiredField)
	}
	existing, err := s.Get(ctx, record.GetProviderAttemptId())
	if err != nil {
		if !errors.Is(err, constants.ErrNotFound) {
			return fmt.Errorf("inference attempt store: %w", err)
		}
	} else {
		switch existing.GetStatus() {
		case operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_IN_PROGRESS,
			operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED,
			operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_FAILED:
			return fmt.Errorf("inference attempt store: begin: %w", constants.ErrInferenceProviderAttemptConflict)
		}
	}
	if err := s.fileSvc.MkdirAll(ctx, s.attemptsDir(), constants.PermDirPrivate); err != nil {
		return fmt.Errorf("inference attempt store: mkdir: %w", err)
	}
	if record.GetStartedAtUnixMs() == 0 {
		record.StartedAtUnixMs = time.Now().UnixMilli()
	}
	if record.GetStatus() == operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_UNSPECIFIED {
		record.Status = operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_IN_PROGRESS
	}
	return s.write(ctx, record)
}

func (s *fileAttemptStore) Import(ctx context.Context, record *operatorv1.InferenceProviderAttemptRecord) error {
	if record == nil || record.GetProviderAttemptId() == "" || record.GetStartedAtUnixMs() == 0 || record.GetStatus() == operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_UNSPECIFIED {
		return fmt.Errorf("inference attempt store: import: %w", constants.ErrMissingRequiredField)
	}
	if record.GetStatus() != operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_IN_PROGRESS && record.GetCompletedAtUnixMs() == 0 {
		return fmt.Errorf("inference attempt store: import: %w", constants.ErrMissingRequiredField)
	}
	existing, err := s.Get(ctx, record.GetProviderAttemptId())
	if err == nil {
		if proto.Equal(existing, record) {
			return nil
		}
		return fmt.Errorf("inference attempt store: import: %w", constants.ErrInferenceProviderAttemptConflict)
	}
	if !errors.Is(err, constants.ErrNotFound) {
		return fmt.Errorf("inference attempt store: import: %w", err)
	}
	if err := s.fileSvc.MkdirAll(ctx, s.attemptsDir(), constants.PermDirPrivate); err != nil {
		return fmt.Errorf("inference attempt store: import mkdir: %w", err)
	}
	return s.write(ctx, record)
}

func (s *fileAttemptStore) Complete(ctx context.Context, providerAttemptID, resultDigest string) error {
	record, err := s.loadRequired(ctx, providerAttemptID)
	if err != nil {
		return err
	}
	if record.GetStatus() != operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_IN_PROGRESS {
		return fmt.Errorf("inference attempt store: complete: %w", constants.ErrInferenceProviderAttemptConflict)
	}
	record.Status = operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED
	record.ResultDigest = resultDigest
	record.CompletedAtUnixMs = time.Now().UnixMilli()
	return s.write(ctx, record)
}

func (s *fileAttemptStore) Fail(ctx context.Context, providerAttemptID string, class AttemptFailureClass) error {
	if !class.valid() {
		return fmt.Errorf("inference attempt store: fail: %w", constants.ErrInferenceAttemptFailureClassInvalid)
	}
	record, err := s.loadRequired(ctx, providerAttemptID)
	if err != nil {
		return err
	}
	if record.GetStatus() != operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_IN_PROGRESS {
		return fmt.Errorf("inference attempt store: fail: %w", constants.ErrInferenceProviderAttemptConflict)
	}
	record.Status = operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_FAILED
	record.FailureSummary = string(class)
	record.CompletedAtUnixMs = time.Now().UnixMilli()
	return s.write(ctx, record)
}

func (s *fileAttemptStore) Get(ctx context.Context, providerAttemptID string) (*operatorv1.InferenceProviderAttemptRecord, error) {
	path, err := s.recordPath(providerAttemptID)
	if err != nil {
		return nil, err
	}
	data, err := s.fileSvc.ReadFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("inference attempt store: read: %w", err)
	}
	record := &operatorv1.InferenceProviderAttemptRecord{}
	if err := protojson.Unmarshal(data, record); err != nil {
		return nil, fmt.Errorf("inference attempt store: unmarshal: %w", err)
	}
	return record, nil
}

func (s *fileAttemptStore) loadRequired(ctx context.Context, providerAttemptID string) (*operatorv1.InferenceProviderAttemptRecord, error) {
	record, err := s.Get(ctx, providerAttemptID)
	if err != nil {
		return nil, fmt.Errorf("inference attempt store: %w", err)
	}
	return record, nil
}

func (s *fileAttemptStore) write(ctx context.Context, record *operatorv1.InferenceProviderAttemptRecord) error {
	path, err := s.recordPath(record.GetProviderAttemptId())
	if err != nil {
		return err
	}
	data, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(record)
	if err != nil {
		return fmt.Errorf("inference attempt store: marshal: %w", err)
	}
	return s.fileSvc.WriteFile(ctx, path, data, constants.PermFilePrivate)
}
