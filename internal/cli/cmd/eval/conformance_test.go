// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/run"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	conformanceTestRunID           = "run-1"
	conformanceTestOperatorID      = "outbound-operator"
	conformanceTestEmbeddedSession = "embedded-session"
	conformanceTestOutboundSession = "outbound-session"
	conformanceTestUserID          = "user-1"
	conformanceTestCLISession      = "cli-session-1"
)

// conformanceTestActuatorKey is the outbound Operator's actuator key as bound at enrollment.
var conformanceTestActuatorKey = hex.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize)).Public().(ed25519.PublicKey))

// conformanceTestOutboundDocument is the outbound Operator's active session, which carries its enrollment-bound key.
func conformanceTestOutboundDocument() *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                conformanceTestOperatorID,
		OperatorSessionId: conformanceTestOutboundSession,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		ActuatorPublicKey: conformanceTestActuatorKey,
		ActuatorKeyId:     conformanceTestActuatorKey,
	}
}

// conformanceTestOperators is the Operator inventory of a healthy Gateway: the
// embedded Operator and one active outbound session.
func conformanceTestOperators() []*operatorv1.OperatorDocument {
	return []*operatorv1.OperatorDocument{
		{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: conformanceTestEmbeddedSession},
		conformanceTestOutboundDocument(),
	}
}

// conformanceTestRuntimeFiles is a fresh runtime root for the roots mapping.
func conformanceTestRuntimeFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	return files
}

// fakeConformancePoster is the CLI driver's poster. The tests here never post.
type fakeConformancePoster struct{}

func (fakeConformancePoster) Post(string, any) ([]byte, error) {
	return nil, errors.New("conformance test: poster is not used")
}

// rigOptions tunes one conformance command under test.
type rigOptions struct {
	posture   constants.Posture
	operators []*operatorv1.OperatorDocument
	run       func(context.Context, run.Config) (*evalv1.ConformanceRunRecord, error)
	// wrapGateway wraps the Gateway's runtime file service, e.g. to corrupt reads.
	wrapGateway func(fs.RuntimeFileService) fs.RuntimeFileService
	// serveGateway starts the Gateway the rig's client dials and returns its base URL.
	// Nil leaves the rig without a Gateway, so a test that never dials opens no socket.
	serveGateway func(t *testing.T, posture constants.Posture, operators []*operatorv1.OperatorDocument) string
}

// conformanceRig is one conformance command: a Gateway served by httptest, two
// temporary runtime roots, and fakes for the run and the CLI poster.
type conformanceRig struct {
	gwRoot string
	opRoot string
	deps   nativeEvalDeps
}

// newConformanceRig builds the deps the command reads. The run defaults to a
// fake that returns an empty record for the id it was given.
func newConformanceRig(t *testing.T, opts rigOptions) conformanceRig {
	t.Helper()
	if opts.posture == "" {
		opts.posture = constants.PostureDoctrine
	}
	if opts.operators == nil {
		opts.operators = conformanceTestOperators()
	}
	if opts.run == nil {
		opts.run = func(_ context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error) {
			return &evalv1.ConformanceRunRecord{RunId: cfg.RunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, Attended: cfg.Attended}, nil
		}
	}
	gatewayURL := ""
	if opts.serveGateway != nil {
		gatewayURL = opts.serveGateway(t, opts.posture, opts.operators)
	}
	rig := conformanceRig{gwRoot: testutil.TempDir(t), opRoot: testutil.TempDir(t)}
	gwRoot := rig.gwRoot
	rig.deps = nativeEvalDeps{
		configLoader: func(string) (*config.Config, error) { return &config.Config{ProjectRoot: gwRoot}, nil },
		fileSvcFactory: func(root string, _ *slog.Logger) (fs.RuntimeFileService, error) {
			files, err := fs.NewRuntimeFileService(root, testutil.NewTestLogger())
			if err != nil {
				return nil, err
			}
			if root == gwRoot && opts.wrapGateway != nil {
				return opts.wrapGateway(files), nil
			}
			return files, nil
		},
		createRuntimeTree: func(ctx context.Context, files fs.RuntimeFileService) error { return files.CreateRuntimeTree(ctx) },
		clientFactory: func(cfg harnessconfig.Config) (*harnessclient.Client, error) {
			cfg.MTLSBaseURL = gatewayURL
			cfg.PublicBaseURL = gatewayURL
			return harnessclient.New(cfg)
		},
		authLoader: func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
			return &auth.ClientAuthContext{UserID: conformanceTestUserID, CLISessionID: conformanceTestCLISession}, nil
		},
		conformance: conformanceDeps{
			run:    opts.run,
			listen: drive.StartA2AStub,
			poster: func(fs.RuntimeFileService, *config.Config) (cliop.CommandPoster, error) {
				return fakeConformancePoster{}, nil
			},
		},
		now:   time.Now,
		newID: func() (string, error) { return conformanceTestRunID, nil },
	}
	return rig
}

