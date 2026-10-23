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
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	conformanceTestRunID           = "run-1"
	conformanceTestOperatorID      = "outbound-operator"
	conformanceTestEmbeddedSession = "embedded-session"
	conformanceTestOutboundSession = "outbound-session"
)

// conformanceTestOutboundDocument is an active outbound session with no
// enrollment-bound key. Target selection reads only identity and status; the
// bound key is checked over the real stack in the integration tests.
func conformanceTestOutboundDocument() *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                conformanceTestOperatorID,
		OperatorSessionId: conformanceTestOutboundSession,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
	}
}

// conformanceTestRuntimeFiles is a fresh runtime root for the roots mapping.
func conformanceTestRuntimeFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	return files
}

// conformanceRig is one conformance command: the Gateway and Operator runtime
// roots it names, the Data Operator it targets, and the deps it reads.
type conformanceRig struct {
	gwRoot     string
	opRoot     string
	operatorID string
	// a2aListen is the A2A stub address; the stack rig's Gateway forwards A2A there.
	a2aListen string
	deps      nativeEvalDeps
}

// execute runs the command tree with args against the rig's Gateway root and
// returns its combined output.
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
	command.SetArgs(append(append([]string{}, args...), "--project-root", r.gwRoot))
	err := command.Execute()
	return out.String(), err
}

// runArgs is a complete `conformance run` invocation for this rig.
func (r conformanceRig) runArgs(extra ...string) []string {
	listen := r.a2aListen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	args := []string{"conformance", "run", "--a2a-stub-listen", listen, "--operator-id", r.operatorID, "--operator-root", r.opRoot}
	return append(args, extra...)
}

// wrapFiles returns the rig with every runtime file service it opens passed
// through wrap.
func (r conformanceRig) wrapFiles(wrap func(fs.RuntimeFileService) fs.RuntimeFileService) conformanceRig {
	open := r.deps.fileSvcFactory
	r.deps.fileSvcFactory = func(root string, logger *slog.Logger) (fs.RuntimeFileService, error) {
		files, err := open(root, logger)
		if err != nil {
			return nil, err
		}
		return wrap(files), nil
	}
	return r
}

// newConformanceStoreRig is a conformance command over a store-only runtime root:
// no Gateway is started, and the rig is for commands that fail before any Gateway
// call or only read stored evidence.
func newConformanceStoreRig(t *testing.T) conformanceRig {
	t.Helper()
	env := newStoreRunEnv(t)
	return conformanceRig{gwRoot: env.root, opRoot: testutil.TempDir(t), operatorID: conformanceTestOperatorID, deps: env.deps}
}

// conformanceVerifyRig is the verify command over one runtime root. Verification
// reads stored bytes through files and needs no Gateway, so the deps are the
// config and the file service only.
func conformanceVerifyRig(cfg *config.Config, files fs.RuntimeFileService) conformanceRig {
	return conformanceRig{gwRoot: cfg.ProjectRoot, deps: nativeEvalDeps{
		configLoader:      func(string) (*config.Config, error) { return cfg, nil },
		fileSvcFactory:    func(string, *slog.Logger) (fs.RuntimeFileService, error) { return files, nil },
		createRuntimeTree: func(ctx context.Context, runtime fs.RuntimeFileService) error { return runtime.CreateRuntimeTree(ctx) },
	}}
}

// conformanceVaultProvider opens the runtime vault from keyring the way the
// production evidence file service does: the vault key comes from the key store
// and the vault unlocks over the runtime root.
func conformanceVaultProvider(keyring keystore.Keyring) evaluation.EvidenceVaultProvider {
	return func(keys fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
		ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, keys)
		if err != nil {
			return nil, err
		}
		return ks.OpenVault()
	}
}

// storedVault builds the vault a verify command opens stored evidence through,
// given the key store the evidence was sealed under.
type storedVault func(t *testing.T, keyring keystore.Keyring) evaluation.EvidenceVaultProvider

// productionStoredVault is the runtime vault the production evidence file service opens.
func productionStoredVault(_ *testing.T, keyring keystore.Keyring) evaluation.EvidenceVaultProvider {
	return conformanceVaultProvider(keyring)
}

// lockedStoredVault is a runtime vault that was never unlocked, so every call
// fails as the real vault's calls do.
func lockedStoredVault(_ *testing.T, _ keystore.Keyring) evaluation.EvidenceVaultProvider {
	return func(keys fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
		return vault.NewVault(&vault.VaultConfig{FileSvc: keys, Logger: testutil.NewTestLogger()})
	}
}

// unreadableStoredVault opens the production vault over a runtime root that holds
// no vault key, so the key read fails before any unlock.
func unreadableStoredVault(t *testing.T, keyring keystore.Keyring) evaluation.EvidenceVaultProvider {
	t.Helper()
	empty, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	open := conformanceVaultProvider(keyring)
	return func(fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
		return open(empty)
	}
}

