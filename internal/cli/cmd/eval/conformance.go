// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/report"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/run"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/verify"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// conformancePersonaID names the harness persona that submits conformance cases.
const conformancePersonaID = "ztd-conformance"

// conformanceSyntheticVotes is the vote count of the synthetic L2 proof that
// stands in for deliberation under doctrine and ratify (row 19).
const conformanceSyntheticVotes = 2

// conformanceReportMaxRuns is one run per posture: the report has a column per posture.
const conformanceReportMaxRuns = 4

// conformanceDeps are the seams of the conformance commands. Tests replace them.
type conformanceDeps struct {
	run    func(ctx context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error)
	listen func(addr string) (*drive.A2AStub, error)
	poster func(fileSvc fs.RuntimeFileService, cfg *config.Config) (cliop.CommandPoster, error)
	stdio  func(ctx context.Context, cmd *cobra.Command, cfg *config.Config, gwFiles fs.RuntimeFileService) (*drive.MCPStdio, error)
}

func defaultConformanceDeps() conformanceDeps {
	return conformanceDeps{run: run.Run, listen: drive.StartA2AStub, poster: defaultConformancePoster, stdio: startConformanceStdio}
}

// startConformanceStdio spawns `ztd mcp stdio` from this binary, the bridge an
// agent's MCP config launches, in the Gateway's project root with the same
// Gateway endpoint flags. It authenticates with the enrolled CLI identity, as
// the persona does. Its stderr is appended to the conformance stdio log.
func startConformanceStdio(ctx context.Context, cmd *cobra.Command, cfg *config.Config, gwFiles fs.RuntimeFileService) (*drive.MCPStdio, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("evaluation: locate ztd binary: %w", err)
	}
	var args []string
	for _, name := range []string{"endpoint", "port"} {
		if flag := cmd.Root().PersistentFlags().Lookup(name); flag != nil && flag.Changed {
			args = append(args, "--"+name, flag.Value.String())
		}
	}
	args = append(args, "mcp", "stdio")

	if err := gwFiles.MkdirAll(ctx, constants.LogDirname, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("evaluation: create log dir: %w", err)
	}
	logFile, err := gwFiles.OpenForAppend(ctx, filepath.Join(constants.LogDirname, constants.ConformanceMCPStdioLogFilename), constants.PermFilePrivate)
	if err != nil {
		return nil, fmt.Errorf("evaluation: open MCP stdio log: %w", err)
	}
	// The child holds its own descriptor once started, so this one closes on return.
	defer func() { _ = logFile.Close() }()

	bridge := exec.CommandContext(ctx, exe, args...)
	bridge.Dir = cfg.ProjectRoot
	bridge.Stderr = logFile
	stdio, err := drive.StartMCPStdio(ctx, bridge)
	if err != nil {
		return nil, fmt.Errorf("evaluation: MCP stdio bridge (log %s): %w", gwFiles.Resolve(filepath.Join(constants.LogDirname, constants.ConformanceMCPStdioLogFilename)), err)
	}
	return stdio, nil
}

// conformanceDispatchDeadline bounds one CLI-driven dispatch. Under ratify and notary
// the Gateway holds the request open for the human approval wait and then runs the
// dispatch, so the client allows both plus a response margin.
const conformanceDispatchDeadline = mcp.ApprovalRequestTTL + gateway.DispatchTimeout + conformanceDispatchMargin

// conformanceDispatchMargin is the response margin `operator run` also adds to its
// dispatch timeout.
const conformanceDispatchMargin = 5 * time.Second

// defaultConformancePoster is the Gateway API client the CLI driver posts through,
// the same client `operator run` uses. It reads the CLI identity from the Gateway root.
func defaultConformancePoster(fileSvc fs.RuntimeFileService, cfg *config.Config) (cliop.CommandPoster, error) {
	client, err := api.NewClientWithOptions(fileSvc, cfg, api.ClientOptions{Timeout: conformanceDispatchDeadline})
	if err != nil {
		return nil, fmt.Errorf("evaluation: create gateway API client: %w", err)
	}
	return client, nil
}

