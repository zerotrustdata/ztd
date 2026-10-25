// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Deliberate uses the authenticated Gateway for the internal Tribunal, or the
// bound application HTTPS protobuf endpoint, and returns the subject's proof. Failures wrap
// constants.ErrIngressL2DeliberationFailed.
func (c *Client) Deliberate(ctx context.Context, p Persona, env *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error) {
	status, raw, err := c.do(ctx, p, http.MethodGet, c.cfg.MTLSBaseURL+constants.APIPaths.AuthPlatformEnrollmentConsensus, nil)
	if err != nil {
		return nil, fmt.Errorf("deliberate: discover binding: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("deliberate: binding status %d: %w", status, constants.ErrIngressL2DeliberationFailed)
	}
	var binding models.ConsensusPolicy
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, fmt.Errorf("deliberate: decode binding: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
	}
	if !binding.Enabled {
		return nil, fmt.Errorf("deliberate: disabled binding: %w: %w", constants.ErrIngressL2DeliberationFailed, constants.ErrTxL2ConsensusNotConfigured)
	}
	var signed commonv1.GovernanceEnvelope
	if binding.Provider == string(constants.ConsensusProviderTribunal) {
		wire, err := protojson.Marshal(env)
		if err != nil {
			return nil, fmt.Errorf("deliberate: encode envelope: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
		}
		status, raw, err = c.do(ctx, p, http.MethodPost, c.cfg.MTLSBaseURL+constants.APIPaths.GovernanceDeliberations, wire)
		if err != nil {
			return nil, fmt.Errorf("deliberate: Tribunal: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("deliberate: Tribunal status %d: %w", status, constants.ErrIngressL2DeliberationFailed)
		}
		var decision operatorv1.GovernanceDeliberationResult
		if err := protojson.Unmarshal(raw, &decision); err != nil {
			return nil, fmt.Errorf("deliberate: decode Tribunal result: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
		}
		if decision.GetEnvelope() == nil {
			return nil, fmt.Errorf("deliberate: missing Tribunal envelope: %w", constants.ErrIngressL2DeliberationFailed)
		}
		proto.Merge(&signed, decision.GetEnvelope())
	} else {
		if models.ValidateConsensusEndpoint(binding.EndpointURL) != nil {
			return nil, fmt.Errorf("deliberate: binding endpoint: %w: %w", constants.ErrIngressL2DeliberationFailed, constants.ErrConsensusEndpointInvalid)
		}
		appClient, err := c.consensusAppClient(binding.PrimaryAppID())
		if err != nil {
			return nil, fmt.Errorf("deliberate: application identity: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
		}
		wire, err := proto.Marshal(env)
		if err != nil {
			return nil, fmt.Errorf("deliberate: marshal envelope: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
		}
		status, raw, err = c.doWithClient(ctx, Persona{ID: p.ID, UserAgent: p.UserAgent}, http.MethodPost, binding.EndpointURL, wire, appClient, "application/x-protobuf", false)
		if err != nil {
			return nil, fmt.Errorf("deliberate: post envelope: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("deliberate: status %d: %w", status, constants.ErrIngressL2DeliberationFailed)
		}

		if err := proto.Unmarshal(raw, &signed); err != nil {
			return nil, fmt.Errorf("deliberate: decode envelope: %w: %w", constants.ErrIngressL2DeliberationFailed, err)
		}
	}
	if signed.GetTransactionHash() != env.GetTransactionHash() {
		return nil, fmt.Errorf("deliberate: proof is for transaction %q, sent %q: %w",
			signed.GetTransactionHash(), env.GetTransactionHash(), constants.ErrIngressL2DeliberationFailed)
	}
	l2 := signed.GetGovernance().GetL2()
	if len(l2.GetVotes()) == 0 {
		return nil, fmt.Errorf("deliberate: envelope carries no L2 votes: %w", constants.ErrIngressL2DeliberationFailed)
	}
	// Return exactly the required number of votes when the provider supplied a
	// surplus. Row 18's below-quorum mutation must remove a required vote, not
	// merely one of several redundant votes. Captured exchanges retain all votes.
	quorum := binding.Quorum
	if quorum > 0 && l2.GetConsensusSetId() == binding.ID {
		selected := make([]*commonv1.L2Vote, 0, quorum)
		seen := make(map[string]bool)
		for _, vote := range l2.GetVotes() {
			if vote.GetDecision() && !seen[vote.GetSignerKeyId()] {
				seen[vote.GetSignerKeyId()] = true
				selected = append(selected, vote)
				if len(selected) == quorum {
					break
				}
			}
		}
		if len(selected) == quorum {
			l2 = proto.Clone(l2).(*commonv1.L2Metadata)
			l2.Votes = selected
		}
	}
	return l2, nil
}

// consensusAppClient builds the HTTPS client for the bound primary application
// appID. Like the Gateway's own deliberator, it authenticates the application
// by its Gateway-issued SPIFFE identity against the Operator identity's trust
// anchors, since application certificates carry no DNS name. It presents no
// client certificate: the application receives the envelope, not the
// harness's Gateway identity or session headers. A redirect must not send the
// envelope to a different endpoint or downgrade TLS.
func (c *Client) consensusAppClient(appID string) (*http.Client, error) {
	if appID == "" {
		return nil, constants.ErrConsensusAppNotEnrolled
	}
	tlsCfg, err := httpclient.SPIFFEServerTLSConfig(c.roots, protocol.NewWorkloadIdentity().AppSPIFFEID(appID), constants.ErrConsensusAppNotEnrolled)
	if err != nil {
		return nil, err
	}
	appClient := &http.Client{Timeout: httpclient.DefaultTimeout, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	return httpclient.WithoutRedirects(appClient, constants.ErrConsensusEndpointInvalid), nil
}
