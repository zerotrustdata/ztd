// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// constgen validates protocol/constants/events.json and can emit generated
// constant files. Phase 1 ships validation only; code generation follows W2.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

type registryFile struct {
	Events map[string]eventEntry `json:"events"`
}

type eventEntry struct {
	GoConst     string   `json:"_go_const"`
	Value       string   `json:"value"`
	Kind        string   `json:"kind"`
	Transport   []string `json:"transport"`
	Producers   []string `json:"producers"`
	Persistence string   `json:"persistence"`
	Outcomes    []string `json:"outcomes"`
	Governance  *struct {
		ActionType string `json:"action_type"`
		Payload    string `json:"payload"`
	} `json:"governance"`
	GrammarAllowlistOwner string `json:"grammar_allowlist_owner"`
	Reserved              bool   `json:"reserved"`
}

var (
	wirePattern = regexp.MustCompile(`^ztd\.v1\.[a-z]+(?:\.[a-z0-9_]+)+$`)
	goConstPat  = regexp.MustCompile(`^Event[A-Z][A-Za-z0-9]*$`)

	allowedTerminals = map[string]struct{}{
		"requested": {}, "started": {}, "received": {}, "completed": {}, "failed": {},
		"cancelled": {}, "timeout": {}, "recorded": {}, "created": {}, "updated": {},
		"deleted": {}, "granted": {}, "rejected": {}, "denied": {}, "revoked": {},
		"expired": {}, "acknowledged": {}, "sent": {}, "missed": {}, "bound": {},
		"unbound": {}, "opened": {}, "closed": {}, "established": {}, "checkpointed": {},
		"exported": {}, "published": {}, "rotated": {}, "available": {}, "invoked": {},
		"reached": {}, "detected": {}, "resolved": {}, "appended": {}, "truncated": {},
		"retry": {}, "heartbeat": {},
		"active": {}, "ai": {}, "answered": {}, "append": {}, "assigned": {},
		"authenticated": {}, "blocked": {}, "changed": {}, "cleared": {}, "complete": {},
		"configured": {}, "confirmed": {}, "disabled": {}, "end": {}, "error": {},
		"escalated": {}, "feedback": {}, "lettered": {}, "loaded": {}, "logged": {},
		"occurred": {}, "offline": {}, "open": {}, "preparing": {}, "questions": {},
		"queued": {}, "registered": {}, "replayed": {}, "reported": {}, "running": {},
		"selected": {}, "skipped": {}, "stale": {}, "stopped": {}, "submitted": {},
		"succeeded": {}, "switched": {}, "system": {}, "terminated": {}, "tier1": {},
		"tier2": {}, "tier3": {}, "unauthenticated": {}, "unavailable": {}, "update": {},
		"user": {}, "verified": {},
	}

	allowedKinds = map[string]struct{}{
		"request": {}, "outcome": {}, "fact": {}, "stream": {},
	}

	allowedTransport = map[string]struct{}{
		"governed": {}, "pubsub": {}, "sse": {},
	}

	allowedProducers = map[string]struct{}{
		"gateway": {}, "operator": {},
		"cli": {}, "mcp": {},
	}

	allowedPersistence = map[string]struct{}{
		"operator.audit_log":    {},
		"gateway.audit_log":     {},
		"gateway.sse_store":     {},
		"gateway.operator_docs": {},
		"gateway.docstore":      {},
		"ephemeral":             {},
	}

	// requiredEventFields and requiredGovernanceFields are the required-field
	// sets validateRegistry enforces (missing values fail with "missing <field>"
	// or "governance requires ..."). The parity test pins them to the schema.
	requiredEventFields      = []string{"_go_const", "value", "producers", "persistence"}
	requiredGovernanceFields = []string{"action_type", "payload"}
)

func main() {
	checkOnly := flag.Bool("check", false, "validate registry and verify generated files match committed output")
	write := flag.Bool("write", false, "validate registry and write generated constant files")
	flag.Parse()
	if *checkOnly && *write {
		fatal("use only one of -check or -write")
	}
	if !*checkOnly && !*write {
		*checkOnly = true
	}

	root, err := findRepoRoot()
	if err != nil {
		fatal("%v", err)
	}
	if err := validateEnvRegistries(root); err != nil {
		fatal("%v", err)
	}
	if err := execute(root, *write, os.Stdout); err != nil {
		fatal("%v", err)
	}
}

