// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cases

import (
	"errors"
	"strings"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// cloneCases copies the cases and their expectation slices so a test can mutate one freely.
func cloneCases(cs []Case) []Case {
	out := make([]Case, len(cs))
	for i, c := range cs {
		c.Expect = append([]Expect(nil), c.Expect...)
		out[i] = c
	}
	return out
}

func cloneGaps(gs []Gap) []Gap {
	return append([]Gap(nil), gs...)
}

// indexOf returns the index of the first case matching every predicate, failing the test if none does.
func indexOf(t *testing.T, cs []Case, preds ...func(Case) bool) int {
	t.Helper()
	for i, c := range cs {
		if countCases([]Case{c}, preds...) == 1 {
			return i
		}
	}
	t.Fatal("no case matches the predicates")
	return -1
}

// requireInvalid asserts err wraps ErrConformanceCatalogueInvalid and names the failing check.
func requireInvalid(t *testing.T, err error, detail string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Validate = nil, want an error mentioning %q", detail)
	}
	if !errors.Is(err, constants.ErrConformanceCatalogueInvalid) {
		t.Fatalf("Validate error %v does not wrap ErrConformanceCatalogueInvalid", err)
	}
	if !strings.Contains(err.Error(), detail) {
		t.Fatalf("Validate error %q does not mention %q", err.Error(), detail)
	}
}

func TestValidateAcceptsBaseCatalogue(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	if err := Validate(cs, gaps, mustCorpus(t)); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
}

func TestValidateRejectsNilCorpus(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	requireInvalid(t, Validate(cs, gaps, nil), "corpus is nil")
}

func TestValidateRejectsDuplicateCaseID(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	cs = cloneCases(cs)
	cs[1].ID = cs[0].ID
	requireInvalid(t, Validate(cs, gaps, mustCorpus(t)), "duplicate case id")
}

func TestValidateRejectsUnknownPosture(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	cs = cloneCases(cs)
	cs[0].Posture = evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED
	requireInvalid(t, Validate(cs, gaps, mustCorpus(t)), "not one of the four postures")
}

func TestValidateRejectsUnknownIngressAndAction(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)

	bad := cloneCases(cs)
	bad[0].Ingress = Ingress("SMTP")
	requireInvalid(t, Validate(bad, gaps, corpus), "ingress")

	bad = cloneCases(cs)
	bad[0].Action = Action("delete")
	requireInvalid(t, Validate(bad, gaps, corpus), "action")
}

func TestValidateRejectsWrongExpectCount(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)

	twoOnPlain := cloneCases(cs)
	i := indexOf(t, twoOnPlain, row(1))
	twoOnPlain[i].Expect = append(twoOnPlain[i].Expect, twoOnPlain[i].Expect[0])
	requireInvalid(t, Validate(twoOnPlain, gaps, corpus), "has 2 expectations, want 1")

	oneOnReplay := cloneCases(cs)
	j := indexOf(t, oneOnReplay, func(c Case) bool { return c.Fault == FaultReplay })
	oneOnReplay[j].Expect = oneOnReplay[j].Expect[:1]
	requireInvalid(t, Validate(oneOnReplay, gaps, corpus), "has 1 expectations, want 2")
}

