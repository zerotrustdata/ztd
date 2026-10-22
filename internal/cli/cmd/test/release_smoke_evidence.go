// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// smokeRequiredChecks are the verification checks INV-REL-SMOKE-04 requires to
// PASS in every smoke report. Each must appear at least once.
var smokeRequiredChecks = []string{
	"commitment_chain",
	"git_merkle_root",
	"file_mutation_linkage",
	"event_receipt_commitment_crosslink",
}

// smokeReportEvidence is the typed outcome of checking one `report all` output
// directory against INV-REL-SMOKE-03 and INV-REL-SMOKE-04.
type smokeReportEvidence struct {
	Name             string   `json:"name"`
	Dir              string   `json:"dir"`
	EventReceiptRows int      `json:"event_receipt_rows"`
	CommitmentRows   int      `json:"commitment_rows"`
	FileMutationRows int      `json:"file_mutation_rows"`
	VerificationRows int      `json:"verification_rows"`
	FailedRows       int      `json:"failed_rows"`
	SkippedRows      int      `json:"skipped_rows"`
	PassedChecks     []string `json:"passed_checks"`
	Failures         []string `json:"failures,omitempty"`

	// eventReceiptTransactions maps each completed event receipt row's action type to its
	// transaction IDs, so scenario event receipts can be correlated to the export.
	eventReceiptTransactions map[string][]string
}

// hasCompletedEventReceipt reports whether the export holds a completed event receipt for
// txID, the transaction the scenario observed through the Gateway.
func (ev *smokeReportEvidence) hasCompletedEventReceipt(actionType, txID string) bool {
	return slices.Contains(ev.eventReceiptTransactions[actionType], txID)
}

// evaluateSmokeReport reads the CSV exports and the verification summary that
// `report all` wrote into dir. Every requirement is checked, and every failed
// requirement is recorded so the runner can report all of them at once.
func evaluateSmokeReport(name, dir string) (smokeReportEvidence, error) {
	ev := smokeReportEvidence{Name: name, Dir: dir, eventReceiptTransactions: map[string][]string{}}

	eventReceipts, err := readSmokeCSV(filepath.Join(dir, constants.ReportEventReceiptsFilename))
	if err != nil {
		return ev, err
	}
	ev.EventReceiptRows = len(eventReceipts.rows)
	completed := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED.String()
	for _, row := range eventReceipts.rows {
		if row["status"] == completed {
			ev.eventReceiptTransactions[row["action_type"]] = append(ev.eventReceiptTransactions[row["action_type"]], row["transaction_id"])
		}
	}

	commitments, err := readSmokeCSV(filepath.Join(dir, constants.ReportCommitmentsFilename))
	if err != nil {
		return ev, err
	}
	ev.CommitmentRows = len(commitments.rows)

	mutations, err := readSmokeCSV(filepath.Join(dir, constants.ReportFileMutationsFilename))
	if err != nil {
		return ev, err
	}
	ev.FileMutationRows = len(mutations.rows)

	verification, err := readSmokeCSV(filepath.Join(dir, constants.ReportVerificationFilename))
	if err != nil {
		return ev, err
	}
	ev.VerificationRows = len(verification.rows)
	ev.checkVerification(verification.rows)

	if ev.EventReceiptRows == 0 {
		ev.Failures = append(ev.Failures, constants.ReportEventReceiptsFilename+" is empty")
	}
	if ev.CommitmentRows == 0 {
		ev.Failures = append(ev.Failures, "commitments.csv is empty")
	}
	if ev.FileMutationRows == 0 {
		ev.Failures = append(ev.Failures, "file_mutations.csv is empty")
	}
	return ev, nil
}

// checkVerification applies the zero-FAIL, zero-SKIPPED rule to every row and
// requires each INV-REL-SMOKE-04 check to be present with only PASS results.
func (ev *smokeReportEvidence) checkVerification(rows []map[string]string) {
	for _, row := range rows {
		switch row["result"] {
		case "PASS":
		case "FAIL", "SKIPPED":
			if row["result"] == "FAIL" {
				ev.FailedRows++
			} else {
				ev.SkippedRows++
			}
			ev.Failures = append(ev.Failures, fmt.Sprintf("%s %s (%s): %s", row["result"], row["check"], row["subject"], row["detail"]))
		default:
			ev.Failures = append(ev.Failures, fmt.Sprintf("unknown result %q for %s", row["result"], row["check"]))
		}
	}
	for _, check := range smokeRequiredChecks {
		present, allPass := false, true
		for _, row := range rows {
			if row["check"] != check {
				continue
			}
			present = true
			if row["result"] != "PASS" {
				allPass = false
			}
		}
		switch {
		case !present:
			ev.Failures = append(ev.Failures, "required check missing: "+check)
		case !allPass:
			ev.Failures = append(ev.Failures, "required check not PASS: "+check)
		default:
			ev.PassedChecks = append(ev.PassedChecks, check)
		}
	}
}

// requireCompletedEventReceipt records a failure unless the export holds a completed
// event receipt of actionType for txID.
func (ev *smokeReportEvidence) requireCompletedEventReceipt(actionType, txID string) {
	if !ev.hasCompletedEventReceipt(actionType, txID) {
		ev.Failures = append(ev.Failures, fmt.Sprintf("no completed %s event receipt %s in %s export", actionType, txID, ev.Name))
	}
}

// smokeCSV is one parsed CSV file keyed by its header names.
type smokeCSV struct {
	rows []map[string]string
}

func readSmokeCSV(path string) (smokeCSV, error) {
	f, err := os.Open(path)
	if err != nil {
		return smokeCSV{}, fmt.Errorf("release smoke evidence %s: %w", path, err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return smokeCSV{}, fmt.Errorf("release smoke evidence parse %s: %w", path, err)
	}
	if len(records) == 0 {
		return smokeCSV{}, fmt.Errorf("release smoke evidence %s has no header", path)
	}
	header := records[0]
	out := smokeCSV{}
	for _, rec := range records[1:] {
		if len(rec) != len(header) {
			return smokeCSV{}, fmt.Errorf("release smoke evidence %s: row has %d fields, header has %d", path, len(rec), len(header))
		}
		row := make(map[string]string, len(header))
		for i, h := range header {
			row[strings.TrimSpace(h)] = rec[i]
		}
		out.rows = append(out.rows, row)
	}
	return out, nil
}
