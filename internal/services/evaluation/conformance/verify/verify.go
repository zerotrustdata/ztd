// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// executorEmbedded and executorOutbound are the ConformanceCaseEvidence.executor
// values, equal to cases.ExecutorEmbedded and cases.ExecutorOutbound.
const (
	executorEmbedded = string(cases.ExecutorEmbedded)
	executorOutbound = string(cases.ExecutorOutbound)
)

// VerifyPersisted checks the record digest, decodes the canonical record and
// verifies it. A digest mismatch or a non-canonical record gives a verdict set
// whose suite verdict is INVALID_EVIDENCE, never an error.
func VerifyPersisted(raw []byte, digestHex string) (*evalv1.ConformanceVerdictSet, error) {
	return verifyPersisted(raw, digestHex, Verify)
}

// verifyPersisted is VerifyPersisted with the record verifier injected, so tests
// can verify a small case list instead of the full catalogue.
func verifyPersisted(raw []byte, digestHex string, verifyRecord func(*evalv1.ConformanceRunRecord) (*evalv1.ConformanceVerdictSet, error)) (*evalv1.ConformanceVerdictSet, error) {
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digestHex {
		return digestMismatchSet(raw), nil
	}
	record := &evalv1.ConformanceRunRecord{}
	if err := evalv1.UnmarshalCanonical(raw, record); err != nil {
		//nolint:nilerr // a non-canonical record is an INVALID_EVIDENCE verdict, not an error
		return invalidSet(nil), nil
	}
	return verifyRecord(record)
}

// Verify recomputes every verdict for the record's posture against the catalogue.
func Verify(record *evalv1.ConformanceRunRecord) (*evalv1.ConformanceVerdictSet, error) {
	if !isRealPosture(record.GetPosture()) {
		return invalidSet(record), nil
	}
	applicable, err := cases.Applicable(record.GetPosture())
	if err != nil {
		return nil, fmt.Errorf("conformance verify: load applicable cases: %w", err)
	}
	return verifyAgainst(record, applicable)
}

// verifyAgainst is Verify with an explicit case list (tests use small lists).
func verifyAgainst(record *evalv1.ConformanceRunRecord, applicable []cases.Case) (*evalv1.ConformanceVerdictSet, error) {
	if !isRealPosture(record.GetPosture()) {
		return invalidSet(record), nil
	}
	check, err := newRecordCheck(record, applicable)
	if err != nil {
		// A record problem carries no per-case verdicts: the record is not evidence.
		//nolint:nilerr // a record problem is an INVALID_EVIDENCE verdict, not an error
		return invalidSet(record), nil
	}

	set := &evalv1.ConformanceVerdictSet{
		RunId:        record.GetRunId(),
		Posture:      record.GetPosture(),
		SuiteVerdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS,
	}
	evidence := make(map[string]*evalv1.ConformanceCaseEvidence, len(record.GetCases()))
	for _, ev := range record.GetCases() {
		evidence[ev.GetCaseId()] = ev
	}
	for _, c := range applicable {
		verdict, err := check.verifyCase(c, evidence[c.ID])
		if err != nil {
			return nil, fmt.Errorf("conformance verify: case %s: %w", c.ID, err)
		}
		set.Verdicts = append(set.Verdicts, verdict)
	}
	set.SuiteVerdict = suiteVerdict(set.Verdicts)
	return set, nil
}

// suiteVerdict folds case verdicts into the suite verdict. Any invalid evidence
// dominates, then any failure, then any unavailable (not-run) case.
func suiteVerdict(verdicts []*evalv1.ConformanceVerdict) evalv1.ConformanceSuiteVerdict {
	var fail, unavailable bool
	for _, v := range verdicts {
		switch v.GetStatus() {
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE:
			return evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL:
			fail = true
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE:
			unavailable = true
		}
	}
	switch {
	case fail:
		return evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_FAIL
	case unavailable:
		return evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE
	default:
		return evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS
	}
}

