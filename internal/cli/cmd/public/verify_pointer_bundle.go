// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package public

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type verifiedPointerBundle struct {
	Pointer     models.PublicFeedPointer
	Assignments []verifiedBundleAssignment
}

// verifiedBundleAssignment decodes the identity and evidence binding subset of
// authenticated assignment records, including enriched envelopes with named
// extensions. Other presentation fields do not affect offline file verification.
type verifiedBundleAssignment struct {
	RunID            string                          `json:"run_id"`
	AssignmentID     string                          `json:"assignment_id"`
	EvidenceBindings []*evalv1.PublicEvidenceBinding `json:"evidence_bindings"`
}

// Only summary fields used for the bundle binding are decoded here. The entire
// read model has already been authenticated against the pointer before decoding.
type pointerRunSummary struct {
	Kind         string `json:"kind"`
	RunID        string `json:"run_id"`
	BundleSHA256 string `json:"assignment_bundle_sha256"`
}

// decodePublicFile accepts transport-decoded JSON or a downloaded gzip object.
// Its bound applies after decompression, including JSONL records.
func decodePublicFile(body []byte) ([]byte, error) {
	if len(body) > constants.PublicFeedProofIngestMaxBytes {
		return nil, constants.ErrPublicRunBundleInvalid
	}
	if !bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
		return body, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("public-feed: open gzip: %w", err)
	}
	defer func() { _ = reader.Close() }()
	decoded, err := io.ReadAll(io.LimitReader(reader, constants.PublicFeedProofIngestMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("public-feed: decode gzip: %w", err)
	}
	if len(decoded) > constants.PublicFeedProofIngestMaxBytes {
		return nil, constants.ErrPublicRunBundleInvalid
	}
	return decoded, nil
}

func verifyPointerRunBundle(pointerPath, readModelPath, bundlePath, runID, publicKeyHex, keyRegistryPath string) (verifiedPointerBundle, error) {
	if pointerPath == "" || readModelPath == "" || bundlePath == "" || runID == "" {
		return verifiedPointerBundle{}, constants.ErrMissingRequiredField
	}
	registry, err := loadPublicProofKeyRegistry(publicKeyHex, keyRegistryPath)
	if err != nil {
		return verifiedPointerBundle{}, err
	}
	pointer, err := readPublicFeedPointer(pointerPath)
	if err != nil {
		return verifiedPointerBundle{}, err
	}
	// A self-published key cannot establish identity. Require this pointer's key
	// in the trusted registry, including a source-qualified pin or a single-key pin.
	key, found := registry[pointer.SigningKeyID]
	if !found {
		key, found = registry[pointer.SourceID+":"+pointer.SigningKeyID]
	}
	if !found {
		key, found = registry["*"]
	}
	if !found {
		return verifiedPointerBundle{}, fmt.Errorf("%w: pointer key is not trusted; supply --key-registry or --public-key", constants.ErrPublicFeedSignatureInvalid)
	}
	if err := models.VerifyPublicFeedPointer(pointer, key); err != nil {
		return verifiedPointerBundle{}, fmt.Errorf("public-feed: verify pointer: %w", err)
	}
	body, err := os.ReadFile(readModelPath)
	if err != nil {
		return verifiedPointerBundle{}, fmt.Errorf("public-feed: read read model: %w", err)
	}
	body, err = decodePublicFile(body)
	if err != nil {
		return verifiedPointerBundle{}, err
	}
	if models.SHA256Hex(body) != pointer.ReadModelSHA256 {
		return verifiedPointerBundle{}, fmt.Errorf("%w: read model digest", constants.ErrPublicFeedProofHashMismatch)
	}
	var readModel models.PublicFeedBootstrap
	if err := json.Unmarshal(body, &readModel); err != nil {
		return verifiedPointerBundle{}, fmt.Errorf("%w: %w", constants.ErrPublicReadModelInvalid, err)
	}
	if readModel.SchemaVersion != constants.PublicReadModelSchemaVersion || readModel.Snapshot.SourceID != pointer.SourceID || readModel.Snapshot.HighWaterSequence != pointer.HighWaterSequence || readModel.Snapshot.FeedChainHash != pointer.FeedChainHash || readModel.Snapshot.WithdrawalDigest != pointer.WithdrawalDigest {
		return verifiedPointerBundle{}, fmt.Errorf("%w: pointer and read model identity differ", constants.ErrPublicReadModelInvalid)
	}
	digest := ""
	matched := false
	for _, projection := range readModel.RecentProjections {
		encoded, err := json.Marshal(projection)
		if err != nil {
			return verifiedPointerBundle{}, fmt.Errorf("public-feed: encode summary: %w", err)
		}
		var summary pointerRunSummary
		if err := json.Unmarshal(encoded, &summary); err != nil {
			return verifiedPointerBundle{}, fmt.Errorf("%w: %w", constants.ErrPublicReadModelInvalid, err)
		}
		if summary.Kind == "evaluation_summary" && summary.RunID == runID {
			if matched {
				return verifiedPointerBundle{}, fmt.Errorf("%w: duplicate run summary", constants.ErrPublicReadModelInvalid)
			}
			matched = true
			digest = summary.BundleSHA256
		}
	}
	if !publicEvidenceDigest(digest) {
		return verifiedPointerBundle{}, fmt.Errorf("%w: run has no settled bundle", constants.ErrPublicRunBundleInvalid)
	}
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		return verifiedPointerBundle{}, fmt.Errorf("public-feed: read run bundle: %w", err)
	}
	// Bundle digests cover the gzip file, not its decoded bytes.
	if models.SHA256Hex(bundle) != digest {
		return verifiedPointerBundle{}, fmt.Errorf("%w: run bundle digest", constants.ErrPublicFeedProofHashMismatch)
	}
	if !bytes.HasPrefix(bundle, []byte{0x1f, 0x8b}) {
		return verifiedPointerBundle{}, constants.ErrPublicRunBundleInvalid
	}
	decoded, err := decodePublicFile(bundle)
	if err != nil {
		return verifiedPointerBundle{}, err
	}
	assignments, err := verifyBundleRecords(decoded, runID, pointer.HighWaterSequence)
	if err != nil {
		return verifiedPointerBundle{}, err
	}
	return verifiedPointerBundle{Pointer: pointer, Assignments: assignments}, nil
}

