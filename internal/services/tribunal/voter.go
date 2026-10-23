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
)

// MinConsensus is the fewest members that must propose one command for it to
// win the vote.
const MinConsensus = 2

// clusterID is the anonymized label of the i-th cluster.
func clusterID(i int) string {
	return fmt.Sprintf("cluster_%c", 'a'+i)
}

// clusterCandidates groups one round's candidates by canonical typed arguments in
// first-proposal order. A member that proposed no command joins no cluster.
func clusterCandidates(candidates []Candidate) []Cluster {
	var clusters []Cluster
	for _, candidate := range candidates {
		if candidate.Arguments.Command == "" {
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

// vote runs the uniform-weight vote over one round. Every member is one vote
// for its own proposal; the largest cluster wins when it holds at least
// MinConsensus members. A tie at the top is broken by the shortest command
// when the tied commands differ in length, then by a cluster without Nemesis
// over clusters with it; any tie left has no winner.
func vote(round uint32, candidates []Candidate) Vote {
	result := Vote{Round: round, Clusters: clusterCandidates(candidates)}
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
	withNemesis := func(c Cluster) bool { return slices.Contains(c.Members, MemberNemesis) }
	if i := slices.IndexFunc(tied, func(c Cluster) bool { return !withNemesis(c) }); i >= 0 && slices.ContainsFunc(tied, withNemesis) {
		result.Winner = tied[i].Arguments
	}
	return result
}
