// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package report turns verified conformance runs into the coverage table of the
// governance conformance suite and the live L1 recall report. It reads only run
// records and their verdict sets.
package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// CellStatus is the coverage of one (catalogue row, ingress, posture) cell.
type CellStatus string

const (
	CellCovered        CellStatus = "COVERED"
	CellPartial        CellStatus = "PARTIAL"         // only attended cases unrun
	CellMissing        CellStatus = "MISSING"         // no run for the posture, or any case failed or invalid
	CellNotExpressible CellStatus = "NOT_EXPRESSIBLE" // a catalogue Gap
	CellNotApplicable  CellStatus = "NOT_APPLICABLE"  // the row does not apply to the posture
)

// l1RecallRows are the catalogue rows that feed the L1 recall report.
var l1RecallRows = map[int]bool{13: true, 14: true}

// postureColumn pairs a report posture name with its wire enum. The slice order is
// the column order of every report table.
type postureColumn struct {
	name    string
	posture evalv1.EvaluationGovernancePosture
}

var postureColumns = []postureColumn{
	{name: constants.PostureDoctrine, posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE},
	{name: constants.PostureConsensus, posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS},
	{name: constants.PostureRatify, posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY},
	{name: constants.PostureNotary, posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY},
}

// Cell is the coverage of one (row, ingress, posture). Reason is the gap reason for
// NOT_EXPRESSIBLE, the sorted distinct failure classes for MISSING, and empty otherwise.
type Cell struct {
	Row     int
	Ingress cases.Ingress
	Posture string // doctrine | consensus | ratify | notary
	Status  CellStatus
	Reason  string
}

// Recall counts the L1 doctrine rows of one (category, posture, ingress) that were
// blocked. DoctrineBundleHash is "" on ingresses that refuse before an envelope.
type Recall struct {
	Category           doctrinecorpus.Category
	Posture            string
	Ingress            cases.Ingress
	Blocked, Total     int
	DoctrineBundleHash string
}

// RunSummary is one verified run's identity and suite verdict.
type RunSummary struct {
	RunID        string
	Posture      string
	SuiteVerdict evalv1.ConformanceSuiteVerdict
}

// Report is the coverage and recall report of up to four verified runs.
type Report struct {
	SuiteID, SuiteVersion string
	Runs                  []RunSummary
	Cells                 []Cell
	Recall                []Recall
}

// VerifiedRun is one run record with the verdict set the verifier computed for it.
type VerifiedRun struct {
	Record   *evalv1.ConformanceRunRecord
	Verdicts *evalv1.ConformanceVerdictSet
}

// runView is one posture's verified run, indexed for the cell and recall passes.
type runView struct {
	summary  RunSummary
	verdicts map[string]*evalv1.ConformanceVerdict // by case id; nil when the suite is INVALID_EVIDENCE
	evidence map[string]*evalv1.ConformanceCaseEvidence
	// bundleHash is the doctrine bundle hash of every signed L1 rejection in the posture.
	bundleHash    string
	signedIngress map[cases.Ingress]bool
}

// Build fails with ErrConformanceRecordInvalid if two runs share a posture, a
// run's verdict set does not belong to its record, or L1 stages in one posture
// disagree on the doctrine bundle hash.
func Build(runs []VerifiedRun) (*Report, error) {
	all, gaps, err := cases.Catalogue()
	if err != nil {
		return nil, fmt.Errorf("conformance report: catalogue: %w", err)
	}
	corpus, err := doctrinecorpus.Load()
	if err != nil {
		return nil, fmt.Errorf("conformance report: load corpus: %w", err)
	}
	categoryOf := make(map[string]doctrinecorpus.Category, len(corpus.Entries))
	for _, e := range corpus.Entries {
		categoryOf[e.ID] = e.Category
	}

	views := make(map[string]*runView, len(runs))
	for _, run := range runs {
		name, view, err := indexRun(run, all)
		if err != nil {
			return nil, err
		}
		if _, dup := views[name]; dup {
			return nil, fmt.Errorf("conformance report: two runs for posture %s: %w", name, constants.ErrConformanceRecordInvalid)
		}
		views[name] = view
	}

	if err := collectBundleHashes(all, views); err != nil {
		return nil, err
	}
	cells, err := buildCells(all, gaps, views)
	if err != nil {
		return nil, err
	}
	recall, err := buildRecall(all, categoryOf, views)
	if err != nil {
		return nil, err
	}

	rep := &Report{SuiteID: cases.SuiteID, SuiteVersion: cases.SuiteVersion, Cells: cells, Recall: recall}
	for _, col := range postureColumns {
		if view, ok := views[col.name]; ok {
			rep.Runs = append(rep.Runs, view.summary)
		}
	}
	return rep, nil
}

