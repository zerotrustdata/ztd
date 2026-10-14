// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	testSmokeTxFile = "tx-file-edit"
	testSmokeTxDoc  = "tx-document"
)

// writeSmokeReportFixture writes a `report all` output directory. The defaults
// satisfy every INV-REL-SMOKE-04 requirement, so each test breaks one thing.
type smokeFixture struct {
	receiptRows      []string
	commitmentRows   []string
	mutationRows     []string
	verificationRows []string
}

func completeSmokeFixture() smokeFixture {
	completed := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED.String()
	return smokeFixture{
		receiptRows: []string{
			testSmokeTxFile + ",FILE_EDIT," + completed,
			testSmokeTxDoc + ",DOCUMENT_UPDATE," + completed,
		},
		commitmentRows: []string{"1,tx,hash"},
		mutationRows:   []string{"1,file"},
		verificationRows: []string{
			"commitment_chain,commitment_ledger,all,PASS,ok",
			"git_merkle_root,ledger,HEAD,PASS,ok",
			"file_mutation_linkage,audit_store,all,PASS,ok",
			"receipt_commitment_crosslink,audit_store+commitment_ledger,all,PASS,ok",
		},
	}
}

func writeSmokeReport(t *testing.T, dir string, f smokeFixture) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		constants.ReportReceiptsFilename:      "transaction_id,action_type,status\n" + strings.Join(f.receiptRows, "\n") + "\n",
		constants.ReportCommitmentsFilename:   "seq,transaction_id,hash\n" + strings.Join(f.commitmentRows, "\n") + "\n",
		constants.ReportFileMutationsFilename: "id,file\n" + strings.Join(f.mutationRows, "\n") + "\n",
		constants.ReportVerificationFilename:  "check,scope,subject,result,detail\n" + strings.Join(f.verificationRows, "\n") + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimRight(body, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEvaluateSmokeReportAcceptsCompleteWorkerExport(t *testing.T) {
	dir := testutil.TempDir(t)
	writeSmokeReport(t, dir, completeSmokeFixture())

	ev, err := evaluateSmokeReport("worker", dir)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(ev.Failures) != 0 {
		t.Fatalf("unexpected failures: %v", ev.Failures)
	}
	if len(ev.PassedChecks) != len(smokeRequiredChecks) {
		t.Fatalf("passed checks = %v, want all %d required", ev.PassedChecks, len(smokeRequiredChecks))
	}
	ev.requireCompletedReceipt(releaseSmokeFileActionType, testSmokeTxFile)
	if len(ev.Failures) != 0 {
		t.Fatalf("file receipt not correlated: %v", ev.Failures)
	}
}

func TestEvaluateSmokeReportRejectsFailAndSkippedRows(t *testing.T) {
	f := completeSmokeFixture()
	f.verificationRows[0] = "commitment_chain,commitment_ledger,abc,FAIL,prior hash mismatch"
	f.verificationRows[1] = "git_merkle_root,ledger,HEAD,SKIPPED,ledger unavailable"
	dir := testutil.TempDir(t)
	writeSmokeReport(t, dir, f)

	ev, err := evaluateSmokeReport("worker", dir)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	joined := strings.Join(ev.Failures, "\n")
	for _, want := range []string{"FAIL commitment_chain", "SKIPPED git_merkle_root", "required check not PASS: commitment_chain", "required check not PASS: git_merkle_root"} {
		if !strings.Contains(joined, want) {
			t.Errorf("failures missing %q:\n%s", want, joined)
		}
	}
}

func TestEvaluateSmokeReportRejectsMissingRequiredCheck(t *testing.T) {
	f := completeSmokeFixture()
	f.verificationRows = f.verificationRows[:3]
	dir := testutil.TempDir(t)
	writeSmokeReport(t, dir, f)

	ev, err := evaluateSmokeReport("gateway", dir)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !strings.Contains(strings.Join(ev.Failures, "\n"), "required check missing: receipt_commitment_crosslink") {
		t.Fatalf("missing-check failure not reported: %v", ev.Failures)
	}
}

func TestEvaluateSmokeReportRejectsEmptyEvidence(t *testing.T) {
	f := completeSmokeFixture()
	f.receiptRows = nil
	f.commitmentRows = nil
	f.mutationRows = nil
	dir := testutil.TempDir(t)
	writeSmokeReport(t, dir, f)

	ev, err := evaluateSmokeReport("worker", dir)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	for _, want := range []string{"receipts.csv is empty", "commitments.csv is empty", "file_mutations.csv is empty"} {
		if !strings.Contains(strings.Join(ev.Failures, "\n"), want) {
			t.Errorf("failures missing %q: %v", want, ev.Failures)
		}
	}
}

func TestEvaluateSmokeReportRejectsGatewayWithoutFileMutations(t *testing.T) {
	f := completeSmokeFixture()
	f.mutationRows = nil
	dir := testutil.TempDir(t)
	writeSmokeReport(t, dir, f)

	ev, err := evaluateSmokeReport("gateway", dir)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !strings.Contains(strings.Join(ev.Failures, "\n"), "file_mutations.csv is empty") {
		t.Fatalf("gateway report accepted empty file_mutations: %v", ev.Failures)
	}
}

func TestRequireCompletedReceiptFailsClosedWhenNotExported(t *testing.T) {
	dir := testutil.TempDir(t)
	writeSmokeReport(t, dir, completeSmokeFixture())
	ev, err := evaluateSmokeReport("gateway", dir)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	ev.requireCompletedReceipt(releaseSmokeDocAction, "tx-not-exported")
	if len(ev.Failures) != 1 || !strings.Contains(ev.Failures[0], "tx-not-exported") {
		t.Fatalf("expected one uncorrelated receipt failure, got %v", ev.Failures)
	}
}

func TestEvaluateSmokeReportFailsOnMalformedCSV(t *testing.T) {
	dir := testutil.TempDir(t)
	writeSmokeReport(t, dir, completeSmokeFixture())
	if err := os.WriteFile(filepath.Join(dir, constants.ReportReceiptsFilename), []byte("a,b,c\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := evaluateSmokeReport("worker", dir)
	if err == nil || !strings.Contains(err.Error(), "release smoke evidence") {
		t.Fatalf("err = %v, want evidence parse error", err)
	}
}

func TestParseDeploySessionReadsOnlyThisWorker(t *testing.T) {
	out := "  localhost:/tmp/other  session other-session\n  localhost:/tmp/run/worker  session worker-session-123\n"
	got, err := parseSmokeDeploySession(out, "/tmp/run/worker")
	if err != nil || got != "worker-session-123" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseSmokeDeploySession(out, "/tmp/absent"); err == nil {
		t.Fatal("missing worker should fail")
	}
}

func TestRedactArgvHidesMasterKeyPath(t *testing.T) {
	got := redactArgv([]string{"ztd", "report", "--master-key-file", "/run/secrets/master.key"})
	if got[3] != "<run-master-key>" || strings.Contains(strings.Join(got, " "), "master.key") {
		t.Fatalf("key path not redacted: %v", got)
	}
}

func TestExecuteRunsCleanupsAndWritesFailedSummary(t *testing.T) {
	runDir := filepath.Join(testutil.TempDir(t), "new-run")
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: runDir})
	var cleaned []string
	phases := []smokePhase{
		{"start", func(context.Context) error {
			r.addCleanup("first", func(context.Context) error { cleaned = append(cleaned, "first"); return nil })
			r.addCleanup("second", func(context.Context) error { cleaned = append(cleaned, "second"); return nil })
			return nil
		}},
		{"fail", func(context.Context) error { return errors.New("scenario broke") }},
		{"never", func(context.Context) error { t.Fatal("phase after failure ran"); return nil }},
	}
	err := r.execute(context.Background(), phases)
	if err == nil || !strings.Contains(err.Error(), "scenario broke") {
		t.Fatalf("err = %v, want wrapped step failure", err)
	}
	if strings.Join(cleaned, ",") != "second,first" {
		t.Fatalf("cleanup order = %v, want LIFO", cleaned)
	}
	summary, readErr := os.ReadFile(filepath.Join(runDir, "evidence", releaseSmokeSummaryName))
	if readErr != nil {
		t.Fatalf("summary not written: %v", readErr)
	}
	if !strings.Contains(string(summary), `"result": "FAIL"`) {
		t.Fatalf("summary does not record FAIL: %s", summary)
	}
}

func TestExecuteJoinsCleanupFailureWithStepFailure(t *testing.T) {
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: filepath.Join(testutil.TempDir(t), "new-run")})
	r.addCleanup("gateway stop", func(context.Context) error { return errors.New("gw stop refused") })
	err := r.execute(context.Background(), []smokePhase{{"fail", func(context.Context) error { return errors.New("boom") }}})
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "gw stop refused") {
		t.Fatalf("err = %v, want both step and cleanup failures", err)
	}
}

