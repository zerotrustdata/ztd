// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package testcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// conformanceLoopEvidenceName is the loop's own evidence file, next to the
// smoke summary.
const conformanceLoopEvidenceName = "conformance-loop.json"

// conformanceLoopReceiptPage is one audit_receipt_list page of the receipts a
// conformance run added to one Operator session.
const conformanceLoopReceiptPage = 100

// This Tier 3 test starts actual OS processes. The Gateway, both Operators, the
// stdio bridge and every conformance and report command are production code.
func TestConformanceLoopCrossProcess(t *testing.T) {
	for _, posture := range constants.AllPostures {
		t.Run(string(posture), func(t *testing.T) {
			// A short prefix keeps the run root's key-agent socket under the
			// 107-byte Unix socket limit; the run's budget leaves the posture's
			// deadline room for cleanup.
			r := newEvalLoopRun(t, "conf-", posture, conformanceLoopPostureTimeout-2*time.Minute)
			port, err := freeSmokePort()
			require.NoError(t, err)
			// The Gateway forwards A2A to the address each conformance run's stub listens on.
			st := &conformanceLoopState{a2aListen: "127.0.0.1:" + strconv.Itoa(port)}
			r.opts.GatewayArgs = []string{"--a2a-downstream-url", "http://" + st.a2aListen}
			ctx, cancel := context.WithTimeout(t.Context(), r.opts.Timeout)
			defer cancel()
			require.NoError(t, runEvalLoopPhases(ctx, t, r, conformanceLoopPhases(t, r, st)))
			require.Len(t, st.evidence.Runs, 2, "the Data Operator and the embedded Operator each ran the suite")
			if st.approvals != nil {
				assert.Empty(t, st.approvals.Errors())
			}
			if st.consensus != nil {
				assert.NotZero(t, st.consensus.DeliberationCount(), "conformance L2 proofs must come from the bound application")
			}
		})
	}
}

// conformanceLoopState is what one posture's stack hands between phases.
type conformanceLoopState struct {
	a2aListen string
	approvals *releaseSmokeCLIApprovalResponder
	owner     *conformanceLoopOwner
	consensus *evalLoopConsensusApp
	bridge    *drive.MCPStdio
	// started is when the first conformance run began; receipts the runs added
	// are read from it on.
	started time.Time
	// completed holds, per runtime, the completed receipts the runs added.
	completed map[string][]*models.EventReceiptRecord
	evidence  conformanceLoopEvidence
}

// conformanceLoopEvidence is the loop's typed evidence for one posture.
type conformanceLoopEvidence struct {
	Posture       string                       `json:"posture"`
	A2ADownstream string                       `json:"a2a_downstream"`
	Runs          []conformanceLoopRunEvidence `json:"runs"`
	AuditChains   []conformanceLoopAuditChain  `json:"audit_chains"`
}

// conformanceLoopRunEvidence is one conformance run and its offline re-verification.
type conformanceLoopRunEvidence struct {
	Executor     string `json:"executor"`
	OperatorID   string `json:"operator_id"`
	RunID        string `json:"run_id"`
	SuiteVerdict string `json:"suite_verdict"`
	Cases        int    `json:"cases"`
	Passed       int    `json:"passed"`
	Verified     bool   `json:"verified_offline"`
}

// conformanceLoopAuditChain is one Operator session's receipt chain, read through
// the stdio bridge before and after the conformance runs.
type conformanceLoopAuditChain struct {
	Runtime           string `json:"runtime"`
	OperatorSessionID string `json:"operator_session_id"`
	HeadBefore        string `json:"head_before"`
	HeadAfter         string `json:"head_after"`
	AddedReceipts     int    `json:"added_receipts"`
	AddedCompleted    int    `json:"added_completed"`
}