// indexRun checks one run against the catalogue and returns its posture name and view.
// A run whose suite verdict is INVALID_EVIDENCE carries no per-case verdicts to check.
func indexRun(run VerifiedRun, all []cases.Case) (string, *runView, error) {
	record, set := run.Record, run.Verdicts
	if record == nil || set == nil {
		return "", nil, fmt.Errorf("conformance report: run without record or verdict set: %w", constants.ErrConformanceRecordInvalid)
	}
	if set.GetRunId() != record.GetRunId() || set.GetPosture() != record.GetPosture() {
		return "", nil, fmt.Errorf("conformance report: verdict set of run %s does not belong to its record: %w", record.GetRunId(), constants.ErrConformanceRecordInvalid)
	}
	if record.GetSuiteId() != cases.SuiteID || record.GetSuiteVersion() != cases.SuiteVersion {
		return "", nil, fmt.Errorf("conformance report: run %s suite %s@%s: %w", record.GetRunId(), record.GetSuiteId(), record.GetSuiteVersion(), constants.ErrConformanceRecordInvalid)
	}
	name, err := postureName(record.GetPosture())
	if err != nil {
		return "", nil, err
	}

	view := &runView{
		summary: RunSummary{
			RunID:        record.GetRunId(),
			Posture:      name,
			SuiteVerdict: set.GetSuiteVerdict(),
		},
		evidence:      make(map[string]*evalv1.ConformanceCaseEvidence, len(record.GetCases())),
		signedIngress: make(map[cases.Ingress]bool),
	}
	for _, ev := range record.GetCases() {
		if ev == nil {
			return "", nil, fmt.Errorf("conformance report: run %s: nil case evidence: %w", record.GetRunId(), constants.ErrConformanceRecordInvalid)
		}
		if _, dup := view.evidence[ev.GetCaseId()]; dup {
			return "", nil, fmt.Errorf("conformance report: run %s: duplicate evidence for case %s: %w", record.GetRunId(), ev.GetCaseId(), constants.ErrConformanceRecordInvalid)
		}
		view.evidence[ev.GetCaseId()] = ev
	}

	if set.GetSuiteVerdict() == evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE {
		return name, view, nil
	}
	applicable := make(map[string]bool)
	for _, c := range all {
		if c.Posture == record.GetPosture() {
			applicable[c.ID] = true
		}
	}
	view.verdicts = make(map[string]*evalv1.ConformanceVerdict, len(set.GetVerdicts()))
	for _, v := range set.GetVerdicts() {
		id := v.GetCaseId()
		if !applicable[id] {
			return "", nil, fmt.Errorf("conformance report: run %s: verdict for unknown case %q: %w", record.GetRunId(), id, constants.ErrConformanceRecordInvalid)
		}
		if _, dup := view.verdicts[id]; dup {
			return "", nil, fmt.Errorf("conformance report: run %s: duplicate verdict for case %s: %w", record.GetRunId(), id, constants.ErrConformanceRecordInvalid)
		}
		view.verdicts[id] = v
	}
	if len(view.verdicts) != len(applicable) {
		return "", nil, fmt.Errorf("conformance report: run %s: %d verdicts, want %d: %w", record.GetRunId(), len(view.verdicts), len(applicable), constants.ErrConformanceRecordInvalid)
	}
	return name, view, nil
}

