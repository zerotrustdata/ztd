// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package report

import (
	"errors"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const testBundleHash = "sha256-test-bundle"

var allPostures = []evalv1.EvaluationGovernancePosture{
	evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
	evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
	evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY,
	evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY,
}

// l1Observation is a signed L1 rejection carrying bundleHash on its L1_DOCTRINE stage.
func l1Observation(bundleHash string) *evalv1.ConformanceObservation {
	return &evalv1.ConformanceObservation{
		Rejection: &operatorv1.GovernanceRejection{
			Receipt: &operatorv1.ActionReceipt{
				DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{{
					Kind:               operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE,
					DoctrineBundleHash: bundleHash,
				}},
			},
		},
	}
}

// passingRun builds a verified run for posture in which every applicable case passes.
func passingRun(t *testing.T, posture evalv1.EvaluationGovernancePosture) VerifiedRun {
	t.Helper()
	applicable, err := cases.Applicable(posture, true)
	if err != nil {
		t.Fatalf("cases.Applicable(%s): %v", posture, err)
	}
	runID := "run-" + posture.String()
	record := &evalv1.ConformanceRunRecord{
		RunId:        runID,
		SuiteId:      cases.SuiteID,
		SuiteVersion: cases.SuiteVersion,
		Posture:      posture,
	}
	set := &evalv1.ConformanceVerdictSet{
		RunId:        runID,
		Posture:      posture,
		SuiteVerdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS,
	}
	for _, c := range applicable {
		ev := &evalv1.ConformanceCaseEvidence{CaseId: c.ID}
		if isL1Rejection(c) {
			ev.Observations = []*evalv1.ConformanceObservation{l1Observation(testBundleHash)}
		}
		record.Cases = append(record.Cases, ev)
		set.Verdicts = append(set.Verdicts, &evalv1.ConformanceVerdict{
			CaseId: c.ID,
			Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		})
	}
	return VerifiedRun{Record: record, Verdicts: set}
}

// fourRuns returns a passing run for every posture.
func fourRuns(t *testing.T) []VerifiedRun {
	t.Helper()
	var runs []VerifiedRun
	for _, p := range allPostures {
		runs = append(runs, passingRun(t, p))
	}
	return runs
}

// runFor returns the run whose record has posture p.
func runFor(t *testing.T, runs []VerifiedRun, p evalv1.EvaluationGovernancePosture) VerifiedRun {
	t.Helper()
	for _, r := range runs {
		if r.Record.GetPosture() == p {
			return r
		}
	}
	t.Fatalf("no run for posture %s", p)
	return VerifiedRun{}
}

// findCase returns the first applicable case of posture p that matches pred.
func findCase(t *testing.T, p evalv1.EvaluationGovernancePosture, pred func(cases.Case) bool) cases.Case {
	t.Helper()
	applicable, err := cases.Applicable(p, true)
	if err != nil {
		t.Fatalf("cases.Applicable(%s): %v", p, err)
	}
	for _, c := range applicable {
		if pred(c) {
			return c
		}
	}
	t.Fatalf("no case matches for posture %s", p)
	return cases.Case{}
}

// setVerdict replaces the verdict of case id in run's verdict set.
func setVerdict(t *testing.T, run VerifiedRun, id string, status evalv1.EvaluationVerdictStatus, class evalv1.ConformanceFailureClass) {
	t.Helper()
	for _, v := range run.Verdicts.GetVerdicts() {
		if v.GetCaseId() == id {
			v.Status = status
			v.FailureClass = class
			return
		}
	}
	t.Fatalf("no verdict for case %s", id)
}

// evidenceOf returns run's evidence for case id.
func evidenceOf(t *testing.T, run VerifiedRun, id string) *evalv1.ConformanceCaseEvidence {
	t.Helper()
	for _, ev := range run.Record.GetCases() {
		if ev.GetCaseId() == id {
			return ev
		}
	}
	t.Fatalf("no evidence for case %s", id)
	return nil
}

// cellAt returns the report cell for (row, ingress, posture).
func cellAt(t *testing.T, rep *Report, row int, ingress cases.Ingress, posture constants.Posture) Cell {
	t.Helper()
	for _, c := range rep.Cells {
		if c.Row == row && c.Ingress == ingress && c.Posture == posture {
			return c
		}
	}
	t.Fatalf("no cell for row %d ingress %s posture %s", row, ingress, posture)
	return Cell{}
}

func buildOK(t *testing.T, runs []VerifiedRun) *Report {
	t.Helper()
	rep, err := Build(runs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return rep
}

func isDoctrineL1Direct(c cases.Case) bool {
	return c.Row == 13 && c.Ingress == cases.IngressDirect && isL1Rejection(c)
}

func TestBuildCoversEveryCellStatus(t *testing.T) {
	runs := fourRuns(t)
	// Row 1 is a DIRECT mutation that only doctrine and consensus run.
	setVerdict(t, runFor(t, runs, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS),
		findCase(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
			func(c cases.Case) bool { return c.Row == 1 && c.Ingress == cases.IngressDirect }).ID,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA)
	attended := findCase(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY,
		func(c cases.Case) bool { return c.Attended })
	setVerdict(t, runFor(t, runs, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY), attended.ID,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED)

	rep := buildOK(t, runs)
	seen := make(map[CellStatus]bool)
	for _, c := range rep.Cells {
		seen[c.Status] = true
	}
	for _, want := range []CellStatus{CellCovered, CellPartial, CellMissing, CellNotExpressible, CellNotApplicable} {
		if !seen[want] {
			t.Errorf("no cell with status %s", want)
		}
	}
}

func TestBuildFailingCaseMarksCellMissingWithClass(t *testing.T) {
	runs := fourRuns(t)
	consensus := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS
	c := findCase(t, consensus, func(c cases.Case) bool { return c.Row == 1 && c.Ingress == cases.IngressDirect })
	setVerdict(t, runFor(t, runs, consensus), c.ID,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA)

	cell := cellAt(t, buildOK(t, runs), 1, cases.IngressDirect, constants.PostureConsensus)
	if cell.Status != CellMissing {
		t.Fatalf("status = %s, want %s", cell.Status, CellMissing)
	}
	if want := evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA.String(); cell.Reason != want {
		t.Errorf("reason = %q, want %q", cell.Reason, want)
	}
}

func TestBuildUnrunAttendedCaseMarksCellPartial(t *testing.T) {
	runs := fourRuns(t)
	ratify := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY
	c := findCase(t, ratify, func(c cases.Case) bool { return c.Attended })
	setVerdict(t, runFor(t, runs, ratify), c.ID,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED)

	cell := cellAt(t, buildOK(t, runs), c.Row, c.Ingress, "ratify")
	if cell.Status != CellPartial {
		t.Fatalf("status = %s, want %s", cell.Status, CellPartial)
	}
}

func TestBuildA2AAbsentRunMarksA2ACellsNotApplicable(t *testing.T) {
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	a2a := findCase(t, doctrine, func(c cases.Case) bool { return c.Ingress == cases.IngressA2A })
	run := passingRun(t, doctrine)
	run.Record.A2AIngressAbsent = true
	applicable, err := cases.Applicable(doctrine, false)
	if err != nil {
		t.Fatalf("cases.Applicable: %v", err)
	}
	ids := make(map[string]bool, len(applicable))
	for _, c := range applicable {
		ids[c.ID] = true
	}
	keep := func(id string) bool { return ids[id] }
	var evidence []*evalv1.ConformanceCaseEvidence
	for _, ev := range run.Record.GetCases() {
		if keep(ev.GetCaseId()) {
			evidence = append(evidence, ev)
		}
	}
	run.Record.Cases = evidence
	var verdicts []*evalv1.ConformanceVerdict
	for _, v := range run.Verdicts.GetVerdicts() {
		if keep(v.GetCaseId()) {
			verdicts = append(verdicts, v)
		}
	}
	run.Verdicts.Verdicts = verdicts

	rep := buildOK(t, []VerifiedRun{run})
	if cell := cellAt(t, rep, a2a.Row, cases.IngressA2A, constants.PostureDoctrine); cell.Status != CellNotApplicable {
		t.Fatalf("A2A cell status = %s, want %s", cell.Status, CellNotApplicable)
	}
	direct := findCase(t, doctrine, func(c cases.Case) bool { return c.Ingress == cases.IngressDirect })
	if cell := cellAt(t, rep, direct.Row, cases.IngressDirect, constants.PostureDoctrine); cell.Status != CellCovered {
		t.Fatalf("DIRECT cell status = %s, want %s", cell.Status, CellCovered)
	}
}

func TestBuildMissingPostureRunMarksItsCellsMissing(t *testing.T) {
	var runs []VerifiedRun
	notary := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY
	for _, p := range allPostures {
		if p != notary {
			runs = append(runs, passingRun(t, p))
		}
	}
	rep := buildOK(t, runs)

	missing := 0
	for _, c := range rep.Cells {
		if c.Posture != constants.PostureNotary {
			continue
		}
		switch c.Status {
		case CellMissing:
			missing++
		case CellNotApplicable, CellNotExpressible:
		default:
			t.Errorf("notary cell row %d %s has status %s, want MISSING, NOT_APPLICABLE or NOT_EXPRESSIBLE", c.Row, c.Ingress, c.Status)
		}
	}
	if missing == 0 {
		t.Fatal("no notary cell is MISSING")
	}
}

func TestBuildRejectsInvalidRuns(t *testing.T) {
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	tests := []struct {
		name   string
		mutate func(t *testing.T, runs []VerifiedRun) []VerifiedRun
		want   error
	}{
		{
			name: "two runs share a posture",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				return append(runs, passingRun(t, doctrine))
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "verdict set run id does not match its record",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				runFor(t, runs, doctrine).Verdicts.RunId = "other-run"
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "record suite id is not the catalogue suite",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				runFor(t, runs, doctrine).Record.SuiteId = "other-suite"
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "posture is not one of the four",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				r := runFor(t, runs, doctrine)
				r.Record.Posture = evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED
				r.Verdicts.Posture = evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED
				return runs
			},
			want: constants.ErrConformancePostureUnknown,
		},
		{
			name: "verdict for a case outside the catalogue",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				v := runFor(t, runs, doctrine).Verdicts
				v.Verdicts = append(v.Verdicts, &evalv1.ConformanceVerdict{CaseId: "not-a-case"})
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "a verdict for an applicable case is missing",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				v := runFor(t, runs, doctrine).Verdicts
				v.Verdicts = v.Verdicts[:len(v.Verdicts)-1]
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "a verdict for an applicable case is duplicated",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				v := runFor(t, runs, doctrine).Verdicts
				v.Verdicts[1] = v.Verdicts[0]
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "L1 stages in one posture disagree on the bundle hash",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				run := runFor(t, runs, doctrine)
				c := findCase(t, doctrine, isDoctrineL1Direct)
				evidenceOf(t, run, c.ID).Observations[0].Rejection.Receipt.DeterministicStageEvidence[0].DoctrineBundleHash = "forged-bundle"
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "a signed L1 rejection has no L1 stage",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				run := runFor(t, runs, doctrine)
				c := findCase(t, doctrine, isDoctrineL1Direct)
				evidenceOf(t, run, c.ID).Observations[0].Rejection.Receipt.DeterministicStageEvidence = nil
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "a signed L1 rejection has an empty bundle hash",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				run := runFor(t, runs, doctrine)
				c := findCase(t, doctrine, isDoctrineL1Direct)
				evidenceOf(t, run, c.ID).Observations[0].Rejection.Receipt.DeterministicStageEvidence[0].DoctrineBundleHash = ""
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
		{
			name: "a run carries no record",
			mutate: func(t *testing.T, runs []VerifiedRun) []VerifiedRun {
				runs[0].Record = nil
				return runs
			},
			want: constants.ErrConformanceRecordInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runs := tc.mutate(t, fourRuns(t))
			_, err := Build(runs)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Build error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBuildRecallCountsMatchCatalogue(t *testing.T) {
	type key struct {
		category doctrinecorpus.Category
		posture  constants.Posture
		ingress  cases.Ingress
	}
	corpus, err := doctrinecorpus.Load()
	if err != nil {
		t.Fatalf("doctrinecorpus.Load: %v", err)
	}
	categoryOf := make(map[string]doctrinecorpus.Category)
	for _, e := range corpus.Entries {
		categoryOf[e.ID] = e.Category
	}
	all, _, err := cases.Catalogue()
	if err != nil {
		t.Fatalf("cases.Catalogue: %v", err)
	}
	wantTotal := make(map[key]int)
	var wantCases int
	for _, c := range all {
		if c.Row != 13 && c.Row != 14 {
			continue
		}
		wantCases++
		name, err := cases.GovernancePosture(c.Posture)
		if err != nil {
			t.Fatalf("cases.GovernancePosture: %v", err)
		}
		wantTotal[key{categoryOf[c.CorpusEntryID], name, c.Ingress}]++
	}

	// One doctrine DIRECT attack fails, so its recall line must count one less blocked.
	runs := fourRuns(t)
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	failed := findCase(t, doctrine, isDoctrineL1Direct)
	setVerdict(t, runFor(t, runs, doctrine), failed.ID,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_EFFECT_MISSING)
	failedKey := key{categoryOf[failed.CorpusEntryID], constants.PostureDoctrine, cases.IngressDirect}

	rep := buildOK(t, runs)
	gotTotal := 0
	var lines int
	for _, line := range rep.Recall {
		lines++
		k := key{line.Category, line.Posture, line.Ingress}
		if line.Total != wantTotal[k] {
			t.Errorf("%v: Total = %d, want %d", k, line.Total, wantTotal[k])
		}
		wantBlocked := line.Total
		if k == failedKey {
			wantBlocked--
		}
		if line.Blocked != wantBlocked {
			t.Errorf("%v: Blocked = %d, want %d", k, line.Blocked, wantBlocked)
		}
		gotTotal += line.Total
	}
	if lines != len(wantTotal) {
		t.Errorf("recall lines = %d, want %d", lines, len(wantTotal))
	}
	if gotTotal != wantCases {
		t.Errorf("recall total = %d, want %d", gotTotal, wantCases)
	}
}

func TestBuildRecallHashIsEmptyOnRefusalOnlyIngresses(t *testing.T) {
	rep := buildOK(t, fourRuns(t))
	signed := map[cases.Ingress]bool{cases.IngressDirect: true, cases.IngressMCP: true, cases.IngressA2A: true}
	var checked int
	for _, line := range rep.Recall {
		checked++
		if signed[line.Ingress] {
			if line.DoctrineBundleHash != testBundleHash {
				t.Errorf("%s %s %s: hash = %q, want %q", line.Category, line.Posture, line.Ingress, line.DoctrineBundleHash, testBundleHash)
			}
			continue
		}
		if line.DoctrineBundleHash != "" {
			t.Errorf("%s %s %s: hash = %q, want empty on a refusal-only ingress", line.Category, line.Posture, line.Ingress, line.DoctrineBundleHash)
		}
	}
	if checked == 0 {
		t.Fatal("no recall lines")
	}
}

func TestMarkdownRendersCoverageAndRecallTables(t *testing.T) {
	rep := &Report{
		SuiteID:      cases.SuiteID,
		SuiteVersion: cases.SuiteVersion,
		Cells: []Cell{
			{Row: 1, Ingress: cases.IngressDirect, Posture: "doctrine", Status: CellCovered},
			{Row: 1, Ingress: cases.IngressDirect, Posture: "consensus", Status: CellPartial},
			{Row: 1, Ingress: cases.IngressDirect, Posture: "ratify", Status: CellNotApplicable},
			{Row: 1, Ingress: cases.IngressDirect, Posture: constants.PostureNotary, Status: CellMissing,
				Reason: "CONFORMANCE_FAILURE_CLASS_CANARY_DELTA"},
			{Row: 2, Ingress: cases.IngressA2A, Posture: "doctrine", Status: CellNotExpressible, Reason: "every A2A call is a mutation"},
			{Row: 2, Ingress: cases.IngressA2A, Posture: "consensus", Status: CellNotExpressible, Reason: "every A2A call is a mutation"},
			{Row: 2, Ingress: cases.IngressA2A, Posture: "ratify", Status: CellNotExpressible, Reason: "every A2A call is a mutation"},
			{Row: 2, Ingress: cases.IngressA2A, Posture: constants.PostureNotary, Status: CellNotExpressible, Reason: "every A2A call is a mutation"},
		},
		Recall: []Recall{
			{Category: doctrinecorpus.CategoryDestructiveDelete, Posture: "doctrine", Ingress: cases.IngressDirect,
				Blocked: 3, Total: 4, DoctrineBundleHash: "abc123"},
			{Category: doctrinecorpus.CategoryDestructiveDelete, Posture: "doctrine", Ingress: cases.IngressDispatchEmbedded,
				Blocked: 0, Total: 2},
		},
	}
	want := "| Row | Ingress | doctrine | consensus | ratify | notary |\n" +
		"| --- | --- | --- | --- | --- | --- |\n" +
		"| 1 | DIRECT | COVERED | PARTIAL | NOT_APPLICABLE | MISSING (CONFORMANCE_FAILURE_CLASS_CANARY_DELTA) |\n" +
		"| 2 | A2A | NOT_EXPRESSIBLE (every A2A call is a mutation) | NOT_EXPRESSIBLE (every A2A call is a mutation) | NOT_EXPRESSIBLE (every A2A call is a mutation) | NOT_EXPRESSIBLE (every A2A call is a mutation) |\n" +
		"\n" +
		"| Category | Posture | Ingress | Blocked | Total | Doctrine bundle hash |\n" +
		"| --- | --- | --- | --- | --- | --- |\n" +
		"| destructive_delete | doctrine | DIRECT | 3 | 4 | abc123 |\n" +
		"| destructive_delete | doctrine | DISPATCH_EMBEDDED | 0 | 2 | - |\n"
	if got := rep.Markdown(); got != want {
		t.Fatalf("Markdown() mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}
