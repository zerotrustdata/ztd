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
