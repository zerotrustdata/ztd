// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package public

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/assignmentaudit"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	assignmentTestSourceID  = "src-1"
	assignmentTestDBName    = "assignment-audit.db"
	assignmentTestProofLog  = "proof-segments.jsonl"
	assignmentTestRunID     = "run-1"
	assignmentTestAssignID  = "assignment-1"
	assignmentTestEventRows = 2
)

var assignmentTestSegmentTime = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func assignmentTestFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	return files
}

// assignmentTestEventBody returns a live event body that passes the public feed
// schema and belongs to the test run and assignment, encoded as the Gateway encodes it.
func assignmentTestEventBody(t *testing.T, eventID string) []byte {
	t.Helper()
	body, err := evaluation.MarshalPublicLiveEvent(&evalv1.PublicLiveEvent{
		SchemaVersion:   "1.5.0",
		Kind:            "stage_updated",
		DatasetId:       "dataset-1",
		QualityState:    "live_in_progress",
		ObservedAt:      "2026-10-01T00:00:00Z",
		EventId:         eventID,
		RunId:           assignmentTestRunID,
		AssignmentId:    assignmentTestAssignID,
		VariantId:       "variant-a",
		Role:            "primary",
		LifecycleStatus: "running",
		Completed:       proto.Uint32(1),
		Total:           proto.Uint32(2),
		StageLabel:      "model role invoked",
	})
	require.NoError(t, err)
	return body
}

// buildAssignmentTestDatabase returns an assignment audit database of two live
// events that belong to the test run and assignment.
func buildAssignmentTestDatabase(t *testing.T, files fs.RuntimeFileService) []byte {
	t.Helper()
	records := make([]models.PublicFeedRecord, 0, assignmentTestEventRows)
	for sequence := int64(1); sequence <= assignmentTestEventRows; sequence++ {
		body := assignmentTestEventBody(t, fmt.Sprintf("event-%d", sequence))
		records = append(records, assignmentTestRecord(sequence, body))
	}
	header := assignmentaudit.Header{
		SourceID:         assignmentTestSourceID,
		CampaignID:       "campaign-1",
		CampaignRevision: "revision-1",
		RunID:            assignmentTestRunID,
		AssignmentID:     assignmentTestAssignID,
	}
	data, err := assignmentaudit.BuildDatabase(context.Background(), files, header, records)
	require.NoError(t, err)
	return data
}

func assignmentTestRecord(sequence int64, body []byte) models.PublicFeedRecord {
	sum := sha256.Sum256(body)
	return models.PublicFeedRecord{
		Sequence:    sequence,
		RecordType:  models.PublicFeedRecordTypeEvent,
		RecordHash:  hex.EncodeToString(sum[:]),
		RecordBytes: string(body),
	}
}

// writeAssignmentTestDatabase stores database bytes under the runtime root and
// returns their absolute path.
func writeAssignmentTestDatabase(t *testing.T, files fs.RuntimeFileService, data []byte) string {
	t.Helper()
	require.NoError(t, files.WriteFile(context.Background(), assignmentTestDBName, data, constants.PermFilePrivate))
	return files.Resolve(assignmentTestDBName)
}

// assignmentTestEntry lists content as an artifact named filename. Its artifact
// ID is the content's SHA-256, as the publisher assigns it.
func assignmentTestEntry(filename string, content []byte) models.PublicProofCatalogEntry {
	digest := models.SHA256Hex(content)
	return models.PublicProofCatalogEntry{
		ArtifactID:          digest,
		Filename:            filename,
		MediaType:           "application/vnd.sqlite3",
		ByteSize:            int64(len(content)),
		SHA256:              digest,
		Classification:      models.PublicFeedProofClassificationPublicSafe,
		CampaignID:          "campaign-1",
		GeneratedAt:         assignmentTestSegmentTime,
		VerificationCommand: "ztd public verify-assignment --db " + filename,
		ImmutableURL:        "/proofs/" + digest,
	}
}

