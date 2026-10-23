// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"fmt"
	"slices"

	"github.com/zerotrustdata/ztd/internal/models"
)

// MinConsensus is the fewest members that must propose one command for it to
// win the vote.
const MinConsensus = 2

// clusterID is the anonymized label of the i-th cluster.
func clusterID(i int) string {
	return fmt.Sprintf("cluster_%c", 'a'+i)
}

// nemesisProposal returns Nemesis's proposed arguments in one round; empty when
// Nemesis abstained.
func nemesisProposal(candidates []Candidate) models.RunShellCommandRequest {
	for _, candidate := range candidates {
		if candidate.Member == MemberNemesis {
			return candidate.Arguments
		}
	}
	return models.RunShellCommandRequest{}
}

// clusterCandidates groups one round's eligible candidates by canonical typed
// arguments in first-proposal order. A candidate matching excluded, Nemesis's
// proposal, is not tallied, and a member that proposed no command joins no
// cluster.
func clusterCandidates(candidates []Candidate, excluded models.RunShellCommandRequest) []Cluster {
	var clusters []Cluster
	for _, candidate := range candidates {
		if candidate.Arguments.Command == "" || candidate.excludedBy(excluded) {
			continue
		}
		key := candidate.Arguments.CanonicalJSON()
		i := slices.IndexFunc(clusters, func(c Cluster) bool { return c.Arguments.CanonicalJSON() == key })
		if i < 0 {
			clusters = append(clusters, Cluster{ID: clusterID(len(clusters)), Arguments: candidate.Arguments})
			i = len(clusters) - 1
		}
		clusters[i].Members = append(clusters[i].Members, candidate.Member)
	}
	return clusters
}

// excludedBy reports whether c proposed excluded, Nemesis's proposal.
func (c Candidate) excludedBy(excluded models.RunShellCommandRequest) bool {
	return excluded.Command != "" && c.Arguments.CanonicalJSON() == excluded.CanonicalJSON()
}

// vote runs the uniform-weight vote over one round. Every eligible member is
// one vote for its own proposal; the largest cluster wins when it holds at
// least MinConsensus members. A tie at the top is broken by the shortest
// command when the tied commands differ in length; any tie left has no winner.
// Nemesis's proposal and every matching proposal are recorded in Excluded and
// never tallied.
func vote(round uint32, candidates []Candidate) Vote {
	excluded := nemesisProposal(candidates)
	result := Vote{Round: round, Clusters: clusterCandidates(candidates, excluded), Excluded: excluded}
	top := 0
	for _, cluster := range result.Clusters {
		top = max(top, len(cluster.Members))
	}
	if top < MinConsensus {
		return result
	}
	var tied []Cluster
	for _, cluster := range result.Clusters {
		if len(cluster.Members) == top {
			tied = append(tied, cluster)
		}
	}
	if len(tied) == 1 {
		result.Winner = tied[0].Arguments
		return result
	}
	shortest := slices.MinFunc(tied, func(a, b Cluster) int { return len(a.Arguments.CommandLine()) - len(b.Arguments.CommandLine()) })
	if slices.ContainsFunc(tied, func(c Cluster) bool { return len(c.Arguments.CommandLine()) != len(shortest.Arguments.CommandLine()) }) {
		result.Winner = shortest.Arguments
		return result
	}
	return result
}
