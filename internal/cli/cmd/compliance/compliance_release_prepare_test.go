// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliancecmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pathutil"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const releasePrepareTestDigest = "5f0c3c1f0d5b4e2a9c7d8e6f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"

var releasePrepareTestNow = time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC)

type releasePrepareFixture struct {
	repoRoot    string
	projectRoot string
	metadata    string
	privateKey  string
	opts        releasePrepareOptions
	deps        releasePrepareDeps
	calls       map[string][]string
	verifyValid bool
	binaryCalls int
	dbPath      string
	keystore    *keystore.Keystore
}

func newReleasePrepareFixture(t *testing.T) *releasePrepareFixture {
	t.Helper()
	base := testutil.TempDir(t)
	fixture := &releasePrepareFixture{
		repoRoot:    filepath.Join(base, "repo"),
		projectRoot: filepath.Join(base, "runtime"),
		calls:       make(map[string][]string),
		verifyValid: true,
	}
	require.NoError(t, os.MkdirAll(fixture.repoRoot, constants.PermDirStandard))
	require.NoError(t, os.WriteFile(filepath.Join(fixture.repoRoot, constants.ComplianceReleaseVersionFilename), []byte("v2.2.2\n"), constants.PermFilePrivate))
	fixture.metadata = filepath.Join(base, "signing-metadata.json")
	fixture.privateKey = filepath.Join(base, "signing-private-key.hex")
	fixture.opts = releasePrepareOptions{
		projectRoot:       fixture.projectRoot,
		repoRoot:          fixture.repoRoot,
		windowSpan:        constants.ComplianceReleaseDefaultWindowSpan,
		maxRows:           constants.ComplianceOperationalExportDefaultMaxRows,
		organization:      "Example Org",
		assessor:          constants.ComplianceReleaseDefaultAssessor,
		signingMetadata:   fixture.metadata,
		signingPrivateKey: fixture.privateKey,
		newKey:            true,
	}
	bundlePath := filepath.Join(fixture.projectRoot, "bundle", "bundle.json")
	// The runtime vault key lives in an in-memory key store, so the command's
	// unlock and the seeded ciphertext share one key without the OS key store.
	fileSvc := releasePrepareFileService(t, fixture.projectRoot)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())
	fixture.keystore = ks
	fixture.deps = releasePrepareDeps{
		fileSvcFactory: fs.NewRuntimeFileService,
		runtimeVault: func(fileSvc fs.RuntimeFileService) (*vault.Vault, func(), error) {
			return openVault(fixture.keystore)
		},
		signingLoader: loadComplianceReportSigningIdentity,
		binaryDigest: func() (string, error) {
			fixture.binaryCalls++
			return releasePrepareTestDigest, nil
		},
		gitRevision: func(context.Context, string) (string, error) { return strings.Repeat("a", 40), nil },
		newGenerateCmd: func() *cobra.Command {
			return fixture.fakeCommand("generate", filepath.Join(fixture.projectRoot, constants.RuntimeDirname, constants.ComplianceBundlesDirname, "sealed", constants.ComplianceBundleManifestPath)+"\n")
		},
		newExportCmd: func() *cobra.Command { return fixture.fakeCommand("export", bundlePath+"\n") },
		newVerifyCmd: func() *cobra.Command {
			report, err := compliancev1.MarshalCanonical(&compliancev1.ComplianceVerificationReport{
				ReportId:               "v2.2.2-gateway-operational-public",
				Valid:                  fixture.verifyValid,
				VerifierId:             constants.ComplianceBundleVerifierID,
				VerifierVersion:        constants.ComplianceBundleVerifierVersion,
				ReproducedChecksumRoot: releasePrepareTestDigest,
			})
			require.NoError(t, err)
			return fixture.fakeCommand("verify", string(report)+"\n")
		},
		newProjectCmd: func() *cobra.Command {
			return fixture.fakeCommand("project", "wrote docs/release_notes/v2.2.x/v2.2.2-compliance-evidence.md\n")
		},
		random: rand.Reader,
		now:    func() time.Time { return releasePrepareTestNow },
	}
	return fixture
}

