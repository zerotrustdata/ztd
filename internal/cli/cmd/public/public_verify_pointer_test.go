// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package public

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type pointerBundleFixture struct {
	files     fs.RuntimeFileService
	key       ed25519.PrivateKey
	keyID     string
	pointer   models.PublicFeedPointer
	readModel models.PublicFeedBootstrap
	bundle    []byte
}

func newPointerBundleFixture(t *testing.T, files fs.RuntimeFileService, assignments ...*evalv1.PublicAssignmentResultProjection) *pointerBundleFixture {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	f := &pointerBundleFixture{files: files, key: key, keyID: models.PublicProofSigningKeyID(pub)}
	if len(assignments) == 0 {
		assignments = []*evalv1.PublicAssignmentResultProjection{{RunId: assignmentTestRunID, AssignmentId: assignmentTestAssignID, EvidenceBindings: []*evalv1.PublicEvidenceBinding{{Sha256: models.SHA256Hex([]byte("private trace")), Kind: "assignment_agent_trace", SchemaRef: "trace/v1"}}}}
	}
	var lines bytes.Buffer
	for i, assignment := range assignments {
		record, err := evalv1.MarshalCanonical(assignment)
		require.NoError(t, err)
		envelope, err := json.Marshal(evaluation.CampaignProjectionEnvelope{SchemaVersion: "1.0.0", MessageType: "PublicAssignmentResultProjection", IdempotencyKey: assignment.GetAssignmentId(), Record: record})
		require.NoError(t, err)
		line, err := json.Marshal(models.PublicRunBundleLine{Sequence: int64(i + 1), RecordHash: models.SHA256Hex(envelope), RecordBytes: string(envelope)})
		require.NoError(t, err)
		lines.Write(line)
		lines.WriteByte('\n')
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err = writer.Write(lines.Bytes())
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	f.bundle = compressed.Bytes()
	f.readModel = models.PublicFeedBootstrap{SchemaVersion: constants.PublicReadModelSchemaVersion, ProtocolVersion: constants.PublicFeedProtocolVersion,
		Snapshot:          models.PublicFeedSnapshot{SourceID: assignmentTestSourceID, HighWaterSequence: 10, FeedChainHash: constants.PublicFeedZeroHashHex},
		RecentProjections: []models.PublicFeedObject{models.NewPublicFeedObject(map[string]string{"kind": "evaluation_summary", "run_id": assignmentTestRunID, "assignment_bundle_sha256": models.SHA256Hex(f.bundle)})},
	}
	f.pointer = models.PublicFeedPointer{SchemaVersion: constants.PublicFeedPointerSchemaVersion, SourceID: assignmentTestSourceID, Revision: 1, HighWaterSequence: 10, FeedChainHash: constants.PublicFeedZeroHashHex, GeneratedAt: time.Now().UTC()}
	oldPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	registry, err := json.Marshal(map[string]string{models.PublicProofSigningKeyID(oldPub): hex.EncodeToString(oldPub), f.keyID: hex.EncodeToString(pub)})
	require.NoError(t, err)
	f.write(t, "keys.json", registry)
	f.save(t)
	return f
}
func (f *pointerBundleFixture) write(t *testing.T, name string, body []byte) {
	t.Helper()
	require.NoError(t, f.files.WriteFile(t.Context(), name, body, constants.PermFilePrivate))
}
func (f *pointerBundleFixture) save(t *testing.T) {
	t.Helper()
	model, err := json.Marshal(f.readModel)
	require.NoError(t, err)
	f.write(t, "read-model.json", model)
	f.pointer.ReadModelSHA256 = models.SHA256Hex(model)
	f.pointer, err = models.SignPublicFeedPointer(f.pointer, f.key, f.keyID)
	require.NoError(t, err)
	pointer, err := json.Marshal(f.pointer)
	require.NoError(t, err)
	f.write(t, "pointer.json", pointer)
	f.write(t, "bundle.jsonl.gz", f.bundle)
}
func (f *pointerBundleFixture) args() []string {
	return []string{"--pointer", f.files.Resolve("pointer.json"), "--read-model", f.files.Resolve("read-model.json"), "--bundle", f.files.Resolve("bundle.jsonl.gz"), "--run", assignmentTestRunID, "--key-registry", f.files.Resolve("keys.json")}
}
func runPointerProof(t *testing.T, f *pointerBundleFixture, extra ...string) (string, error) {
	t.Helper()
	cmd := publicVerifyProofCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(f.args(), extra...))
	err := cmd.Execute()
	return out.String(), err
}
func TestVerifyProofCmd_PointerAuthenticatesRunBundle(t *testing.T) {
	f := newPointerBundleFixture(t, assignmentTestFiles(t))
	out, err := runPointerProof(t, f)
	require.NoError(t, err)
	assert.Contains(t, out, "run run-1, 1 assignments, 0 delivered files")
	require.NoError(t, f.files.MkdirAll(t.Context(), "delivered", constants.PermDirPrivate))
	f.write(t, "delivered/"+models.SHA256Hex([]byte("private trace")), []byte("private trace"))
	out, err = runPointerProof(t, f, "--artifacts", f.files.Resolve("delivered"))
	require.NoError(t, err)
	assert.Contains(t, out, "1 delivered files")
	f.write(t, "delivered/"+models.SHA256Hex([]byte("private trace")), []byte("tampered"))
	_, err = runPointerProof(t, f, "--artifacts", f.files.Resolve("delivered"))
	require.ErrorIs(t, err, constants.ErrPublicFeedProofHashMismatch)
}
func TestVerifyProofCmd_RejectsBrokenPointerTrustChain(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *pointerBundleFixture)
		want   error
	}{
		{"untrusted self published key", func(t *testing.T, f *pointerBundleFixture) { t.Helper(); f.write(t, "keys.json", []byte(`{}`)) }, constants.ErrPublicFeedSignatureInvalid},
		{"unsigned pointer", func(t *testing.T, f *pointerBundleFixture) {
			t.Helper()
			f.pointer.Signature = ""
			body, err := json.Marshal(f.pointer)
			require.NoError(t, err)
			f.write(t, "pointer.json", body)
		}, constants.ErrPublicFeedSignatureInvalid},
		{"unknown pointer field", func(t *testing.T, f *pointerBundleFixture) {
			t.Helper()
			f.write(t, "pointer.json", []byte(`{"bogus":true}`))
		}, constants.ErrPublicFeedPointerInvalid},
		{"read model tampering", func(t *testing.T, f *pointerBundleFixture) { t.Helper(); f.write(t, "read-model.json", []byte(`{}`)) }, constants.ErrPublicFeedProofHashMismatch},
		{"bundle tampering", func(t *testing.T, f *pointerBundleFixture) { t.Helper(); f.write(t, "bundle.jsonl.gz", []byte(`{}`)) }, constants.ErrPublicFeedProofHashMismatch},
		{"read model source mismatch", func(t *testing.T, f *pointerBundleFixture) {
			t.Helper()
			f.readModel.Snapshot.SourceID = "other"
			f.save(t)
		}, constants.ErrPublicReadModelInvalid},
		{"run absent or withdrawn", func(t *testing.T, f *pointerBundleFixture) {
			t.Helper()
			f.readModel.RecentProjections = nil
			f.save(t)
		}, constants.ErrPublicRunBundleInvalid},
		{"summary has no settled bundle", func(t *testing.T, f *pointerBundleFixture) {
			t.Helper()
			delete(f.readModel.RecentProjections[0], "assignment_bundle_sha256")
			f.save(t)
		}, constants.ErrPublicRunBundleInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPointerBundleFixture(t, assignmentTestFiles(t))
			tt.change(t, f)
			_, err := runPointerProof(t, f)
			require.ErrorIs(t, err, tt.want)
		})
	}
}
func TestVerifyProofCmd_RejectsBundleIdentityAndRecordTampering(t *testing.T) {
	for _, row := range []struct {
		name       string
		assignment *evalv1.PublicAssignmentResultProjection
		duplicate  bool
	}{
		{name: "wrong run", assignment: &evalv1.PublicAssignmentResultProjection{RunId: "other", AssignmentId: assignmentTestAssignID}},
		{name: "duplicate assignment", assignment: &evalv1.PublicAssignmentResultProjection{RunId: assignmentTestRunID, AssignmentId: assignmentTestAssignID}, duplicate: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			assignments := []*evalv1.PublicAssignmentResultProjection{row.assignment}
			if row.duplicate {
				assignments = append(assignments, row.assignment)
			}
			f := newPointerBundleFixture(t, assignmentTestFiles(t), assignments...)
			_, err := runPointerProof(t, f)
			require.ErrorIs(t, err, constants.ErrPublicRunBundleInvalid)
		})
	}
	_, err := verifyBundleRecords([]byte(`{"sequence":1,"record_hash":"bad","record_bytes":"{}"}`), assignmentTestRunID, 10)
	require.ErrorIs(t, err, constants.ErrPublicFeedRecordHashMismatch)
}