// digestMismatchSet is the verdict for bytes that do not match their digest. The
// run id is recorded only when the bytes still decode as a canonical record.
func digestMismatchSet(raw []byte) *evalv1.ConformanceVerdictSet {
	set := invalidSet(nil)
	record := &evalv1.ConformanceRunRecord{}
	if err := evalv1.UnmarshalCanonical(raw, record); err == nil {
		set.RunId = record.GetRunId()
	}
	set.Verdicts = []*evalv1.ConformanceVerdict{{
		Status:       evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE,
		FailureClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_RECORD_DIGEST_MISMATCH,
	}}
	return set
}

// SealedRecordTamperedSet is the verdict for a record or digest sidecar whose
// sealed bytes fail authentication under the open runtime vault. The stored bytes
// changed, the same fact digestMismatchSet reports. The bytes cannot be decoded,
// so the caller names the run.
func SealedRecordTamperedSet(runID string) *evalv1.ConformanceVerdictSet {
	set := digestMismatchSet(nil)
	set.RunId = runID
	return set
}

// invalidSet is the suite verdict for a record that fails a record check. It
// carries no case verdicts.
func invalidSet(record *evalv1.ConformanceRunRecord) *evalv1.ConformanceVerdictSet {
	return &evalv1.ConformanceVerdictSet{
		RunId:        record.GetRunId(),
		Posture:      record.GetPosture(),
		SuiteVerdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE,
	}
}

// isRealPosture reports whether posture is one of the four governance postures.
func isRealPosture(posture evalv1.EvaluationGovernancePosture) bool {
	switch posture {
	case evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY:
		return true
	default:
		return false
	}
}

// recordCheck holds the record-level facts every case verdict needs: the anchor
// set and the runtime each executor maps to.
type recordCheck struct {
	record   *evalv1.ConformanceRunRecord
	anchors  *oracle.AnchorSet
	runtimes map[string]string
}

// newRecordCheck runs the record checks of the verifier. Any failure is
// returned as an error with constants.ErrConformanceRecordInvalid (or the anchor
// or digest sentinel), and the caller turns it into a suite INVALID_EVIDENCE.
func newRecordCheck(record *evalv1.ConformanceRunRecord, applicable []cases.Case) (*recordCheck, error) {
	if record.GetSuiteId() != cases.SuiteID || record.GetSuiteVersion() != cases.SuiteVersion {
		return nil, fmt.Errorf("conformance verify: suite %s@%s: %w", record.GetSuiteId(), record.GetSuiteVersion(), constants.ErrConformanceRecordInvalid)
	}
	if record.GetDoctrineCorpusSha256() != doctrinecorpus.SHA256() {
		return nil, fmt.Errorf("conformance verify: doctrine corpus digest: %w", constants.ErrConformanceRecordInvalid)
	}

	runtimes, anchors, err := checkAnchors(record)
	if err != nil {
		return nil, err
	}
	check := &recordCheck{record: record, anchors: anchors, runtimes: runtimes}

	if err := checkCaseSet(record, applicable, runtimes); err != nil {
		return nil, err
	}
	if err := checkQuiescence(record, applicable, runtimes, anchors); err != nil {
		return nil, err
	}
	return check, nil
}

// checkAnchors requires exactly one Gateway anchor and at most one outbound
// anchor, and maps each executor to its runtime id.
func checkAnchors(record *evalv1.ConformanceRunRecord) (map[string]string, *oracle.AnchorSet, error) {
	anchors, err := oracle.NewAnchorSet(record.GetAnchors())
	if err != nil {
		return nil, nil, fmt.Errorf("conformance verify: anchors: %w", err)
	}
	var gateway int
	var outbound []string
	for _, anchor := range record.GetAnchors() {
		if anchor.GetRuntimeId() == oracle.GatewayRuntimeID {
			gateway++
		} else {
			outbound = append(outbound, anchor.GetRuntimeId())
		}
	}
	if gateway != 1 || len(outbound) > 1 {
		return nil, nil, fmt.Errorf("conformance verify: anchors name %d gateway and %d outbound runtimes: %w", gateway, len(outbound), constants.ErrConformanceAnchorInvalid)
	}
	runtimes := map[string]string{executorEmbedded: oracle.GatewayRuntimeID}
	if len(outbound) == 1 {
		runtimes[executorOutbound] = outbound[0]
	}
	return runtimes, anchors, nil
}