// fakeCommand records the raw arguments an in-process sub-command receives and
// prints canned output, so the orchestration is tested without re-running the
// generate, verify, and projection implementations that have their own tests.
func (f *releasePrepareFixture) fakeCommand(name, output string) *cobra.Command {
	return &cobra.Command{
		Use:                name,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.calls[name] = append([]string(nil), args...)
			_, err := fmt.Fprint(cmd.OutOrStdout(), output)
			return err
		},
	}
}

func (f *releasePrepareFixture) flagValue(t *testing.T, call, flag string) string {
	t.Helper()
	args, ok := f.calls[call]
	require.True(t, ok, "%s was not run", call)
	for index, arg := range args {
		if arg == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	t.Fatalf("%s was run without %s: %v", call, flag, args)
	return ""
}

// seedRuntimeDatabase creates the runtime audit database the export reads.
// The schema carries only the columns the read-only snapshot queries. Content
// columns hold ciphertext sealed by the fixture's runtime vault, as the runtime
// writes them.
func (f *releasePrepareFixture) seedRuntimeDatabase(t *testing.T, receiptSigner, auditor string, receiptsAt, commitmentsAt []time.Time) {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(f.projectRoot, testutil.NewTestLogger())
	require.NoError(t, err)
	encryptionVault, cleanup, err := openVault(f.keystore)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	dbPath := pathutil.ResolveDBPath(fileSvc.Resolve(constants.DataDirname), constants.DbFilename)
	f.dbPath = dbPath
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), constants.PermDirStandard))
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`
		CREATE TABLE commitment_ledger (id INTEGER PRIMARY KEY AUTOINCREMENT, transaction_id TEXT NOT NULL, committed_at_unix_ms INTEGER NOT NULL, hash TEXT NOT NULL, attestation_json BLOB);
		CREATE TABLE events (seq INTEGER, prev_hash TEXT, hash TEXT, type TEXT, operator_session_id TEXT, timestamp TEXT, content_digest TEXT, transaction_id TEXT, content_text BLOB, command_raw BLOB, command_exit_code INTEGER, command_stdout BLOB, command_stderr BLOB, execution_duration_ms INTEGER, execution_id TEXT, event_receipt_status INTEGER, event_receipt_executed_at_ms INTEGER);
	`)
	require.NoError(t, err)
	for index, executedAt := range receiptsAt {
		transactionID := fmt.Sprintf("tx-%d", index)
		body, marshalErr := compliancev1.MarshalCanonical(&operatorv1.EventReceipt{TransactionId: transactionID, SignerKeyId: receiptSigner})
		require.NoError(t, marshalErr)
		seq := int64(index + 1)
		sealed := storagetest.EncryptEventContent(t, encryptionVault, seq, body)
		_, err = db.Exec(`INSERT INTO events (seq, type, transaction_id, content_text, event_receipt_status, event_receipt_executed_at_ms) VALUES (?, ?, ?, ?, ?, ?)`, seq, string(constants.EventOperatorReceiptRecorded), transactionID, sealed, int64(operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED), executedAt.UnixMilli())
		require.NoError(t, err)
	}
	for index, committedAt := range commitmentsAt {
		body, marshalErr := compliancev1.MarshalCanonical(&operatorv1.CommitmentAttestation{AuditorKeyId: auditor})
		require.NoError(t, marshalErr)
		commitmentHash := fmt.Sprintf("commitment-%d", index)
		sealed := storagetest.EncryptCommitmentAttestation(t, encryptionVault, commitmentHash, string(body))
		_, err = db.Exec(`INSERT INTO commitment_ledger (transaction_id, committed_at_unix_ms, hash, attestation_json) VALUES (?, ?, ?, ?)`, fmt.Sprintf("tx-%d", index), committedAt.UnixMilli(), commitmentHash, sealed)
		require.NoError(t, err)
	}
}

func releasePrepareSignerKey(t *testing.T) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return hex.EncodeToString(publicKey)
}