func publicEvidenceDigest(digest string) bool {
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == digest
}

// verifyBundleRecords authenticates each exact record_bytes and rejects records
// from another run, duplicated assignments, and non-monotonic sequences.
func verifyBundleRecords(body []byte, runID string, high int64) ([]verifiedBundleAssignment, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), constants.PublicFeedProofIngestMaxBytes)
	var assignments []verifiedBundleAssignment
	seen := make(map[string]bool)
	var previous int64
	for scanner.Scan() {
		var line models.PublicRunBundleLine
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&line); err != nil {
			return nil, fmt.Errorf("%w: line: %w", constants.ErrPublicRunBundleInvalid, err)
		}
		if decoder.Decode(&json.RawMessage{}) != io.EOF {
			return nil, constants.ErrPublicRunBundleInvalid
		}
		if line.Sequence <= previous || line.Sequence > high {
			return nil, fmt.Errorf("%w: sequence out of range", constants.ErrPublicRunBundleInvalid)
		}
		previous = line.Sequence
		if models.SHA256Hex([]byte(line.RecordBytes)) != line.RecordHash {
			return nil, constants.ErrPublicFeedRecordHashMismatch
		}
		var envelope evaluation.CampaignProjectionEnvelope
		if err := json.Unmarshal([]byte(line.RecordBytes), &envelope); err != nil {
			return nil, fmt.Errorf("%w: envelope: %w", constants.ErrPublicRunBundleInvalid, err)
		}
		if (envelope.SchemaVersion != "1.0.0" && envelope.SchemaVersion != "1.1.0" && envelope.SchemaVersion != "1.2.0") || envelope.MessageType != "PublicAssignmentResultProjection" {
			return nil, constants.ErrPublicRunBundleInvalid
		}
		var assignment verifiedBundleAssignment
		if err := json.Unmarshal(envelope.Record, &assignment); err != nil {
			return nil, fmt.Errorf("%w: assignment: %w", constants.ErrPublicRunBundleInvalid, err)
		}
		if assignment.RunID != runID || assignment.AssignmentID == "" || seen[assignment.AssignmentID] {
			return nil, fmt.Errorf("%w: assignment identity", constants.ErrPublicRunBundleInvalid)
		}
		seen[assignment.AssignmentID] = true
		for _, binding := range assignment.EvidenceBindings {
			if !publicEvidenceDigest(binding.GetSha256()) || binding.GetKind() == "" || binding.GetSchemaRef() == "" {
				return nil, fmt.Errorf("%w: evidence binding", constants.ErrPublicRunBundleInvalid)
			}
		}
		assignments = append(assignments, assignment)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan: %w", constants.ErrPublicRunBundleInvalid, err)
	}
	if len(assignments) == 0 {
		return nil, constants.ErrPublicRunBundleEmpty
	}
	return assignments, nil
}

// verifyDeliveredEvidence verifies only files actually delivered. Each must be
// a regular digest-named file bound by an authenticated assignment result.
func verifyDeliveredEvidence(directory string, assignments []verifiedBundleAssignment) (int, error) {
	if directory == "" {
		return 0, nil
	}
	bound := make(map[string]bool)
	for _, assignment := range assignments {
		for _, binding := range assignment.EvidenceBindings {
			bound[binding.GetSha256()] = true
		}
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("public-feed: list delivered evidence: %w", err)
	}
	for _, file := range files {
		if !bound[file.Name()] {
			return 0, fmt.Errorf("%w: unbound delivered file %s", constants.ErrPublicFeedProofCatalogMismatch, file.Name())
		}
		info, err := file.Info()
		if err != nil {
			return 0, fmt.Errorf("public-feed: inspect delivered file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return 0, constants.ErrPublicFeedProofSymlinkRejected
		}
		body, err := os.ReadFile(filepath.Join(directory, file.Name()))
		if err != nil {
			return 0, fmt.Errorf("public-feed: read delivered file: %w", err)
		}
		if models.SHA256Hex(body) != file.Name() {
			return 0, fmt.Errorf("%w: delivered file %s", constants.ErrPublicFeedProofHashMismatch, file.Name())
		}
	}
	return len(files), nil
}