// conformanceRunDir is the run directory of conformanceTestRunID under the runtime.
func conformanceRunDir() string {
	return filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, conformanceTestRunID)
}

// storedRunRig is a verify rig over one run that the production sealed store wrote.
// mutate then changes the stored bytes, and open is the vault the verify command
// opens them with.
func storedRunRig(t *testing.T, open storedVault, mutate func(context.Context, fs.RuntimeFileService) error) conformanceRig {
	t.Helper()
	ctx := context.Background()
	keyring := keystoretest.NewMemoryKeyring()
	sealed, cfg := cmdtest.NewEvidenceCmdTestEnvWithKeyring(t, keyring)
	record := &evalv1.ConformanceRunRecord{RunId: conformanceTestRunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE}
	require.NoError(t, evaluation.NewStore(sealed).SaveConformanceRecord(ctx, record))
	raw := evaluation.SealedFiles(sealed)
	if mutate != nil {
		require.NoError(t, mutate(ctx, raw))
	}
	reader, err := evaluation.NewEvidenceFileService(raw, open(t, keyring))
	require.NoError(t, err)
	return conformanceVerifyRig(cfg, reader)
}

// replaceStoredRunFile overwrites one stored file of the run with body.
func replaceStoredRunFile(name string, body []byte) func(context.Context, fs.RuntimeFileService) error {
	return func(ctx context.Context, raw fs.RuntimeFileService) error {
		return raw.WriteFile(ctx, filepath.Join(conformanceRunDir(), name), body, constants.PermFilePrivate)
	}
}

// removeStoredRunFile deletes one stored file of the run.
func removeStoredRunFile(name string) func(context.Context, fs.RuntimeFileService) error {
	return func(ctx context.Context, raw fs.RuntimeFileService) error {
		return raw.Remove(ctx, filepath.Join(conformanceRunDir(), name))
	}
}

// flipRecordBytes serves the stored conformance record with one byte changed, so
// verification sees bytes that no longer match their sha256 sidecar.
type flipRecordBytes struct {
	fs.RuntimeFileService
}

func (f flipRecordBytes) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	raw, err := f.RuntimeFileService.ReadFile(ctx, relPath)
	if err != nil || len(raw) == 0 || !strings.HasSuffix(relPath, constants.ConformanceRecordFilename) {
		return raw, err
	}
	flipped := bytes.Clone(raw)
	flipped[len(flipped)/2] ^= 0x01
	return flipped, nil
}

// flipStoredRecord wraps files so the stored conformance record reads with one byte changed.
func flipStoredRecord(files fs.RuntimeFileService) fs.RuntimeFileService {
	return flipRecordBytes{RuntimeFileService: files}
}

func TestConformanceRoots_RefusesOutboundOperatorWithoutEnrollmentBoundKey(t *testing.T) {
	unbound := conformanceTestOutboundDocument()
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

func TestConformanceEmbeddedTarget_RequiresExactlyOneSession(t *testing.T) {
	embedded := func(session string) *operatorv1.OperatorDocument {
		return &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: session}
	}
	tests := []struct {
		name      string
		operators []*operatorv1.OperatorDocument
		wantErr   error
		wantOK    string
	}{
		{name: "no embedded session", operators: []*operatorv1.OperatorDocument{conformanceTestOutboundDocument()}, wantErr: constants.ErrEvaluationTargetUnavailable},
		{name: "one embedded session", operators: []*operatorv1.OperatorDocument{embedded(conformanceTestEmbeddedSession)}, wantOK: conformanceTestEmbeddedSession},
		{name: "two embedded sessions", operators: []*operatorv1.OperatorDocument{embedded("session-a"), embedded("session-b")}, wantErr: constants.ErrEvaluationTargetAmbiguous},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := conformanceEmbeddedTarget(tt.operators)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, string(constants.DocIDEmbeddedOperator), target.OperatorID)
			assert.Equal(t, tt.wantOK, target.OperatorSessionID)
		})
	}
}

func TestConformanceReport_RejectsMoreThanFourRunsBeforeTouchingState(t *testing.T) {
	rig := newConformanceStoreRig(t)
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
			rig := storedRunRig(t, productionStoredVault, tt.mutate)
			out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
			require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "a changed sealed byte fails the exit rule")
			assert.Contains(t, out, "Run: "+conformanceTestRunID)
			assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
			assert.Contains(t, out, "RECORD_DIGEST_MISMATCH")
		})
	}
}

func TestConformanceVerify_UnchangedSealedRecordIsNotTheDigestMismatchVerdict(t *testing.T) {
	rig := storedRunRig(t, productionStoredVault, nil)

	out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "an empty record is not a passing run")
	assert.Contains(t, out, "Run: "+conformanceTestRunID, "the record decrypted and was verified")
	assert.NotContains(t, out, "RECORD_DIGEST_MISMATCH")
}