func TestRunReleasePrepare_DerivesInputsThenGeneratesExportsVerifiesAndProjectsInOrder(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	receiptSigner, auditor := releasePrepareSignerKey(t), releasePrepareSignerKey(t)
	commitAt := time.Date(2026, 9, 28, 2, 58, 52, 999_000_000, time.UTC)
	fixture.seedRuntimeDatabase(t, receiptSigner, auditor,
		[]time.Time{commitAt.Add(-3 * time.Second), commitAt.Add(-time.Second)}, []time.Time{commitAt.Add(-2 * time.Second), commitAt})

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReleasePrepare(context.Background(), fixture.deps, fixture.opts, &stdout, &stderr))

	workDir := filepath.Join(fixture.projectRoot, constants.ComplianceReleaseWorkDirname, "v2.2.2")
	scope, err := loadComplianceAssessmentScope(filepath.Join(workDir, constants.ComplianceReleaseScopeFilename))
	require.NoError(t, err)
	assert.Equal(t, "2.2.2", scope.GetProductVersion())
	assert.Equal(t, "gateway-operational-20260928", scope.GetScopeId())
	assert.Equal(t, commitAt, scope.GetAssessmentWindowEnd().AsTime(), "the window ends at the newest ledger commitment")
	assert.Equal(t, commitAt.Add(-constants.ComplianceReleaseDefaultWindowSpan), scope.GetAssessmentWindowStart().AsTime())
	assert.Equal(t, releasePrepareTestDigest, scope.GetBuildIdentity(), "build identity defaults to the binary digest")
	assert.Equal(t, releasePrepareTestDigest, scope.GetComponentInventory()[0].GetDigest(), "the gateway component is identified by the running binary")
	assert.Equal(t, strings.Repeat("a", 40), scope.GetSourceRevision())
	assert.Equal(t, 1, fixture.binaryCalls)

	reportTrustBody, err := os.ReadFile(filepath.Join(workDir, constants.ComplianceReleaseReportTrustFilename))
	require.NoError(t, err)
	reportTrust := &compliancev1.ComplianceReportTrustPolicy{}
	require.NoError(t, compliancev1.UnmarshalCanonical(reportTrustBody, reportTrust))
	require.Len(t, reportTrust.GetTrustedKeys(), 1)
	assert.Equal(t, []string{scope.GetScopeId()}, reportTrust.GetTrustedKeys()[0].GetAllowedScopeRefs())
	assert.Equal(t, constants.ComplianceReleaseDefaultAssessor, reportTrust.GetTrustedKeys()[0].GetAssessorIdentity())

	evidenceTrustBody, err := os.ReadFile(filepath.Join(workDir, constants.ComplianceReleaseEvidenceTrustFilename))
	require.NoError(t, err)
	evidenceTrust := &compliancev1.ComplianceEvidenceTrustPolicy{}
	require.NoError(t, compliancev1.UnmarshalCanonical(evidenceTrustBody, evidenceTrust))
	var trusted []string
	for _, key := range evidenceTrust.GetTrustedKeys() {
		trusted = append(trusted, key.GetKeyId())
	}
	assert.ElementsMatch(t, []string{receiptSigner, auditor}, trusted, "source trust covers exactly the signers present in the exported evidence")

	assert.Equal(t, constants.ComplianceBundleProfilePublic, fixture.flagValue(t, "generate", "--profile"))
	assert.Equal(t, filepath.Join(workDir, constants.ComplianceReleaseScopeFilename), fixture.flagValue(t, "generate", "--scope"))
	assert.Equal(t, filepath.Join(workDir, constants.ComplianceReleaseExportDirname), fixture.flagValue(t, "generate", "--source"))
	assert.Equal(t, "v2.2.2-gateway-operational-public", fixture.flagValue(t, "generate", "--report-id"))
	assert.Equal(t, fixture.privateKey, fixture.flagValue(t, "generate", "--signing-private-key"))
	assert.Equal(t, "v2.2.2-gateway-operational-public", fixture.flagValue(t, "export", "--report-id"))
	assert.Equal(t, filepath.Join(workDir, constants.ComplianceReleaseBundleDirname), fixture.flagValue(t, "export", "--out"))
	assert.Equal(t, filepath.Join(fixture.projectRoot, "bundle", "bundle.json"), fixture.calls["verify"][0], "verification replays the exported bundle, never the sealed copy")
	assert.Equal(t, filepath.Join(workDir, constants.ComplianceReleaseReportTrustFilename), fixture.flagValue(t, "verify", "--trust-policy"))
	assert.Equal(t, filepath.Join(workDir, constants.ComplianceReleaseEvidenceTrustFilename), fixture.flagValue(t, "verify", "--evidence-trust"))
	assert.Equal(t, filepath.Join(fixture.repoRoot, "docs", "release_notes", "v2.2.x"), fixture.flagValue(t, "project", "--out"))
	assert.Equal(t, filepath.Join(fixture.projectRoot, "bundle", "bundle.json"), fixture.calls["project"][0], "projection consumes the bundle that export printed")

	assert.Contains(t, stdout.String(), "valid=true failures=0")
	assert.Contains(t, stderr.String(), "uncommitted release changes are not part of the recorded revision")
	keyBody, err := os.Stat(fixture.privateKey)
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, keyBody.IsDir()), keyBody.Mode().Perm(), "the generated private key is owner-only")
}

