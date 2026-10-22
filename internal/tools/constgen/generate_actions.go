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
	// StateChange marks an action whose execution changes state. Every state
	// change is a mutation.
	StateChange bool
	// ReadOnlyTargets are the constants package identifiers of the target
	// resources against which a state-change action changes no state.
	ReadOnlyTargets []string
}

func loadActionTypeMeta(path string) (map[string]actionTypeMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Status struct {
			ActionType map[string]struct {
				GoConst              string   `json:"_go_const"`
				Value                string   `json:"value"`
				Mutation             bool     `json:"_mutation"`
				Bootstrap            bool     `json:"_bootstrap"`
				AuthorizedByDecision bool     `json:"_authorized_by_decision"`
				StateChange          bool     `json:"_state_change"`
				ReadOnlyTargets      []string `json:"_read_only_targets"`
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
		if meta.StateChange && !meta.Mutation {
			return nil, fmt.Errorf("decode %s: action %s: _state_change requires _mutation", path, key)
		}
		if len(meta.ReadOnlyTargets) > 0 && !meta.StateChange {
			return nil, fmt.Errorf("decode %s: action %s: _read_only_targets requires _state_change", path, key)
		}
		targets := append([]string(nil), meta.ReadOnlyTargets...)
		sort.Strings(targets)
		out[key] = actionTypeMeta{
			GoConst:              meta.GoConst,
			Value:                meta.Value,
			Mutation:             meta.Mutation,
			Bootstrap:            meta.Bootstrap,
			AuthorizedByDecision: meta.AuthorizedByDecision,
			StateChange:          meta.StateChange,
			ReadOnlyTargets:      targets,
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
	var stateChanges []string
	var readOnlyTargetKeys []string
	for _, key := range keys {
		meta := actionTypes[key]
		if meta.Mutation {
			mutations = append(mutations, meta.GoConst)
		}
		if meta.StateChange {
			stateChanges = append(stateChanges, meta.GoConst)
		}
		if len(meta.ReadOnlyTargets) > 0 {
			readOnlyTargetKeys = append(readOnlyTargetKeys, key)
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
		{
			name: "IsStateChange",
			comment: `// IsStateChange returns true for actions whose execution changes state.
// ChangesState narrows it by target resource.`,
			actions: stateChanges,
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
	b.WriteString(`
// ChangesState reports whether executing the action against targetResource
// changes state. A state-change action changes state against every target
// except its registered read-only targets, so an unknown target fails closed.
func (a ActionType) ChangesState(targetResource string) bool {
	return a.IsStateChange() && !a.readOnlyTarget(targetResource)
}

// readOnlyTarget reports whether targetResource is a registered read-only
// target of state-change action a.
`)
	if len(readOnlyTargetKeys) == 0 {
		b.WriteString("func (ActionType) readOnlyTarget(string) bool {\n\treturn false\n}\n")
		return b.String()
	}
	b.WriteString("func (a ActionType) readOnlyTarget(targetResource string) bool {\n\tswitch a {\n")
	for _, key := range readOnlyTargetKeys {
		meta := actionTypes[key]
		fmt.Fprintf(&b, "\tcase %s:\n\t\tswitch targetResource {\n\t\tcase %s:\n\t\t\treturn true\n\t\t}\n",
			meta.GoConst, strings.Join(meta.ReadOnlyTargets, ",\n\t\t\t"))
	}
	b.WriteString("\t}\n\treturn false\n}\n")
	return b.String()
}