// collectBundleHashes reads the doctrine bundle hash of every PASS L1 rejection in
// the recall rows, requires all of one posture's hashes to agree, and records which
// ingresses produced signed rejections.
func collectBundleHashes(all []cases.Case, views map[string]*runView) error {
	for _, col := range postureColumns {
		view, ok := views[col.name]
		if !ok || view.verdicts == nil {
			continue
		}
		for _, c := range all {
			if c.Posture != col.posture || !l1RecallRows[c.Row] || !isL1Rejection(c) {
				continue
			}
			if view.verdicts[c.ID].GetStatus() != evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
				continue
			}
			hash, err := l1BundleHash(view.evidence[c.ID])
			if err != nil {
				return fmt.Errorf("conformance report: case %s: %w", c.ID, err)
			}
			if view.bundleHash == "" {
				view.bundleHash = hash
			} else if view.bundleHash != hash {
				return fmt.Errorf("conformance report: posture %s: L1 stages disagree on doctrine bundle hash: %w", col.name, constants.ErrConformanceRecordInvalid)
			}
			view.signedIngress[c.Ingress] = true
		}
	}
	return nil
}

// isL1Rejection reports whether a case expects a REJECTED outcome with the L1 doctrine code.
func isL1Rejection(c cases.Case) bool {
	return len(c.Expect) > 0 && c.Expect[0].Kind == cases.ExpectRejected &&
		c.Expect[0].RejectionCode == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE
}

// l1BundleHash returns the doctrine bundle hash of the single L1_DOCTRINE stage on
// observations[0].rejection.receipt. A missing, duplicate or empty stage is invalid.
func l1BundleHash(ev *evalv1.ConformanceCaseEvidence) (string, error) {
	obs := ev.GetObservations()
	if len(obs) == 0 {
		return "", fmt.Errorf("no observation: %w", constants.ErrConformanceRecordInvalid)
	}
	var hash string
	var stages int
	for _, st := range obs[0].GetRejection().GetReceipt().GetDeterministicStageEvidence() {
		if st.GetKind() != operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE {
			continue
		}
		stages++
		hash = st.GetDoctrineBundleHash()
	}
	if stages != 1 || hash == "" {
		return "", fmt.Errorf("L1 stages %d with bundle hash %q: %w", stages, hash, constants.ErrConformanceRecordInvalid)
	}
	return hash, nil
}

type cellKey struct {
	row     int
	ingress cases.Ingress
}

// buildCells returns one cell per (row, ingress) and posture, rows sorted by row then ingress.
func buildCells(all []cases.Case, gaps []cases.Gap, views map[string]*runView) ([]Cell, error) {
	keySet := make(map[cellKey]bool)
	for _, c := range all {
		keySet[cellKey{c.Row, c.Ingress}] = true
	}
	for _, g := range gaps {
		keySet[cellKey{g.Row, g.Ingress}] = true
	}
	keys := make([]cellKey, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].row != keys[j].row {
			return keys[i].row < keys[j].row
		}
		return keys[i].ingress < keys[j].ingress
	})

	var cells []Cell
	for _, k := range keys {
		for _, col := range postureColumns {
			cell, err := cellFor(k, col, all, gaps, views[col.name])
			if err != nil {
				return nil, err
			}
			cells = append(cells, cell)
		}
	}
	return cells, nil
}

