// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// auditorMaxAttempts bounds the Auditor's answers before the deliberation
// fails closed.
const auditorMaxAttempts = 2

const auditorSystemPrompt = "You are the Auditor, the Tribunal's final technical gate. Check the selected run_shell_command arguments for correctness against the request, safe argv construction, error handling, and portability. The executable runs directly without a shell; no pipes, &&, redirects, globs, or substitutions.\n" +
	"- APPROVE: the selected arguments (cluster_a) are correct as written.\n" +
	"- REVISE: fix a flaw; put the complete corrected argument object in revised_arguments.\n" +
	"- SWAP: another cluster is better; name it in swap_to_cluster. Not allowed when the panel was unanimous.\n\n" +
	`Answer with exactly one JSON object and nothing else: {"verdict": "APPROVE"|"REVISE"|"SWAP", "swap_to_cluster": "<cluster id for SWAP>", "revised_arguments": {"command": "...", "args": [...]}, "rationale": "<why>"}`

type auditorAnswer struct {
	Verdict          string `json:"verdict"`
	SwapToCluster    string `json:"swap_to_cluster"`
	RevisedArguments *struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"revised_arguments"`
	Rationale string `json:"rationale"`
}

// auditClusters labels the winner cluster_a and every other cluster after it
// in vote order, as the Auditor sees them.
func auditClusters(v Vote) []Cluster {
	clusters := make([]Cluster, 0, len(v.Clusters))
	for _, cluster := range v.Clusters {
		if cluster.Arguments.CanonicalJSON() == v.Winner.CanonicalJSON() {
			clusters = append(clusters, cluster)
		}
	}
	for _, cluster := range v.Clusters {
		if cluster.Arguments.CanonicalJSON() != v.Winner.CanonicalJSON() {
			clusters = append(clusters, cluster)
		}
	}
	for i := range clusters {
		clusters[i].ID = clusterID(i)
	}
	return clusters
}

func auditorPrompt(req Request, v Vote, clusters []Cluster) string {
	var b strings.Builder
	b.WriteString(contextBlock(req))
	mode := "MAJORITY: the panel split; APPROVE, REVISE and SWAP are allowed."
	if v.Unanimous() {
		mode = "UNANIMOUS: every member proposed cluster_a; APPROVE and REVISE are allowed."
	}
	fmt.Fprintf(&b, "\nVoting mode: %s\n\n<clusters>\n", mode)
	for _, cluster := range clusters {
		fmt.Fprintf(&b, "[%s] (support: %d)\n%s\n", cluster.ID, len(cluster.Members), cluster.Arguments.CanonicalJSON())
	}
	b.WriteString("</clusters>\n\nGive your verdict on cluster_a.")
	return b.String()
}

// audit asks the Auditor for a verdict on the vote winner. The verdict must
// fit the voting mode: SWAP names a known cluster other than the winner and
// is not allowed for a unanimous panel, and REVISE carries a non-empty
// command. After auditorMaxAttempts invalid answers the audit fails closed
// with Failure set; only an inference failure returns an error.
func audit(ctx context.Context, cfg Config, req Request, v Vote) (*Audit, error) {
	clusters := auditClusters(v)
	prompt := auditorPrompt(req, v, clusters)
	var failure string
	for attempt := uint32(1); attempt <= auditorMaxAttempts; attempt++ {
		text, err := cfg.Inferer.Infer(ctx, Call{Stage: StageAudit, Persona: PersonaAuditor, Round: attempt, System: auditorSystemPrompt, User: prompt})
		if err != nil {
			return nil, fmt.Errorf("tribunal: auditor attempt %d: %w", attempt, err)
		}
		result, reason := auditVerdict(text, v, clusters, req)
		if result != nil {
			return result, nil
		}
		failure = reason
	}
	return &Audit{Verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_UNSPECIFIED, Failure: failure}, nil
}

// auditVerdict validates one Auditor answer, returning the reason it is
// invalid when it does not fit the vote.
func auditVerdict(text string, v Vote, clusters []Cluster, req Request) (*Audit, string) {
	var answer auditorAnswer
	if err := decodeAnswer(text, &answer); err != nil {
		return nil, err.Error()
	}
	rationale := strings.TrimSpace(answer.Rationale)
	swap := strings.ToLower(strings.TrimSpace(answer.SwapToCluster))
	var revised models.RunShellCommandRequest
	if answer.RevisedArguments != nil && models.ValidExecutableName(answer.RevisedArguments.Command) {
		revised = models.RunShellCommandRequest{Command: answer.RevisedArguments.Command, Args: answer.RevisedArguments.Args, Timeout: req.TimeoutSeconds, WorkingDir: req.WorkingDirectory}
	}
	hasRevision := answer.RevisedArguments != nil
	switch strings.ToUpper(strings.TrimSpace(answer.Verdict)) {
	case "APPROVE":
		if swap != "" || hasRevision {
			return nil, "APPROVE carries a swap or revision"
		}
		return &Audit{Verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, Arguments: v.Winner, Rationale: rationale}, ""
	case "REVISE":
		if !hasRevision || revised.Command == "" {
			return nil, "REVISE carries no valid revised_arguments"
		}
		if swap != "" {
			return nil, "REVISE carries a swap_to_cluster"
		}
		return &Audit{Verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_REVISE, Arguments: revised, Rationale: rationale}, ""
	case "SWAP":
		if v.Unanimous() {
			return nil, "SWAP is not allowed for a unanimous panel"
		}
		if hasRevision {
			return nil, "SWAP carries revised_arguments"
		}
		for _, cluster := range clusters[1:] {
			if cluster.ID == swap {
				return &Audit{Verdict: evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_SWAP, Arguments: cluster.Arguments, Rationale: rationale}, ""
			}
		}
		return nil, fmt.Sprintf("SWAP names no alternative cluster %q", answer.SwapToCluster)
	default:
		return nil, fmt.Sprintf("unknown verdict %q", answer.Verdict)
	}
}