func TestConformanceVerify_FlippedSealedByteIsDigestMismatchUnlessTheVaultIsLocked(t *testing.T) {
	recordPath := filepath.Join(conformanceRunDir(), constants.ConformanceRecordFilename)
	flipSealedRecord := func(ctx context.Context, raw fs.RuntimeFileService) error {
		ciphertext, err := raw.ReadFile(ctx, recordPath)
		if err != nil {
			return err
		}
		flipped := bytes.Clone(ciphertext)
		flipped[len(flipped)/2] ^= 0x01
		return raw.WriteFile(ctx, recordPath, flipped, constants.PermFilePrivate)
	}
	t.Run("the open vault refuses the changed ciphertext", func(t *testing.T) {
		rig := storedRunRig(t, productionStoredVault, flipSealedRecord)
		out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
		require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
		assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
		assert.Contains(t, out, "RECORD_DIGEST_MISMATCH", "the open vault refuses the changed ciphertext")
	})
	t.Run("the same bytes under a locked vault are a locked-vault error", func(t *testing.T) {
		rig := storedRunRig(t, lockedStoredVault, flipSealedRecord)
		out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
		require.ErrorIs(t, err, constants.ErrVaultLocked)
		assert.NotContains(t, out, "Suite: ", "no verdicts are printed for a run the vault cannot open")
		assert.NotContains(t, out, "RECORD_DIGEST_MISMATCH", "a locked vault is not evidence that the bytes changed")
	})
}

func TestConformanceVerify_LoadFailuresThatAreNotAuthenticationStayErrors(t *testing.T) {
	tests := []struct {
		name    string
		open    storedVault
		mutate  func(context.Context, fs.RuntimeFileService) error
		wantErr error
	}{
		{name: "vault locked over the sealed record", open: lockedStoredVault, wantErr: constants.ErrVaultLocked},
		{name: "vault key unreadable", open: unreadableStoredVault, wantErr: constants.ErrVaultKeyReadFailed},
		{name: "record file missing", open: productionStoredVault, mutate: removeStoredRunFile(constants.ConformanceRecordFilename), wantErr: constants.ErrNotFound},
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

func TestConformanceVerify_FlippedByteIsInvalidEvidenceAndExitsNonZero(t *testing.T) {
	tests := []struct {
		name        string
		flip        bool
		wantInvalid bool
	}{
		{name: "stored bytes unchanged", flip: false, wantInvalid: false},
		{name: "one stored byte flipped", flip: true, wantInvalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := storedRunRig(t, productionStoredVault, nil)
			if tt.flip {
				rig = rig.wrapFiles(flipStoredRecord)
			}

			out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
			if !tt.wantInvalid {
				assert.NotContains(t, out, "RECORD_DIGEST_MISMATCH", "an unchanged record must not fail the digest check")
				return
			}
			require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
			assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
			assert.Contains(t, out, "RECORD_DIGEST_MISMATCH")
		})
	}
}

func TestConformanceVerify_JSONEmitsCanonicalVerdictSet(t *testing.T) {
	rig := storedRunRig(t, productionStoredVault, nil).wrapFiles(flipStoredRecord)

	root := cmdtest.GlobalJSONRoot(t, evalCmdWithConfig(rig.deps))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs([]string{"eval", "conformance", "verify", "--run", conformanceTestRunID})

	require.ErrorIs(t, root.Execute(), constants.ErrEvalRunVerificationFailed)
	var set evalv1.ConformanceVerdictSet
	require.NoError(t, evalv1.UnmarshalCanonical(bytes.TrimSpace(out.Bytes()), &set))
	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict())
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

// TestApprovalRequestWriterPrintsOneCanonicalLinePerRequest checks the attended --json
// stderr contract: each request is one canonical JSON line that decodes back to itself, and
// no writer is no sink.
func TestApprovalRequestWriterPrintsOneCanonicalLinePerRequest(t *testing.T) {
	var nilWriter *approvalRequestWriter
	assert.Nil(t, nilWriter.sink())

	var out bytes.Buffer
	rw := &approvalRequestWriter{w: &out}
	sent := []*evalv1.ConformanceApprovalRequest{
		{CaseId: "c22", AttemptId: "run-1-0001", Ingress: "mcp", Decision: evalv1.ConformanceApprovalDecision_CONFORMANCE_APPROVAL_DECISION_LEAVE_PENDING, TxHash: "tx-22"},
		{CaseId: "c23", AttemptId: "run-1-0002", Ingress: "cli", Decision: evalv1.ConformanceApprovalDecision_CONFORMANCE_APPROVAL_DECISION_APPROVE},
	}
	for _, req := range sent {
		rw.sink()(req)
	}
	require.NoError(t, rw.Err())

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, len(sent))
	for i, line := range lines {
		got := &evalv1.ConformanceApprovalRequest{}
		require.NoError(t, evalv1.UnmarshalCanonical([]byte(line), got))
		assert.Equal(t, sent[i].GetAttemptId(), got.GetAttemptId())
		assert.Equal(t, sent[i].GetDecision(), got.GetDecision())
		assert.Equal(t, sent[i].GetTxHash(), got.GetTxHash())
	}
}
