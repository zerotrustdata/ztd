// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/agentpersona"
)

const (
	defaultOS = "linux"
)

// personaSystemPrompt renders one stage's registry persona.
func personaSystemPrompt(personas *agentpersona.Registry, id string) (string, error) {
	if personas == nil {
		return "", fmt.Errorf("tribunal: personas: %w", constants.ErrMissingRequiredField)
	}
	persona, err := personas.Get(id)
	if err != nil {
		return "", fmt.Errorf("tribunal: persona %s: %w", id, err)
	}
	return persona.SystemPrompt(), nil
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

// authoredArguments is the part of a run_shell_command argument set a model
// authors; orchestration composes timeout and working_dir.
type authoredArguments struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// authoredJSON renders the authored part of arguments in the shape a model
// answers with.
func authoredJSON(arguments models.RunShellCommandRequest) string {
	args := arguments.Args
	if args == nil {
		args = []string{}
	}
	// A fixed struct of strings always marshals.
	encoded, _ := json.Marshal(authoredArguments{Command: arguments.Command, Args: args})
	return string(encoded)
}

// generationPrompt is one round's user prompt. Round 2 shows the round 1
// clusters anonymized with their support so members can converge.
func generationPrompt(req Request, prior []Cluster) string {
	var b strings.Builder
	b.WriteString(contextBlock(req))
	if len(prior) > 0 {
		b.WriteString("\nThe panel did not agree in round 1. Its anonymized proposals were:\n")
		for _, cluster := range prior {
			fmt.Fprintf(&b, "[%s] (support: %d)\n%s\n", cluster.ID, len(cluster.Members), authoredJSON(cluster.Arguments))
		}
		b.WriteString("\nWrite the single best run_shell_command argument object for the request; you may adopt a proposal or improve it.\n")
	}
	b.WriteString("\nRespond now as exactly {\"command\": \"...\", \"args\": [...]}. The command executes directly without a shell.")
	return b.String()
}

// generate runs one sealed generation round. Each member sees only its own
// validation and L1 failures and retries until allowed or exhausted. Only allowed arguments
// enter the vote; invalid answers and exhausted members abstain.
func generate(ctx context.Context, cfg Config, req Request, round uint32, prior []Cluster) ([]Candidate, error) {
	prompt := generationPrompt(req, prior)
	candidates := make([]Candidate, 0, len(AllMembers))
	for _, member := range AllMembers {
		candidate := Candidate{Member: member, Round: round}
		memberPrompt := prompt
		system, err := personaSystemPrompt(cfg.Personas, string(member))
		if err != nil {
			return nil, err
		}
		for attempt := 0; attempt < candidateMaxAttempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("tribunal: generate round %d %s: %w", round, member, err)
			}
			text, err := cfg.Inferer.Infer(ctx, Call{
				Stage: StageGenerate, Persona: string(member), Round: round,
				System: system, User: memberPrompt, JSONSchema: generationJSONSchema,
			})
			if err != nil {
				return nil, fmt.Errorf("tribunal: generate round %d %s: %w", round, member, err)
			}
			var answer authoredArguments
			err = decodeAuthoringAnswer(text, generationJSONSchema, &answer)
			if err == nil && !models.ValidExecutableName(answer.Command) {
				err = fmt.Errorf("command must be one executable name without whitespace: %w", constants.ErrTribunalAnswerInvalid)
			}
			if err != nil {
				memberPrompt += fmt.Sprintf("\n\nYour previous answer was invalid: %s\nReturn a fresh command and args object for the original request that satisfies the response schema.", err)
				continue
			}
			arguments := models.RunShellCommandRequest{
				Command: answer.Command, Args: answer.Args,
				Timeout: req.TimeoutSeconds, WorkingDir: req.WorkingDirectory,
			}
			if feedback := doctrineFeedback(cfg.Doctrine, arguments); feedback != "" {
				memberPrompt += "\n\n" + feedback
				continue
			}
			candidate.Arguments = arguments
			break
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}