func TestValidateRejectsBadRejectionAndRefusalCodes(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)
	rejectedIdx := func(t *testing.T, base []Case) int {
		t.Helper()
		return indexOf(t, base, row(3), func(c Case) bool { return c.Ingress == IngressDirect })
	}
	refusedIdx := func(t *testing.T, base []Case) int {
		t.Helper()
		return indexOf(t, base, row(11))
	}

	tests := []struct {
		name   string
		mutate func(t *testing.T, cs []Case)
		detail string
	}{
		{"rejection UNSPECIFIED", func(t *testing.T, cs []Case) {
			t.Helper()
			cs[rejectedIdx(t, cs)].Expect[0].RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED
		}, "not a defined rejection"},
		{"rejection UNCLASSIFIED", func(t *testing.T, cs []Case) {
			t.Helper()
			cs[rejectedIdx(t, cs)].Expect[0].RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED
		}, "not a defined rejection"},
		{"rejection undefined value", func(t *testing.T, cs []Case) {
			t.Helper()
			cs[rejectedIdx(t, cs)].Expect[0].RejectionCode = operatorv1.GovernanceRejectionCode(9999)
		}, "not a defined rejection"},
		{"rejection carries refusal code", func(t *testing.T, cs []Case) {
			t.Helper()
			cs[rejectedIdx(t, cs)].Expect[0].RefusalCode = operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD
		}, "rejection carries a refusal code"},
		{"refusal UNCLASSIFIED", func(t *testing.T, cs []Case) {
			t.Helper()
			cs[refusedIdx(t, cs)].Expect[0].RefusalCode = operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED
		}, "not a defined refusal"},
		{"refusal undefined value", func(t *testing.T, cs []Case) {
			t.Helper()
			cs[refusedIdx(t, cs)].Expect[0].RefusalCode = operatorv1.IngressRefusalCode(9999)
		}, "not a defined refusal"},
		{"refusal carries rejection code", func(t *testing.T, cs []Case) {
			t.Helper()
			cs[refusedIdx(t, cs)].Expect[0].RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY
		}, "refusal carries a rejection code"},
		{"executed carries failure code", func(t *testing.T, cs []Case) {
			t.Helper()
			i := indexOf(t, cs, row(1))
			cs[i].Expect[0].RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY
		}, "executed expectation carries a failure code"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := cloneCases(cs)
			tc.mutate(t, bad)
			requireInvalid(t, Validate(bad, gaps, corpus), tc.detail)
		})
	}
}

func TestValidateRejectsBadExecutedExpectation(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)

	t.Run("executed without L2 or L3 status", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(1))
		bad[i].Expect[0].L2Status = operatorv1.L2Status_L2_STATUS_UNSPECIFIED
		requireInvalid(t, Validate(bad, gaps, corpus), "no L2 or L3 status")
	})

	t.Run("L2 status does not match posture", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(1), posture(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS))
		bad[i].Expect[0].L2Status = operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
		requireInvalid(t, Validate(bad, gaps, corpus), "does not match")
	})

	t.Run("L3 status set on a read", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(2), ingress(IngressDirect), posture(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY))
		bad[i].Expect[0].L3Status = operatorv1.L3Status_L3_STATUS_REQUIRED_VALID
		requireInvalid(t, Validate(bad, gaps, corpus), "does not match")
	})

	t.Run("target marker effect on an executed read", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(2), ingress(IngressDirect), posture(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))
		bad[i].Expect[0].Effect = EffectTargetMarker
		requireInvalid(t, Validate(bad, gaps, corpus), "does not match")
	})

	t.Run("no effect on an executed mutation", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(1), ingress(IngressDirect))
		bad[i].Expect[0].Effect = EffectNone
		requireInvalid(t, Validate(bad, gaps, corpus), "does not match")
	})

	t.Run("target marker effect on an executed A2A mutation", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(1), ingress(IngressA2A))
		bad[i].Expect[0].Effect = EffectTargetMarker
		requireInvalid(t, Validate(bad, gaps, corpus), "does not match")
	})

	t.Run("a2a stub effect on an executed DIRECT mutation", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(1), ingress(IngressDirect))
		bad[i].Expect[0].Effect = EffectA2AStubCall
		requireInvalid(t, Validate(bad, gaps, corpus), "does not match")
	})
}

func TestValidateRejectsEffectOnNonExecuted(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)

	t.Run("target marker on a rejection", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(3), ingress(IngressDirect))
		bad[i].Expect[0].Effect = EffectTargetMarker
		requireInvalid(t, Validate(bad, gaps, corpus), "want \"none\"")
	})

	t.Run("a2a stub on a suspension", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(22))
		bad[i].Expect[0].Effect = EffectA2AStubCall
		requireInvalid(t, Validate(bad, gaps, corpus), "want \"none\"")
	})

	t.Run("L2 status on a refusal", func(t *testing.T) {
		bad := cloneCases(cs)
		i := indexOf(t, bad, row(9))
		bad[i].Expect[0].L2Status = operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
		requireInvalid(t, Validate(bad, gaps, corpus), "carries an L2 or L3 status")
	})
}

func TestValidateRejectsUnknownExpectKind(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	bad := cloneCases(cs)
	i := indexOf(t, bad, row(3))
	bad[i].Expect[0].Kind = ExpectKind("MAYBE")
	requireInvalid(t, Validate(bad, gaps, mustCorpus(t)), "is unknown")
}

