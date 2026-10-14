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
	"log/slog"
	"path/filepath"

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
}

func defaultConformanceDeps() conformanceDeps {
	return conformanceDeps{run: run.Run, listen: drive.StartA2AStub, poster: defaultConformancePoster}
}

// defaultConformancePoster is the Gateway API client the CLI driver posts through,
// the same client `operator run` uses. It reads the CLI identity from the Gateway root.
func defaultConformancePoster(fileSvc fs.RuntimeFileService, cfg *config.Config) (cliop.CommandPoster, error) {
	client, err := api.NewClientWithOptions(fileSvc, cfg, api.ClientOptions{})
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

// validate rejects an incomplete flag set before any file or network access.
func (o conformanceRunOptions) validate() error {
	if o.operatorID == "" {
		return fmt.Errorf("evaluation: conformance run: --operator-id is required, every posture runs cases on the outbound Operator: %w", constants.ErrMissingRequiredField)
	}
	if o.operatorRoot == "" {
		return fmt.Errorf("evaluation: conformance run: --operator-root is required with --operator-id: %w", constants.ErrMissingRequiredField)
	}
	if o.a2aStubListen == "" {
		return fmt.Errorf("evaluation: conformance run: --a2a-stub-listen is required: %w", constants.ErrMissingRequiredField)
	}
	if o.attended && o.approverUserID == "" {
		return fmt.Errorf("evaluation: conformance run: --approver-user-id is required with --attended: %w", constants.ErrMissingRequiredField)
	}
	return nil
}

func conformanceRunCmd(deps nativeEvalDeps) *cobra.Command {
	var opts conformanceRunOptions
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the conformance suite and persist its record",
		Long: `Drive the governance conformance suite against a live native stack, persist the
record, and print the verdicts that verification computes from the stored bytes.

Start-up order:
  1. Keep the --a2a-stub-listen address free. This command starts the A2A stub on it.
  2. Start the Gateway with --a2a-downstream-url http://<a2a-stub-listen>.
  3. Run this command with --operator-id and --operator-root for the outbound Operator.

--operator-id and --operator-root are required because every posture runs cases on the
outbound Operator. Exit is non-zero unless the suite verdict is PASS, or INCOMPLETE for an
unattended run (its attended cases were not run).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			return runConformance(cmd, deps, opts)
		},
	}
	command.Flags().StringVar(&opts.operatorID, "operator-id", "", "Outbound Operator id; its single active session is the outbound target")
	command.Flags().StringVar(&opts.operatorRoot, "operator-root", "", "Runtime root of the outbound Operator (its .ztd lives under it)")
	command.Flags().StringVar(&opts.a2aStubListen, "a2a-stub-listen", "", "host:port the A2A stub listens on; must equal the Gateway's --a2a-downstream-url host and port")
	command.Flags().BoolVar(&opts.attended, "attended", false, "Run attended cases; needs --approver-user-id")
	command.Flags().StringVar(&opts.approverUserID, "approver-user-id", "", "User id that approves attended cases (required with --attended)")
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

	// The posture is read once. Drivers and the run must agree on it, and the
	// posture is immutable at runtime, so one read is the whole truth.
	posture, err := gatewayPosture(ctx, gatewayClient)
	if err != nil {
		return err
	}
	embedded, outbound, err := conformanceTargets(ctx, gatewayClient, opts.operatorID)
	if err != nil {
		return err
	}
	operatorRoot, err := filepath.Abs(opts.operatorRoot)
	if err != nil {
		return fmt.Errorf("evaluation: resolve operator root: %w", err)
	}
	operatorFiles, err := deps.fileSvcFactory(operatorRoot, slog.Default())
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
	}
	roots, err := conformanceRoots(gwFiles, operatorFiles, opts.operatorID)
	if err != nil {
		return err
	}
	poster, err := deps.conformance.poster(gwFiles, cfg)
	if err != nil {
		return err
	}

	stub, err := deps.conformance.listen(opts.a2aStubListen)
	if err != nil {
		return fmt.Errorf("evaluation: start A2A stub: %w", err)
	}
	defer func() { err = errors.Join(err, stub.Close(ctx)) }()

	drivers, sentinel, err := conformanceDrivers(ctx, conformanceSetup{
		client:         gatewayClient,
		persona:        persona,
		posture:        posture,
		embedded:       embedded,
		outbound:       outbound,
		stub:           stub,
		poster:         poster,
		attended:       opts.attended,
		approverUserID: opts.approverUserID,
	})
	if err != nil {
		return err
	}

	runID, err := deps.newID()
	if err != nil {
		return err
	}
	record, err := deps.conformance.run(ctx, run.Config{
		RunID:    runID,
		Drivers:  drivers,
		Sentinel: sentinel,
		Roots:    roots,
		Posture:  func(context.Context) (evalv1.EvaluationGovernancePosture, error) { return posture, nil },
		Recorder: gatewayClient.Record,
		Attended: opts.attended,
		Now:      deps.now,
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
	if err := writeConformanceVerdicts(cmd, record.GetRunId(), stored.verdicts, output.JSONEnabled(cmd)); err != nil {
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
			if err := writeConformanceVerdicts(cmd, runID, stored.verdicts, output.JSONEnabled(cmd)); err != nil {
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

// conformancePosture maps the posture name the Gateway health reports to its
// evaluation enum. An unknown name is ErrConformancePostureUnknown.
func conformancePosture(name string) (evalv1.EvaluationGovernancePosture, error) {
	switch name {
	case constants.PostureDoctrine:
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, nil
	case constants.PostureConsensus:
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS, nil
	case constants.PostureRatify:
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY, nil
	case constants.PostureNotary:
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY, nil
	default:
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, fmt.Errorf("evaluation: gateway posture %q: %w", name, constants.ErrConformancePostureUnknown)
	}
}

// gatewayPosture reads the Gateway's governance posture from its health.
func gatewayPosture(ctx context.Context, client *harnessclient.Client) (evalv1.EvaluationGovernancePosture, error) {
	health, _, err := client.Health(ctx)
	if err != nil {
		return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, fmt.Errorf("evaluation: read gateway health: %w", err)
	}
	return conformancePosture(health.Posture)
}

// conformanceTargets selects the two Operator sessions the suite drives. The
// embedded target is the Gateway's own Operator. The outbound target is the one
// active session of operatorID.
func conformanceTargets(ctx context.Context, client *harnessclient.Client, operatorID string) (cliop.Target, cliop.Target, error) {
	operators, _, err := client.ListOperators(ctx)
	if err != nil {
		return cliop.Target{}, cliop.Target{}, fmt.Errorf("evaluation: list operators: %w", err)
	}
	embedded, err := singleConformanceTarget(operators, string(constants.DocIDEmbeddedOperator), false)
	if err != nil {
		return cliop.Target{}, cliop.Target{}, err
	}
	outbound, err := singleConformanceTarget(operators, operatorID, true)
	if err != nil {
		return cliop.Target{}, cliop.Target{}, err
	}
	return embedded, outbound, nil
}

// singleConformanceTarget returns the one session of operatorID that has a
// session id (and is active when requireActive). Zero sessions is
// ErrEvaluationTargetUnavailable, and several is ErrEvaluationTargetAmbiguous.
func singleConformanceTarget(operators []*operatorv1.OperatorDocument, operatorID string, requireActive bool) (cliop.Target, error) {
	var matches []*operatorv1.OperatorDocument
	for _, op := range operators {
		if op.GetId() != operatorID || op.GetOperatorSessionId() == "" {
			continue
		}
		if requireActive && constants.OperatorStatus(op.GetStatus()) != constants.OperatorStatusActive {
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
func conformanceRoots(gwFiles, operatorFiles fs.RuntimeFileService, operatorID string) (map[cases.Executor]run.Root, error) {
	gwDir, err := absoluteRuntimeDir(gwFiles)
	if err != nil {
		return nil, err
	}
	operatorDir, err := absoluteRuntimeDir(operatorFiles)
	if err != nil {
		return nil, err
	}
	return map[cases.Executor]run.Root{
		cases.ExecutorEmbedded: {Files: gwFiles, Dir: gwDir, RuntimeID: oracle.GatewayRuntimeID},
		cases.ExecutorOutbound: {Files: operatorFiles, Dir: operatorDir, RuntimeID: operatorID},
	}, nil
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
	poster         cliop.CommandPoster
	attended       bool
	approverUserID string
}

// conformanceDrivers builds one driver per ingress and the sentinel. The L2 source
// follows the posture. The Gateway deliberates under consensus and notary, and a
// synthetic proof stands in under doctrine and ratify. Attended runs wrap the MCP
// and A2A drivers.
func conformanceDrivers(ctx context.Context, s conformanceSetup) (map[cases.Ingress]cases.Driver, *drive.Sentinel, error) {
	// directL2 stays nil where the base direct cases need no L2 proof.
	var directL2, mutatorL2 drive.L2Source
	switch s.posture {
	case evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY:
		synthetic, err := drive.NewSyntheticL2(conformanceSyntheticVotes)
		if err != nil {
			return nil, nil, fmt.Errorf("evaluation: synthetic L2 source: %w", err)
		}
		mutatorL2 = synthetic
	case evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY:
		deliberator := drive.NewDeliberator(s.client, s.persona)
		directL2, mutatorL2 = deliberator, deliberator
	default:
		return nil, nil, fmt.Errorf("evaluation: conformance drivers: posture %s: %w", s.posture, constants.ErrConformancePostureUnknown)
	}

	direct, err := drive.NewDirect(s.client, s.persona, s.embedded, directL2, drive.BaseMutators(), drive.L2Mutators(mutatorL2), drive.L3Mutators())
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: direct driver: %w", err)
	}
	targets := map[cases.Executor]cliop.Target{cases.ExecutorEmbedded: s.embedded, cases.ExecutorOutbound: s.outbound}
	dispatch, err := drive.NewDispatch(s.client, s.persona, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: dispatch driver: %w", err)
	}
	cli, err := drive.NewCLI(s.poster, s.outbound, s.persona.CLISessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: CLI driver: %w", err)
	}
	sentinel, err := drive.NewSentinel(s.client, s.persona, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: sentinel: %w", err)
	}
	mcpDriver, err := drive.NewMCP(ctx, s.client, s.persona)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluation: MCP driver: %w", err)
	}
	a2aDriver := drive.NewA2A(s.client, s.persona, s.stub)

	var mcpIngress, a2aIngress cases.Driver = mcpDriver, a2aDriver
	if s.attended {
		mcpIngress = drive.NewAttended(mcpDriver, s.client, s.persona, s.approverUserID)
		a2aIngress = drive.NewAttended(a2aDriver, s.client, s.persona, s.approverUserID)
	}
	return map[cases.Ingress]cases.Driver{
		cases.IngressDirect:           direct,
		cases.IngressDispatchEmbedded: dispatch,
		cases.IngressDispatchOutbound: dispatch,
		cases.IngressCLI:              cli,
		cases.IngressMCP:              mcpIngress,
		cases.IngressA2A:              a2aIngress,
	}, sentinel, nil
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

// writeConformanceVerdicts prints the verdict set: canonical JSON, or the run,
// posture, suite verdict and one line per non-PASS case.
func writeConformanceVerdicts(cmd *cobra.Command, runID string, set *evalv1.ConformanceVerdictSet, jsonOutput bool) error {
	out := cmd.OutOrStdout()
	if jsonOutput {
		body, err := evalv1.MarshalCanonical(set)
		if err != nil {
			return fmt.Errorf("evaluation: canonicalize conformance verdicts: %w", err)
		}
		_, err = fmt.Fprintln(out, string(body))
		return err
	}
	if _, err := fmt.Fprintf(out, "Run: %s\nPosture: %s\nSuite: %s\n", runID, set.GetPosture(), set.GetSuiteVerdict()); err != nil {
		return err
	}
	for _, verdict := range set.GetVerdicts() {
		if verdict.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
			continue
		}
		caseID := verdict.GetCaseId()
		if caseID == "" {
			caseID = "(record)"
		}
		if _, err := fmt.Fprintf(out, "%s %s %s\n", caseID, verdict.GetStatus(), verdict.GetFailureClass()); err != nil {
			return err
		}
	}
	return nil
}
