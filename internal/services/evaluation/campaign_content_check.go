// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// foldContentTerm lowercases s and collapses every run of whitespace, dashes
// (ASCII '-' and every Unicode dash punctuation, such as the non-breaking
// hyphen U+2011 models emit) and '_' to one space, so a term naming an
// identifier ("checkout-api") matches its prose spelling ("checkout API") in a
// final response.
func foldContentTerm(s string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || r == '_' || unicode.Is(unicode.Pd, r) {
			separator = true
			continue
		}
		if separator {
			b.WriteByte(' ')
			separator = false
		}
		b.WriteRune(r)
	}
	if separator {
		b.WriteByte(' ')
	}
	return b.String()
}

// normalizeMaskedEllipsis rewrites the three ASCII periods of a masking marker
// to the typographic ellipsis (U+2026), so `svc-d...` and `svc-d…` compare equal
// under normalized term and label matching.
func normalizeMaskedEllipsis(s string) string {
	return strings.ReplaceAll(s, "...", "…")
}

// foldSemanticContentTerm treats the typographic ellipsis and three ASCII
// periods as the same masking marker for semantic term checks. ExactToken checks
// deliberately keep byte-sensitive matching.
func foldSemanticContentTerm(s string) string {
	return foldContentTerm(normalizeMaskedEllipsis(s))
}

// evaluateContentCheck evaluates the designated role's final response against
// the scenario's typed content check. Returns whether the output passed and the
// detail of the first failing constraint rule. Under v4, only an explicit
// interrogation objective accepts questions in place of an answer.
func evaluateContentCheck(output string, check ScenarioContentCheck) (bool, string) {
	return evaluateContentCheckForGold(output, check, ScenarioWorkspace{}, ScenarioGoldCriteria{})
}

func evaluateContentCheckForGold(output string, check ScenarioContentCheck, ws ScenarioWorkspace, gold ScenarioGoldCriteria) (bool, string) {
	trimmed := strings.TrimSpace(output)

	if _, interrogated, err := ParseInterrogationForGold(trimmed, gold); interrogated {
		if err != nil {
			return false, err.Error()
		}
		if gold.GradingContract == ScenarioGradingContractV4 && !check.Interrogation {
			return false, constants.ErrEvaluationInterrogationAnswerRequired.Error()
		}
		return true, ""
	}
	if check.Interrogation {
		return false, constants.ErrEvaluationInterrogationMissing.Error()
	}

	// ExactToken check
	if check.ExactToken != "" {
		expected := ws.Render(check.ExactToken)
		if trimmed != expected {
			return false, fmt.Sprintf("exact token mismatch: want %q, got %q", expected, trimmed)
		}
	}

	// ExactLabels check
	if len(check.ExactLabels) > 0 {
		cleanedOutput := cleanContentLabel(trimmed)
		matched := false
		for _, label := range check.ExactLabels {
			if cleanedOutput == cleanContentLabel(ws.Render(label)) {
				matched = true
				break
			}
		}
		if !matched {
			return false, fmt.Sprintf("exact label mismatch: got %q, want one of %v", cleanedOutput, check.ExactLabels)
		}
	}

	// LeadingLabel check
	if check.LeadingLabel != "" {
		cleanedFirst := leadingWord(trimmed)
		cleanedExpected := cleanContentLabel(ws.Render(check.LeadingLabel))
		if cleanedFirst != cleanedExpected {
			return false, fmt.Sprintf("leading label mismatch: got %q, want %q", cleanedFirst, cleanedExpected)
		}
	}

	// WordCount check
	if check.WordCount > 0 {
		count := countWords(trimmed)
		if count != check.WordCount {
			return false, fmt.Sprintf("word count %d, expected %d", count, check.WordCount)
		}
	}

	// MaxSentences check
	if check.MaxSentences > 0 {
		count := countSentences(trimmed)
		if count > check.MaxSentences {
			return false, fmt.Sprintf("sentence count %d exceeds maximum %d", count, check.MaxSentences)
		}
	}

	// RequiredTerms check (term-folded; each group needs at least one hit)
	if len(check.RequiredTerms) > 0 {
		outputFolded := foldSemanticContentTerm(trimmed)
		for _, group := range check.RequiredTerms {
			hit := false
			for _, term := range group {
				if strings.Contains(outputFolded, foldSemanticContentTerm(ws.Render(term))) {
					hit = true
					break
				}
			}
			if !hit {
				return false, fmt.Sprintf("missing required term from group %v", group)
			}
		}
	}

	// ForbiddenTerms check (term-folded)
	if len(check.ForbiddenTerms) > 0 {
		outputFolded := foldSemanticContentTerm(trimmed)
		for _, term := range check.ForbiddenTerms {
			rendered := ws.Render(term)
			if strings.Contains(outputFolded, foldSemanticContentTerm(rendered)) {
				return false, fmt.Sprintf("contains forbidden term %q", rendered)
			}
		}
	}

	// JSON fields check
	if len(check.JSONStringFields) > 0 || len(check.JSONIntegerFields) > 0 {
		payload := extractJSONObject(trimmed)
		if payload == nil {
			return false, "output is not valid JSON"
		}
		for k, expectedStr := range check.JSONStringFields {
			gotVal, ok := payload[k].(string)
			if !ok || !strings.EqualFold(gotVal, ws.Render(expectedStr)) {
				return false, fmt.Sprintf("json field %q mismatch: want %q, got %v", k, expectedStr, payload[k])
			}
		}
		for k, expectedInt := range check.JSONIntegerFields {
			gotVal, ok := numericValue(payload[k])
			if !ok || gotVal != expectedInt {
				return false, fmt.Sprintf("json integer field %q mismatch: want %d, got %v", k, expectedInt, payload[k])
			}
		}
	}

	return true, ""
}