func TestRunReleasePrepare_StopsBeforeProjectionWhenVerificationFails(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	commitAt := time.Date(2026, 9, 28, 2, 58, 52, 0, time.UTC)
	fixture.seedRuntimeDatabase(t, releasePrepareSignerKey(t), releasePrepareSignerKey(t), []time.Time{commitAt.Add(-time.Second)}, []time.Time{commitAt})
	fixture.verifyValid = false

	err := runReleasePrepare(context.Background(), fixture.deps, fixture.opts, &bytes.Buffer{}, &bytes.Buffer{})

	require.ErrorIs(t, err, constants.ErrReportVerificationFailed)
	assert.NotContains(t, fixture.calls, "project", "an unverified bundle must never be projected into the release notes")
}

func TestRunReleasePrepare_RejectsWindowWithoutReceiptsBeforeGenerating(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	commitAt := time.Date(2026, 9, 28, 2, 58, 52, 0, time.UTC)
	fixture.seedRuntimeDatabase(t, releasePrepareSignerKey(t), releasePrepareSignerKey(t), []time.Time{commitAt.Add(-time.Hour)}, []time.Time{commitAt})

	err := runReleasePrepare(context.Background(), fixture.deps, fixture.opts, &bytes.Buffer{}, &bytes.Buffer{})

	require.ErrorIs(t, err, constants.ErrValidationFailed)
	assert.Empty(t, fixture.calls, "no bundle is generated from an empty assessment window")
}

func TestRunReleasePrepare_RefusesRerunBeforeWritingAnything(t *testing.T) {
	commitAt := time.Date(2026, 9, 28, 2, 58, 52, 0, time.UTC)
	workDir := func(fixture *releasePrepareFixture) string {
		return filepath.Join(fixture.projectRoot, constants.ComplianceReleaseWorkDirname, "v2.2.2")
	}
	for name, seed := range map[string]func(*testing.T, *releasePrepareFixture) string{
		"exported bundle": func(t *testing.T, fixture *releasePrepareFixture) string {
			t.Helper()
			bundleDir := filepath.Join(workDir(fixture), constants.ComplianceReleaseBundleDirname)
			require.NoError(t, os.MkdirAll(bundleDir, constants.PermDirPrivate))
			return bundleDir
		},
		"persisted report": func(t *testing.T, fixture *releasePrepareFixture) string {
			t.Helper()
			persisted := filepath.Join(fixture.projectRoot, constants.RuntimeDirname, constants.ComplianceBundlesDirname, "v2.2.2-gateway-operational-public")
			require.NoError(t, os.MkdirAll(persisted, constants.PermDirPrivate))
			return persisted
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newReleasePrepareFixture(t)
			fixture.seedRuntimeDatabase(t, releasePrepareSignerKey(t), releasePrepareSignerKey(t), []time.Time{commitAt.Add(-time.Second)}, []time.Time{commitAt})
			existing := seed(t, fixture)

			err := runReleasePrepare(context.Background(), fixture.deps, fixture.opts, &bytes.Buffer{}, &bytes.Buffer{})

			require.ErrorIs(t, err, constants.ErrValidationFailed)
			assert.Contains(t, err.Error(), existing, "the error names what already exists")
			assert.Empty(t, fixture.calls)
			assert.NoFileExists(t, filepath.Join(workDir(fixture), constants.ComplianceReleaseScopeFilename), "a refused rerun rewrites nothing")
			assert.NoFileExists(t, fixture.privateKey, "a refused rerun creates no signing key")
		})
	}
}