// assignmentTestSegment returns an unsealed segment of the test source.
func assignmentTestSegment(index int64, previousRoot, signingKeyID string, entries []models.PublicProofCatalogEntry, withdrawn []string) models.PublicProofManifestSegment {
	return models.PublicProofManifestSegment{
		SchemaVersion:        constants.PublicProofSegmentSchemaVersion,
		SourceID:             assignmentTestSourceID,
		SegmentIndex:         index,
		PreviousRoot:         previousRoot,
		CampaignID:           "campaign-1",
		CampaignRevision:     "revision-1",
		VerificationOK:       true,
		Entries:              entries,
		WithdrawnArtifactIDs: withdrawn,
		GeneratedAt:          assignmentTestSegmentTime,
		SigningKeyID:         signingKeyID,
	}
}

// signAssignmentTestSegment fills in the root over the segment's signed fields
// and signs that root with priv.
func signAssignmentTestSegment(t *testing.T, priv ed25519.PrivateKey, seg models.PublicProofManifestSegment) models.PublicProofManifestSegment {
	t.Helper()
	seg.ProofRootSHA256 = models.ComputePublicProofSegmentRoot(seg)
	root, err := hex.DecodeString(seg.ProofRootSHA256)
	require.NoError(t, err)
	seg.Signature = hex.EncodeToString(ed25519.Sign(priv, root))
	return seg
}

// writeAssignmentTestSegments writes segments as a JSON Lines proof log and
// returns its absolute path.
func writeAssignmentTestSegments(t *testing.T, files fs.RuntimeFileService, segments ...models.PublicProofManifestSegment) string {
	t.Helper()
	var buf bytes.Buffer
	for _, seg := range segments {
		line, err := json.Marshal(seg)
		require.NoError(t, err)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	require.NoError(t, files.WriteFile(context.Background(), assignmentTestProofLog, buf.Bytes(), constants.PermFilePrivate))
	return files.Resolve(assignmentTestProofLog)
}

// runVerifyAssignment executes verify-assignment with args and returns stdout.
func runVerifyAssignment(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := publicVerifyAssignmentCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestVerifyAssignmentCmd_VerifiesDatabaseInActiveSet(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)
	files := assignmentTestFiles(t)
	data := buildAssignmentTestDatabase(t, files)
	dbPath := writeAssignmentTestDatabase(t, files, data)
	segmentsPath := writeAssignmentTestSegments(t, files, signAssignmentTestSegment(t, priv,
		assignmentTestSegment(1, constants.PublicFeedZeroHashHex, pubHex,
			[]models.PublicProofCatalogEntry{assignmentTestEntry(assignmentTestDBName, data)}, nil)))

	out, err := runVerifyAssignment(t, "--db", dbPath, "--segments", segmentsPath, "--public-key", pubHex)
	require.NoError(t, err)
	assert.Equal(t, "Assignment audit verified (run run-1, assignment assignment-1, 2 events, sha256 "+models.SHA256Hex(data)+")\n", out)
}

func TestVerifyAssignmentCmd_RejectsDatabaseNotInProof(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)
	files := assignmentTestFiles(t)
	data := buildAssignmentTestDatabase(t, files)
	dbPath := writeAssignmentTestDatabase(t, files, data)
	otherData := []byte("audit bytes of another assignment")
	segmentsPath := writeAssignmentTestSegments(t, files, signAssignmentTestSegment(t, priv,
		assignmentTestSegment(1, constants.PublicFeedZeroHashHex, pubHex,
			[]models.PublicProofCatalogEntry{assignmentTestEntry("other-assignment.db", otherData)}, nil)))

	_, err = runVerifyAssignment(t, "--db", dbPath, "--segments", segmentsPath, "--public-key", pubHex)
	require.ErrorIs(t, err, constants.ErrPublicAssignmentAuditNotInProof)
}

func TestVerifyAssignmentCmd_RejectsWithdrawnDatabase(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)
	files := assignmentTestFiles(t)
	data := buildAssignmentTestDatabase(t, files)
	dbPath := writeAssignmentTestDatabase(t, files, data)
	first := signAssignmentTestSegment(t, priv, assignmentTestSegment(1, constants.PublicFeedZeroHashHex, pubHex,
		[]models.PublicProofCatalogEntry{assignmentTestEntry(assignmentTestDBName, data)}, nil))
	withdrawal := signAssignmentTestSegment(t, priv, assignmentTestSegment(2, first.ProofRootSHA256, pubHex,
		nil, []string{models.SHA256Hex(data)}))
	segmentsPath := writeAssignmentTestSegments(t, files, first, withdrawal)

	_, err = runVerifyAssignment(t, "--db", dbPath, "--segments", segmentsPath, "--public-key", pubHex)
	require.ErrorIs(t, err, constants.ErrPublicAssignmentAuditNotInProof)
}