// conformanceLoopPhases is the isolated stack for r's posture: a Gateway with A2A
// ingress, the CLI owner, approvals while the stack is provisioned under ratify
// and notary, the bound consensus application under consensus and notary, and a
// Data Operator. Under ratify and notary the conformance runs are attended, and
// the owner answers them through the CLI approval flow.
func conformanceLoopPhases(t *testing.T, r *releaseSmokeRun, st *conformanceLoopState) []smokePhase {
	return []smokePhase{
		{"prepare", func(ctx context.Context) error {
			if err := r.prepare(ctx); err != nil {
				return err
			}
			r.summary.Ingress = []string{"MCP stdio bridge: ztd mcp stdio", "every conformance ingress: ztd eval conformance run"}
			st.evidence = conformanceLoopEvidence{Posture: string(r.opts.Posture), A2ADownstream: "http://" + st.a2aListen}
			return nil
		}},
		{"start isolated Gateway with A2A ingress", r.startGateway},
		{"enroll CLI owner", r.loginCLI},
		{"subscribe CLI approvals", func(context.Context) error {
			if r.opts.Posture.RequiresL3() {
				st.approvals = startReleaseSmokeCLIApprovalResponder(t, r)
				r.addCleanup("CLI approval responder", func(context.Context) error { st.approvals.Stop(); return nil })
			}
			return nil
		}},
		{"bind real consensus application", func(context.Context) error {
			if r.opts.Posture.RequiresL2() {
				st.consensus = provisionEvalLoopConsensus(t, r)
			}
			return nil
		}},
		{"deploy dedicated Data Operator", r.deployWorker},
		{"hand CLI approvals to the conformance owner", func(context.Context) error {
			if st.approvals != nil {
				st.owner = &conformanceLoopOwner{}
				st.approvals.Decide(st.owner.decide)
			}
			return nil
		}},
		{"start MCP stdio bridge", func(ctx context.Context) error { return st.startBridge(ctx, r) }},
		{"read audit chain heads through the bridge", func(ctx context.Context) error { return st.readHeads(ctx, r) }},
		{"conformance on the Data Operator", func(ctx context.Context) error {
			return st.runConformance(ctx, r, string(cases.ExecutorOutbound), r.workerOperatorID, "--operator-root", r.workerRoot)
		}},
		{"conformance on the embedded Operator", func(ctx context.Context) error {
			return st.runConformance(ctx, r, string(cases.ExecutorEmbedded), releaseSmokeEmbeddedOpID)
		}},
		{"audit chain advanced through the bridge", func(ctx context.Context) error { return st.checkChains(ctx) }},
		{"export local runtime evidence", r.exportReports},
		{"verify runtime evidence", func(ctx context.Context) error { return st.verifyReports(ctx, r) }},
		{"write conformance evidence", func(context.Context) error { return st.writeEvidence(r) }},
	}
}

// startBridge spawns `ztd mcp stdio` the way an agent's MCP config does: from
// the Gateway's project root, with the enrolled CLI identity and the run's key
// agent. The bridge's stderr goes to the run's logs.
func (st *conformanceLoopState) startBridge(ctx context.Context, r *releaseSmokeRun) error {
	stderr, err := os.OpenFile(filepath.Join(r.logDir, "mcp-stdio.stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open MCP stdio log: %w", err)
	}
	// The child holds its own descriptor once started.
	defer func() { _ = stderr.Close() }()
	cmd := exec.Command(r.opts.ZtdBin, "-e", fmt.Sprintf("127.0.0.1:%d", r.ports.http), "-p", strconv.Itoa(r.ports.https), "mcp", "stdio")
	cmd.Dir = r.gwRoot
	cmd.Env = append(cliconfig.WithoutUserEnv(os.Environ()), r.childEnv...)
	cmd.Stderr = stderr
	st.bridge, err = drive.StartMCPStdio(ctx, cmd)
	if err != nil {
		return err
	}
	r.addCleanup("MCP stdio bridge", func(context.Context) error { return st.bridge.Close() })
	return nil
}

// readHeads records the newest receipt of the Data and embedded sessions.
func (st *conformanceLoopState) readHeads(ctx context.Context, r *releaseSmokeRun) error {
	_, embeddedSession, err := r.embeddedOperator(ctx)
	if err != nil {
		return err
	}
	for _, target := range []struct{ runtime, session string }{{"worker", r.workerSession}, {"gateway", embeddedSession}} {
		list, err := st.listReceipts(ctx, target.session, time.Time{}, 1, 0)
		if err != nil {
			return err
		}
		chain := conformanceLoopAuditChain{Runtime: target.runtime, OperatorSessionID: target.session}
		if len(list.Receipts) > 0 {
			chain.HeadBefore = list.Receipts[0].TransactionID
		}
		st.evidence.AuditChains = append(st.evidence.AuditChains, chain)
	}
	st.started = time.Now().UTC()
	return nil
}

