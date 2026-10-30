// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// interrogationOpenTag opens an Interrogation Protocol block. A response that
// carries it is an interrogation attempt and is graded as one.
const interrogationOpenTag = "<interrogation>"

var (
	interrogationBlock    = regexp.MustCompile(`(?si)^<interrogation>\s*(.*?)\s*</interrogation>$`)
	interrogationQuestion = regexp.MustCompile(`^\d+[.)]\s*(\S.*\?)$`)
	// interrogationBinaryMarker is a leading "YES/NO" answer-type marker. It
	// echoes the protocol's own wording (I1), so it is not part of the question.
	interrogationBinaryMarker   = regexp.MustCompile(`(?i)^yes\s*/\s*no\s*[-–—:]?\s*`)
	interrogationQuestionV4     = regexp.MustCompile(`^\d+[.)]\s*(\S.*\?)\s*(?:\((?i:yes\s*/\s*no)\))?$`)
	interrogationBinaryMarkerV4 = regexp.MustCompile(`(?i)^yes(?:\s*/\s*|\s+or\s+)no\s*[-–—:,]?\s*`)
)

// binaryOpeners are the words a yes/no question starts with. A question that
// opens with anything else ("which", "what", "how", ...) asks for an answer
// other than yes or no.
var binaryOpeners = []string{
	"is", "are", "was", "were", "do", "does", "did", "can", "could", "has", "have", "had",
	"will", "would", "should", "shall", "may", "might", "must",
}

// clarificationSignal is a reasoner's whole response asking the Interrogator
// to clarify; its body names the missing context.
var clarificationSignal = regexp.MustCompile(`(?s)^<clarification_needed>\s*(.*?)\s*</clarification_needed>$`)

// ParseClarificationSignal reports whether a reasoner's response is exactly a
// clarification signal, and the missing context it names.
func ParseClarificationSignal(text string) (missing string, signalled bool) {
	match := clarificationSignal.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return "", false
	}
	return match[1], true
}

// AgentInterrogation is one response under the Interrogation Protocol
// (the agentpersona interrogation protocol I1-I4): exactly three numbered, strictly yes/no
// questions that are the model's entire response.
type AgentInterrogation struct {
	Questions [3]string
}

// ParseInterrogation reads a model response as an Interrogation Protocol
// attempt. attempted is false when the response carries no <interrogation>
// tag: it is an ordinary answer. An attempt that breaks the protocol is still
// attempted, and err wraps the ErrEvaluationInterrogation* sentinel for the
// first rule it breaks. The executor and the grader read interrogations only
// through this parser.
func ParseInterrogation(text string) (interrogation AgentInterrogation, attempted bool, err error) {
	return parseInterrogation(text, ScenarioGoldCriteria{})
}

// ParseInterrogationForGold applies the protocol frozen in scenario gold.
// Older contracts retain the lexical "or" rule; v3 accepts coordinated noun
// phrases while still rejecting common explicit action choices. V4 also accepts
// explicit binary answer markers before or after a question.
func ParseInterrogationForGold(text string, gold ScenarioGoldCriteria) (AgentInterrogation, bool, error) {
	return parseInterrogation(text, gold)
}

func parseInterrogation(text string, gold ScenarioGoldCriteria) (interrogation AgentInterrogation, attempted bool, err error) {
	trimmed := strings.TrimSpace(text)
	if !strings.Contains(strings.ToLower(trimmed), interrogationOpenTag) {
		return AgentInterrogation{}, false, nil
	}
	match := interrogationBlock.FindStringSubmatch(trimmed)
	if match == nil {
		return AgentInterrogation{}, true, constants.ErrEvaluationInterrogationNotWhole
	}
	var lines []string
	for _, line := range strings.Split(match[1], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != len(interrogation.Questions) {
		return AgentInterrogation{}, true, fmt.Errorf("%w: got %d", constants.ErrEvaluationInterrogationCount, len(lines))
	}
	for i, line := range lines {
		questionPattern, markerPattern := interrogationQuestion, interrogationBinaryMarker
		if gold.GradingContract == ScenarioGradingContractV4 {
			questionPattern, markerPattern = interrogationQuestionV4, interrogationBinaryMarkerV4
		}
		numbered := questionPattern.FindStringSubmatch(line)
		if numbered == nil {
			return AgentInterrogation{}, true, fmt.Errorf("%w: line %d", constants.ErrEvaluationInterrogationNotNumbered, i+1)
		}
		question := markerPattern.ReplaceAllString(numbered[1], "")
		if !binaryQuestion(question, gold.closedInterrogationContract()) {
			return AgentInterrogation{}, true, fmt.Errorf("%w: question %d", constants.ErrEvaluationInterrogationNotBinary, i+1)
		}
		interrogation.Questions[i] = question
	}
	return interrogation, true, nil
}

func endedInInterrogationForGold(finalOutput string, gold ScenarioGoldCriteria) bool {
	_, attempted, _ := ParseInterrogationForGold(finalOutput, gold)
	return attempted
}

// binaryQuestion reports whether question is closed: it opens with a binary
// auxiliary. A disjunction between terms does not by itself make a question
// open-ended (for example, "a previous request or operation"). Reject the
// common form that explicitly asks the user to choose between actions.
func binaryQuestion(question string, closedQuestionContract bool) bool {
	words := strings.Fields(strings.ToLower(question))
	if len(words) == 0 || !containsWord(binaryOpeners, strings.Trim(words[0], "\"'`*_,.:;")) {
		return false
	}
	if !closedQuestionContract {
		for _, word := range words {
			if strings.Trim(word, "\"'`*_,.:;?()") == "or" {
				return false
			}
		}
		return true
	}
	lower := strings.ToLower(question)
	if strings.Contains(lower, "would you like") || strings.Contains(lower, "do you want") {
		return !strings.Contains(lower, " or ")
	}
	for i, word := range words {
		if strings.Trim(word, "\"'`*_,.:;?()") != "or" || i+1 == len(words) {
			continue
		}
		next := strings.Trim(words[i+1], "\"'`*_,.:;?()")
		if containsWord(binaryOpeners, next) || containsWord([]string{"wait", "fetch", "run", "get", "provide", "use", "choose", "select"}, next) {
			return false
		}
	}
	return true
}

func containsWord(words []string, word string) bool {
	for _, candidate := range words {
		if candidate == word {
			return true
		}
	}
	return false
}
