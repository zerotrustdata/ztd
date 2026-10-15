// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// SignerStoreService provides trusted signer CRUD operations and resolves the
// verification key for ActionReceipts by the executing Operator's identity.
type SignerStoreService struct {
	db     *sqliteutil.DB
	logger *slog.Logger
	docSvc *DocumentStoreService

	// embeddedActuatorPub and embeddedActuatorKeyID are the Gateway's own L5
	// Actuator key. They are the bound key of the embedded Operator
	// (constants.DocIDEmbeddedOperator).
	embeddedActuatorPub   ed25519.PublicKey
	embeddedActuatorKeyID string
}

// ReceiptSignerResolver resolves the verification key for an ActionReceipt by
// the identity of the Operator that executed it. SignerStoreService implements it.
type ReceiptSignerResolver interface {
	ReceiptSignerKey(ctx context.Context, operatorID string, receipt *operatorv1.ActionReceipt) (ed25519.PublicKey, error)
}

// NewSignerStoreService creates a new signer store service.
// docSvc is the shared DocumentStoreService instance from CanonicalDBService.
// embeddedActuatorPub and embeddedActuatorKeyID are the Gateway's Actuator
// public key and its key id (key_id == hex(pub)), bound to the embedded Operator.
func NewSignerStoreService(db *sqliteutil.DB, logger *slog.Logger, docSvc *DocumentStoreService, embeddedActuatorPub ed25519.PublicKey, embeddedActuatorKeyID string) *SignerStoreService {
	return &SignerStoreService{
		db:                    db,
		logger:                logger,
		docSvc:                docSvc,
		embeddedActuatorPub:   embeddedActuatorPub,
		embeddedActuatorKeyID: embeddedActuatorKeyID,
	}
}

// GetTrustedSigner retrieves an L2 signer public key from the database.
// Only registered signers resolve. A key id is never decoded into a key, so a
// key id cannot vouch for itself. Implements governance.SignerStore.
func (s *SignerStoreService) GetTrustedSigner(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	doc, err := s.docSvc.DocGet(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), keyID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", constants.ErrDocumentStoreUnmarshalData, keyID)
	}
	if doc == nil {
		return nil, nil
	}

	data, err := json.Marshal(doc.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", constants.ErrDocumentStoreMarshalDocument, err)
	}

	var signer models.TrustedSigner
	if err := json.Unmarshal(data, &signer); err != nil {
		return nil, fmt.Errorf("%w: %v", constants.ErrDocumentStoreUnmarshalData, err)
	}

	if !signer.Enabled {
		return nil, nil
	}

	pubBytes, err := hex.DecodeString(signer.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", constants.ErrValidationFailed, err)
	}

	if len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: invalid public key size: %d", constants.ErrValidationFailed, len(pubBytes))
	}

	return ed25519.PublicKey(pubBytes), nil
}

// AddTrustedSigner adds or updates a trusted L2 signer in the database.
func (s *SignerStoreService) AddTrustedSigner(ctx context.Context, signer models.TrustedSigner) error {
	if signer.ID == "" {
		return fmt.Errorf("%w: signer ID", constants.ErrMissingRequiredField)
	}
	if signer.PublicKey == "" {
		return fmt.Errorf("%w: signer public key", constants.ErrMissingRequiredField)
	}

	if signer.AddedAt.IsZero() {
		signer.AddedAt = time.Now().UTC()
	}

	data, err := json.Marshal(signer)
	if err != nil {
		return fmt.Errorf("%w: %v", constants.ErrDocumentStoreMarshalDocument, err)
	}

	return s.docSvc.DocSet(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), signer.ID, data)
}

// ListTrustedSigners returns all trusted L2 signers in the database.
func (s *SignerStoreService) ListTrustedSigners(ctx context.Context) ([]models.TrustedSigner, error) {
	docs, err := s.docSvc.DocQuery(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), nil, "id", 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", constants.ErrDocumentStoreUnmarshalData, err)
	}

	results := make([]models.TrustedSigner, 0, len(docs))
	for _, doc := range docs {
		data, err := json.Marshal(doc.Data)
		if err != nil {
			s.logger.Warn("failed to marshal signer document", "error", err)
			continue
		}
		var signer models.TrustedSigner
		if err := json.Unmarshal(data, &signer); err != nil {
			s.logger.Warn("failed to unmarshal signer document", "error", err)
			continue
		}
		// id is not in the data map usually, so we set it from doc.ID
		signer.ID = doc.ID
		results = append(results, signer)
	}
	return results, nil
}