func TestValidateRejectsMissingAttackEntryInPosture(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)
	bad := cloneCases(cs)
	// Drop the DIRECT doctrine case for the first attack entry under notary.
	first := corpus.Attacks()[0].ID
	drop := indexOf(t, bad, func(c Case) bool {
		return c.Fault == FaultDoctrine && c.Ingress == IngressDirect && c.CorpusEntryID == first &&
			c.Posture == evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY
	})
	bad = append(bad[:drop], bad[drop+1:]...)
	requireInvalid(t, Validate(bad, gaps, corpus), "has no DIRECT doctrine case in posture notary")
}

func TestValidateRejectsMissingCategoryOnIngress(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)
	bad := cloneCases(cs)
	// Drop every MCP doctrine case in ratify, which removes one whole category there.
	bad = slicesDropIf(bad, func(c Case) bool {
		return c.Fault == FaultDoctrine && c.Ingress == IngressMCP &&
			c.Posture == evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	})
	requireInvalid(t, Validate(bad, gaps, corpus), "has no MCP doctrine case in posture ratify")
}

// slicesDropIf returns cs without the cases matching drop.
func slicesDropIf(cs []Case, drop func(Case) bool) []Case {
	var out []Case
	for _, c := range cs {
		if !drop(c) {
			out = append(out, c)
		}
	}
	return out
}

func TestValidateRejectsDoctrineCommandNotBlockedOffline(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)
	entryID := corpus.Attacks()[0].ID
	// Replace the corpus command and the case command together, so only the offline check fails.
	for i := range corpus.Entries {
		if corpus.Entries[i].ID == entryID {
			corpus.Entries[i].Command = "echo hello"
		}
	}
	bad := cloneCases(cs)
	for i := range bad {
		if bad[i].Fault == FaultDoctrine && bad[i].CorpusEntryID == entryID {
			bad[i].Command = "echo hello"
		}
	}
	requireInvalid(t, Validate(bad, gaps, corpus), "not blocked offline")
}

func TestValidateRejectsDoctrineCommandMismatchingCorpus(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	bad := cloneCases(cs)
	i := indexOf(t, bad, func(c Case) bool { return c.Fault == FaultDoctrine })
	bad[i].Command = "echo hello"
	requireInvalid(t, Validate(bad, gaps, mustCorpus(t)), "does not match corpus entry")
}

func TestValidateRejectsDoctrineCaseWithoutAttackEntry(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	bad := cloneCases(cs)
	i := indexOf(t, bad, func(c Case) bool { return c.Fault == FaultDoctrine })
	bad[i].CorpusEntryID = "no-such-entry"
	requireInvalid(t, Validate(bad, gaps, mustCorpus(t)), "names no attack entry")
}

func TestValidateRejectsMissingRow(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)

	// Row 25 is a gap only, so removing its gaps leaves it uncovered.
	noRow25 := make([]Gap, 0, len(gaps))
	for _, g := range gaps {
		if g.Row != 25 {
			noRow25 = append(noRow25, g)
		}
	}
	requireInvalid(t, Validate(cs, noRow25, corpus), "row 25 has no case and no gap")

	// Row 9 is cases only, so removing them leaves it uncovered.
	noRow9 := slicesDropIf(cloneCases(cs), func(c Case) bool { return c.Row == 9 })
	requireInvalid(t, Validate(noRow9, gaps, corpus), "row 9 has no case and no gap")
}

func TestValidateRejectsCaseOrGapRowOutOfRange(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	corpus := mustCorpus(t)

	badGap := append(cloneGaps(gaps), Gap{Row: 26, Ingress: IngressCLI, Fault: FaultL3ViaArguments, Reason: "x"})
	requireInvalid(t, Validate(cs, badGap, corpus), "outside 1 to 25")

	badGap = append(cloneGaps(gaps), Gap{Row: 0, Ingress: IngressCLI, Fault: FaultL3ViaArguments, Reason: "x"})
	requireInvalid(t, Validate(cs, badGap, corpus), "outside 1 to 25")
}

func TestValidateRejectsGapWithoutReason(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	bad := cloneGaps(gaps)
	bad[0].Reason = ""
	requireInvalid(t, Validate(cs, bad, mustCorpus(t)), "has no reason")
}

func TestValidateRejectsGapSharingCaseCell(t *testing.T) {
	cs, gaps := mustCatalogue(t)
	shared := append(cloneGaps(gaps), Gap{Row: 1, Ingress: IngressDirect, Fault: FaultNone, Reason: "x"})
	requireInvalid(t, Validate(cs, shared, mustCorpus(t)), "shares a cell with a case")
}