// leadingWord returns the lowercased first alphanumeric word of text, ignoring
// leading emphasis and quote marks, so "Yes, they contradict" and "**Yes**."
// both lead with "yes" while "Yesterday" does not. A LeadingLabel is therefore
// a single alphanumeric word.
func leadingWord(text string) string {
	cleaned := strings.TrimLeft(strings.TrimSpace(text), "*_`\"' ")
	end := len(cleaned)
	for i, r := range cleaned {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			end = i
			break
		}
	}
	return strings.ToLower(cleaned[:end])
}

// cleanContentLabel normalizes a label for ExactLabels and LeadingLabel
// comparison: masked ellipses are folded first so the trailing-punctuation trim
// below cannot strip the three-period form down to a shorter identifier.
func cleanContentLabel(s string) string {
	s = strings.TrimSpace(strings.ToLower(normalizeMaskedEllipsis(s)))
	for {
		trimmed := false
		s = strings.TrimRight(s, ".!")
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "**") && strings.HasSuffix(s, "**") && len(s) >= 4 {
			s = s[2 : len(s)-2]
			trimmed = true
		}
		if (strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`")) ||
			(strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"")) ||
			(strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) {
			if len(s) >= 2 {
				s = s[1 : len(s)-1]
				trimmed = true
			}
		}
		s = strings.TrimSpace(s)
		if !trimmed {
			break
		}
	}
	s = strings.TrimRight(s, ".!")
	return strings.TrimSpace(s)
}

func countSentences(text string) int {
	text = strings.TrimSpace(normalizeMaskedEllipsis(text))
	if text == "" {
		return 0
	}
	runes := []rune(text)
	n := len(runes)
	count := 0
	lastTerminatorIdx := -1
	for i := 0; i < n; i++ {
		r := runes[i]
		if r == '.' || r == '!' || r == '?' {
			if i+1 == n || unicode.IsSpace(runes[i+1]) {
				count++
				lastTerminatorIdx = i
			}
		}
	}
	if lastTerminatorIdx < n-1 {
		trailing := strings.TrimSpace(string(runes[lastTerminatorIdx+1:]))
		if trailing != "" {
			count++
		}
	}
	return count
}

func countWords(value string) int {
	return len(strings.Fields(value))
}

// extractJSONObject parses the outermost JSON object in a model's free-form
// answer. The answer is untrusted model output of unknown shape, so it decodes
// into a generic object that the content check then reads field by field.
func extractJSONObject(value string) map[string]any {
	trimmed := strings.TrimSpace(value)
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return nil
	}
	payload := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(trimmed[start : end+1]))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil
	}
	return payload
}

func numericValue(raw any) (int64, bool) {
	switch typed := raw.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < -0x1p63 || typed >= 0x1p63 {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed, true
		}
		literal := string(typed)
		if len(literal) > 128 {
			return 0, false
		}
		if exponentIndex := strings.IndexAny(literal, "eE"); exponentIndex >= 0 {
			exponent, err := strconv.Atoi(literal[exponentIndex+1:])
			if err != nil || exponent < -19 || exponent > 19 {
				return 0, false
			}
		}
		rational, ok := new(big.Rat).SetString(literal)
		if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
			return 0, false
		}
		return rational.Num().Int64(), true
	default:
		return 0, false
	}
}