// DeleteTrustedSigner removes a trusted L2 signer from the database.
func (s *SignerStoreService) DeleteTrustedSigner(ctx context.Context, keyID string) (bool, error) {
	return s.docSvc.DocDeleteWithResult(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), keyID)
}

// HasTrustedSigners returns true if at least one trusted L2 signer is provisioned in the database.
func (s *SignerStoreService) HasTrustedSigners(ctx context.Context) (bool, error) {
	filters := []models.DocFilter{
		{Field: "enabled", Op: "==", Value: json.RawMessage("true")},
	}
	docs, err := s.docSvc.DocQuery(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), filters, "", 1)
	if err != nil {
		return false, fmt.Errorf("%w: %v", constants.ErrDocumentStoreUnmarshalData, err)
	}
	return len(docs) > 0, nil
}

// ReceiptSignerKey resolves the verification key for a receipt executed by the
// Operator identified by operatorID. The key comes from the Gateway's own
// actuator key (embedded Operator) or from the key bound on the Operator's
// document at enrollment. The receipt's own signer_key_id is never trusted:
// it must equal the bound key id.
func (s *SignerStoreService) ReceiptSignerKey(ctx context.Context, operatorID string, receipt *operatorv1.ActionReceipt) (ed25519.PublicKey, error) {
	boundPub, boundKeyID, err := s.boundActuatorKey(ctx, operatorID)
	if err != nil {
		return nil, fmt.Errorf("signer store: receipt signer key: %w", err)
	}
	if receipt.GetSignerKeyId() != boundKeyID {
		return nil, fmt.Errorf("signer store: receipt signer key: %w: signer %q, bound %q",
			constants.ErrReceiptSignerNotBound, receipt.GetSignerKeyId(), boundKeyID)
	}
	return boundPub, nil
}

// boundActuatorKey returns the Actuator public key and key id bound to the
// Operator identified by operatorID.
func (s *SignerStoreService) boundActuatorKey(ctx context.Context, operatorID string) (ed25519.PublicKey, string, error) {
	if operatorID == string(constants.DocIDEmbeddedOperator) {
		if len(s.embeddedActuatorPub) != ed25519.PublicKeySize || s.embeddedActuatorKeyID == "" {
			return nil, "", fmt.Errorf("embedded operator: %w", constants.ErrReceiptSignerNotBound)
		}
		return s.embeddedActuatorPub, s.embeddedActuatorKeyID, nil
	}

	doc, err := s.docSvc.DocGet(ctx, marshaler.CollectionName(constants.CollectionOperators), operatorID)
	if err != nil {
		return nil, "", fmt.Errorf("load operator %s: %w", operatorID, err)
	}
	if doc == nil {
		return nil, "", fmt.Errorf("operator %s: %w", operatorID, constants.ErrReceiptSignerNotBound)
	}
	operatorDoc, err := models.OperatorDocumentFromStore(doc)
	if err != nil {
		return nil, "", fmt.Errorf("decode operator %s: %w", operatorID, err)
	}
	if operatorDoc.GetActuatorPublicKey() == "" || operatorDoc.GetActuatorKeyId() == "" {
		return nil, "", fmt.Errorf("operator %s: %w", operatorID, constants.ErrReceiptSignerNotBound)
	}
	pub, err := models.DecodeActuatorPublicKey(operatorDoc.GetActuatorPublicKey())
	if err != nil {
		return nil, "", fmt.Errorf("operator %s: %w", operatorID, err)
	}
	if hex.EncodeToString(pub) != operatorDoc.GetActuatorKeyId() {
		return nil, "", fmt.Errorf("operator %s: %w: key id does not match bound key", operatorID, constants.ErrReceiptSignerNotBound)
	}
	return pub, operatorDoc.GetActuatorKeyId(), nil
}