// listReceipts calls audit_receipt_list through the bridge for one page of a
// session's receipts, newest first, without the audit reads themselves.
func (st *conformanceLoopState) listReceipts(ctx context.Context, session string, notBefore time.Time, limit, offset int) (mcp.AuditReceiptListResult, error) {
	req := mcp.AuditReceiptListRequest{OperatorSessionID: session, ExcludeAuditReads: true, Limit: limit, Offset: offset}
	if !notBefore.IsZero() {
		req.NotBefore = notBefore.Format(time.RFC3339)
	}
	args, err := json.Marshal(req)
	if err != nil {
		return mcp.AuditReceiptListResult{}, err
	}
	result, err := st.bridge.CallTool(ctx, mcp.CallToolRequest{Name: constants.MCPToolAuditReceiptList, Arguments: args})
	if err != nil {
		return mcp.AuditReceiptListResult{}, err
	}
	if result.IsError || len(result.Content) != 1 {
		return mcp.AuditReceiptListResult{}, fmt.Errorf("audit_receipt_list %s: tool error %v: %w", session, result.Content, constants.ErrValidationFailed)
	}
	var list mcp.AuditReceiptListResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &list); err != nil {
		return mcp.AuditReceiptListResult{}, fmt.Errorf("audit_receipt_list %s: %w: %w (near %q)", session, constants.ErrInvalidJSONResponse, err, jsonErrorExcerpt(result.Content[0].Text, err))
	}
	if list.Error != "" {
		return mcp.AuditReceiptListResult{}, fmt.Errorf("audit_receipt_list %s: %s: %w", session, list.Error, constants.ErrValidationFailed)
	}
	return list, nil
}

// jsonErrorExcerpt returns up to 120 bytes of text on each side of a JSON
// syntax error's offset, or the head of text for any other decode error.
func jsonErrorExcerpt(text string, err error) string {
	const span = 120
	at := 0
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		at = int(syntaxErr.Offset)
	}
	return text[max(at-span, 0):min(at+span, len(text))]
}

// runConformance runs the suite against one Data Operator, requires the verdicts
// to cover every applicable case, then re-verifies the stored run offline.
func (st *conformanceLoopState) runConformance(ctx context.Context, r *releaseSmokeRun, executor, operatorID string, extra ...string) error {
	args := append([]string{"--json", "eval", "conformance", "run", "--operator-id", operatorID, "--a2a-stub-listen", st.a2aListen}, extra...)
	if st.owner != nil {
		pending, err := st.approvals.Pending(ctx)
		if err != nil {
			return err
		}
		if err := st.owner.arm(r.opts.Posture, pending); err != nil {
			return err
		}
		args = append(args, "--attended")
	}
	out, err := r.ztdGateway(ctx, "conformance-run-"+executor, args...)
	if st.owner != nil {
		err = errors.Join(err, st.owner.disarm())
	}
	if err != nil {
		return err
	}
	set, err := decodeConformanceVerdicts(out)
	if err != nil {
		return err
	}
	run, err := checkConformanceLoopVerdicts(r.opts.Posture, set)
	if err != nil {
		return fmt.Errorf("conformance run %s on %s: %w", set.GetRunId(), executor, err)
	}
	run.Executor, run.OperatorID = executor, operatorID
	// verify needs only the Gateway root: no endpoint, no CLI identity.
	out, err = r.ztd(ctx, "conformance-verify-"+executor, r.gwRoot, "--json", "eval", "conformance", "verify", "--run", run.RunID)
	if err != nil {
		return err
	}
	verified, err := decodeConformanceVerdicts(out)
	if err != nil {
		return err
	}
	if !proto.Equal(set, verified) {
		return fmt.Errorf("conformance run %s: offline verdicts differ from the run's: %w", run.RunID, constants.ErrEvalRunVerificationFailed)
	}
	run.Verified = true
	st.evidence.Runs = append(st.evidence.Runs, run)
	return nil
}

// conformanceLoopOwner is the human owner of the attended conformance runs under
// ratify and notary, answering through the CLI approval flow. A run suspends one
// transaction per case that waits for a human: each unattended mutation (row 22)
// stays suspended, each attended case asks for the approve (row 23) or deny (row
// 24) it names. Cases run one at a time in catalogue order, and an attended case
// holds the run until it is answered, so the k-th transaction a run suspends
// belongs to its k-th suspending case. The bridge's audit reads are approved
// whenever they come; nothing else is answered outside a run.
type conformanceLoopOwner struct {
	mu sync.Mutex
	// armed is set while a run is in flight; before holds what was pending
	// when it started, and plan the faults of its suspending cases not yet seen.
	armed    bool
	before   map[string]struct{}
	plan     []cases.Fault
	answered map[string]releaseSmokeApprovalDecision
	errs     []error
}