func TestExecuteRefusesExistingRootWithoutChangingEvidence(t *testing.T) {
	runDir := testutil.TempDir(t)
	evidenceDir := filepath.Join(runDir, "evidence")
	if err := os.Mkdir(evidenceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(evidenceDir, releaseSmokeSummaryName)
	want := []byte("preserve existing evidence\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: runDir})
	err := r.execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected existing root to be rejected")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(want) {
		t.Fatalf("existing evidence changed: %q", got)
	}
}

func TestClaimRunRootRejectsSymlink(t *testing.T) {
	parent := testutil.TempDir(t)
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "linked")
	testutil.Symlink(t, target, link)
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: link})
	if err := r.claimRunRoot(); err == nil {
		t.Fatal("symlink root must be rejected")
	}
	if r.ownedRoot {
		t.Fatal("symlink root marked owned")
	}
}

func TestVerifyEvidenceFailsRunOnUnverifiedWorkerExport(t *testing.T) {
	runDir := testutil.TempDir(t)
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: runDir})
	r.reportsDir = filepath.Join(runDir, "reports")
	r.fileTx = testSmokeTxFile
	good := completeSmokeFixture()
	bad := completeSmokeFixture()
	bad.verificationRows[2] = "file_mutation_linkage,audit_store,all,SKIPPED,audit store not configured"
	writeSmokeReport(t, filepath.Join(r.reportsDir, "worker"), bad)
	writeSmokeReport(t, filepath.Join(r.reportsDir, "gateway"), good)
	r.summary.Scenarios = []releaseSmokeScenario{{Name: "document-merge", TransactionIDs: []string{testSmokeTxDoc}}}

	err := r.verifyEvidence(context.Background())
	if err == nil || !strings.Contains(err.Error(), "file_mutation_linkage") {
		t.Fatalf("err = %v, want evidence verification failure", err)
	}
	if len(r.summary.Reports) != 2 || len(r.summary.Reports[1].Failures) != 0 {
		t.Fatalf("gateway report should pass while worker fails: %+v", r.summary.Reports)
	}
}

