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
)

const (
	defaultOS = "linux"
)

// memberSystemPrompt returns one panel member's sealed generation instructions.
func memberSystemPrompt(member Member) string {
	base := "You are a member of the Tribunal, a sealed panel that authors one run_shell_command argument set for the request. The executable runs directly without a shell.\n" +
		"Answer with exactly one JSON object containing command and args, with no explanation or markdown. command must be one executable name with no whitespace; args is an array of argv strings. No pipes, chaining, redirects, globs, or substitutions.\n\n"
	return base + memberStance(member)
}

// memberStance is one panel member's persona stance, shared by command
// generation and the L2 consensus vote.
func memberStance(member Member) string {
	switch member {
	case MemberAxiom:
		return "Role: Axiom (the Composer). Prefer a clean, coherent argv with canonical flag order."
	case MemberConcord:
		return "Role: Concord (the Guardian). Prefer read-only queries and safe, explicit argv values."
	case MemberVariance:
		return "Role: Variance (the Exhaustive). Handle spaces as individual argv values, explicit locales, and robust matching."
	case MemberPragma:
		return "Role: Pragma (the Conventional). Prefer modern, idiomatic utilities (ss over netstat, journalctl on systemd) and standard flags."
	case MemberNemesis:
		return "Role: Nemesis (the Calibrated Adversary). Probe the request for edge cases and ambiguity; emit the standard argv when it is the right one."
	default:
		return ""
	}
}

// contextBlock renders the execution context and the caller's request.
func contextBlock(req Request) string {
	var b strings.Builder
	osName := req.OS
	if osName == "" {
		osName = defaultOS
	}
	fmt.Fprintf(&b, "<system_context>\nOS: %s\nExecution: direct executable argv (no shell)\n", osName)
	if req.WorkingDirectory != "" {
		fmt.Fprintf(&b, "Working directory: %s\n", req.WorkingDirectory)
	}
	b.WriteString("</system_context>\n\n")
	if req.Guidelines != "" {
		fmt.Fprintf(&b, "<guidelines>\n%s\n</guidelines>\n\n", req.Guidelines)
	}
	fmt.Fprintf(&b, "<request>\n%s\n</request>\n", req.Request)
	return b.String()
}

// generationPrompt is one round's user prompt. Round 2 shows the round 1
// clusters anonymized with their support so members can converge.
func generationPrompt(req Request, prior []Cluster) string {
	var b strings.Builder
	b.WriteString(contextBlock(req))
	if len(prior) > 0 {
		b.WriteString("\nThe panel did not agree in round 1. Its anonymized proposals were:\n")
		for _, cluster := range prior {
			fmt.Fprintf(&b, "[%s] (support: %d)\n%s\n", cluster.ID, len(cluster.Members), cluster.Arguments.CanonicalJSON())
		}
		b.WriteString("\nWrite the single best run_shell_command argument object for the request; you may adopt a proposal or improve it.\n")
	}
	b.WriteString("\nRespond now as exactly {\"command\": \"...\", \"args\": [...]}. The command executes directly without a shell.")
	return b.String()
}

// generate runs one sealed generation round: every member answers the same
// prompt without seeing the others' answers.
func generate(ctx context.Context, cfg Config, req Request, round uint32, prior []Cluster) ([]Candidate, error) {
	prompt := generationPrompt(req, prior)
	candidates := make([]Candidate, 0, len(AllMembers))
	for _, member := range AllMembers {
		text, err := cfg.Inferer.Infer(ctx, Call{Stage: StageGenerate, Persona: string(member), Round: round, System: memberSystemPrompt(member), User: prompt})
		if err != nil {
			return nil, fmt.Errorf("tribunal: generate round %d %s: %w", round, member, err)
		}
		var answer struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		candidate := Candidate{Member: member, Round: round}
		if decodeAnswer(text, &answer) == nil && models.ValidExecutableName(answer.Command) {
			candidate.Arguments = models.RunShellCommandRequest{
				Command: answer.Command, Args: answer.Args,
				Timeout: req.TimeoutSeconds, WorkingDir: req.WorkingDirectory,
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}