func conformanceEvalCmd(deps nativeEvalDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conformance",
		Short: "Governance conformance suite against a live native stack",
	}
	cmd.AddCommand(
		jsonLeaf(conformanceRunCmd(deps)),
		jsonLeaf(conformanceVerifyCmd(deps)),
		jsonLeaf(conformanceReportCmd(deps)),
	)
	return cmd
}

// conformanceRunOptions are the flags of conformance run.
type conformanceRunOptions struct {
	operatorID     string
	operatorRoot   string
	a2aStubListen  string
	attended       bool
	approverUserID string
}

func conformanceRunCmd(deps nativeEvalDeps) *cobra.Command {
	var opts conformanceRunOptions
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the conformance suite and persist its record",
		Long: `Drive the governance conformance suite against a live native stack, persist the
record, and print the verdicts that verification computes from the stored bytes.

The active Data Operator is discovered automatically, including the Gateway's embedded
Operator. When several are available, choose one from the list. --operator-id pins a
target for scripts. Outbound runtime roots are discovered from enrollment metadata;
--operator-root overrides the local path when needed.

The embedded Operator is a complete Data Operator: with it selected, it also executes the
outbound dispatch and CLI cases, so every ingress runs. A2A ingress is opt-in: a Gateway
without --a2a-downstream-url exposes none, and its A2A cases are not applicable. A Gateway
started with
--a2a-downstream-url http://<address> needs this command's --a2a-stub-listen <address>
for A2A coverage; without it, A2A cases are recorded as unavailable, yielding INCOMPLETE.
Unattended INCOMPLETE runs exit zero; failures and invalid evidence always exit non-zero.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConformance(cmd, deps, opts)
		},
	}
	command.Flags().StringVar(&opts.operatorID, "operator-id", "", "Select a Data Operator explicitly (default: discover or prompt)")
	command.Flags().StringVar(&opts.operatorRoot, "operator-root", "", "Override the selected outbound Operator's local runtime root (default: discover)")
	command.Flags().StringVar(&opts.a2aStubListen, "a2a-stub-listen", "", "Enable A2A coverage using this stub address; must match the Gateway's --a2a-downstream-url")
	command.Flags().BoolVar(&opts.attended, "attended", false, "Run cases requiring human approval")
	command.Flags().StringVar(&opts.approverUserID, "approver-user-id", "", "User approving attended cases (default: current CLI user)")
	return command
}

// runConformance drives the suite, persists the record, and prints the verdicts
// VerifyPersisted computes from the bytes it just wrote.
func runConformance(cmd *cobra.Command, deps nativeEvalDeps, opts conformanceRunOptions) (err error) {
	ctx := cmd.Context()
	cfg, gwFiles, err := nativeEvalEnvironment(cmd, deps)
	if err != nil {
		return err
	}
	authContext, err := deps.authLoader(gwFiles, cfg)
	if err != nil {
		return fmt.Errorf("evaluation: load CLI identity: %w", err)
	}
	gatewayClient, err := deps.clientFactory(nativeEvalClientConfig(cfg, authContext))
	if err != nil {
		return fmt.Errorf("evaluation: initialize gateway client: %w", err)
	}
	persona := harnessclient.Persona{ID: conformancePersonaID, CLISessionID: authContext.CLISessionID, UserID: authContext.UserID}
	if opts.approverUserID == "" {
		opts.approverUserID = authContext.UserID
	}

	// The posture and A2A ingress are read once. Drivers and the run must agree
	// on them, and both are immutable at runtime, so one read is the whole truth.
	posture, a2aIngress, err := gatewayStack(ctx, gatewayClient)
	if err != nil {
		return err
	}
	if opts.a2aStubListen != "" && !a2aIngress {
		return fmt.Errorf("evaluation: --a2a-stub-listen needs a Gateway started with --a2a-downstream-url; this Gateway exposes no A2A ingress: %w", constants.ErrEvaluationFlagsInvalid)
	}
	embedded, outbound, outboundDoc, err := conformanceTargets(cmd, gatewayClient, opts.operatorID)
	if err != nil {
		return err
	}
	var operatorFiles fs.RuntimeFileService
	if outboundDoc != nil {
		operatorRoot, rootErr := conformanceOperatorRoot(opts.operatorRoot, outboundDoc)
		if rootErr != nil {
			return rootErr
		}
		operatorFiles, err = deps.fileSvcFactory(operatorRoot, slog.Default())
		if err != nil {
			return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
		}
	} else if opts.operatorRoot != "" {
		return fmt.Errorf("evaluation: --operator-root applies to an outbound Data Operator; select one from the list or with --operator-id: %w", constants.ErrEvaluationFlagsInvalid)
	}
	roots, err := conformanceRoots(gwFiles, operatorFiles, outbound.OperatorID, outboundDoc)
	if err != nil {
		return err
	}
	operators, _, err := gatewayClient.ListOperators(ctx)
	if err != nil {
		return fmt.Errorf("evaluation: conformance evidence runtimes: %w", err)
	}
	consensusAnchors, err := conformanceConsensusAnchors(operators, outbound.OperatorID)
	if err != nil {
		return err
	}
	poster, err := deps.conformance.poster(gwFiles, cfg)
	if err != nil {
		return err
	}

	var stub *drive.A2AStub
	if opts.a2aStubListen != "" {
		stub, err = deps.conformance.listen(opts.a2aStubListen)
		if err != nil {
			return fmt.Errorf("evaluation: start A2A stub: %w", err)
		}
		defer func() { err = errors.Join(err, stub.Close(ctx)) }()
	}
	stdio, err := deps.conformance.stdio(ctx, cmd, cfg, gwFiles)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stdio.Close()) }()

	// Under --json an attended run reports each approval it asks for as a typed
	// line on stderr, so an automated approver can answer it by transaction.
	var requests *approvalRequestWriter
	if opts.attended && output.JSONEnabled(cmd) {
		requests = &approvalRequestWriter{w: cmd.ErrOrStderr()}
		defer func() { err = errors.Join(err, requests.Err()) }()
	}

	drivers, sentinel, err := conformanceDrivers(ctx, conformanceSetup{
		client:         gatewayClient,
		persona:        persona,
		posture:        posture,
		embedded:       embedded,
		outbound:       outbound,
		stub:           stub,
		stdio:          stdio,
		poster:         poster,
		attended:       opts.attended,
		approverUserID: opts.approverUserID,
		requests:       requests.sink(),
	})
	if err != nil {
		return err
	}
	unavailable := make(map[cases.Ingress]evalv1.ConformanceObservationKind)
	if a2aIngress && drivers[cases.IngressA2A] == nil {
		unavailable[cases.IngressA2A] = cases.IngressA2A.UnavailableObservation()
	}
	if !output.JSONEnabled(cmd) {
		if err := writeConformanceSetup(cmd.ErrOrStderr(), outboundDoc, unavailable); err != nil {
			return err
		}
	}

	runID, err := deps.newID()
	if err != nil {
		return err
	}
	record, err := deps.conformance.run(ctx, run.Config{
		RunID:            runID,
		Drivers:          drivers,
		Unavailable:      unavailable,
		Sentinel:         sentinel,
		Roots:            roots,
		ConsensusAnchors: consensusAnchors,
		Posture:          func(context.Context) (evalv1.EvaluationGovernancePosture, error) { return posture, nil },
		A2AIngress:       a2aIngress,
		Recorder:         gatewayClient.Record,
		Attended:         opts.attended,
		Now:              deps.now,
	})
	if err != nil {
		return fmt.Errorf("evaluation: run conformance suite: %w", err)
	}

	store := evaluation.NewStore(gwFiles)
	if err := store.SaveConformanceRecord(ctx, record); err != nil {
		return fmt.Errorf("evaluation: save conformance record: %w", err)
	}
	stored, err := verifyStoredConformanceRun(ctx, store, record.GetRunId())
	if err != nil {
		return err
	}
	if err := writeConformanceVerdicts(cmd, record.GetRunId(), stored.verdicts, stored.record, output.JSONEnabled(cmd)); err != nil {
		return err
	}
	return conformanceVerdictError(stored.verdicts, stored.record.GetAttended())
}

func conformanceVerifyCmd(deps nativeEvalDeps) *cobra.Command {
	var runID string
	command := &cobra.Command{
		Use:   "verify",
		Short: "Recompute a persisted conformance run's verdicts offline",
		Long: `Recompute the verdicts of a persisted conformance run from its stored record. This
command needs only the Gateway runtime root: it makes no network call and needs no CLI identity.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, gwFiles, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			stored, err := verifyStoredConformanceRun(cmd.Context(), evaluation.NewStore(gwFiles), runID)
			if err != nil {
				return err
			}
			if err := writeConformanceVerdicts(cmd, runID, stored.verdicts, stored.record, output.JSONEnabled(cmd)); err != nil {
				return err
			}
			return conformanceVerdictError(stored.verdicts, stored.record.GetAttended())
		},
	}
	command.Flags().StringVar(&runID, "run", "", "Conformance run id to verify")
	_ = command.MarkFlagRequired("run")
	return command
}