// arm prepares the owner for one run under posture; pending is what already waits.
func (o *conformanceLoopOwner) arm(posture constants.Posture, pending []models.SuspendedTxResponse) error {
	wire, err := cases.WirePosture(posture)
	if err != nil {
		return err
	}
	applicable, err := cases.Applicable(wire, cases.Stack{A2AIngress: true, Outbound: true})
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.armed, o.plan, o.errs = true, nil, nil
	o.before = make(map[string]struct{}, len(pending))
	for _, transaction := range pending {
		o.before[transaction.TransactionHash] = struct{}{}
	}
	if o.answered == nil {
		o.answered = make(map[string]releaseSmokeApprovalDecision)
	}
	for _, c := range applicable {
		switch c.Fault {
		case cases.FaultUnattendedMutation, cases.FaultAttendedApprove, cases.FaultAttendedDeny:
			o.plan = append(o.plan, c.Fault)
		}
	}
	return nil
}

// disarm ends the run and reports a suspension no case accounts for, or a
// suspending case whose transaction never came.
func (o *conformanceLoopOwner) disarm() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.armed = false
	errs := o.errs
	if len(o.plan) > 0 {
		errs = append(errs, fmt.Errorf("conformance owner: %d suspending cases never suspended, next %s: %w", len(o.plan), o.plan[0], constants.ErrValidationFailed))
	}
	return errors.Join(errs...)
}

// decide answers one pending transaction for the approvals responder.
func (o *conformanceLoopOwner) decide(transaction models.SuspendedTxResponse) releaseSmokeApprovalDecision {
	// Every MCP tool call is a governed mutation, so under ratify and notary the
	// bridge's audit_receipt_list reads wait for L3 like any other call.
	if transaction.ToolName == constants.MCPToolAuditReceiptList {
		return releaseSmokeApprove
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	hash := transaction.TransactionHash
	if decision, ok := o.answered[hash]; ok {
		return decision
	}
	if _, waited := o.before[hash]; !o.armed || waited {
		return releaseSmokeLeavePending
	}
	decision := releaseSmokeLeavePending
	switch {
	case len(o.plan) == 0:
		o.errs = append(o.errs, fmt.Errorf("conformance owner: transaction %s (%s) suspended after the last suspending case: %w", hash, transaction.ToolName, constants.ErrValidationFailed))
	case o.plan[0] == cases.FaultAttendedApprove:
		decision = releaseSmokeApprove
	case o.plan[0] == cases.FaultAttendedDeny:
		decision = releaseSmokeDeny
	}
	if len(o.plan) > 0 {
		o.plan = o.plan[1:]
	}
	o.answered[hash] = decision
	return decision
}

// decodeConformanceVerdicts reads the canonical verdict set `--json` prints.
func decodeConformanceVerdicts(out string) (*evalv1.ConformanceVerdictSet, error) {
	set := &evalv1.ConformanceVerdictSet{}
	if err := evalv1.UnmarshalCanonical([]byte(strings.TrimSpace(out)), set); err != nil {
		return nil, err
	}
	if set.GetRunId() == "" {
		return nil, fmt.Errorf("conformance verdicts carry no run id: %w", constants.ErrMissingRequiredField)
	}
	return set, nil
}

// checkConformanceLoopVerdicts requires one verdict per case the posture applies
// with A2A ingress and an outbound Operator, so an embedded-only run is held to
// the full suite too. Under ratify and notary the CLI owner attends the run, so
// every case, attended ones included, passes and the suite verdict is PASS.
func checkConformanceLoopVerdicts(posture constants.Posture, set *evalv1.ConformanceVerdictSet) (conformanceLoopRunEvidence, error) {
	run := conformanceLoopRunEvidence{RunID: set.GetRunId(), SuiteVerdict: set.GetSuiteVerdict().String(), Cases: len(set.GetVerdicts())}
	wire, err := cases.WirePosture(posture)
	if err != nil {
		return run, err
	}
	if set.GetPosture() != wire {
		return run, fmt.Errorf("verdict posture %s, want %s: %w", set.GetPosture(), wire, constants.ErrValidationFailed)
	}
	applicable, err := cases.Applicable(wire, cases.Stack{A2AIngress: true, Outbound: true})
	if err != nil {
		return run, err
	}
	applicableIDs := make(map[string]bool, len(applicable))
	for _, c := range applicable {
		applicableIDs[c.ID] = true
	}
	var errs []error
	if len(set.GetVerdicts()) != len(applicable) {
		errs = append(errs, fmt.Errorf("%d verdicts, want %d applicable cases: %w", len(set.GetVerdicts()), len(applicable), constants.ErrValidationFailed))
	}
	seen := make(map[string]bool, len(applicable))
	for _, v := range set.GetVerdicts() {
		switch {
		case !applicableIDs[v.GetCaseId()] || seen[v.GetCaseId()]:
			errs = append(errs, fmt.Errorf("case %s is not applicable or repeated: %w", v.GetCaseId(), constants.ErrValidationFailed))
		case v.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS:
			run.Passed++
		default:
			errs = append(errs, fmt.Errorf("case %s: %s %s: %w", v.GetCaseId(), v.GetStatus(), v.GetFailureClass(), constants.ErrValidationFailed))
		}
		seen[v.GetCaseId()] = true
	}
	if set.GetSuiteVerdict() != evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS {
		errs = append(errs, fmt.Errorf("suite verdict %s, want PASS: %w", set.GetSuiteVerdict(), constants.ErrValidationFailed))
	}
	return run, errors.Join(errs...)
}

// checkChains requires each session's chain to have advanced: a new head, and
// signed receipts the conformance runs added, some of them completed.
func (st *conformanceLoopState) checkChains(ctx context.Context) error {
	var errs []error
	st.completed = make(map[string][]*models.EventReceiptRecord, len(st.evidence.AuditChains))
	for i := range st.evidence.AuditChains {
		chain := &st.evidence.AuditChains[i]
		// Pages keep each tool result, and under ratify and notary the approval
		// event carrying it, a bounded size.
		for offset, reachedHead := 0, false; !reachedHead; offset += conformanceLoopReceiptPage {
			list, err := st.listReceipts(ctx, chain.OperatorSessionID, st.started.Truncate(time.Second), conformanceLoopReceiptPage, offset)
			if err != nil {
				return err
			}
			if offset == 0 && len(list.Receipts) > 0 {
				chain.HeadAfter = list.Receipts[0].TransactionID
			}
			for _, rec := range list.Receipts {
				if rec.TransactionID == chain.HeadBefore {
					reachedHead = true
					break
				}
				if rec.Signature == "" || rec.SignerKeyID == "" {
					errs = append(errs, fmt.Errorf("%s receipt %s is unsigned: %w", chain.Runtime, rec.TransactionID, constants.ErrValidationFailed))
				}
				chain.AddedReceipts++
				if rec.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
					chain.AddedCompleted++
					st.completed[chain.Runtime] = append(st.completed[chain.Runtime], rec)
				}
			}
			reachedHead = reachedHead || len(list.Receipts) < conformanceLoopReceiptPage
		}
		if chain.HeadAfter == "" || chain.HeadAfter == chain.HeadBefore || chain.AddedCompleted == 0 {
			errs = append(errs, fmt.Errorf("%s session %s chain did not advance (head %q to %q, %d completed): %w", chain.Runtime, chain.OperatorSessionID, chain.HeadBefore, chain.HeadAfter, chain.AddedCompleted, constants.ErrValidationFailed))
		}
	}
	return errors.Join(errs...)
}