// execute runs the command tree with args and returns its combined output.
func (r conformanceRig) execute(t *testing.T, args ...string) (string, error) {
	return r.executeInput(t, "", args...)
}

func (r conformanceRig) executeInput(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	command := evalCmdWithConfig(r.deps)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetIn(strings.NewReader(input))
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

// runArgs is a complete `conformance run` invocation for this rig.
func (r conformanceRig) runArgs(extra ...string) []string {
	args := []string{"conformance", "run", "--a2a-stub-listen", "127.0.0.1:0", "--operator-id", conformanceTestOperatorID, "--operator-root", r.opRoot}
	return append(args, extra...)
}

func TestConformanceRoots_OutboundRootCarriesTheEnrollmentBoundKeyOnly(t *testing.T) {
	roots, err := conformanceRoots(conformanceTestRuntimeFiles(t), conformanceTestRuntimeFiles(t), conformanceTestOperatorID, conformanceTestOutboundDocument())
	require.NoError(t, err)

	assert.Nil(t, roots[cases.ExecutorEmbedded].Bound, "the embedded anchor is read from the Gateway's own export")
	bound := roots[cases.ExecutorOutbound].Bound
	require.NotNil(t, bound)
	assert.Equal(t, conformanceTestOperatorID, bound.GetRuntimeId())
	assert.Equal(t, conformanceTestActuatorKey, bound.GetKeyId())
	assert.Equal(t, conformanceTestActuatorKey, bound.GetPublicKeyHex())
	assert.Empty(t, bound.GetExportSha256(), "the export digest is checked and pinned by run.Run, not by the CLI")
}

func TestConformanceRoots_RefusesOutboundOperatorWithoutEnrollmentBoundKey(t *testing.T) {
	unbound := conformanceTestOutboundDocument()
	unbound.ActuatorPublicKey = ""
	unbound.ActuatorKeyId = ""
	tests := []struct {
		name string
		doc  *operatorv1.OperatorDocument
	}{
		{name: "no outbound document", doc: nil},
		{name: "outbound document without an actuator key", doc: unbound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roots, err := conformanceRoots(conformanceTestRuntimeFiles(t), conformanceTestRuntimeFiles(t), conformanceTestOperatorID, tt.doc)
			assert.ErrorIs(t, err, constants.ErrConformanceAnchorInvalid)
			assert.Nil(t, roots)
		})
	}
}

func TestConformanceReport_RejectsMoreThanFourRunsBeforeTouchingState(t *testing.T) {
	rig := newConformanceRig(t, rigOptions{})
	rig.deps.configLoader = func(string) (*config.Config, error) {
		t.Fatal("config loader called before the run count was checked")
		return nil, nil
	}
	args := []string{"conformance", "report"}
	for _, id := range []string{"run-a", "run-b", "run-c", "run-d", "run-e"} {
		args = append(args, "--run", id)
	}
	_, err := rig.execute(t, args...)
	assert.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

// errConformanceTestAuth is what sealedTestVault returns for bytes it did not seal.
var errConformanceTestAuth = errors.New("conformance test: ciphertext does not authenticate")

// conformanceTestSealedPrefix marks the bytes sealedTestVault seals.
const conformanceTestSealedPrefix = "sealed:"

// sealedTestVault seals by prefixing and opens only what it sealed, so a stored
// byte that changed no longer authenticates.
type sealedTestVault struct{}

func (sealedTestVault) EncryptWithAAD(plaintext, _ []byte) ([]byte, error) {
	return append([]byte(conformanceTestSealedPrefix), plaintext...), nil
}

func (sealedTestVault) DecryptWithAAD(ciphertext, _ []byte) ([]byte, error) {
	body, ok := bytes.CutPrefix(ciphertext, []byte(conformanceTestSealedPrefix))
	if !ok {
		return nil, errConformanceTestAuth
	}
	return body, nil
}

// lockedTestVault is a runtime vault that is locked: every call fails as the
// real vault's calls do.
type lockedTestVault struct{}

func (lockedTestVault) EncryptWithAAD([]byte, []byte) ([]byte, error) {
	return nil, constants.ErrVaultLocked
}

func (lockedTestVault) DecryptWithAAD([]byte, []byte) ([]byte, error) {
	return nil, constants.ErrVaultLocked
}

func openSealedTestVault(fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
	return sealedTestVault{}, nil
}

// conformanceTestRunDir is the run directory of conformanceTestRunID under the runtime.
func conformanceTestRunDir() string {
	return filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, conformanceTestRunID)
}