func TestRunReleasePrepare_RejectsLoneSigningKeyPath(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	fixture.opts.signingMetadata = ""

	err := runReleasePrepare(context.Background(), fixture.deps, fixture.opts, &bytes.Buffer{}, &bytes.Buffer{})

	require.ErrorIs(t, err, constants.ErrValidationFailed)
	assert.Zero(t, fixture.binaryCalls, "no runtime facts are derived before the key paths are valid")
	assert.Empty(t, fixture.calls)
}

func TestRunReleasePrepare_RequiresRuntimeDatabaseBeforeGenerating(t *testing.T) {
	fixture := newReleasePrepareFixture(t)

	err := runReleasePrepare(context.Background(), fixture.deps, fixture.opts, &bytes.Buffer{}, &bytes.Buffer{})

	require.Error(t, err, "no runtime database under --project-root means nothing to assess")
	assert.Empty(t, fixture.calls, "no bundle is generated without a runtime database")
}

func TestRunReleasePrepare_FailsClosedWhenRuntimeVaultCannotUnlock(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	commitAt := time.Date(2026, 9, 28, 2, 58, 52, 0, time.UTC)
	fixture.seedRuntimeDatabase(t, releasePrepareSignerKey(t), releasePrepareSignerKey(t), []time.Time{commitAt.Add(-time.Second)}, []time.Time{commitAt})
	fixture.deps.runtimeVault = func(fs.RuntimeFileService) (*vault.Vault, func(), error) {
		return nil, nil, constants.ErrVaultUnlockFailed
	}

	err := runReleasePrepare(context.Background(), fixture.deps, fixture.opts, &bytes.Buffer{}, &bytes.Buffer{})

	require.ErrorIs(t, err, constants.ErrVaultUnlockFailed, "a locked runtime vault stops the release before any export")
	assert.Empty(t, fixture.calls, "no bundle is generated from an unreadable runtime")
}

// releasePrepareFileService builds the real runtime file service the command
// uses, rooted at an isolated runtime root.
func releasePrepareFileService(t *testing.T, projectRoot string) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(projectRoot, testutil.NewTestLogger())
	require.NoError(t, err)
	return fileSvc
}

func TestResolveSigningKeyPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("defaults to the runtime secrets directory", func(t *testing.T) {
		fileSvc := releasePrepareFileService(t, newReleasePrepareFixture(t).projectRoot)
		opts := releasePrepareOptions{newKey: true}
		location, err := resolveSigningKeyPaths(ctx, &opts, fileSvc)
		require.NoError(t, err)
		assert.Equal(t, fileSvc.Resolve(filepath.Join(constants.SecretsDirname, constants.ComplianceReleaseSigningMetadataFilename)), opts.signingMetadata)
		assert.Equal(t, fileSvc.Resolve(filepath.Join(constants.SecretsDirname, constants.ComplianceReleaseSigningPrivateFilename)), opts.signingPrivateKey)
		assert.Equal(t, filepath.Join(constants.SecretsDirname, constants.ComplianceReleaseSigningMetadataFilename), location.metadataRel)
		assert.Equal(t, filepath.Join(constants.SecretsDirname, constants.ComplianceReleaseSigningPrivateFilename), location.privateRel)
	})

	t.Run("a missing key names --new-key", func(t *testing.T) {
		fileSvc := releasePrepareFileService(t, newReleasePrepareFixture(t).projectRoot)
		_, err := resolveSigningKeyPaths(ctx, &releasePrepareOptions{}, fileSvc)
		require.ErrorIs(t, err, constants.ErrValidationFailed)
		assert.Contains(t, err.Error(), "--new-key")
	})

	t.Run("an existing key is reused without --new-key", func(t *testing.T) {
		fileSvc := releasePrepareFileService(t, newReleasePrepareFixture(t).projectRoot)
		for _, name := range []string{constants.ComplianceReleaseSigningMetadataFilename, constants.ComplianceReleaseSigningPrivateFilename} {
			require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join(constants.SecretsDirname, name), []byte("x"), constants.PermFilePrivate))
		}
		_, err := resolveSigningKeyPaths(ctx, &releasePrepareOptions{}, fileSvc)
		require.NoError(t, err)
	})

	t.Run("explicit paths are kept and must be set together", func(t *testing.T) {
		fileSvc := releasePrepareFileService(t, newReleasePrepareFixture(t).projectRoot)
		opts := releasePrepareOptions{newKey: true, signingMetadata: "m.json", signingPrivateKey: "k.hex"}
		location, err := resolveSigningKeyPaths(ctx, &opts, fileSvc)
		require.NoError(t, err)
		assert.Equal(t, "m.json", opts.signingMetadata)
		assert.Nil(t, location.fileSvc, "user-chosen paths stay outside the runtime file service")
		_, err = resolveSigningKeyPaths(ctx, &releasePrepareOptions{signingPrivateKey: "k.hex"}, fileSvc)
		require.ErrorIs(t, err, constants.ErrValidationFailed)
	})
}

func TestWriteNewReleaseSigningKey_DefaultLocationGoesThroughRuntimeFileService(t *testing.T) {
	ctx := context.Background()
	// A runtime root that has no secrets directory yet: the fixture's keystore
	// already created one through the file service's 0755 parent-directory
	// fallback, which would mask the mode this command requests.
	fileSvc := releasePrepareFileService(t, filepath.Join(testutil.TempDir(t), "runtime"))
	opts := releasePrepareOptions{newKey: true}
	location, err := resolveSigningKeyPaths(ctx, &opts, fileSvc)
	require.NoError(t, err)

	require.NoError(t, writeNewReleaseSigningKey(ctx, rand.Reader, opts, location, "v2.2.2", releasePrepareTestNow))

	for _, path := range []string{opts.signingMetadata, opts.signingPrivateKey} {
		rel, err := fileSvc.Rel(path)
		require.NoError(t, err)
		exists, err := fileSvc.FileExists(ctx, rel)
		require.NoError(t, err)
		assert.True(t, exists, "the default key %s is a runtime file", rel)
	}
	secretsInfo, err := fileSvc.Stat(ctx, constants.SecretsDirname)
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermDirPrivate, true), secretsInfo.Mode().Perm(), "the runtime secrets directory is owner-only")
	keyInfo, err := os.Stat(opts.signingPrivateKey)
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, false), keyInfo.Mode().Perm(), "the default private key is owner-only")

	err = writeNewReleaseSigningKey(ctx, rand.Reader, opts, location, "v2.2.2", releasePrepareTestNow)
	require.ErrorIs(t, err, constants.ErrValidationFailed, "the default location refuses to overwrite an existing identity")
	identity, err := loadComplianceReportSigningIdentity(ctx, location, opts.signingMetadata, opts.signingPrivateKey)
	require.NoError(t, err)
	assert.Equal(t, "v2.2.2"+constants.ComplianceReleaseSigningKeyIDSuffix, identity.Metadata().GetKeyId(), "the refused write leaves the identity unchanged")
}

