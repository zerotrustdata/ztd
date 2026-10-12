// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	operatorcli "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	govservice "github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	harness "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	releaseSmokeSchemaVersion  = "ztd.release-smoke/v1"
	releaseSmokeDefaultTimeout = 20 * time.Minute
	releaseSmokeCleanupTimeout = 90 * time.Second
	releaseSmokeWaitTimeout    = 90 * time.Second
	releaseSmokeSummaryName    = "smoke-summary.json"
	releaseSmokeFileActionType = string(constants.ActionTypeFileEdit)
	releaseSmokeDocAction      = string(constants.ActionTypeDocumentUpdate)
	releaseSmokeCollection     = string(constants.CollectionInvestigations)
	releaseSmokeEmbeddedOpID   = string(constants.DocIDEmbeddedOperator)
)

type releaseSmokeOptions struct {
	RunDir  string
	ZtdBin  string
	Timeout time.Duration
}

type releaseSmokeSummary struct {
	SchemaVersion string                 `json:"schema_version"`
	Result        string                 `json:"result"`
	Source        releaseSmokeSource     `json:"source"`
	Platform      string                 `json:"platform"`
	BuildVersion  string                 `json:"build_version"`
	Posture       string                 `json:"posture"`
	Ingress       []string               `json:"ingress"`
	StartedAt     time.Time              `json:"started_at"`
	FinishedAt    time.Time              `json:"finished_at"`
	RunDir        string                 `json:"run_dir"`
	Gateway       releaseSmokeGateway    `json:"gateway"`
	Worker        releaseSmokeWorker     `json:"worker"`
	Scenarios     []releaseSmokeScenario `json:"scenarios"`
	Receipts      []smokeReceiptEvidence `json:"local_receipts"`
	Reports       []smokeReportEvidence  `json:"reports"`
	Commands      []releaseSmokeCommand  `json:"commands"`
	Errors        []string               `json:"errors,omitempty"`
}
type smokeReceiptEvidence struct {
	Runtime         string `json:"runtime"`
	TransactionID   string `json:"transaction_id"`
	OperatorID      string `json:"operator_id"`
	OperatorSession string `json:"operator_session_id"`
	SignerKeyID     string `json:"signer_key_id"`
	Signature       string `json:"signature"`
	Status          string `json:"status"`
}
type releaseSmokeSource struct {
	GitRevision     string `json:"git_revision"`
	GitDirty        bool   `json:"git_dirty"`
	ZtdBinarySHA256 string `json:"ztd_binary_sha256"`
}
type releaseSmokeGateway struct {
	Root      string `json:"root"`
	HTTPPort  int    `json:"http_port"`
	HTTPSPort int    `json:"https_port"`
}
type releaseSmokeWorker struct {
	Root              string `json:"root"`
	OperatorSessionID string `json:"operator_session_id"`
}
type releaseSmokeScenario struct {
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	TransactionIDs []string `json:"transaction_ids"`
}
type releaseSmokeCommand struct {
	Name      string   `json:"name"`
	Argv      []string `json:"argv"`
	ExitCode  int      `json:"exit_code"`
	StdoutLog string   `json:"stdout_log"`
	StderrLog string   `json:"stderr_log"`
}
type smokeCleanup struct {
	name string
	fn   func(context.Context) error
}
type smokePhase struct {
	name string
	run  func(context.Context) error
}

type releaseSmokeRun struct {
	opts                                                  releaseSmokeOptions
	owner                                                 smokeProcessOwner
	summary                                               releaseSmokeSummary
	logDir, gwRoot, workerRoot, reportsDir, masterKey     string
	ports                                                 struct{ http, https int }
	cleanups                                              []smokeCleanup
	workerSession                                         string
	workerOperatorID, embeddedOperatorID, embeddedSession string
	ownedRoot                                             bool
	identity                                              struct {
		UserID       string
		CLISessionID string
	}
	client                        *harness.Client
	persona                       harness.Persona
	fileTx, fileName, fileContent string
}