// execute validates the registries under root, then either writes the
// generated constant files (write) or verifies the committed ones match.
func execute(root string, write bool, stdout io.Writer) error {
	eventsPath := filepath.Join(root, relPathProtocolEvents)
	statusPath := filepath.Join(root, relPathProtocolStatus)

	events, err := loadRegistry(eventsPath)
	if err != nil {
		return err
	}
	actionTypeMeta, err := loadActionTypeMeta(statusPath)
	if err != nil {
		return err
	}
	actionTypes := actionTypeValues(actionTypeMeta)

	if err := validateRegistry(events, actionTypes); err != nil {
		return err
	}

	out, err := generateAll(root, events, actionTypeMeta)
	if err != nil {
		return err
	}

	if write {
		if err := writeGenerated(root, out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "generated constants from protocol/constants/events.json")
		return nil
	}

	return verifyGenerated(root, out)
}

func actionTypeValues(actionTypes map[string]actionTypeMeta) map[string]struct{} {
	out := make(map[string]struct{}, len(actionTypes))
	for _, meta := range actionTypes {
		out[meta.Value] = struct{}{}
	}
	return out
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, relPathProtocolEvents)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find protocol/constants/events.json from %s", dir)
		}
		dir = parent
	}
}

// loadRegistry decodes the registry strictly: any key outside the declared
// shape (entry level or inside governance) fails with ErrEventRegistryUnknownField.
func loadRegistry(path string) (registryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return registryFile{}, err
	}
	var reg registryFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reg); err != nil {
		// encoding/json exposes no typed error for unknown fields; its message
		// prefix is the only signal, so match it here and nowhere else.
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return registryFile{}, fmt.Errorf("decode %s: %w: %v", path, constants.ErrEventRegistryUnknownField, err)
		}
		return registryFile{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return reg, nil
}

// registryIssues is the aggregate returned by validateRegistry. Unwrap exposes
// every issue so callers can match rule sentinels with errors.Is.
type registryIssues []error

func (r registryIssues) Error() string {
	msgs := make([]string, len(r))
	for i, err := range r {
		msgs[i] = err.Error()
	}
	return fmt.Sprintf("event registry validation failed (%d issues):\n  %s", len(r), strings.Join(msgs, "\n  "))
}

func (r registryIssues) Unwrap() []error { return r }