// recordingRuntimeFileService records the runtime-relative paths read through
// it, so a test can prove which reads reached the runtime file service.
type recordingRuntimeFileService struct {
	fs.RuntimeFileService
	reads []string
}

func (r *recordingRuntimeFileService) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	r.reads = append(r.reads, relPath)
	return r.RuntimeFileService.ReadFile(ctx, relPath)
}

func TestLoadSigningIdentity_DefaultLocationReadsThroughRuntimeFileService(t *testing.T) {
	ctx := context.Background()
	fixture := newReleasePrepareFixture(t)
	fileSvc := &recordingRuntimeFileService{RuntimeFileService: releasePrepareFileService(t, fixture.projectRoot)}
	opts := releasePrepareOptions{newKey: true}
	location, err := resolveSigningKeyPaths(ctx, &opts, fileSvc)
	require.NoError(t, err)
	require.NoError(t, writeNewReleaseSigningKey(ctx, rand.Reader, opts, location, "v2.2.2", releasePrepareTestNow))

	identity, err := loadComplianceReportSigningIdentity(ctx, location, opts.signingMetadata, opts.signingPrivateKey)
	require.NoError(t, err)
	assert.Equal(t, "v2.2.2"+constants.ComplianceReleaseSigningKeyIDSuffix, identity.Metadata().GetKeyId())
	assert.ElementsMatch(t, []string{location.metadataRel, location.privateRel}, fileSvc.reads, "the default key is read through the runtime file service, not raw os")
}

func TestWriteNewReleaseSigningKey_CreatesMissingKeyDirectory(t *testing.T) {
	dir := filepath.Join(testutil.TempDir(t), "nested", constants.SecretsDirname)
	opts := releasePrepareOptions{
		signingMetadata:   filepath.Join(dir, constants.ComplianceReleaseSigningMetadataFilename),
		signingPrivateKey: filepath.Join(dir, constants.ComplianceReleaseSigningPrivateFilename),
	}

	require.NoError(t, writeNewReleaseSigningKey(context.Background(), rand.Reader, opts, signingKeyLocation{}, "v2.2.2", releasePrepareTestNow))

	assert.FileExists(t, opts.signingMetadata)
	assert.FileExists(t, opts.signingPrivateKey)
}

func TestResolveReleasePaths_DefaultsFollowTheVersionFile(t *testing.T) {
	fixture := newReleasePrepareFixture(t)

	prepared, err := resolveReleasePaths(fixture.opts)
	require.NoError(t, err)
	assert.Equal(t, "v2.2.2", prepared.version)
	assert.Equal(t, "v2.2.2-gateway-operational-public", prepared.reportID)
	assert.Equal(t, filepath.Join(fixture.projectRoot, constants.ComplianceReleaseWorkDirname, "v2.2.2"), prepared.workDir)
	assert.Equal(t, filepath.Join(fixture.repoRoot, "docs", "release_notes", "v2.2.x"), prepared.releaseDir)

	require.NoError(t, os.WriteFile(filepath.Join(fixture.repoRoot, constants.ComplianceReleaseVersionFilename), []byte("2.2.2\n"), constants.PermFilePrivate))
	_, err = resolveReleasePaths(fixture.opts)
	require.ErrorIs(t, err, constants.ErrValidationFailed, "VERSION must carry the v prefix")
}

func TestResolveReleaseWindow_ExplicitBoundsMustBeSetTogether(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	explicit := fixture.opts
	explicit.windowStart, explicit.windowEnd = "2026-09-28T02:58:19Z", "2026-09-28T02:58:52.999Z"
	prepared := &releasePrepareContext{}

	require.NoError(t, resolveReleaseWindow(context.Background(), "", nil, explicit, prepared))
	assert.Equal(t, time.Date(2026, 9, 28, 2, 58, 19, 0, time.UTC), prepared.windowStart)
	assert.Equal(t, time.Date(2026, 9, 28, 2, 58, 52, 999_000_000, time.UTC), prepared.windowEnd)

	for name, mutate := range map[string]func(*releasePrepareOptions){
		"only start":      func(o *releasePrepareOptions) { o.windowEnd = "" },
		"only end":        func(o *releasePrepareOptions) { o.windowStart = "" },
		"inverted":        func(o *releasePrepareOptions) { o.windowStart, o.windowEnd = o.windowEnd, o.windowStart },
		"malformed start": func(o *releasePrepareOptions) { o.windowStart = "yesterday" },
		"malformed end":   func(o *releasePrepareOptions) { o.windowEnd = "today" },
	} {
		t.Run(name, func(t *testing.T) {
			options := explicit
			mutate(&options)
			err := resolveReleaseWindow(context.Background(), "", nil, options, &releasePrepareContext{})
			require.ErrorIs(t, err, constants.ErrValidationFailed)
		})
	}
}