func conformanceReportCmd(deps nativeEvalDeps) *cobra.Command {
	var runIDs []string
	var markdown bool
	command := &cobra.Command{
		Use:   "report",
		Short: "Build the coverage and recall report from persisted conformance runs",
		Long: `Build the coverage and recall report from one persisted conformance run per posture.
Each run is verified from its stored bytes before it enters the report. At most four runs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(runIDs) > conformanceReportMaxRuns {
				return fmt.Errorf("evaluation: conformance report takes at most %d runs, got %d: %w", conformanceReportMaxRuns, len(runIDs), constants.ErrEvaluationFlagsInvalid)
			}
			_, gwFiles, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			store := evaluation.NewStore(gwFiles)
			runs := make([]report.VerifiedRun, 0, len(runIDs))
			for _, runID := range runIDs {
				stored, err := verifyStoredConformanceRun(cmd.Context(), store, runID)
				if err != nil {
					return err
				}
				runs = append(runs, report.VerifiedRun{Record: stored.record, Verdicts: stored.verdicts})
			}
			built, err := report.Build(runs)
			if err != nil {
				return fmt.Errorf("evaluation: build conformance report: %w", err)
			}
			if markdown {
				_, err := fmt.Fprint(cmd.OutOrStdout(), built.Markdown())
				return err
			}
			return output.WriteJSON(cmd.OutOrStdout(), built)
		},
	}
	command.Flags().StringArrayVar(&runIDs, "run", nil, "Conformance run id to include; repeat once per posture (at most four)")
	command.Flags().BoolVar(&markdown, "markdown", false, "Print the coverage and recall tables as Markdown instead of JSON")
	_ = command.MarkFlagRequired("run")
	return command
}

// gatewayStack reads the Gateway's governance posture and whether it exposes
// A2A ingress from one health read. An unknown posture is
// ErrConformancePostureUnknown.
func gatewayStack(ctx context.Context, client *harnessclient.Client) (evalv1.EvaluationGovernancePosture, bool, error) {
	health, err := gatewayHealth(ctx, client)
	if err != nil {
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, false, err
	}
	posture, err := cases.WirePosture(health.Posture)
	if err != nil {
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, false, fmt.Errorf("evaluation: gateway posture: %w", err)
	}
	return posture, health.A2AIngress, nil
}

// conformanceTargets selects the Gateway's embedded session and, when selected,
// an outbound Data session. Its document carries the enrollment-bound anchor.
func conformanceTargets(cmd *cobra.Command, client *harnessclient.Client, operatorID string) (cliop.Target, cliop.Target, *operatorv1.OperatorDocument, error) {
	operators, _, err := client.ListOperators(cmd.Context())
	if err != nil {
		return cliop.Target{}, cliop.Target{}, nil, fmt.Errorf("evaluation: list operators: %w", err)
	}
	embedded, err := conformanceEmbeddedTarget(operators)
	if err != nil {
		return cliop.Target{}, cliop.Target{}, nil, err
	}
	selected, err := selectConformanceDataOperator(cmd, operators, operatorID)
	if err != nil {
		return cliop.Target{}, cliop.Target{}, nil, err
	}
	if selected.GetId() == string(constants.DocIDEmbeddedOperator) {
		return embedded, cliop.Target{}, nil, nil
	}
	return embedded, cliop.Target{OperatorID: selected.GetId(), OperatorSessionID: selected.GetOperatorSessionId()}, selected, nil
}

// conformanceEmbeddedTarget requires exactly one session for Gateway-local cases.
func conformanceEmbeddedTarget(operators []*operatorv1.OperatorDocument) (cliop.Target, error) {
	operatorID := string(constants.DocIDEmbeddedOperator)
	var matches []*operatorv1.OperatorDocument
	for _, op := range operators {
		if op.GetId() != operatorID || op.GetOperatorSessionId() == "" {
			continue
		}
		matches = append(matches, op)
	}
	switch len(matches) {
	case 0:
		return cliop.Target{}, fmt.Errorf("evaluation: operator %s has no matching session: %w", operatorID, constants.ErrEvaluationTargetUnavailable)
	case 1:
		return cliop.Target{OperatorSessionID: matches[0].GetOperatorSessionId(), OperatorID: operatorID}, nil
	default:
		return cliop.Target{}, fmt.Errorf("evaluation: operator %s has %d matching sessions: %w", operatorID, len(matches), constants.ErrEvaluationTargetAmbiguous)
	}
}

// conformanceRoots maps each executor to the runtime it reads and writes. Dir is
// the runtime directory (.ztd) of each executor: the canary and the FILE_EDIT
// policy both address paths under it (security.IsEvaluationControlledTargetPath).
// The outbound root carries the key bound at enrollment by outboundDoc; run.Run
// cross-checks that key against the Operator's export before any case runs.
func conformanceRoots(gwFiles, operatorFiles fs.RuntimeFileService, operatorID string, outboundDoc *operatorv1.OperatorDocument) (map[cases.Executor]run.Root, error) {
	gwDir, err := absoluteRuntimeDir(gwFiles)
	if err != nil {
		return nil, err
	}
	roots := map[cases.Executor]run.Root{
		cases.ExecutorEmbedded: {Files: gwFiles, Dir: gwDir, RuntimeID: oracle.GatewayRuntimeID},
	}
	if outboundDoc == nil && operatorID == "" {
		return roots, nil
	}
	operatorDir, err := absoluteRuntimeDir(operatorFiles)
	if err != nil {
		return nil, err
	}
	bound, err := oracle.BoundAnchor(outboundDoc)
	if err != nil {
		return nil, fmt.Errorf("evaluation: conformance run: outbound operator %s anchor: %w", operatorID, err)
	}
	roots[cases.ExecutorOutbound] = run.Root{Files: operatorFiles, Dir: operatorDir, RuntimeID: operatorID, Bound: bound}
	return roots, nil
}

// Supporting inference and observer receipts can come from runtimes other
// than the selected Data Operator. Pin their enrollment keys before any case;
// never take a trust anchor from the receipt it is meant to authenticate.
func conformanceConsensusAnchors(operators []*operatorv1.OperatorDocument, executorID string) ([]*evalv1.ConformanceAnchor, error) {
	var anchors []*evalv1.ConformanceAnchor
	for _, doc := range operators {
		if doc.GetId() == string(constants.DocIDEmbeddedOperator) || doc.GetId() == executorID || doc.GetStatus() != string(constants.OperatorStatusActive) {
			continue
		}
		if doc.GetActuatorKeyId() == "" && doc.GetActuatorPublicKey() == "" {
			continue
		}
		anchor, err := oracle.BoundAnchor(doc)
		if err != nil {
			return nil, fmt.Errorf("evaluation: conformance consensus runtime %s: %w", doc.GetId(), err)
		}
		anchors = append(anchors, anchor)
	}
	return anchors, nil
}

// absoluteRuntimeDir is the absolute runtime directory a file service resolves
// its relative paths against.
func absoluteRuntimeDir(files fs.RuntimeFileService) (string, error) {
	dir, err := filepath.Abs(files.Resolve(""))
	if err != nil {
		return "", fmt.Errorf("evaluation: resolve runtime directory: %w", err)
	}
	return dir, nil
}

// conformanceSetup is what the drivers are built from.
type conformanceSetup struct {
	client         *harnessclient.Client
	persona        harnessclient.Persona
	posture        evalv1.EvaluationGovernancePosture
	embedded       cliop.Target
	outbound       cliop.Target
	stub           *drive.A2AStub
	stdio          *drive.MCPStdio
	poster         cliop.CommandPoster
	attended       bool
	approverUserID string
	// requests receives each attended approval request; nil reports none.
	requests drive.RequestSink
}

// approvalRequestWriter prints each approval request as one canonical JSON line.
// The first write failure is kept and returned by Err.
type approvalRequestWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

// sink is the writer as a request sink; a nil writer is a nil sink.
func (rw *approvalRequestWriter) sink() drive.RequestSink {
	if rw == nil {
		return nil
	}
	return rw.write
}

func (rw *approvalRequestWriter) write(req *evalv1.ConformanceApprovalRequest) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.err != nil {
		return
	}
	body, err := evalv1.MarshalCanonical(req)
	if err != nil {
		rw.err = fmt.Errorf("evaluation: canonicalize approval request %s: %w", req.GetAttemptId(), err)
		return
	}
	if _, err := fmt.Fprintln(rw.w, string(body)); err != nil {
		rw.err = fmt.Errorf("evaluation: write approval request %s: %w", req.GetAttemptId(), err)
	}
}

// Err is the first write failure.
func (rw *approvalRequestWriter) Err() error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.err
}

// conformanceDrivers builds one driver per ingress and the sentinel. The L2 source
// follows the posture. The bound app deliberates under consensus and notary, and a
// synthetic proof stands in under doctrine and ratify. Attended runs wrap every
// driver whose ingress a human approval resumes: MCP, MCP stdio, A2A, governed
// dispatch and the CLI.
func conformanceDrivers(ctx context.Context, s conformanceSetup) (map[cases.Ingress]cases.Driver, *drive.Sentinel, error) {
	posture, err := cases.GovernancePosture(s.posture)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: conformance drivers: %w", err)
	}
	// directL2 stays nil where the base direct cases need no L2 proof.
	var directL2, mutatorL2 drive.L2Source
	if posture.RequiresL2() {
		deliberator := drive.NewDeliberator(s.client, s.persona)
		directL2, mutatorL2 = deliberator, deliberator
	} else {
		synthetic, err := drive.NewSyntheticL2(conformanceSyntheticVotes)
		if err != nil {
			return nil, nil, fmt.Errorf("evaluation: synthetic L2 source: %w", err)
		}
		mutatorL2 = synthetic
	}

	direct, err := drive.NewDirect(s.client, s.persona, s.embedded, directL2, drive.BaseMutators(), drive.L2Mutators(mutatorL2), drive.L3Mutators())
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: direct driver: %w", err)
	}
	// The outbound role is the selected Data Operator, which is the embedded
	// Operator when no outbound one is selected.
	data := s.outbound
	if data.OperatorSessionID == "" {
		data = s.embedded
	}
	targets := map[cases.Executor]cliop.Target{cases.ExecutorEmbedded: s.embedded, cases.ExecutorOutbound: data}
	dispatch, err := drive.NewDispatch(s.client, s.persona, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: dispatch driver: %w", err)
	}
	sentinel, err := drive.NewSentinel(s.client, s.persona, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: sentinel: %w", err)
	}
	mcpDriver, err := drive.NewMCP(ctx, s.client, s.persona)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: MCP driver: %w", err)
	}
	var mcpIngress cases.Driver = mcpDriver
	if s.attended {
		mcpIngress = drive.NewAttended(mcpDriver, s.client, s.persona, s.approverUserID, s.requests)
	}
	var dispatchIngress cases.Driver = dispatch
	if s.attended {
		dispatchIngress = drive.NewAttended(dispatch, s.client, s.persona, s.approverUserID, s.requests)
	}
	cli, err := drive.NewCLI(s.poster, data, s.persona.CLISessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: CLI driver: %w", err)
	}
	var cliIngress cases.Driver = cli
	if s.attended {
		cliIngress = drive.NewAttended(cli, s.client, s.persona, s.approverUserID, s.requests)
	}
	drivers := map[cases.Ingress]cases.Driver{
		cases.IngressDirect:           direct,
		cases.IngressDispatchEmbedded: dispatchIngress,
		cases.IngressDispatchOutbound: dispatchIngress,
		cases.IngressCLI:              cliIngress,
		cases.IngressMCP:              mcpIngress,
	}
	if s.stdio != nil {
		var stdioIngress cases.Driver = s.stdio
		if s.attended {
			stdioIngress = drive.NewAttended(s.stdio, s.client, s.persona, s.approverUserID, s.requests)
		}
		drivers[cases.IngressMCPStdio] = stdioIngress
	}
	if s.stub != nil {
		var a2aIngress cases.Driver = drive.NewA2A(s.client, s.persona, s.stub)
		if s.attended {
			a2aIngress = drive.NewAttended(a2aIngress, s.client, s.persona, s.approverUserID, s.requests)
		}
		drivers[cases.IngressA2A] = a2aIngress
	}
	return drivers, sentinel, nil
}

// storedConformance is one persisted run as the verifier reads it.
type storedConformance struct {
	verdicts *evalv1.ConformanceVerdictSet
	// record is nil when the stored bytes do not decode. Such a run's verdicts are
	// INVALID_EVIDENCE, so it fails the exit rule whatever attended is.
	record *evalv1.ConformanceRunRecord
}

// verifyStoredConformanceRun verifies the persisted bytes of one run. It is the
// only path from stored evidence to a verdict set in this command group.
//
// A sealed record or sidecar that fails authentication under the open vault was
// changed on disk, so it is the digest-mismatch verdict for the run. A locked vault
// is ErrVaultLocked, not a decrypt failure. Every other load error stays an error.
func verifyStoredConformanceRun(ctx context.Context, store *evaluation.Store, runID string) (storedConformance, error) {
	raw, digest, err := store.LoadConformanceRecordRaw(ctx, runID)
	if err != nil {
		if errors.Is(err, constants.ErrEvaluationEvidenceDecryptFailed) {
			return storedConformance{verdicts: verify.SealedRecordTamperedSet(runID)}, nil
		}
		return storedConformance{}, fmt.Errorf("evaluation: load conformance record %s: %w", runID, err)
	}
	verdicts, err := verify.VerifyPersisted(raw, digest)
	if err != nil {
		return storedConformance{}, fmt.Errorf("evaluation: verify conformance record %s: %w", runID, err)
	}
	stored := storedConformance{verdicts: verdicts}
	record := &evalv1.ConformanceRunRecord{}
	if evalv1.UnmarshalCanonical(raw, record) == nil {
		stored.record = record
	}
	return stored, nil
}

// conformanceVerdictError is the exit rule. PASS exits zero. INCOMPLETE exits
// zero only for an unattended run. Anything else fails with
// ErrEvalRunVerificationFailed.
func conformanceVerdictError(set *evalv1.ConformanceVerdictSet, attended bool) error {
	switch set.GetSuiteVerdict() {
	case evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS:
		return nil
	case evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE:
		if !attended {
			return nil
		}
	}
	return fmt.Errorf("evaluation: conformance suite verdict %s: %w", set.GetSuiteVerdict(), constants.ErrEvalRunVerificationFailed)
}

// writeConformanceVerdicts prints canonical JSON or a summary with grouped
// unavailable coverage and individual failures.
func writeConformanceVerdicts(cmd *cobra.Command, runID string, set *evalv1.ConformanceVerdictSet, record *evalv1.ConformanceRunRecord, jsonOutput bool) error {
	out := cmd.OutOrStdout()
	if jsonOutput {
		body, err := evalv1.MarshalCanonical(set)
		if err != nil {
			return fmt.Errorf("evaluation: canonicalize conformance verdicts: %w", err)
		}
		_, err = fmt.Fprintln(out, string(body))
		return err
	}
	posture := set.GetPosture().String()
	if gp, err := cases.GovernancePosture(set.GetPosture()); err == nil {
		posture = string(gp)
	}
	verdict := strings.TrimPrefix(set.GetSuiteVerdict().String(), "CONFORMANCE_SUITE_VERDICT_")
	if _, err := fmt.Fprintf(out, "Run: %s\nPosture: %s\nSuite: %s\n", runID, posture, verdict); err != nil {
		return err
	}
	counts := make(map[evalv1.EvaluationVerdictStatus]int)
	unavailableIDs := make(map[string]bool)
	for _, v := range set.GetVerdicts() {
		counts[v.GetStatus()]++
		if v.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE {
			unavailableIDs[v.GetCaseId()] = true
		}
	}
	if _, err := fmt.Fprintf(out, "Cases: %d passed, %d failed, %d invalid, %d unavailable\n",
		counts[evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS],
		counts[evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL],
		counts[evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE],
		counts[evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE]); err != nil {
		return err
	}
	skipped := make(map[evalv1.ConformanceObservationKind]int)
	for _, ev := range record.GetCases() {
		if unavailableIDs[ev.GetCaseId()] && len(ev.GetObservations()) == 1 {
			skipped[ev.GetObservations()[0].GetKind()]++
		}
	}
	for _, reason := range []struct {
		kind        evalv1.ConformanceObservationKind
		description string
	}{
		{evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_A2A, "A2A test downstream not configured"},
		{evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED, "human approval cases not requested (use --attended)"},
	} {
		if count := skipped[reason.kind]; count > 0 {
			if _, err := fmt.Fprintf(out, "Unavailable: %d cases — %s\n", count, reason.description); err != nil {
				return err
			}
		}
	}
	for _, verdict := range set.GetVerdicts() {
		if verdict.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS || verdict.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE {
			continue
		}
		caseID := verdict.GetCaseId()
		if caseID == "" {
			caseID = "(record)"
		}
		if _, err := fmt.Fprintf(out, "%s %s %s\n", caseID, strings.TrimPrefix(verdict.GetStatus().String(), "EVALUATION_VERDICT_STATUS_"), strings.TrimPrefix(verdict.GetFailureClass().String(), "CONFORMANCE_FAILURE_CLASS_")); err != nil {
			return err
		}
	}
	return nil
}
