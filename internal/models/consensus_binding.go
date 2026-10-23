// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ResolveConsensusMembership normalizes the member set and quorum of a
// consensus binding for primary application appID. No members means appID
// alone. Every member is trimmed, must be non-empty and unique, and appID must
// be one of them. A zero quorum defaults to 1 only for a single member: a
// multi-member set must state its quorum so a K-of-N policy is never written
// by omission. The Gateway that admits a binding and the L5 handler that
// writes it both resolve it here.
func ResolveConsensusMembership(appID string, memberAppIDs []string, quorum int) ([]string, int, error) {
	if len(memberAppIDs) == 0 {
		memberAppIDs = []string{appID}
	}
	members := make([]string, 0, len(memberAppIDs))
	seen := make(map[string]struct{}, len(memberAppIDs))
	primaryIncluded := false
	for _, id := range memberAppIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, 0, fmt.Errorf("consensus members: empty member: %w", constants.ErrConsensusAppNotEnrolled)
		}
		if _, exists := seen[id]; exists {
			return nil, 0, constants.ErrConsensusMemberDuplicate
		}
		seen[id] = struct{}{}
		members = append(members, id)
		if id == appID {
			primaryIncluded = true
		}
	}
	if !primaryIncluded {
		return nil, 0, constants.ErrConsensusAppNotEnrolled
	}
	if quorum == 0 {
		if len(members) > 1 {
			return nil, 0, fmt.Errorf("consensus quorum must be explicit for %d members: %w", len(members), constants.ErrConsensusQuorumInvalid)
		}
		quorum = 1
	}
	if quorum < 1 || quorum > len(members) {
		return nil, 0, constants.ErrConsensusQuorumInvalid
	}
	return members, quorum, nil
}

// ConsensusBindingSpec is the provider-specific shape of one consensus binding
// request, as carried by models.PlatformEnrollmentBindConsensusRequest at
// admission and by the PLATFORM_ENROLLMENT_BIND_CONSENSUS payload at L5.
type ConsensusBindingSpec struct {
	VoteKind            string                       `json:"vote_kind,omitempty"`
	InferenceOperatorID string                       `json:"inference_operator_id,omitempty"`
	VoteThresholds      []ConsensusDecisionThreshold `json:"vote_thresholds,omitempty"`
	AllowHosted         bool                         `json:"allow_hosted,omitempty"`

	Provider     constants.ConsensusProvider
	AppID        string
	EndpointURL  string
	MemberAppIDs []string
	Quorum       int
	ModelTag     string
	ModelDigest  string
}

// ResolveConsensusBinding validates and normalizes a binding for its provider.
// It is the single owner of binding validation: the Gateway that admits a bind
// and the L5 handler that writes it both call it, so the two cannot drift.
//
// application: app_id and an HTTPS endpoint are required, membership resolves
// through ResolveConsensusMembership, and no model may be pinned.
//
// tribunal: the binding is Gateway-hosted, so the endpoint must be empty;
// app_id must be the first canonical member; members must be exactly the
// canonical Tribunal member app IDs in canonical order (empty means canonical);
// quorum defaults to TribunalConsensusDefaultQuorum and must be between
// TribunalConsensusMinQuorum and the member count; model tag and digest are
// required except for explicitly opted-in versioned hosted decisions. Per-member
// owner and trusted-signer checks stay with the caller.
func ResolveConsensusBinding(spec ConsensusBindingSpec) (ConsensusBindingSpec, error) {
	provider, err := constants.ParseConsensusProvider(string(spec.Provider))
	if err != nil {
		return ConsensusBindingSpec{}, err
	}
	out := ConsensusBindingSpec{
		Provider:    provider,
		AppID:       strings.TrimSpace(spec.AppID),
		EndpointURL: strings.TrimSpace(spec.EndpointURL),
		ModelTag:    strings.TrimSpace(spec.ModelTag),
		ModelDigest: strings.TrimSpace(spec.ModelDigest),
	}
	out.VoteKind = strings.TrimSpace(spec.VoteKind)
	out.InferenceOperatorID = strings.TrimSpace(spec.InferenceOperatorID)
	out.VoteThresholds = append([]ConsensusDecisionThreshold(nil), spec.VoteThresholds...)
	out.AllowHosted = spec.AllowHosted
	if err := validateConsensusDecisionBinding(out); err != nil {
		return ConsensusBindingSpec{}, err
	}
	if out.AppID == "" {
		return ConsensusBindingSpec{}, fmt.Errorf("%w: app_id", constants.ErrMissingRequiredField)
	}
	switch provider {
	case constants.ConsensusProviderTribunal:
		if out.EndpointURL != "" {
			return ConsensusBindingSpec{}, constants.ErrConsensusTribunalEndpointSet
		}
		if out.ModelTag == "" || (out.ModelDigest == "" && !out.AllowHosted) {
			return ConsensusBindingSpec{}, constants.ErrConsensusModelRequired
		}
		canonical := constants.TribunalConsensusMemberAppIDs()
		if out.AppID != canonical[0] {
			return ConsensusBindingSpec{}, constants.ErrConsensusTribunalMembersInvalid
		}
		if len(spec.MemberAppIDs) != 0 {
			if len(spec.MemberAppIDs) != len(canonical) {
				return ConsensusBindingSpec{}, constants.ErrConsensusTribunalMembersInvalid
			}
			for i, id := range spec.MemberAppIDs {
				if strings.TrimSpace(id) != canonical[i] {
					return ConsensusBindingSpec{}, constants.ErrConsensusTribunalMembersInvalid
				}
			}
		}
		quorum := spec.Quorum
		if quorum == 0 {
			quorum = constants.TribunalConsensusDefaultQuorum
		}
		if quorum < constants.TribunalConsensusMinQuorum || quorum > len(canonical) {
			return ConsensusBindingSpec{}, constants.ErrConsensusQuorumInvalid
		}
		out.MemberAppIDs = canonical
		out.Quorum = quorum
		return out, nil
	default:
		if out.EndpointURL == "" {
			return ConsensusBindingSpec{}, fmt.Errorf("%w: endpoint_url", constants.ErrMissingRequiredField)
		}
		if err := ValidateConsensusEndpoint(out.EndpointURL); err != nil {
			return ConsensusBindingSpec{}, fmt.Errorf("consensus endpoint: %w", err)
		}
		if out.ModelTag != "" || out.ModelDigest != "" {
			return ConsensusBindingSpec{}, constants.ErrConsensusModelUnexpected
		}
		members, quorum, err := ResolveConsensusMembership(out.AppID, spec.MemberAppIDs, spec.Quorum)
		if err != nil {
			return ConsensusBindingSpec{}, err
		}
		out.MemberAppIDs = members
		out.Quorum = quorum
		return out, nil
	}
}