// checkCaseSet requires the record's cases to be exactly the applicable cases,
// each once, each run on the executor its ingress names.
func checkCaseSet(record *evalv1.ConformanceRunRecord, applicable []cases.Case, runtimes map[string]string) error {
	if len(record.GetCases()) != len(applicable) {
		return fmt.Errorf("conformance verify: record holds %d cases, want %d: %w", len(record.GetCases()), len(applicable), constants.ErrConformanceRecordInvalid)
	}
	byID := make(map[string]*evalv1.ConformanceCaseEvidence, len(record.GetCases()))
	for _, ev := range record.GetCases() {
		if ev == nil {
			return fmt.Errorf("conformance verify: nil case evidence: %w", constants.ErrConformanceRecordInvalid)
		}
		if _, exists := byID[ev.GetCaseId()]; exists {
			return fmt.Errorf("conformance verify: duplicate case %s: %w", ev.GetCaseId(), constants.ErrConformanceRecordInvalid)
		}
		byID[ev.GetCaseId()] = ev
	}
	for _, c := range applicable {
		ev, ok := byID[c.ID]
		if !ok {
			return fmt.Errorf("conformance verify: case %s missing: %w", c.ID, constants.ErrConformanceRecordInvalid)
		}
		executor := string(c.Ingress.Executor())
		if ev.GetExecutor() != executor {
			return fmt.Errorf("conformance verify: case %s ran on %q, want %q: %w", c.ID, ev.GetExecutor(), executor, constants.ErrConformanceRecordInvalid)
		}
		if _, ok := runtimes[executor]; !ok {
			return fmt.Errorf("conformance verify: case %s needs executor %q with no pinned anchor: %w", c.ID, executor, constants.ErrConformanceAnchorInvalid)
		}
	}
	return nil
}

// checkQuiescence requires one quiescence pair per executor used by any applicable
// case. Each pair must hold two executed sentinels on that executor's runtime
// whose ledger links are adjacent.
func checkQuiescence(record *evalv1.ConformanceRunRecord, applicable []cases.Case, runtimes map[string]string, anchors *oracle.AnchorSet) error {
	required := make(map[string]bool)
	for _, c := range applicable {
		required[string(c.Ingress.Executor())] = true
	}
	seen := make(map[string]bool, len(required))
	for _, q := range record.GetQuiescence() {
		executor := q.GetExecutor()
		if !required[executor] || seen[executor] {
			return fmt.Errorf("conformance verify: quiescence for executor %q is extra or duplicate: %w", executor, constants.ErrConformanceRecordInvalid)
		}
		seen[executor] = true
		runtime := runtimes[executor]
		first, err := classifyExecuted(anchors, runtime, q.GetFirst())
		if err != nil {
			return fmt.Errorf("conformance verify: quiescence first on %s: %w", executor, err)
		}
		second, err := classifyExecuted(anchors, runtime, q.GetSecond())
		if err != nil {
			return fmt.Errorf("conformance verify: quiescence second on %s: %w", executor, err)
		}
		if err := oracle.VerifyBracket(first, second, nil); err != nil {
			return fmt.Errorf("conformance verify: quiescence on %s: %w", executor, err)
		}
	}
	if len(seen) != len(required) {
		return fmt.Errorf("conformance verify: quiescence covers %d executors, want %d: %w", len(seen), len(required), constants.ErrConformanceStackNotQuiescent)
	}
	return nil
}
