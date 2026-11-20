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

type marshalAnswer struct {
	Risk      string `json:"risk"`
	Rationale string `json:"rationale"`
	Feedback  string `json:"feedback"`
}

// assessRisk asks Marshal to classify the vote winner. An answer that is not
// a valid assessment, including a rationale or non-empty feedback that is not
// authored text (authoredText), classifies the command HIGH by fail-closed
// rule; only an inference failure returns an error.
func assessRisk(ctx context.Context, cfg Config, req Request, arguments models.RunShellCommandRequest) (*Risk, error) {
	system, err := personaSystemPrompt(cfg.Personas, PersonaMarshal)
	if err != nil {
		return nil, err
	}
	user := contextBlock(req) + fmt.Sprintf("\n<proposed_arguments>\n%s\n</proposed_arguments>", arguments.CanonicalJSON())
	text, err := cfg.Inferer.Infer(ctx, Call{Stage: StageMarshal, Persona: PersonaMarshal, Round: 1, System: system, User: user, JSONSchema: marshalJSONSchema})
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
	rationale, err := authoredText("rationale", answer.Rationale)
	if err != nil {
		return &Risk{Level: RiskHigh, Invalid: true, Rationale: "Marshal gave no valid risk assessment: " + err.Error()}, nil //nolint:nilerr // An unwritten rationale is a fail-closed HIGH assessment.
	}
	feedback := strings.TrimSpace(answer.Feedback)
	if feedback != "" {
		if feedback, err = authoredText("feedback", feedback); err != nil {
			return &Risk{Level: RiskHigh, Invalid: true, Rationale: "Marshal gave no valid risk assessment: " + err.Error()}, nil //nolint:nilerr // An unwritten feedback is a fail-closed HIGH assessment.
		}
	}
	return &Risk{Level: level, Rationale: rationale, Feedback: feedback}, nil
}