func validateRegistry(reg registryFile, actionTypes map[string]struct{}) error {
	if len(reg.Events) == 0 {
		return fmt.Errorf("events registry is empty")
	}

	var errs []error
	seenValue := map[string]string{}
	seenGoConst := map[string]string{}

	keys := make([]string, 0, len(reg.Events))
	for k := range reg.Events {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		entry := reg.Events[key]
		if entry.GoConst == "" {
			errs = append(errs, fmt.Errorf("%s: missing _go_const", key))
		} else if !goConstPat.MatchString(entry.GoConst) {
			errs = append(errs, fmt.Errorf("%s: invalid _go_const %q", key, entry.GoConst))
		} else if owner, exists := seenGoConst[entry.GoConst]; exists {
			errs = append(errs, fmt.Errorf("%s: duplicate _go_const %q (also %s)", key, entry.GoConst, owner))
		} else {
			seenGoConst[entry.GoConst] = key
		}

		if entry.Value == "" {
			errs = append(errs, fmt.Errorf("%s: missing value", key))
			continue
		}
		if !wirePattern.MatchString(entry.Value) {
			errs = append(errs, fmt.Errorf("%s: invalid wire value %q", key, entry.Value))
		}
		if owner, exists := seenValue[entry.Value]; exists {
			errs = append(errs, fmt.Errorf("%s: duplicate wire value %q (also %s)", key, entry.Value, owner))
		} else {
			seenValue[entry.Value] = key
		}

		if entry.Kind != "" {
			if _, ok := allowedKinds[entry.Kind]; !ok {
				errs = append(errs, fmt.Errorf("%s: unknown kind %q", key, entry.Kind))
			}
		}

		for _, t := range entry.Transport {
			if _, ok := allowedTransport[t]; !ok {
				errs = append(errs, fmt.Errorf("%s: unknown transport %q", key, t))
			}
		}

		if entry.Governance != nil {
			if entry.Kind != "request" {
				errs = append(errs, fmt.Errorf("%s: governance requires kind=request", key))
			}
			if !strings.HasSuffix(entry.Value, ".requested") {
				errs = append(errs, fmt.Errorf("%s: governance requires .requested wire value", key))
			}
			if entry.Governance.ActionType == "" || entry.Governance.Payload == "" {
				errs = append(errs, fmt.Errorf("%s: governance requires action_type and payload", key))
			} else if _, ok := actionTypes[entry.Governance.ActionType]; !ok {
				errs = append(errs, fmt.Errorf("%s: unknown governance.action_type %q", key, entry.Governance.ActionType))
			}
			hasGoverned := false
			for _, t := range entry.Transport {
				if t == "governed" {
					hasGoverned = true
					break
				}
			}
			if !hasGoverned {
				errs = append(errs, fmt.Errorf("%s: governed request missing transport governed", key))
			}
		}

		if entry.GrammarAllowlistOwner != "" {
			errs = append(errs, fmt.Errorf("%s: grammar allowlist is closed; rename the wire value instead of grammar_allowlist_owner", key))
		}
		if err := checkGrammar(entry.Value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", key, err))
		}

		if entry.Kind == "request" && entry.Governance == nil && !strings.HasSuffix(entry.Value, ".requested") {
			errs = append(errs, fmt.Errorf("%s: %w: %q", key, constants.ErrEventRequestWireValue, entry.Value))
		}

		if entry.Kind == "stream" {
			if len(entry.Transport) == 0 {
				errs = append(errs, fmt.Errorf("%s: %w: transport absent", key, constants.ErrEventStreamTransport))
			}
			for _, t := range entry.Transport {
				if t != "sse" {
					errs = append(errs, fmt.Errorf("%s: %w: transport %q", key, constants.ErrEventStreamTransport, t))
				}
			}
		}

		if len(entry.Producers) == 0 {
			errs = append(errs, fmt.Errorf("%s: missing producers", key))
		}
		for _, producer := range entry.Producers {
			if _, ok := allowedProducers[producer]; !ok {
				errs = append(errs, fmt.Errorf("%s: unknown producer %q", key, producer))
			}
		}

		if entry.Persistence == "" {
			errs = append(errs, fmt.Errorf("%s: missing persistence", key))
		} else if _, ok := allowedPersistence[entry.Persistence]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown persistence %q", key, entry.Persistence))
		}

		if entry.Kind == "stream" && entry.Persistence != "ephemeral" {
			errs = append(errs, fmt.Errorf("%s: stream events must use ephemeral persistence", key))
		}

		for _, outcomeKey := range entry.Outcomes {
			outcomeEntry, ok := reg.Events[outcomeKey]
			if !ok {
				errs = append(errs, fmt.Errorf("%s: outcomes references missing event %q", key, outcomeKey))
				continue
			}
			if outcomeEntry.Kind != "outcome" && outcomeEntry.Kind != "fact" {
				errs = append(errs, fmt.Errorf("%s: outcomes entry %q must be kind outcome or fact, got %q", key, outcomeKey, outcomeEntry.Kind))
			}
		}
	}

	actionTypesWithRequest := map[string]bool{}
	for _, entry := range reg.Events {
		if entry.Governance == nil || entry.Governance.ActionType == "" {
			continue
		}
		hasGoverned := false
		for _, t := range entry.Transport {
			if t == "governed" {
				hasGoverned = true
				break
			}
		}
		if !hasGoverned || entry.Kind != "request" {
			continue
		}
		actionTypesWithRequest[entry.Governance.ActionType] = true
	}
	for actionType := range actionTypes {
		if actionTypesWithRequest[actionType] {
			continue
		}
		errs = append(errs, fmt.Errorf("action type %q has no governed request event in registry", actionType))
	}

	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
		return registryIssues(errs)
	}
	return nil
}

func checkGrammar(value string) error {
	parts := strings.Split(strings.TrimPrefix(value, "ztd.v1."), ".")
	if len(parts) < 2 {
		return fmt.Errorf("grammar: expected ztd.v1.<domain>.<entity>.<terminal>")
	}
	terminal := parts[len(parts)-1]
	if _, ok := allowedTerminals[terminal]; !ok {
		if strings.Contains(value, ".status.updated.") {
			return nil
		}
		if strings.Contains(value, ".stream.") || strings.Contains(value, ".chunk.") ||
			strings.Contains(value, ".delta.") || strings.Contains(value, ".keepalive.") ||
			strings.Contains(value, ".thinking.") {
			return nil
		}
		return fmt.Errorf("grammar: terminal %q not in closed list for %q", terminal, value)
	}
	if len(parts) == 2 {
		return nil
	}
	return nil
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "constgen: "+format+"\n", args...)
	os.Exit(1)
}
