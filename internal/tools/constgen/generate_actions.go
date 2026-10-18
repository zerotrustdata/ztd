// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type actionTypeMeta struct {
	GoConst              string
	Value                string
	Mutation             bool
	Bootstrap            bool
	AuthorizedByDecision bool
}

func loadActionTypeMeta(path string) (map[string]actionTypeMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Status struct {
			ActionType map[string]struct {
				GoConst              string `json:"_go_const"`
				Value                string `json:"value"`
				Mutation             bool   `json:"_mutation"`
				Bootstrap            bool   `json:"_bootstrap"`
				AuthorizedByDecision bool   `json:"_authorized_by_decision"`
			} `json:"action_type"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	out := make(map[string]actionTypeMeta, len(raw.Status.ActionType))
	for key, meta := range raw.Status.ActionType {
		if meta.AuthorizedByDecision && !meta.Bootstrap {
			return nil, fmt.Errorf("decode %s: action %s: _authorized_by_decision requires _bootstrap", path, key)
		}
		out[key] = actionTypeMeta{
			GoConst:              meta.GoConst,
			Value:                meta.Value,
			Mutation:             meta.Mutation,
			Bootstrap:            meta.Bootstrap,
			AuthorizedByDecision: meta.AuthorizedByDecision,
		}
	}
	return out, nil
}

func generateActionTypesGo(actionTypes map[string]actionTypeMeta) string {
	keys := make([]string, 0, len(actionTypes))
	for k := range actionTypes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var mutations []string
	var bootstraps []string
	var decisionAuthorized []string
	for _, key := range keys {
		meta := actionTypes[key]
		if meta.Mutation {
			mutations = append(mutations, meta.GoConst)
		}
		if meta.Bootstrap {
			bootstraps = append(bootstraps, meta.GoConst)
		}
		if meta.AuthorizedByDecision {
			decisionAuthorized = append(decisionAuthorized, meta.GoConst)
		}
	}

	var b strings.Builder
	b.WriteString(fileHeader("constgen"))
	b.WriteString(`package constants

// ActionType is a typed string for governed action classes.
type ActionType string

const (
`)
	for _, key := range keys {
		meta := actionTypes[key]
		fmt.Fprintf(&b, "\t%s ActionType = %q\n", meta.GoConst, meta.Value)
	}
	b.WriteString(`)

// AllActionTypes is the canonical slice of all valid action types.
var AllActionTypes = []ActionType{
`)
	for _, key := range keys {
		fmt.Fprintf(&b, "\t%s,\n", actionTypes[key].GoConst)
	}
	b.WriteString("}\n")
	for _, predicate := range []struct {
		name    string
		comment string
		actions []string
	}{
		{
			name:    "IsMutation",
			comment: "// IsMutation returns true if the action type modifies system state.",
			actions: mutations,
		},
		{
			name:    "IsBootstrapAction",
			comment: "// IsBootstrapAction returns true for platform enrollment bootstrap actions.",
			actions: bootstraps,
		},
		{
			name: "AuthorizedByDecision",
			comment: `// AuthorizedByDecision returns true for enrollment actions that carry out an
// owner decision already committed under the posture's L3 gate. They need no
// L3 proof of their own; their handler requires the committed decision.`,
			actions: decisionAuthorized,
		},
	} {
		fmt.Fprintf(&b, "\n%s\nfunc (a ActionType) %s() bool {\n\tswitch a {\n", predicate.comment, predicate.name)
		// Empty classifications have only the default branch: no action qualifies.
		if len(predicate.actions) > 0 {
			fmt.Fprintf(&b, "\tcase %s:\n\t\treturn true\n", strings.Join(predicate.actions, ",\n\t\t"))
		}
		b.WriteString(`	default:
		return false
	}
}
`)
	}
	return b.String()
}