// cellFor applies the cell rules in order: gap, not applicable, no run, invalid run,
// failed or invalid case, unrun attended case, covered.
func cellFor(k cellKey, col postureColumn, all []cases.Case, gaps []cases.Gap, view *runView) (Cell, error) {
	cell := Cell{Row: k.row, Ingress: k.ingress, Posture: col.name}
	for _, g := range gaps {
		if g.Row == k.row && g.Ingress == k.ingress {
			cell.Status, cell.Reason = CellNotExpressible, g.Reason
			return cell, nil
		}
	}

	var inCell []cases.Case
	for _, c := range all {
		if c.Row == k.row && c.Ingress == k.ingress && c.Posture == col.posture {
			inCell = append(inCell, c)
		}
	}
	if len(inCell) == 0 {
		cell.Status = CellNotApplicable
		return cell, nil
	}
	if view == nil {
		cell.Status, cell.Reason = CellMissing, "no verified run for posture"
		return cell, nil
	}
	if view.summary.SuiteVerdict == evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE {
		cell.Status, cell.Reason = CellMissing, "suite verdict INVALID_EVIDENCE"
		return cell, nil
	}

	var classes []string
	var unavailable bool
	for _, c := range inCell {
		v, ok := view.verdicts[c.ID]
		if !ok {
			return Cell{}, fmt.Errorf("conformance report: run %s: no verdict for case %s: %w", view.summary.RunID, c.ID, constants.ErrConformanceRecordInvalid)
		}
		switch v.GetStatus() {
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS:
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
			evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE:
			classes = append(classes, v.GetFailureClass().String())
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE:
			unavailable = true
		default:
			return Cell{}, fmt.Errorf("conformance report: case %s: unknown verdict status %s: %w", c.ID, v.GetStatus(), constants.ErrConformanceRecordInvalid)
		}
	}
	switch {
	case len(classes) > 0:
		cell.Status, cell.Reason = CellMissing, strings.Join(distinctSorted(classes), ", ")
	case unavailable:
		cell.Status = CellPartial
	default:
		cell.Status = CellCovered
	}
	return cell, nil
}

type recallKey struct {
	category doctrinecorpus.Category
	posture  string
	ingress  cases.Ingress
}

// buildRecall counts the rows 13 and 14 cases per (category, posture, ingress) that
// PASS. Lines are ordered by category, posture, then ingress.
func buildRecall(all []cases.Case, categoryOf map[string]doctrinecorpus.Category, views map[string]*runView) ([]Recall, error) {
	lines := make(map[recallKey]*Recall)
	ingressSet := make(map[cases.Ingress]bool)
	for _, c := range all {
		if !l1RecallRows[c.Row] {
			continue
		}
		category, ok := categoryOf[c.CorpusEntryID]
		if !ok {
			return nil, fmt.Errorf("conformance report: case %s has no corpus category: %w", c.ID, constants.ErrConformanceRecordInvalid)
		}
		name, err := postureName(c.Posture)
		if err != nil {
			return nil, err
		}
		ingressSet[c.Ingress] = true
		key := recallKey{category: category, posture: name, ingress: c.Ingress}
		line, ok := lines[key]
		if !ok {
			line = &Recall{Category: category, Posture: name, Ingress: c.Ingress}
			if view := views[name]; view != nil && view.signedIngress[c.Ingress] {
				line.DoctrineBundleHash = view.bundleHash
			}
			lines[key] = line
		}
		line.Total++
		if view := views[name]; view != nil && view.verdicts[c.ID].GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
			line.Blocked++
		}
	}

	ingresses := make([]cases.Ingress, 0, len(ingressSet))
	for in := range ingressSet {
		ingresses = append(ingresses, in)
	}
	sort.Slice(ingresses, func(i, j int) bool { return ingresses[i] < ingresses[j] })

	var recall []Recall
	for _, category := range doctrinecorpus.Categories() {
		for _, col := range postureColumns {
			for _, in := range ingresses {
				if line, ok := lines[recallKey{category: category, posture: col.name, ingress: in}]; ok {
					recall = append(recall, *line)
				}
			}
		}
	}
	return recall, nil
}

// postureName maps a wire posture to its report name.
func postureName(p evalv1.EvaluationGovernancePosture) (string, error) {
	for _, col := range postureColumns {
		if col.posture == p {
			return col.name, nil
		}
	}
	return "", fmt.Errorf("conformance report: posture %s: %w", p, constants.ErrConformancePostureUnknown)
}

// distinctSorted returns the sorted distinct values of in.
func distinctSorted(in []string) []string {
	sorted := append([]string(nil), in...)
	sort.Strings(sorted)
	var out []string
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
