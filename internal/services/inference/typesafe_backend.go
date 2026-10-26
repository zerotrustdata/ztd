// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const TypeSafeSystemOneEndpoint = "https://api.typesafe.ai/v1/systemone"
const TypeSafeCredentialName = "typesafe_jev_api_key"

// DecisionCredentialVault is satisfied by the runtime's sealed keystore.
// No key is accepted through a request, environment variable, or CLI flag.
type DecisionCredentialVault interface {
	DecryptSecret(name string) (string, error)
}

// TypeSafeBackend serves hosted, version-pinned Jev decisions. It never follows redirects.
type TypeSafeBackend struct {
	vault  DecisionCredentialVault
	client *http.Client
}

func NewTypeSafeBackend(vault DecisionCredentialVault) (*TypeSafeBackend, error) {
	if vault == nil {
		return nil, constants.ErrVaultNotInitialized
	}
	return &TypeSafeBackend{vault: vault, client: &http.Client{Timeout: ProviderRequestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (b *TypeSafeBackend) Decide(ctx context.Context, req models.DecisionRequest) (*models.DecisionResponse, error) {
	if !req.AllowHosted || !req.Input.GetAllowHosted() {
		return nil, constants.ErrInferenceHostedNotAllowed
	}
	if !models.IsPinnedJevModel(req.Model) || req.ModelDigest != "" {
		return nil, constants.ErrInferenceModelRefInvalid
	}
	req.KeepAlive = "" // hosted Jev has no residency control
	body, err := decisionBody(req)
	if err != nil {
		return nil, err
	}
	key, err := b.vault.DecryptSecret(TypeSafeCredentialName)
	if err != nil {
		return nil, fmt.Errorf("hosted Jev: read vault credential: %w", err)
	}
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, constants.ErrKeyStoreDecodeFailed
	}
	resp, err := callSystemOne(ctx, b.client, TypeSafeSystemOneEndpoint, key, body, req, defaultMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	resp.HostedEgress = &operatorv1.InferenceHostedEgressAttestation{ProviderOrigin: "https://api.typesafe.ai", ModelVersion: req.Model, Provenance: "hosted-unpinned", NormalizedRequestHash: resp.NormalizedRequestHash, ExplicitConsent: true}
	return resp, nil
}

type decisionBackendRouter struct {
	local  DecisionBackend
	hosted DecisionBackend
}

func (r decisionBackendRouter) Decide(ctx context.Context, req models.DecisionRequest) (*models.DecisionResponse, error) {
	if req.AllowHosted {
		if r.hosted == nil {
			return nil, constants.ErrInferenceHostedNotAllowed
		}
		return r.hosted.Decide(ctx, req)
	}
	if r.local == nil {
		return nil, fmt.Errorf("inference: decide: %w: no local provider", constants.ErrInferenceBackendUnavailable)
	}
	return r.local.Decide(ctx, req)
}

// ConfigureDecisionBackend attaches hosted inference only to an explicitly opted-in Operator.
func (h *InferenceExecutionHandler) ConfigureDecisionBackend(local DecisionBackend, allowHosted bool, vault DecisionCredentialVault) error {
	var hosted DecisionBackend
	if allowHosted {
		b, err := NewTypeSafeBackend(vault)
		if err != nil {
			return err
		}
		hosted = b
	}
	h.decisionBackend = decisionBackendRouter{local: local, hosted: hosted}
	return nil
}