func TestVerifyProofCmd_EnrichedAssignmentBundle(t *testing.T) {
	f := newPointerBundleFixture(t, assignmentTestFiles(t))
	// Production envelopes use schema 1.2.0, canonical snake_case protobuf
	// fields, and named extensions that are not fields of the protobuf itself.
	record, err := evaluation.MarshalAssignmentResultProjectionEnvelope("result", &evaluation.PublicAssignmentRecord{Projection: &evalv1.PublicAssignmentResultProjection{RunId: assignmentTestRunID, AssignmentId: assignmentTestAssignID, ScenarioId: "scenario-1"}, Extensions: evaluation.PublicAssignmentRecordExtensions{ResourceSummary: &evaluation.PublicResourceSummary{}}})
	require.NoError(t, err)
	line, err := json.Marshal(models.PublicRunBundleLine{Sequence: 1, RecordHash: models.SHA256Hex(record), RecordBytes: string(record)})
	require.NoError(t, err)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err = writer.Write(append(line, '\n'))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	f.bundle = compressed.Bytes()
	f.readModel.RecentProjections[0]["assignment_bundle_sha256"] = json.RawMessage(`"` + models.SHA256Hex(f.bundle) + `"`)
	f.save(t)
	out, err := runPointerProof(t, f)
	require.NoError(t, err)
	assert.Contains(t, out, "1 assignments")
}