func releaseSmokeCmd() *cobra.Command { return releaseSmokeCmdWithRunner(runReleaseSmoke) }
func releaseSmokeCmdWithRunner(runner func(context.Context, releaseSmokeOptions) error) *cobra.Command {
	var opts releaseSmokeOptions
	cmd := &cobra.Command{
		Use:   "release-smoke",
		Short: "Run an isolated governed release smoke and verify local evidence",
		Long: `Start an isolated Gateway and dedicated data Operator. Exercise a typed governed FILE_EDIT through the supported command dispatch API and verify exact read-back. Create and merge a document through the embedded governed execution path, preserving untouched fields. Export Gateway and worker evidence from their own runtimes and require nonempty receipts, commitments, and file mutations in both runtimes, with all verification rows passing and none skipped.

The command needs a Unix-like host for process-group cleanup. It needs a built ztd binary and the checked-in Go modules; it does not use an LLM or provider key. All run artifacts are retained under --root.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.RunDir == "" {
				opts.RunDir = filepath.Join(".local.dev", "release-smoke", time.Now().UTC().Format("20060102T150405Z"))
			}
			abs, err := filepath.Abs(opts.RunDir)
			if err != nil {
				return fmt.Errorf("resolve smoke root: %w", err)
			}
			opts.RunDir = abs
			if opts.ZtdBin == "" {
				opts.ZtdBin, err = os.Executable()
				if err != nil {
					return fmt.Errorf("resolve ztd binary: %w", err)
				}
			}
			// Runtimes start in their own directories under the root, so a
			// relative --binary must be anchored to the invoking directory.
			if opts.ZtdBin, err = filepath.Abs(opts.ZtdBin); err != nil {
				return fmt.Errorf("resolve ztd binary: %w", err)
			}
			if opts.Timeout <= 0 {
				opts.Timeout = releaseSmokeDefaultTimeout
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			if err := runner(ctx, opts); err != nil {
				return err
			}
			cmd.Printf("Release smoke PASSED: %s\n", filepath.Join(opts.RunDir, "evidence", releaseSmokeSummaryName))
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.RunDir, "root", "", "Isolated run and artifact root (default: .local.dev/release-smoke/<UTC timestamp>)")
	cmd.Flags().StringVar(&opts.ZtdBin, "binary", "", "ztd binary used for the Gateway and Operator (default: running binary)")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", releaseSmokeDefaultTimeout, "Overall run deadline; owned resources are stopped when it expires")
	return cmd
}

func runReleaseSmoke(ctx context.Context, opts releaseSmokeOptions) error {
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	r := &releaseSmokeRun{opts: opts}
	return r.execute(ctx, r.phases())
}

func newReleaseSmokeRun(opts releaseSmokeOptions) *releaseSmokeRun {
	return &releaseSmokeRun{opts: opts}
}

func (r *releaseSmokeRun) execute(ctx context.Context, phases []smokePhase) error {
	r.summary.StartedAt = time.Now().UTC()
	err := r.claimRunRoot()
	if err == nil {
		err = r.runPhases(ctx, phases)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), releaseSmokeCleanupTimeout)
	defer cancel()
	cleanupErr := r.runCleanups(cleanupCtx)
	err = errors.Join(err, cleanupErr)
	r.summary.SchemaVersion = releaseSmokeSchemaVersion
	r.summary.Result = "PASS"
	if err != nil {
		r.summary.Result = "FAIL"
		r.summary.Errors = append(r.summary.Errors, strings.Split(err.Error(), "\n")...)
	}
	r.summary.FinishedAt = time.Now().UTC()
	if r.ownedRoot {
		if writeErr := r.writeSummary(); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}
	return err
}

func (r *releaseSmokeRun) claimRunRoot() error {
	parent := filepath.Dir(r.opts.RunDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create smoke parent: %w", err)
	}
	if info, err := os.Lstat(r.opts.RunDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("smoke root %s: %w", r.opts.RunDir, constants.ErrSourceTreeSymlink)
		}
		return fmt.Errorf("smoke root %s already exists; choose a new --root", r.opts.RunDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect smoke root: %w", err)
	}
	if err := os.Mkdir(r.opts.RunDir, 0o700); err != nil {
		return fmt.Errorf("claim smoke root: %w", err)
	}
	r.ownedRoot = true
	return nil
}
func (r *releaseSmokeRun) runPhases(ctx context.Context, phases []smokePhase) error {
	for _, p := range phases {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("before %s: %w", p.name, err)
		}
		slog.Default().Info("release smoke phase", "phase", p.name)
		if err := p.run(ctx); err != nil {
			return fmt.Errorf("release smoke %s: %w", p.name, err)
		}
	}
	return nil
}
func (r *releaseSmokeRun) addCleanup(name string, fn func(context.Context) error) {
	r.cleanups = append(r.cleanups, smokeCleanup{name, fn})
}
func (r *releaseSmokeRun) runCleanups(ctx context.Context) error {
	var errs []error
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		if err := r.cleanups[i].fn(ctx); err != nil {
			errs = append(errs, fmt.Errorf("cleanup %s: %w", r.cleanups[i].name, err))
		}
	}
	if err := r.owner.stopAll(5 * time.Second); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
func (r *releaseSmokeRun) phases() []smokePhase {
	return []smokePhase{{"prepare", r.prepare}, {"start isolated Gateway", r.startGateway}, {"enroll CLI owner", r.loginCLI}, {"deploy dedicated data Operator", r.deployWorker}, {"governed FILE_EDIT and read-back", r.scenarioFileCreate}, {"embedded document create and merge", r.scenarioDocument}, {"embedded governed FILE_EDIT and read-back", r.scenarioEmbeddedFile}, {"export local runtime evidence", r.exportReports}, {"verify release evidence", r.verifyEvidence}}
}

func (r *releaseSmokeRun) prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.ownedRoot {
		return fmt.Errorf("smoke root is not owned by this run: %w", constants.ErrMissingRequiredField)
	}
	r.gwRoot = filepath.Join(r.opts.RunDir, "gateway")
	r.workerRoot = filepath.Join(r.opts.RunDir, "worker")
	r.logDir = filepath.Join(r.opts.RunDir, "logs")
	r.reportsDir = filepath.Join(r.opts.RunDir, "reports")
	for _, dir := range []string{r.gwRoot, r.workerRoot, r.logDir, r.reportsDir, filepath.Join(r.opts.RunDir, "evidence"), filepath.Join(r.opts.RunDir, "secrets")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	var err error
	r.masterKey, err = keystore.ProvisionMasterKeyFile(filepath.Join(r.opts.RunDir, "secrets"))
	if err != nil {
		return fmt.Errorf("provision isolated master key: %w", err)
	}
	for _, p := range []*int{&r.ports.http, &r.ports.https} {
		*p, err = freeSmokePort()
		if err != nil {
			return err
		}
	}
	sum, err := smokeFileSHA256(r.opts.ZtdBin)
	if err != nil {
		return fmt.Errorf("hash ztd binary: %w", err)
	}
	rev, dirty, err := smokeGitSource(ctx)
	if err != nil {
		return err
	}
	r.summary.RunDir = r.opts.RunDir
	r.summary.Source = releaseSmokeSource{rev, dirty, sum}
	r.summary.Platform = runtime.GOOS + "/" + runtime.GOARCH
	r.summary.BuildVersion = "sha256:" + sum
	r.summary.Posture = constants.PostureDoctrine
	r.summary.Ingress = []string{"governed HTTP dispatch: POST /api/v1/operators/commands", "embedded governed document update: POST /api/v1/governance/envelopes"}
	r.summary.Gateway = releaseSmokeGateway{r.gwRoot, r.ports.http, r.ports.https}
	return nil
}
func (r *releaseSmokeRun) startGateway(ctx context.Context) error {
	r.addCleanup("Gateway", func(ctx context.Context) error { _, err := r.ztdGateway(ctx, "gateway-stop", "gw", "stop"); return err })
	_, err := r.ztd(ctx, "gateway-start", r.gwRoot, "gw", "start", "--http-port", strconv.Itoa(r.ports.http), "--https-port", strconv.Itoa(r.ports.https), "--listen-host", "127.0.0.1", "--cert-mode", "localhost", "--posture", "doctrine", "--public-spectator=false", "--master-key-file", r.masterKey)
	if err != nil {
		return err
	}
	return nil
}
func (r *releaseSmokeRun) loginCLI(ctx context.Context) error {
	if _, err := r.ztdGateway(ctx, "login-cli", "login", "cli"); err != nil {
		return err
	}
	cfg, err := cliconfig.Load(r.gwRoot)
	if err != nil {
		return err
	}
	fileSvc, err := shared.NewFileSvc(r.gwRoot, slog.Default())
	if err != nil {
		return err
	}
	creds, err := auth.LoadCredentials(fileSvc, cfg)
	if err != nil {
		return err
	}
	if creds == nil || creds.UserID == "" || creds.CLISessionID == "" {
		return fmt.Errorf("CLI enrollment returned no user or session ID: %w", constants.ErrMissingRequiredField)
	}
	r.identity.UserID = creds.UserID
	r.identity.CLISessionID = creds.CLISessionID
	cfg.MasterKeyFile = r.masterKey
	cliCert, err := auth.LoadCLICertificate(fileSvc, cfg)
	if err != nil {
		return err
	}
	r.client, err = harness.New(harnessconfig.Config{MTLSBaseURL: fmt.Sprintf("https://127.0.0.1:%d", r.ports.https), Auth: harnessconfig.Auth{Certificate: &cliCert, CABundle: cfg.ResolvedTrustBundlePath()}, UserID: r.identity.UserID, CLISessionID: r.identity.CLISessionID, EnvelopeTTL: 5 * time.Minute, OutDir: filepath.Join(r.opts.RunDir, "evidence")})
	if err != nil {
		return err
	}
	r.persona = harness.Persona{ID: "ztd-release-smoke", UserAgent: "ztd-release-smoke", CLISessionID: r.identity.CLISessionID, UserID: r.identity.UserID}
	return nil
}
func (r *releaseSmokeRun) deployWorker(ctx context.Context) error {
	cfg, err := cliconfig.Load(r.gwRoot)
	if err != nil {
		return err
	}
	trust := cfg.ResolvedTrustBundlePath()
	out, err := r.ztd(ctx, "deploy-worker", r.gwRoot, "-e", "127.0.0.1", "operator", "deploy", "--local", "--dest-dir", r.workerRoot, "--background", "--approve", "--capabilities", "data", "--trust-bundle", trust, "--master-key-file", r.masterKey, "--gateway-http-port", strconv.Itoa(r.ports.http), "--gateway-https-port", strconv.Itoa(r.ports.https))
	if err != nil {
		return err
	}
	session, err := parseSmokeDeploySession(out, r.workerRoot)
	if err != nil {
		return err
	}
	r.workerSession = session
	r.addCleanup("data Operator", func(ctx context.Context) error {
		_, err := r.ztdGateway(ctx, "worker-stop", "operator", "stop", session, "--grace", "5s", "--reason", "release smoke cleanup")
		return err
	})
	ops, _, err := r.client.ListOperators(ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.GetOperatorSessionId() == session && op.GetStatus() == string(constants.OperatorStatusActive) {
			r.workerOperatorID = op.GetId()
		}
	}
	if r.workerOperatorID == "" {
		return fmt.Errorf("deployed Operator session %s not active: %w", session, constants.ErrNotFound)
	}
	r.summary.Worker = releaseSmokeWorker{r.workerRoot, session}
	return nil
}
func (r *releaseSmokeRun) scenarioFileCreate(ctx context.Context) error {
	token, err := randomSmokeHex(8)
	if err != nil {
		return err
	}
	r.fileName = "release-smoke-" + token + ".txt"
	r.fileContent = "ztd-release-smoke-" + token
	payload, err := proto.Marshal(&operatorv1.FileEditRequested{FilePath: r.fileName, Operation: string(constants.FileOperationWrite), Content: r.fileContent, CreateIfMissing: true})
	if err != nil {
		return err
	}
	status, resp, body, err := r.client.DispatchCommand(ctx, r.persona, harness.DispatchCommandRequest{TargetOperatorSessionID: r.workerSession, ActionType: releaseSmokeFileActionType, Payload: payload, TargetResource: r.fileName})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 || resp == nil || !resp.Success || resp.TransactionID == "" {
		return fmt.Errorf("governed FILE_EDIT failed: status=%d response=%s", status, strings.TrimSpace(string(body)))
	}
	r.fileTx = resp.TransactionID
	if err := r.verifyReceipt(ctx, r.workerRoot, r.workerOperatorID, r.workerSession, r.fileTx); err != nil {
		return fmt.Errorf("FILE_EDIT receipt: %w", err)
	}
	readTx, err := r.readBackFile(ctx)
	if err != nil {
		return err
	}
	r.recordScenario("file-create", []string{r.fileTx, readTx})
	return nil
}

func (r *releaseSmokeRun) verifyReceipt(ctx context.Context, runtimeRoot, expectedOperator, expectedSession, tx string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := readSmokeLocalReceipt(runtimeRoot, r.masterKey, tx)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("local receipt %s: %w", tx, constants.ErrNotFound)
	}
	receipt := record.ActionReceipt
	if receipt == nil {
		return fmt.Errorf("local receipt %s has no signed canonical body: %w", tx, constants.ErrMissingRequiredField)
	}
	if record.OperatorID != expectedOperator || record.OperatorSessionID != expectedSession {
		return fmt.Errorf("receipt %s belongs to Operator %s/%s, want %s/%s", tx, record.OperatorID, record.OperatorSessionID, expectedOperator, expectedSession)
	}
	if receipt.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("receipt %s status %s, want COMPLETED", tx, receipt.Status)
	}
	if receipt.TransactionId != tx {
		return fmt.Errorf("receipt transaction %s does not match %s", receipt.TransactionId, tx)
	}
	pub, err := r.client.GetTrustedSignerPublicKey(ctx, receipt.SignerKeyId)
	if err != nil {
		return err
	}
	if err := govservice.VerifyActionReceiptSignature(receipt, pub); err != nil {
		return err
	}
	runtimeName := "worker"
	if runtimeRoot == r.gwRoot {
		runtimeName = "gateway"
	}
	r.summary.Receipts = append(r.summary.Receipts, smokeReceiptEvidence{runtimeName, tx, record.OperatorID, record.OperatorSessionID, receipt.SignerKeyId, receipt.Signature, receipt.Status.String()})
	return nil
}

// readSmokeLocalReceipt opens the runtime's canonical audit database through
// RuntimeFileService and returns the signed receipt record owned by that
// runtime. It never consults the Gateway mirror for a remote Operator receipt.
func readSmokeLocalReceipt(runtimeRoot, masterKeyFile, tx string) (*models.ActionReceiptRecord, error) {
	fileSvc, err := fs.NewRuntimeFileService(runtimeRoot, slog.Default())
	if err != nil {
		return nil, err
	}
	ks, err := keystore.Open(fileSvc, slog.Default(), keystore.Options{MasterKeyFile: masterKeyFile})
	if err != nil {
		return nil, fmt.Errorf("open runtime keystore: %w", err)
	}
	key, err := ks.LoadVaultKey()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrVaultKeyReadFailed, err)
	}
	defer vault.SecureZero(key)
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: slog.Default()})
	if err != nil {
		return nil, err
	}
	if err := v.Unlock(key); err != nil {
		return nil, err
	}
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), slog.Default())
	if err != nil {
		return nil, err
	}
	cfg := storage.DefaultAuditStoreConfig()
	cfg.EncryptionVault = v
	store, err := storage.NewSQLAuditStore(cfg, db, slog.Default(), fileSvc)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	defer store.Close()
	defer db.Close()
	return store.GetActionReceipt(tx)
}
func (r *releaseSmokeRun) readBackFile(ctx context.Context) (string, error) {
	out, err := r.ztdGateway(ctx, "file-read-back", "--json", "operator", "run", r.workerSession, "--cmd", "cat -- "+r.fileName, "--timeout", "60")
	if err != nil {
		return "", err
	}
	var resp struct {
		Results []operatorcli.RunResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("decode governed read-back: %w", err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Success || resp.Results[0].ExitCode != 0 {
		return "", fmt.Errorf("governed read-back dispatch did not complete: %w", constants.ErrValidationFailed)
	}
	if got := resp.Results[0].Stdout; got != r.fileContent {
		return "", fmt.Errorf("read-back mismatch for %s: got %q, want exact %q", r.fileName, got, r.fileContent)
	}
	if err := r.verifyReceipt(ctx, r.workerRoot, r.workerOperatorID, r.workerSession, resp.Results[0].TransactionID); err != nil {
		return "", fmt.Errorf("read-back receipt: %w", err)
	}
	return resp.Results[0].TransactionID, nil
}
func (r *releaseSmokeRun) scenarioDocument(ctx context.Context) error {
	id, session, err := r.embeddedOperator(ctx)
	if err != nil {
		return err
	}
	r.embeddedOperatorID, r.embeddedSession = id, session
	token, err := randomSmokeHex(8)
	if err != nil {
		return err
	}
	docID := "release-smoke-" + token
	title := "release smoke " + token
	caseID := "case-" + token
	open, closed := "open", "closed"
	stateRoot, err := r.client.StateRootFromMTLS(ctx)
	if err != nil {
		return err
	}
	createTx, err := r.submitDocument(ctx, id, session, stateRoot, harness.DocumentUpdateRequest{Collection: releaseSmokeCollection, DocumentID: docID, Merge: false, Updates: harness.InvestigationUpdate{CaseTitle: &title, CaseID: &caseID, Status: &open}})
	if err != nil {
		return err
	}
	if err := r.verifyReceipt(ctx, r.gwRoot, id, session, createTx); err != nil {
		return fmt.Errorf("document create receipt: %w", err)
	}
	stateRoot, err = r.client.StateRootFromMTLS(ctx)
	if err != nil {
		return err
	}
	mergeTx, err := r.submitDocument(ctx, id, session, stateRoot, harness.DocumentUpdateRequest{Collection: releaseSmokeCollection, DocumentID: docID, Merge: true, Updates: harness.InvestigationUpdate{Status: &closed}})
	if err != nil {
		return err
	}
	if err := r.verifyReceipt(ctx, r.gwRoot, id, session, mergeTx); err != nil {
		return fmt.Errorf("document merge receipt: %w", err)
	}
	doc, _, err := r.client.GetDocument(ctx, r.persona, releaseSmokeCollection, docID)
	if err != nil {
		return err
	}
	if doc == nil || doc.GetString("case_title") != title || doc.GetString("case_id") != caseID || doc.GetString("status") != closed {
		return fmt.Errorf("document merge read-back lost or changed fields: %#v", doc)
	}
	r.recordScenario("document-merge", []string{createTx, mergeTx})
	return nil
}
func (r *releaseSmokeRun) scenarioEmbeddedFile(ctx context.Context) error {
	id, session, err := r.embeddedOperator(ctx)
	if err != nil {
		return err
	}
	token, err := randomSmokeHex(8)
	if err != nil {
		return err
	}
	name := "release-smoke-embedded-" + token + ".txt"
	content := "ztd-release-smoke-embedded-" + token
	payload, err := proto.Marshal(&operatorv1.FileEditRequested{FilePath: name, Operation: string(constants.FileOperationWrite), Content: content, CreateIfMissing: true})
	if err != nil {
		return err
	}
	status, resp, body, err := r.client.DispatchCommand(ctx, r.persona, harness.DispatchCommandRequest{TargetOperatorSessionID: session, ActionType: releaseSmokeFileActionType, Payload: payload, TargetResource: name})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 || resp == nil || !resp.Success || resp.TransactionID == "" {
		return fmt.Errorf("embedded governed FILE_EDIT failed: status=%d response=%s", status, strings.TrimSpace(string(body)))
	}
	if err := r.verifyReceipt(ctx, r.gwRoot, id, session, resp.TransactionID); err != nil {
		return fmt.Errorf("embedded FILE_EDIT receipt: %w", err)
	}
	out, err := r.ztdGateway(ctx, "embedded-file-read-back", "--json", "operator", "run", session, "--cmd", "cat -- "+name, "--timeout", "60")
	if err != nil {
		return err
	}
	var result struct {
		Results []operatorcli.RunResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return fmt.Errorf("decode embedded governed read-back: %w", err)
	}
	if len(result.Results) != 1 || !result.Results[0].Success || result.Results[0].ExitCode != 0 || result.Results[0].Stdout != content {
		return fmt.Errorf("embedded governed read-back did not match exact content: %w", constants.ErrValidationFailed)
	}
	if err := r.verifyReceipt(ctx, r.gwRoot, id, session, result.Results[0].TransactionID); err != nil {
		return fmt.Errorf("embedded read-back receipt: %w", err)
	}
	r.recordScenario("embedded-file-create", []string{resp.TransactionID, result.Results[0].TransactionID})
	return nil
}

func (r *releaseSmokeRun) embeddedOperator(ctx context.Context) (string, string, error) {
	ops, _, err := r.client.ListOperators(ctx)
	if err != nil {
		return "", "", err
	}
	var id, session string
	matches := 0
	for _, op := range ops {
		if op.GetId() == releaseSmokeEmbeddedOpID && op.GetOperatorSessionId() != "" && op.GetStatus() == string(constants.OperatorStatusActive) {
			id, session = op.GetId(), op.GetOperatorSessionId()
			matches++
		}
	}
	if matches != 1 {
		return "", "", fmt.Errorf("expected one active embedded Operator; found %d", matches)
	}
	return id, session, nil
}
func (r *releaseSmokeRun) submitDocument(ctx context.Context, id, session, stateRoot string, req harness.DocumentUpdateRequest) (string, error) {
	req.OperatorID = id
	req.OperatorSessionID = session
	req.RequestorUserID = r.identity.UserID
	req.StateRoot = stateRoot
	req.TTL = 5 * time.Minute
	tx, status, body, err := r.client.SubmitDocumentUpdate(ctx, r.persona, req)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("document update HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	return tx, nil
}
func (r *releaseSmokeRun) recordScenario(name string, txs []string) {
	r.summary.Scenarios = append(r.summary.Scenarios, releaseSmokeScenario{Name: name, Status: "PASS", TransactionIDs: txs})
}
func (r *releaseSmokeRun) exportReports(ctx context.Context) error {
	var reportErrs []error
	for _, target := range []struct{ name, root string }{{"gateway", r.gwRoot}, {"worker", r.workerRoot}} {
		out := filepath.Join(r.reportsDir, target.name)
		if _, err := r.ztd(ctx, "report-"+target.name, r.gwRoot, "report", "all", "--runtime-dir", filepath.Join(target.root, constants.RuntimeDirname), "--out", out, "--master-key-file", r.masterKey); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			reportErrs = append(reportErrs, fmt.Errorf("report %s: %w", target.name, err))
		}
	}
	return errors.Join(reportErrs...)
}
func (r *releaseSmokeRun) verifyEvidence(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	worker, err := evaluateSmokeReport("worker", filepath.Join(r.reportsDir, "worker"))
	if err != nil {
		return err
	}
	gateway, err := evaluateSmokeReport("gateway", filepath.Join(r.reportsDir, "gateway"))
	if err != nil {
		return err
	}
	worker.requireCompletedReceipt(releaseSmokeFileActionType, r.fileTx)
	for _, tx := range r.documentTransactions() {
		gateway.requireCompletedReceipt(releaseSmokeDocAction, tx)
	}
	for _, tx := range r.embeddedFileTransactions() {
		gateway.requireCompletedReceipt(releaseSmokeFileActionType, tx)
	}
	r.summary.Reports = []smokeReportEvidence{worker, gateway}
	var errs []error
	for _, rep := range r.summary.Reports {
		for _, f := range rep.Failures {
			errs = append(errs, fmt.Errorf("%s: %s", rep.Name, f))
		}
	}
	return errors.Join(errs...)
}
func (r *releaseSmokeRun) documentTransactions() []string {
	for _, s := range r.summary.Scenarios {
		if s.Name == "document-merge" {
			return s.TransactionIDs
		}
	}
	return nil
}
func (r *releaseSmokeRun) embeddedFileTransactions() []string {
	for _, s := range r.summary.Scenarios {
		if s.Name == "embedded-file-create" && len(s.TransactionIDs) > 0 {
			return s.TransactionIDs[:1]
		}
	}
	return nil
}
func (r *releaseSmokeRun) ztdGateway(ctx context.Context, name string, args ...string) (string, error) {
	return r.ztd(ctx, name, r.gwRoot, append([]string{"-e", fmt.Sprintf("127.0.0.1:%d", r.ports.http), "-p", strconv.Itoa(r.ports.https), "--master-key-file", r.masterKey}, args...)...)
}
func (r *releaseSmokeRun) ztd(ctx context.Context, name, dir string, args ...string) (string, error) {
	argv := append([]string{r.opts.ZtdBin}, args...)
	prefix := filepath.Join(r.logDir, name)
	p, err := r.owner.start(name, dir, prefix, argv, nil)
	if err != nil {
		return "", err
	}
	waitErr := r.owner.wait(ctx, p)
	r.summary.Commands = append(r.summary.Commands, releaseSmokeCommand{Name: name, Argv: redactArgv(argv), ExitCode: smokeExitCode(waitErr), StdoutLog: p.stdoutPath, StderrLog: prefix + ".stderr.log"})
	out, readErr := os.ReadFile(p.stdoutPath)
	if waitErr != nil {
		return string(out), fmt.Errorf("command %s: %w", name, waitErr)
	}
	return string(out), readErr
}
func (r *releaseSmokeRun) writeSummary() error {
	data, err := json.MarshalIndent(r.summary, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.opts.RunDir, "evidence", releaseSmokeSummaryName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
func redactArgv(argv []string) []string {
	out := append([]string(nil), argv...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--master-key-file" {
			out[i+1] = "<run-master-key>"
		}
	}
	return out
}
func parseSmokeDeploySession(out, dir string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, dir) {
			if i := strings.LastIndex(line, " session "); i >= 0 && strings.TrimSpace(line[i+9:]) != "" {
				return strings.TrimSpace(line[i+9:]), nil
			}
		}
	}
	return "", fmt.Errorf("operator deploy output did not identify session for %s", dir)
}
func smokeExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
func freeSmokePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func randomSmokeHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func smokeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func smokeGitSource(ctx context.Context) (string, bool, error) {
	rev, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false, err
	}
	status, err := exec.CommandContext(ctx, "git", "status", "--porcelain").Output()
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(rev)), len(strings.TrimSpace(string(status))) > 0, nil
}
