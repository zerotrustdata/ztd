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
)

const (
	defaultOS    = "linux"
	defaultShell = "bash"
)

// memberSystemPrompt returns one panel member's sealed generation instructions.
func memberSystemPrompt(member Member) string {
	base := "You are a member of the Tribunal, a sealed panel that writes one shell command for a plain-language request.\n" +
		"Answer with EXACTLY one shell command and nothing else: no explanation, no markdown fences, no prose.\n\n"
	switch member {
	case MemberAxiom:
		return base + "Role: Axiom (the Composer). Prefer clean, coherent pipelines with canonical flag order."
	case MemberConcord:
		return base + "Role: Concord (the Guardian). Prefer read-only queries, safe quoting across pipes, and && chaining."
	case MemberVariance:
		return base + "Role: Variance (the Exhaustive). Handle spaces and null delimiters (-print0, xargs -0), explicit locales, and robust matching."
	case MemberPragma:
		return base + "Role: Pragma (the Conventional). Prefer modern, idiomatic utilities (ss over netstat, journalctl on systemd) and standard flags."
	case MemberNemesis:
		return base + "Role: Nemesis (the Calibrated Adversary). Probe the request for edge cases and ambiguity; emit the standard command when it is the right one."
	default:
		return base
	}
}

// contextBlock renders the execution context and the caller's request.
func contextBlock(req Request) string {
	var b strings.Builder
	osName, shell := req.OS, req.Shell
	if osName == "" {
		osName = defaultOS
	}
	if shell == "" {
		shell = defaultShell
	}
	fmt.Fprintf(&b, "<system_context>\nOS: %s\nShell: %s\n", osName, shell)
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
			fmt.Fprintf(&b, "[%s] (support: %d)\n%s\n", cluster.ID, len(cluster.Members), cluster.Command)
		}
		b.WriteString("\nWrite the single best command for the request; you may adopt a proposal or write a better one.\n")
	}
	b.WriteString("\nRespond now with the exact shell command and nothing else.")
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
		candidates = append(candidates, Candidate{Member: member, Round: round, Command: NormaliseCommand(text)})
	}
	return candidates, nil
}