func TestZtdCommandFailurePropagatesWithExitCode(t *testing.T) {
	bin, args := "cmd.exe", []string{"/c", "exit 1"}
	if runtime.GOOS != "windows" {
		// /bin/false on Linux, /usr/bin/false on macOS.
		path, err := exec.LookPath("false")
		if err != nil {
			t.Fatalf("find false: %v", err)
		}
		bin, args = path, nil
	}
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: testutil.TempDir(t), ZtdBin: bin})
	r.logDir = filepath.Join(r.opts.RunDir, "logs")
	_, err := r.ztd(context.Background(), "always-fails", r.opts.RunDir, args...)
	if err == nil {
		t.Fatal("expected command failure")
	}
	if len(r.summary.Commands) != 1 || r.summary.Commands[0].ExitCode != 1 {
		t.Fatalf("command record = %+v, want exit 1", r.summary.Commands)
	}
}

func TestReleaseSmokeCmdAnchorsRelativeBinaryAndRoot(t *testing.T) {
	var got releaseSmokeOptions
	cmd := releaseSmokeCmdWithRunner(func(_ context.Context, opts releaseSmokeOptions) error {
		got = opts
		return nil
	})
	cmd.SetArgs([]string{"--binary", filepath.Join("candidate", "ztd"), "--root", filepath.Join("runs", "smoke")})
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !filepath.IsAbs(got.ZtdBin) || !strings.HasSuffix(got.ZtdBin, filepath.Join("candidate", "ztd")) {
		t.Fatalf("binary = %q, want absolute path ending in candidate/ztd", got.ZtdBin)
	}
	if !filepath.IsAbs(got.RunDir) {
		t.Fatalf("root = %q, want absolute path", got.RunDir)
	}
}
