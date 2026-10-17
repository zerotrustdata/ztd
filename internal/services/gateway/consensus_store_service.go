// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ConsensusStoreService provides ConsensusPolicy CRUD operations.
type ConsensusStoreService struct {
	db        *sqliteutil.DB
	logger    *slog.Logger
	docSvc    *DocumentStoreService
	signerSvc *SignerStoreService
}

// NewConsensusStoreService creates a new consensus store service.
// docSvc is the shared DocumentStoreService instance from CanonicalDBService.
func NewConsensusStoreService(db *sqliteutil.DB, logger *slog.Logger, docSvc *DocumentStoreService, signerSvc *SignerStoreService) *ConsensusStoreService {
	return &ConsensusStoreService{
		db:        db,
		logger:    logger,
		docSvc:    docSvc,
		signerSvc: signerSvc,
	}
}

// GetConsensus retrieves a ConsensusPolicy by ID from the database.
func (s *ConsensusStoreService) GetConsensus(ctx context.Context, id string) (*models.ConsensusPolicy, error) {
	doc, err := s.docSvc.DocGet(ctx, marshaler.CollectionName(constants.CollectionConsensus), id)
	if err != nil {
		return nil, fmt.Errorf("consensus store: get: %w", err)
	}
	if doc == nil {
		return nil, nil
	}

	data, err := json.Marshal(doc.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDocumentStoreMarshalDocument, err)
	}

	var policy models.ConsensusPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDocumentStoreUnmarshalData, err)
	}
	policy.ID = doc.ID

	return &policy, nil
}

// GetBoundConsensusApp retrieves the single bound consensus application policy.
func (s *ConsensusStoreService) GetBoundConsensusApp(ctx context.Context) (*models.ConsensusPolicy, error) {
	return s.GetConsensus(ctx, string(constants.DocIDConsensusBinding))
}

// BindConsensusApp binds an enrolled application as the Gateway consensus application.
func (s *ConsensusStoreService) BindConsensusApp(ctx context.Context, appID, endpointURL string) (*models.ConsensusPolicy, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, fmt.Errorf("%w: app_id", constants.ErrMissingRequiredField)
	}
	endpointURL = strings.TrimSpace(endpointURL)
	if endpointURL == "" {
		return nil, fmt.Errorf("%w: endpoint_url", constants.ErrMissingRequiredField)
	}
	u, err := url.Parse(endpointURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, constants.ErrConsensusEndpointInvalid
	}

	pubKey, err := s.signerSvc.GetTrustedSigner(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("verify signer key: %w", err)
	}
	if pubKey == nil {
		pubKey, err = s.signerSvc.GetTrustedSigner(ctx, protocol.NewWorkloadIdentity().AppSPIFFEID(appID))
		if err != nil {
			return nil, fmt.Errorf("verify signer key: %w", err)
		}
	}
	if pubKey == nil {
		return nil, constants.ErrConsensusAppMissingSignerKey
	}

	now := time.Now().UTC()
	policy := models.ConsensusPolicy{
		ID:              string(constants.DocIDConsensusBinding),
		MemberAppIDs:    []string{appID},
		Quorum:          1,
		RequireDistinct: true,
		EndpointURL:     endpointURL,
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	data, err := json.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDocumentStoreMarshalDocument, err)
	}

	if err := s.docSvc.DocSet(ctx, marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding), data); err != nil {
		return nil, fmt.Errorf("persist consensus binding: %w", err)
	}

	return &policy, nil
}

// ClearConsensusBinding removes the bound consensus app if it matches appID (or if appID is empty).
func (s *ConsensusStoreService) ClearConsensusBinding(ctx context.Context, appID string) error {
	bound, err := s.GetBoundConsensusApp(ctx)
	if err != nil || bound == nil {
		return err
	}
	if appID == "" || len(bound.MemberAppIDs) == 0 || bound.MemberAppIDs[0] == appID {
		_, err := s.DeleteConsensus(ctx, string(constants.DocIDConsensusBinding))
		return err
	}
	return nil
}