// storedRunRig is a verify rig over one run that the evidence file service sealed
// with sealedTestVault. mutate then changes the stored bytes, and open is the
// runtime vault the verify command opens.
func storedRunRig(t *testing.T, open evaluation.EvidenceVaultProvider, mutate func(context.Context, fs.RuntimeFileService) error) conformanceRig {
	t.Helper()
	ctx := context.Background()
	rig := newConformanceRig(t, rigOptions{})
	raw, err := fs.NewRuntimeFileService(rig.gwRoot, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, raw.CreateRuntimeTree(ctx))
	sealer, err := evaluation.NewEvidenceFileService(raw, openSealedTestVault)
	require.NoError(t, err)
	record := &evalv1.ConformanceRunRecord{RunId: conformanceTestRunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE}
	require.NoError(t, evaluation.NewStore(sealer).SaveConformanceRecord(ctx, record))
	if mutate != nil {
		require.NoError(t, mutate(ctx, raw))
	}
	reader, err := evaluation.NewEvidenceFileService(raw, open)
	require.NoError(t, err)
	rig.deps.fileSvcFactory = func(string, *slog.Logger) (fs.RuntimeFileService, error) { return reader, nil }
	return rig
}

// replaceStoredRunFile overwrites one stored file of the run with body.
func replaceStoredRunFile(name string, body []byte) func(context.Context, fs.RuntimeFileService) error {
	return func(ctx context.Context, raw fs.RuntimeFileService) error {
		return raw.WriteFile(ctx, filepath.Join(conformanceTestRunDir(), name), body, constants.PermFilePrivate)
	}
}

// removeStoredRunFile deletes one stored file of the run.
func removeStoredRunFile(name string) func(context.Context, fs.RuntimeFileService) error {
	return func(ctx context.Context, raw fs.RuntimeFileService) error {
		return raw.Remove(ctx, filepath.Join(conformanceTestRunDir(), name))
	}
}

func TestConformanceVerify_SealedBytesThatFailAuthenticationAreTheDigestMismatchVerdict(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, fs.RuntimeFileService) error
	}{
		{name: "record ciphertext changed on disk", mutate: replaceStoredRunFile(constants.ConformanceRecordFilename, []byte("changed"))},
		{name: "digest sidecar ciphertext changed on disk", mutate: replaceStoredRunFile(constants.ConformanceRecordDigestFilename, []byte("changed"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := storedRunRig(t, openSealedTestVault, tt.mutate)
			out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
			require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "a changed sealed byte fails the exit rule")
			assert.Contains(t, out, "Run: "+conformanceTestRunID)
			assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
			assert.Contains(t, out, "RECORD_DIGEST_MISMATCH")
		})
	}
}

func TestConformanceVerify_UnchangedSealedRecordIsNotTheDigestMismatchVerdict(t *testing.T) {
	rig := storedRunRig(t, openSealedTestVault, nil)

	out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "an empty record is not a passing run")
	assert.Contains(t, out, "Run: "+conformanceTestRunID, "the record decrypted and was verified")
	assert.NotContains(t, out, "RECORD_DIGEST_MISMATCH")
}

func TestConformanceVerify_LoadFailuresThatAreNotAuthenticationStayErrors(t *testing.T) {
	lockedOpen := func(fs.RuntimeFileService) (evaluation.EvidenceVault, error) { return lockedTestVault{}, nil }
	unreadableOpen := func(fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
		return nil, constants.ErrVaultKeyReadFailed
	}
	tests := []struct {
		name    string
		open    evaluation.EvidenceVaultProvider
		mutate  func(context.Context, fs.RuntimeFileService) error
		wantErr error
	}{
		{name: "vault locked over the sealed record", open: lockedOpen, wantErr: constants.ErrVaultLocked},
		{name: "vault key unreadable", open: unreadableOpen, wantErr: constants.ErrVaultKeyReadFailed},
		{name: "record file missing", open: openSealedTestVault, mutate: removeStoredRunFile(constants.ConformanceRecordFilename), wantErr: constants.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := storedRunRig(t, tt.open, tt.mutate)
			out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
			require.ErrorIs(t, err, tt.wantErr)
			assert.NotErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "no verdict set is computed, so the exit rule never runs")
			assert.NotContains(t, out, "Suite: ", "no verdicts are printed")
			assert.NotContains(t, out, "RECORD_DIGEST_MISMATCH")
		})
	}
}

func TestConformanceVerdictError_PassesOnlyPassOrUnattendedIncomplete(t *testing.T) {
	tests := []struct {
		name     string
		verdict  evalv1.ConformanceSuiteVerdict
		attended bool
		wantErr  bool
	}{
		{name: "pass", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS},
		{name: "incomplete unattended", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE},
		{name: "incomplete attended", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE, attended: true, wantErr: true},
		{name: "fail", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_FAIL, wantErr: true},
		{name: "invalid evidence", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, wantErr: true},
		{name: "unspecified", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_UNSPECIFIED, wantErr: true},
		{name: "pass attended", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS, attended: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conformanceVerdictError(&evalv1.ConformanceVerdictSet{SuiteVerdict: tt.verdict}, tt.attended)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
		})
	}
}
