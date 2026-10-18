// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cases

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func mustCatalogue(t *testing.T) ([]Case, []Gap) {
	t.Helper()
	cs, gaps, err := Catalogue()
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	return cs, gaps
}

func mustCorpus(t *testing.T) *doctrinecorpus.Corpus {
	t.Helper()
	corpus, err := doctrinecorpus.Load()
	if err != nil {
		t.Fatalf("doctrinecorpus.Load: %v", err)
	}
	return corpus
}

// countCases counts the cases matching every predicate.
func countCases(cs []Case, preds ...func(Case) bool) int {
	n := 0
	for _, c := range cs {
		match := true
		for _, p := range preds {
			if !p(c) {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

func row(r int) func(Case) bool         { return func(c Case) bool { return c.Row == r } }
func ingress(i Ingress) func(Case) bool { return func(c Case) bool { return c.Ingress == i } }
func posture(p evalv1.EvaluationGovernancePosture) func(Case) bool {
	return func(c Case) bool { return c.Posture == p }
}

func TestCatalogueValidates(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	if err := Validate(cs, gaps, mustCorpus(t)); err != nil {
		t.Fatalf("Validate(Catalogue()) = %v, want nil", err)
	}
}

func TestCatalogueCountsPerRowAndIngress(t *testing.T) {
	cs, _ := mustCatalogue(t)

	// Row 1: every ingress, two postures, one case each.
	for _, ing := range ingressAll {
		if got := countCases(cs, row(1), ingress(ing)); got != 2 {
			t.Errorf("row 1 %s: got %d cases, want 2", ing, got)
		}
	}

	// Row 2: DIRECT, both dispatch ingresses and MCP in all four postures; A2A and CLI are gaps.
	for _, ing := range []Ingress{IngressDirect, IngressDispatchEmbedded, IngressDispatchOutbound, IngressMCP} {
		if got := countCases(cs, row(2), ingress(ing)); got != 4 {
			t.Errorf("row 2 %s: got %d cases, want 4", ing, got)
		}
	}
	for _, ing := range []Ingress{IngressA2A, IngressCLI} {
		if got := countCases(cs, row(2), ingress(ing)); got != 0 {
			t.Errorf("row 2 %s: got %d cases, want 0 (gap)", ing, got)
		}
	}

	// Row 13: 49 attack entries on DIRECT in each of four postures.
	if got := countCases(cs, row(13)); got != 49*4 {
		t.Errorf("row 13: got %d cases, want %d", got, 49*4)
	}
	for _, p := range Postures {
		if got := countCases(cs, row(13), posture(p.Wire)); got != 49 {
			t.Errorf("row 13 posture %s: got %d cases, want 49", p.Posture, got)
		}
	}

	// Row 14: one case per category (5) on each of five ingresses in four postures.
	for _, ing := range []Ingress{IngressDispatchEmbedded, IngressDispatchOutbound, IngressCLI, IngressMCP, IngressA2A} {
		if got := countCases(cs, row(14), ingress(ing)); got != 5*4 {
			t.Errorf("row 14 %s: got %d cases, want %d", ing, got, 5*4)
		}
	}
}

// TestRowSeventeenMissingFieldPinsDirectRejectionAndDispatchRefusal pins the
// missing_field body fault per ingress: DIRECT reaches L4 and is rejected by the
// signed payload-decode check, while governed dispatch refuses before an envelope exists.
func TestRowSeventeenMissingFieldPinsDirectRejectionAndDispatchRefusal(t *testing.T) {
	cs, _ := mustCatalogue(t)
	pinned := map[Ingress]Expect{
		IngressDirect: {
			Kind:          ExpectRejected,
			RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED,
			Effect:        EffectNone,
		},
		IngressDispatchEmbedded: {
			Kind:        ExpectRefused,
			RefusalCode: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD,
			Effect:      EffectNone,
		},
		IngressDispatchOutbound: {
			Kind:        ExpectRefused,
			RefusalCode: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD,
			Effect:      EffectNone,
		},
	}
	seen := make(map[Ingress]bool, len(pinned))
	for _, c := range cs {
		if c.Fault != FaultMissingField {
			continue
		}
		want, ok := pinned[c.Ingress]
		if !ok {
			t.Errorf("case %s: missing_field on %s is not pinned", c.ID, c.Ingress)
			continue
		}
		seen[c.Ingress] = true
		if len(c.Expect) != 1 || c.Expect[0] != want {
			t.Errorf("case %s: expect %+v, want [%+v]", c.ID, c.Expect, want)
		}
	}
	for ingress := range pinned {
		if !seen[ingress] {
			t.Errorf("no missing_field case on %s", ingress)
		}
	}
}

func TestApplicableDoctrineExcludesConsensusAndRatifyRows(t *testing.T) {
	cs, err := Applicable(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)
	if err != nil {
		t.Fatalf("Applicable(DOCTRINE): %v", err)
	}
	for _, c := range cs {
		switch c.Row {
		case 18, 20, 22, 23, 24, 25, 26:
			t.Errorf("Applicable(DOCTRINE) contains row %d case %s", c.Row, c.ID)
		}
	}
	if len(cs) == 0 {
		t.Fatal("Applicable(DOCTRINE) is empty")
	}
}

func TestApplicableRatifyExcludesRowOne(t *testing.T) {
	cs, err := Applicable(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY)
	if err != nil {
		t.Fatalf("Applicable(RATIFY): %v", err)
	}
	for _, c := range cs {
		if c.Row == 1 {
			t.Errorf("Applicable(RATIFY) contains row 1 case %s", c.ID)
		}
	}
}

func TestPosturesBindEveryGovernancePostureInOrder(t *testing.T) {
	got := make([]constants.Posture, 0, len(Postures))
	for _, binding := range Postures {
		got = append(got, binding.Posture)
		wire, err := WirePosture(binding.Posture)
		if err != nil || wire != binding.Wire {
			t.Fatalf("WirePosture(%s) = %s, %v; want %s", binding.Posture, wire, err, binding.Wire)
		}
		posture, err := GovernancePosture(binding.Wire)
		if err != nil || posture != binding.Posture {
			t.Fatalf("GovernancePosture(%s) = %s, %v; want %s", binding.Wire, posture, err, binding.Posture)
		}
	}
	if !slices.Equal(got, constants.AllPostures) {
		t.Fatalf("Postures order = %v, want constants.AllPostures %v", got, constants.AllPostures)
	}
}

func TestPostureMappingRejectsUnknown(t *testing.T) {
	if _, err := GovernancePosture(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED); !errors.Is(err, constants.ErrConformancePostureUnknown) {
		t.Fatalf("GovernancePosture(UNSPECIFIED) = %v, want ErrConformancePostureUnknown", err)
	}
	for _, name := range []constants.Posture{"", "bogus", "DOCTRINE"} {
		wire, err := WirePosture(name)
		if !errors.Is(err, constants.ErrConformancePostureUnknown) || wire != evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED {
			t.Fatalf("WirePosture(%q) = %s, %v; want UNSPECIFIED, ErrConformancePostureUnknown", name, wire, err)
		}
	}
}

func TestApplicableRejectsUnknownPosture(t *testing.T) {
	_, err := Applicable(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED)
	if !errors.Is(err, constants.ErrConformancePostureUnknown) {
		t.Fatalf("Applicable(UNSPECIFIED) = %v, want ErrConformancePostureUnknown", err)
	}
}

func TestExecutedExpectPerPostureAndAction(t *testing.T) {
	const (
		notReq = operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
		reqOK  = operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
	)
	l3NotReq := operatorv1.L3Status_L3_STATUS_NOT_REQUIRED
	l3Req := operatorv1.L3Status_L3_STATUS_REQUIRED_VALID

	tests := []struct {
		name    string
		posture constants.Posture
		action  Action
		l2      operatorv1.L2Status
		l3      operatorv1.L3Status
	}{
		{"doctrine mutation", constants.PostureDoctrine, ActionMutation, notReq, l3NotReq},
		{"doctrine read", constants.PostureDoctrine, ActionRead, notReq, l3NotReq},
		{"consensus mutation", constants.PostureConsensus, ActionMutation, reqOK, l3NotReq},
		{"consensus read", constants.PostureConsensus, ActionRead, reqOK, l3NotReq},
		{"ratify mutation", constants.PostureRatify, ActionMutation, notReq, l3Req},
		{"ratify read", constants.PostureRatify, ActionRead, notReq, l3NotReq},
		{"notary mutation", constants.PostureNotary, ActionMutation, reqOK, l3Req},
		{"notary read", constants.PostureNotary, ActionRead, reqOK, l3NotReq},
		{"unknown posture fails closed", constants.Posture("bogus"), ActionMutation, reqOK, l3Req},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := executedExpect(tc.posture, tc.action, EffectTargetMarker)
			want := Expect{Kind: ExpectExecuted, L2Status: tc.l2, L3Status: tc.l3, Effect: EffectTargetMarker}
			if got != want {
				t.Fatalf("executedExpect = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCaseIDsAreLowerCaseAndUnique(t *testing.T) {
	cs, _ := mustCatalogue(t)
	seen := make(map[string]bool, len(cs))
	for _, c := range cs {
		if c.ID != strings.ToLower(c.ID) {
			t.Errorf("case id %q is not lower-case", c.ID)
		}
		if seen[c.ID] {
			t.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestMarkerCommandWritesMarkerWithEscaping(t *testing.T) {
	a := Attempt{
		RunID:     "run",
		AttemptID: "a1",
		TargetAbs: "/tmp/canary/it's.txt",
		Marker:    Marker("run", "a1"),
	}
	got := MarkerCommand(a)
	want := "printf '%s' 'ztd-conformance run a1\n' > '/tmp/canary/it'\\''s.txt'"
	if got != want {
		t.Fatalf("MarkerCommand = %q, want %q", got, want)
	}

	violations := governance.NewL1Doctrine().ValidatePayload(&operatorv1.CommandRequested{Command: got})
	if len(violations) != 0 {
		t.Fatalf("L1 blocks MarkerCommand %q: %v", got, violations)
	}
}

func TestMarkerArgvPassesL1AndIsNotDangerous(t *testing.T) {
	a := Attempt{
		RunID:     "run",
		AttemptID: "a1",
		TargetAbs: "/tmp/canary/target.txt",
		Marker:    Marker("run", "a1"),
	}
	argv := MarkerArgv(a)
	want := []string{"sh", "-c", `printf '%s' "$1" > "$2"`, "sh", a.Marker, a.TargetAbs}
	if !slices.Equal(argv, want) {
		t.Fatalf("MarkerArgv = %q, want %q", argv, want)
	}
	if slices.Contains(constants.DangerousCommands, argv[0]) {
		t.Fatalf("argv[0] %q is in constants.DangerousCommands", argv[0])
	}

	// The MCP call sends argv[0] as the command and the rest as args; L1 screens that JSON.
	payloadJSON, err := json.Marshal(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{Command: argv[0], Args: argv[1:]})
	if err != nil {
		t.Fatalf("marshal MCP arguments: %v", err)
	}
	signals, err := governance.NewL1Doctrine().AnalyzeMCPArguments(string(payloadJSON))
	if err != nil {
		t.Fatalf("AnalyzeMCPArguments: %v", err)
	}
	for _, s := range signals {
		if s.BlockRecommended {
			t.Fatalf("L1 blocks MarkerArgv: %+v", s)
		}
	}
}