func TestVerifyAssignmentCmd_RejectsWrongPublicKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)
	files := assignmentTestFiles(t)
	data := buildAssignmentTestDatabase(t, files)
	dbPath := writeAssignmentTestDatabase(t, files, data)
	segmentsPath := writeAssignmentTestSegments(t, files, signAssignmentTestSegment(t, priv,
		assignmentTestSegment(1, constants.PublicFeedZeroHashHex, pubHex,
			[]models.PublicProofCatalogEntry{assignmentTestEntry(assignmentTestDBName, data)}, nil)))

	_, err = runVerifyAssignment(t, "--db", dbPath, "--segments", segmentsPath, "--public-key", hex.EncodeToString(wrongPub))
	require.ErrorIs(t, err, constants.ErrPublicFeedSignatureInvalid)
}

func TestVerifyAssignmentCmd_RejectsTamperedDatabase(t *testing.T) {
	t.Run("bytes changed after the proof was sealed", func(t *testing.T) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		pubHex := hex.EncodeToString(pub)
		files := assignmentTestFiles(t)
		data := buildAssignmentTestDatabase(t, files)
		segmentsPath := writeAssignmentTestSegments(t, files, signAssignmentTestSegment(t, priv,
			assignmentTestSegment(1, constants.PublicFeedZeroHashHex, pubHex,
				[]models.PublicProofCatalogEntry{assignmentTestEntry(assignmentTestDBName, data)}, nil)))

		tampered := bytes.Clone(data)
		tampered[len(tampered)-1] ^= 0xff
		dbPath := writeAssignmentTestDatabase(t, files, tampered)

		_, err = runVerifyAssignment(t, "--db", dbPath, "--segments", segmentsPath, "--public-key", pubHex)
		require.ErrorIs(t, err, constants.ErrPublicAssignmentAuditNotInProof)
	})

	t.Run("event rewritten and sealed into the proof", func(t *testing.T) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		pubHex := hex.EncodeToString(pub)
		files := assignmentTestFiles(t)
		dbPath := writeAssignmentTestDatabase(t, files, buildAssignmentTestDatabase(t, files))

		// Rewrite one event body and keep its original hash, so only the record
		// hash check can reject it once the digest is in the proof.
		forged := assignmentTestEventBody(t, "event-forged")
		db, err := sql.Open("sqlite", "file:"+dbPath)
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE events SET record_bytes = ? WHERE sequence = 1`, string(forged))
		require.NoError(t, err)
		require.NoError(t, db.Close())

		tampered, err := files.ReadFile(context.Background(), assignmentTestDBName)
		require.NoError(t, err)
		segmentsPath := writeAssignmentTestSegments(t, files, signAssignmentTestSegment(t, priv,
			assignmentTestSegment(1, constants.PublicFeedZeroHashHex, pubHex,
				[]models.PublicProofCatalogEntry{assignmentTestEntry(assignmentTestDBName, tampered)}, nil)))

		_, err = runVerifyAssignment(t, "--db", dbPath, "--segments", segmentsPath, "--public-key", pubHex)
		require.ErrorIs(t, err, constants.ErrPublicFeedRecordHashMismatch)
	})
}

func TestVerifyAssignmentCmd_RequiresOneKeySource(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)
	files := assignmentTestFiles(t)
	data := buildAssignmentTestDatabase(t, files)
	dbPath := writeAssignmentTestDatabase(t, files, data)
	segmentsPath := writeAssignmentTestSegments(t, files, signAssignmentTestSegment(t, priv,
		assignmentTestSegment(1, constants.PublicFeedZeroHashHex, pubHex,
			[]models.PublicProofCatalogEntry{assignmentTestEntry(assignmentTestDBName, data)}, nil)))

	_, err = runVerifyAssignment(t, "--db", dbPath, "--segments", segmentsPath)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}