func TestResolveReleaseIdentity_QueriesRuntimeOnlyForFactsTheOperatorOmitted(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	prepared := &releasePrepareContext{repoRoot: fixture.repoRoot, windowEnd: releasePrepareTestNow}

	explicit := fixture.opts
	explicit.binaryDigest = releasePrepareTestDigest
	explicit.sourceRevision = strings.Repeat("b", 40)
	require.NoError(t, resolveReleaseIdentity(context.Background(), fixture.deps, &explicit, prepared))
	assert.Zero(t, fixture.binaryCalls, "an explicit digest must not hash the running executable")
	assert.Equal(t, strings.Repeat("b", 40), explicit.sourceRevision)
	assert.Equal(t, releasePrepareTestDigest, explicit.buildIdentity)
	assert.Equal(t, constants.ComplianceReleaseDeploymentIDPrefix+"20260928", explicit.deploymentID)

	invalid := fixture.opts
	invalid.binaryDigest = "sha256:not-hex"
	require.ErrorIs(t, resolveReleaseIdentity(context.Background(), fixture.deps, &invalid, prepared), constants.ErrValidationFailed)
}

func TestDefaultBinaryDigest_HashesTheRunningExecutable(t *testing.T) {
	digest, err := defaultBinaryDigest()
	require.NoError(t, err)
	assert.Regexp(t, sha256HexRegexp, digest)
}

func TestWriteNewReleaseSigningKey_RefusesToOverwriteAnExistingIdentity(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	require.NoError(t, writeNewReleaseSigningKey(context.Background(), rand.Reader, fixture.opts, signingKeyLocation{}, "v2.2.2", releasePrepareTestNow))

	identity, err := loadComplianceReportSigningIdentity(context.Background(), signingKeyLocation{}, fixture.metadata, fixture.privateKey)
	require.NoError(t, err)
	assert.Equal(t, "v2.2.2"+constants.ComplianceReleaseSigningKeyIDSuffix, identity.Metadata().GetKeyId())

	err = writeNewReleaseSigningKey(context.Background(), rand.Reader, fixture.opts, signingKeyLocation{}, "v2.2.2", releasePrepareTestNow)
	require.ErrorIs(t, err, constants.ErrValidationFailed, "a release must not silently rotate its report-signing identity")
}

func TestRequireSigningKeyValidAt_RejectsExpiredAndNotYetValidKeys(t *testing.T) {
	fixture := newReleasePrepareFixture(t)
	require.NoError(t, writeNewReleaseSigningKey(context.Background(), rand.Reader, fixture.opts, signingKeyLocation{}, "v2.2.2", releasePrepareTestNow))
	identity, err := loadComplianceReportSigningIdentity(context.Background(), signingKeyLocation{}, fixture.metadata, fixture.privateKey)
	require.NoError(t, err)

	require.NoError(t, requireSigningKeyValidAt(identity, releasePrepareTestNow.Add(time.Hour)))
	require.ErrorIs(t, requireSigningKeyValidAt(identity, releasePrepareTestNow.Add(constants.ComplianceReleaseNewKeyLifetime+time.Hour)), constants.ErrReportSignatureFailed)
	require.ErrorIs(t, requireSigningKeyValidAt(identity, releasePrepareTestNow.Add(-time.Hour)), constants.ErrReportSignatureFailed)
}
