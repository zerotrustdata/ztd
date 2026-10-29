// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package judge

import (
	"fmt"
	"strings"
)

// Objective is the typed context a criterion is judged against: the request the
// agent answered and everything it was shown or observed while answering. It
// never carries the expected behavior or any criterion other than the one
// being judged.
type Objective struct {
	// Task is the request the final reply answers.
	Task string
	// Conversation is the prior conversation, in order, before the request.
	Conversation []ObjectiveTurn
	// Evidence is content supplied with the request.
	Evidence []ObjectiveEvidence
	// Records are governed calls the agent was shown or made, in order.
	Records []ObjectiveRecord
}

// ObjectiveTurn is one prior conversation message.
type ObjectiveTurn struct {
	Sender string
	Text   string
}

// ObjectiveEvidence is one labelled block of content supplied with the request.
type ObjectiveEvidence struct {
	Label   string
	Source  string
	Content string
}

// ObjectiveRecordSource says how a governed call reached the agent.
type ObjectiveRecordSource string

const (
	// ObjectiveRecordPlatform is a call the platform recorded before the
	// request and showed the agent as a platform record.
	ObjectiveRecordPlatform ObjectiveRecordSource = "platform record shown before the request"
	// ObjectiveRecordAgent is a call the agent made while answering.
	ObjectiveRecordAgent ObjectiveRecordSource = "tool call made by the agent"
)

// ObjectiveRecord is one governed call and its recorded result.
type ObjectiveRecord struct {
	Source    ObjectiveRecordSource
	Tool      string
	Arguments string
	Outcome   string
	Result    string
}

// render is the one text form of an Objective. Sections appear in a fixed
// order and only when they hold something, so equal objectives render equally.
func (o Objective) render() string {
	var b strings.Builder
	b.WriteString(o.Task)
	if len(o.Conversation) > 0 {
		b.WriteString("\n\nPrior conversation:")
		for _, turn := range o.Conversation {
			fmt.Fprintf(&b, "\n[%s] %s", turn.Sender, turn.Text)
		}
	}
	for _, evidence := range o.Evidence {
		fmt.Fprintf(&b, "\n\nEvidence (%s, %s):\n%s", evidence.Label, evidence.Source, evidence.Content)
	}
	for i, record := range o.Records {
		fmt.Fprintf(&b, "\n\nRecord %d (%s): %s, outcome %s\nArguments: %s\nResult: %s", i+1, record.Source, record.Tool, record.Outcome, record.Arguments, record.Result)
	}
	return b.String()
}

func objectiveCriterionUserMessage(criterion Criterion, objective Objective, reply string) string {
	return criterionUserMessage(criterion, objective.render(), reply)
}
