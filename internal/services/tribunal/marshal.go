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

const marshalSystemPrompt = "You are Marshal, the risk analyst of the Tribunal. Classify the risk of running the proposed run_shell_command arguments. The executable runs directly without a shell.\n" +
	"The argument object contains one executable and its argv; pipes, &&, redirects, globs, and substitutions are not interpreted by a shell.\n" +
	"- LOW: read-only diagnostics, state inspection, log reads.\n" +
	"- MEDIUM: localized, reversible changes such as a config edit with a backup.\n" +
	"- HIGH: broad deletion, destructive overwrite, global system configuration, or unguarded service disruption.\n\n" +
	`Answer with exactly one JSON object and nothing else: {"risk": "LOW"|"MEDIUM"|"HIGH", "rationale": "<why>"}. For HIGH, also include "feedback": "<how to reach the goal more safely>".`

type marshalAnswer struct {
	Risk      string `json:"risk"`
	Rationale string `json:"rationale"`
	Feedback  string `json:"feedback"`
}

// assessRisk asks Marshal to classify the vote winner. An answer that is not
// a valid assessment classifies the command HIGH by fail-closed rule; only an
// inference failure returns an error.
func assessRisk(ctx context.Context, cfg Config, req Request, arguments models.RunShellCommandRequest) (*Risk, error) {
	user := contextBlock(req) + fmt.Sprintf("\n<proposed_arguments>\n%s\n</proposed_arguments>", arguments.CanonicalJSON())
	text, err := cfg.Inferer.Infer(ctx, Call{Stage: StageMarshal, Persona: PersonaMarshal, Round: 1, System: marshalSystemPrompt, User: user, JSONSchema: marshalJSONSchema})
	if err != nil {
		return nil, fmt.Errorf("tribunal: marshal: %w", err)
	}
	var answer marshalAnswer
	if err := decodeAuthoringAnswer(text, marshalJSONSchema, &answer); err != nil {
		return &Risk{Level: RiskHigh, Invalid: true, Rationale: "Marshal gave no valid risk assessment: " + err.Error()}, nil //nolint:nilerr // Malformed model output is a fail-closed HIGH assessment; inference failures return errors.
	}
	level := RiskLevel(strings.ToUpper(strings.TrimSpace(answer.Risk)))
	switch level {
	case RiskLow, RiskMedium, RiskHigh:
	default:
		return &Risk{Level: RiskHigh, Invalid: true, Rationale: fmt.Sprintf("Marshal gave an unknown risk level %q", answer.Risk)}, nil
	}
	return &Risk{Level: level, Rationale: strings.TrimSpace(answer.Rationale), Feedback: strings.TrimSpace(answer.Feedback)}, nil
}