// GetConsensusPolicy implements governance.L2ConsensusPolicyStore by loading
// the ConsensusPolicy via GetConsensus (or GetBoundConsensusApp) and adapting it to the generic
// L2ConsensusPolicy struct.
func (s *ConsensusStoreService) GetConsensusPolicy(ctx context.Context, id string) (*governance.L2ConsensusPolicy, error) {
	policy, err := s.GetConsensus(ctx, id)
	if err != nil {
		return nil, err
	}
	if policy == nil && (id == "" || id == string(constants.DocIDConsensusBinding)) {
		policy, err = s.GetBoundConsensusApp(ctx)
		if err != nil {
			return nil, err
		}
	}
	if policy == nil {
		return nil, nil
	}
	return &governance.L2ConsensusPolicy{
		MemberKeyIDs:    policy.MemberAppIDs,
		Quorum:          policy.Quorum,
		RequireDistinct: policy.RequireDistinct,
		Enabled:         policy.Enabled,
	}, nil
}

// OperatorL2Trust snapshots consensus set id and the enabled member keys for
// an Operator enrollment bundle; the Operator verifies L2 votes against it
// in memory. If id is omitted or empty, it resolves the bound consensus app.
func (s *ConsensusStoreService) OperatorL2Trust(ctx context.Context, id ...string) (*models.OperatorL2Trust, error) {
	var policy *models.ConsensusPolicy
	var err error
	if len(id) > 0 && id[0] != "" {
		policy, err = s.GetConsensus(ctx, id[0])
	} else {
		policy, err = s.GetBoundConsensusApp(ctx)
	}
	if err != nil || policy == nil || !policy.Enabled {
		return nil, err
	}
	trust := &models.OperatorL2Trust{
		ConsensusSetID:  policy.ID,
		MemberKeyIDs:    policy.MemberAppIDs,
		Quorum:          policy.Quorum,
		RequireDistinct: policy.RequireDistinct,
		Enabled:         policy.Enabled,
	}
	for _, keyID := range policy.MemberAppIDs {
		pub, err := s.signerSvc.GetTrustedSigner(ctx, keyID)
		if err != nil {
			return nil, fmt.Errorf("consensus store: operator trust: %w", err)
		}
		if pub == nil {
			continue
		}
		trust.Signers = append(trust.Signers, models.OperatorSigner{KeyID: keyID, PublicKeyHex: hex.EncodeToString(pub)})
	}
	return trust, nil
}

// ConsensusAppDeliberator dispatches the GovernanceEnvelope to the bound consensus application's HTTPS endpoint.
type ConsensusAppDeliberator struct {
	store      *ConsensusStoreService
	httpClient *http.Client
}

// NewConsensusAppDeliberator creates a new deliberator backed by the bound consensus app.
func (s *ConsensusStoreService) NewConsensusAppDeliberator(httpClient *http.Client) mcp.L2ConsensusDeliberator {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &ConsensusAppDeliberator{
		store:      s,
		httpClient: httpClient,
	}
}