// verifyReports applies the smoke evidence rules to both runtimes' `report all`
// exports, and requires each runtime's own export to hold the completed receipts
// the Gateway listed for it: the Operator's local store is the authority.
func (st *conformanceLoopState) verifyReports(ctx context.Context, r *releaseSmokeRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reports := make(map[string]*smokeReportEvidence, 2)
	for _, name := range []string{"worker", "gateway"} {
		ev, err := evaluateSmokeReport(name, filepath.Join(r.reportsDir, name))
		if err != nil {
			return err
		}
		reports[name] = &ev
	}
	for _, chain := range st.evidence.AuditChains {
		var txs []string
		for _, rec := range st.completed[chain.Runtime] {
			reports[chain.Runtime].requireCompletedReceipt(string(rec.ActionType), rec.TransactionID)
			txs = append(txs, rec.TransactionID)
		}
		r.recordScenario("conformance-"+chain.Runtime, txs)
	}
	r.summary.Reports = []smokeReportEvidence{*reports["worker"], *reports["gateway"]}
	var errs []error
	for _, rep := range r.summary.Reports {
		for _, f := range rep.Failures {
			errs = append(errs, fmt.Errorf("%s: %s", rep.Name, f))
		}
	}
	return errors.Join(errs...)
}

// writeEvidence writes the loop's evidence next to the smoke summary.
func (st *conformanceLoopState) writeEvidence(r *releaseSmokeRun) error {
	data, err := json.MarshalIndent(st.evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.opts.RunDir, "evidence", conformanceLoopEvidenceName), append(data, '\n'), 0o600)
}