// Deliberate sends the envelope to the bound consensus app and verifies the returned envelope carries a ConsensusProof.
func (d *ConsensusAppDeliberator) Deliberate(ctx context.Context, envelopeBytes []byte) ([]byte, error) {
	bound, err := d.store.GetBoundConsensusApp(ctx)
	if err != nil {
		return nil, err
	}
	if bound == nil || !bound.Enabled || strings.TrimSpace(bound.EndpointURL) == "" {
		return nil, constants.ErrTxL2ConsensusNotConfigured
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bound.EndpointURL, bytes.NewReader(envelopeBytes))
	if err != nil {
		return nil, fmt.Errorf("create consensus deliberation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("consensus deliberation request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("consensus deliberation failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read consensus response: %w", err)
	}

	var updated commonv1.GovernanceEnvelope
	if err := proto.Unmarshal(respBytes, &updated); err != nil {
		if jsonErr := protojson.Unmarshal(respBytes, &updated); jsonErr != nil {
			return nil, fmt.Errorf("unmarshal consensus envelope response: %w", err)
		}
	}
	if updated.GetGovernance() == nil || updated.GetGovernance().GetL2() == nil || len(updated.GetGovernance().GetL2().GetVotes()) == 0 {
		return nil, constants.ErrTxL2SignatureMissing
	}

	return respBytes, nil
}

// AddConsensus adds or updates a ConsensusPolicy in the database.
// Validation at write time, fail-closed:
// - Consensus ID is non-empty and contains only alphanumeric, hyphens, underscores
// - Quorum >= 1
// - Quorum <= len(MemberAppIDs)
// - Every MemberAppID resolves to an enabled TrustedSigner
// - No duplicate member IDs
// - New consensus must be created with Enabled=true (updates may disable)
func (s *ConsensusStoreService) AddConsensus(ctx context.Context, policy models.ConsensusPolicy) error {
	if policy.ID == "" {
		return fmt.Errorf("%w: consensus ID", constants.ErrMissingRequiredField)
	}
	if !isValidConsensusID(policy.ID) {
		return fmt.Errorf("%w: %s", constants.ErrConsensusInvalidID, policy.ID)
	}
	if len(policy.MemberAppIDs) == 0 {
		return fmt.Errorf("%w: consensus member_app_ids", constants.ErrMissingRequiredField)
	}
	if policy.Quorum < 1 {
		return fmt.Errorf("%w: quorum must be >= 1", constants.ErrConstraintViolation)
	}
	if policy.Quorum > len(policy.MemberAppIDs) {
		return fmt.Errorf("%w: quorum cannot exceed member count", constants.ErrConstraintViolation)
	}

	// Check for duplicate member IDs
	memberSet := make(map[string]bool)
	for _, appID := range policy.MemberAppIDs {
		if appID == "" {
			return fmt.Errorf("%w: member_app_ids cannot contain empty strings", constants.ErrConstraintViolation)
		}
		if memberSet[appID] {
			return fmt.Errorf("%w: duplicate member_app_id: %s", constants.ErrConstraintViolation, appID)
		}
		memberSet[appID] = true
	}

	// Verify every member ID resolves to an enabled TrustedSigner
	for _, appID := range policy.MemberAppIDs {
		pubKey, err := s.signerSvc.GetTrustedSigner(ctx, appID)
		if err != nil {
			return fmt.Errorf("%w: failed to verify signer %s: %v", constants.ErrConstraintViolation, appID, err)
		}
		if pubKey == nil {
			return fmt.Errorf("%w: member_app_id %s is not an enabled trusted signer", constants.ErrConstraintViolation, appID)
		}
	}

	// Existence check: prevent silent overwrite of existing consensus.
	// - New consensus must be created with Enabled=true.
	// - Existing consensus may only be updated via Enabled=false (disable path).
	// - Overwriting an existing consensus with Enabled=true is rejected.
	existing, err := s.GetConsensus(ctx, policy.ID)
	if err != nil {
		return fmt.Errorf("%w: failed to check existing consensus: %w", constants.ErrConstraintViolation, err)
	}
	if existing != nil {
		if policy.Enabled {
			return fmt.Errorf("%w: consensus %s", constants.ErrAlreadyExists, policy.ID)
		}
	} else {
		if !policy.Enabled {
			return constants.ErrConsensusMustBeEnabled
		}
	}

	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = time.Now().UTC()
	}
	policy.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrDocumentStoreMarshalDocument, err)
	}

	return s.docSvc.DocSet(ctx, marshaler.CollectionName(constants.CollectionConsensus), policy.ID, data)
}

// ListConsensus returns all ConsensusPolicies in the database.
func (s *ConsensusStoreService) ListConsensus(ctx context.Context) ([]models.ConsensusPolicy, error) {
	docs, err := s.docSvc.DocQuery(ctx, marshaler.CollectionName(constants.CollectionConsensus), nil, "id", 0)
	if err != nil {
		return nil, fmt.Errorf("consensus store: list: %w", err)
	}

	results := make([]models.ConsensusPolicy, 0, len(docs))
	for _, doc := range docs {
		data, err := json.Marshal(doc.Data)
		if err != nil {
			s.logger.Warn("failed to marshal consensus document", "error", err)
			continue
		}
		var policy models.ConsensusPolicy
		if err := json.Unmarshal(data, &policy); err != nil {
			s.logger.Warn("failed to unmarshal consensus document", "error", err)
			continue
		}
		// id is not in the data map usually, so we set it from doc.ID
		policy.ID = doc.ID
		results = append(results, policy)
	}
	return results, nil
}

// DeleteConsensus removes a ConsensusPolicy from the database.
func (s *ConsensusStoreService) DeleteConsensus(ctx context.Context, id string) (bool, error) {
	return s.docSvc.DocDeleteWithResult(ctx, marshaler.CollectionName(constants.CollectionConsensus), id)
}

// isValidConsensusID validates that a consensus ID contains only allowed characters.
// Allowed: letters, digits, hyphens, and underscores. Must be non-empty.
func isValidConsensusID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
