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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/eval"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/assignmentaudit"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

type publicConfigLoader func(string) (*config.Config, error)
type publicFileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)

// openPublicKeystore opens the keystore that seals the public-feed signing key
// and ingest token. Tests replace it with an in-memory keyring.
var openPublicKeystore = func(fileSvc fs.RuntimeFileService) (*keystore.Keystore, error) {
	return keystore.Open(fileSvc, slog.Default(), keystore.Options{})
}

// publicKeystore opens the runtime keystore for a public-feed command.
func publicKeystore(fileSvc fs.RuntimeFileService) (*keystore.Keystore, error) {
	ks, err := openPublicKeystore(fileSvc)
	if err != nil {
		return nil, fmt.Errorf("public-feed: open keystore: %w", err)
	}
	return ks, nil
}

func publicInitCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	var sourceID string
	var mirrorOrigin string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the local public-feed publisher",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(sourceID) == "" {
				return constants.ErrPublicFeedSourceIDRequired
			}
			if err := gateway.ValidatePublicMirrorOrigin(mirrorOrigin); err != nil {
				return err
			}
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := shared.CommandContext(cmd)
			ks, err := publicKeystore(fileSvc)
			if err != nil {
				return err
			}
			_, err = gateway.InitializeLocalPublicFeed(ctx, fileSvc, ks, sourceID, mirrorOrigin)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Public feed initialized for source %s\n", sourceID)
			return err
		},
	}
	cmd.Flags().StringVar(&sourceID, "source-id", "", "Public source deployment pseudonym")
	cmd.Flags().StringVar(&mirrorOrigin, "mirror-origin", "", "Hosted public mirror origin")
	return cmd
}

func publicConfigSetCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	var mirrorOrigin string
	var enabled bool
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update public-feed publisher configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("mirror-origin") && !cmd.Flags().Changed("enabled") {
				return fmt.Errorf("%w: no configuration field was specified", constants.ErrValidationFailed)
			}
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := shared.CommandContext(cmd)
			exportConfig, err := gateway.ReadPublicExportConfig(ctx, fileSvc)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("mirror-origin") {
				exportConfig.MirrorOrigin = mirrorOrigin
			}
			if cmd.Flags().Changed("enabled") {
				exportConfig.Enabled = enabled
			}
			if err := gateway.ValidatePublicExportConfig(exportConfig); err != nil {
				return err
			}
			if err := gateway.WritePublicExportConfig(ctx, fileSvc, exportConfig); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Public feed configuration updated")
			return err
		},
	}
	cmd.Flags().StringVar(&mirrorOrigin, "mirror-origin", "", "Hosted public mirror origin")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable or disable public publication")
	return cmd
}

func publicSourceTransitionCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	var sourceID string
	var confirmed bool
	cmd := &cobra.Command{
		Use:   "transition",
		Short: "Archive the current publisher state and start a fresh public source",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !confirmed {
				return constants.ErrPublicFeedSourceTransitionConfirmation
			}
			if strings.TrimSpace(sourceID) == "" {
				return constants.ErrPublicFeedSourceIDRequired
			}
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ks, err := publicKeystore(fileSvc)
			if err != nil {
				return err
			}
			newConfig, err := gateway.TransitionLocalPublicFeed(shared.CommandContext(cmd), fileSvc, ks, sourceID)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Public feed transitioned to source %s; prior publisher state archived under %s\n", newConfig.SourceID, constants.PublicFeedArchiveDirname)
			return err
		},
	}
	cmd.Flags().StringVar(&sourceID, "source-id", "", "New public source deployment pseudonym")
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm archival of the current publisher state and creation of a fresh source")
	return cmd
}

func Cmd() *cobra.Command {
	cmd := &cobra.Command{Use: "public", Short: "Manage the public spectator feed"}
	configCmd := &cobra.Command{Use: "config", Short: "Manage public-feed configuration"}
	configCmd.AddCommand(publicConfigSetCmdWithConfig(shared.LoadConfig, shared.NewFileSvc))
	sourceCmd := &cobra.Command{Use: "source", Short: "Manage the active public-feed source"}
	sourceCmd.AddCommand(publicSourceTransitionCmdWithConfig(shared.LoadConfig, shared.NewFileSvc))
	cmd.AddCommand(
		publicInitCmdWithConfig(shared.LoadConfig, shared.NewFileSvc),
		configCmd,
		sourceCmd,
		publicPublishCmdWithConfig(shared.LoadConfig, shared.NewFileSvc),
		publicPushCmdWithConfig(shared.LoadConfig, shared.NewFileSvc),
		publicRepairOutboxCmdWithConfig(shared.LoadConfig, shared.NewFileSvc),
		eval.PublicRestoreCmdWithConfig(shared.LoadConfig, shared.NewEvidenceFileSvc),
		publicStatusCmdWithConfig(shared.LoadConfig, shared.NewFileSvc),
		publicRotateKeyCmdWithConfig(shared.LoadConfig, shared.NewFileSvc),
		publicVerifyAssignmentCmd(),
		publicVerifyAgentTraceCmd(),
		publicVerifyProofCmd(),
	)
	return cmd
}

// publicProofKeyRegistry maps a signing key ID to its pinned Ed25519 public key.
// The entry under "*", set by --public-key, verifies a segment whose key ID is
// not otherwise registered.
type publicProofKeyRegistry map[string]ed25519.PublicKey

// publicProofChain is a verified proof segment log and the artifacts it leaves
// active at its head.
type publicProofChain struct {
	// Segments are the log's segments in chain order.
	Segments []models.PublicProofManifestSegment
	// Active maps each artifact the chain lists and does not withdraw to its
	// catalog entry, keyed by artifact ID.
	Active map[string]models.PublicProofCatalogEntry
	// ActiveIDs lists the Active artifact IDs in the order the chain first lists them.
	ActiveIDs []string
}

// loadPublicProofKeyRegistry pins the trusted signing keys named by --public-key
// and --key-registry. At least one of the two must be set.
func loadPublicProofKeyRegistry(publicKeyHex, keyRegistryPath string) (publicProofKeyRegistry, error) {
	if strings.TrimSpace(publicKeyHex) == "" && strings.TrimSpace(keyRegistryPath) == "" {
		return nil, constants.ErrMissingRequiredField
	}
	keyRegistry := make(publicProofKeyRegistry)
	if strings.TrimSpace(publicKeyHex) != "" {
		pubKeyBytes, err := hex.DecodeString(strings.TrimSpace(publicKeyHex))
		if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: invalid ed25519 public key hex", constants.ErrPublicFeedProofSegmentInvalid)
		}
		keyRegistry["*"] = ed25519.PublicKey(pubKeyBytes)
	}
	if strings.TrimSpace(keyRegistryPath) != "" {
		regData, err := os.ReadFile(keyRegistryPath)
		if err != nil {
			return nil, fmt.Errorf("public-feed: read key registry: %w", err)
		}
		var rawMap map[string]string
		if err := json.Unmarshal(regData, &rawMap); err != nil {
			return nil, fmt.Errorf("%w: decode key registry: %w", constants.ErrPublicFeedProofSegmentInvalid, err)
		}
		for k, hexStr := range rawMap {
			pubKeyBytes, err := hex.DecodeString(strings.TrimSpace(hexStr))
			if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("%w: invalid ed25519 public key for %s", constants.ErrPublicFeedProofSegmentInvalid, k)
			}
			keyRegistry[k] = ed25519.PublicKey(pubKeyBytes)
		}
	}
	return keyRegistry, nil
}

// readPublicProofSegments decodes a JSON Lines proof segment log. Blank lines are
// skipped; every other line must hold exactly one segment with no unknown fields.
func readPublicProofSegments(segmentsPath string) ([]models.PublicProofManifestSegment, error) {
	segmentsData, err := os.ReadFile(segmentsPath)
	if err != nil {
		return nil, fmt.Errorf("public-feed: read proof segments: %w", err)
	}
	lines := strings.Split(string(segmentsData), "\n")
	segments := make([]models.PublicProofManifestSegment, 0, len(lines))
	for idx, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var seg models.PublicProofManifestSegment
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&seg); err != nil {
			return nil, fmt.Errorf("%w: segment line %d: %w", constants.ErrPublicFeedProofSegmentInvalid, idx+1, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("%w: trailing JSON on line %d", constants.ErrPublicFeedProofSegmentInvalid, idx+1)
		}
		segments = append(segments, seg)
	}
	return segments, nil
}

// verifyPublicProofChain checks a proof segment log from its zero root: indexes
// and previous roots link consecutively, each root and signature verifies under
// its registered key, withdrawals name only artifacts already listed and not yet
// withdrawn, and no artifact is listed twice. It returns the artifacts still
// active at the head.
func verifyPublicProofChain(segments []models.PublicProofManifestSegment, keyRegistry publicProofKeyRegistry) (publicProofChain, error) {
	if len(segments) == 0 {
		return publicProofChain{}, fmt.Errorf("%w: segments log is empty", constants.ErrPublicFeedProofSegmentInvalid)
	}
	withdrawn := make(map[string]bool)
	type orderedEntry struct {
		id    string
		entry models.PublicProofCatalogEntry
	}
	var allEntries []orderedEntry
	listed := make(map[string]bool)

	for i, seg := range segments {
		expectedIndex := int64(i + 1)
		if seg.SegmentIndex != expectedIndex {
			return publicProofChain{}, fmt.Errorf("%w: segment index %d expected %d", constants.ErrPublicFeedProofSegmentOutOfOrder, seg.SegmentIndex, expectedIndex)
		}
		expectedPrevRoot := constants.PublicFeedZeroHashHex
		if i > 0 {
			expectedPrevRoot = segments[i-1].ProofRootSHA256
		}
		if seg.PreviousRoot != expectedPrevRoot {
			return publicProofChain{}, fmt.Errorf("%w: segment %d previous root %s != expected %s", constants.ErrPublicFeedProofSegmentLinkMismatch, seg.SegmentIndex, seg.PreviousRoot, expectedPrevRoot)
		}

		var segKey ed25519.PublicKey
		if k, ok := keyRegistry[seg.SigningKeyID]; ok {
			segKey = k
		} else if k, ok := keyRegistry[seg.SourceID+":"+seg.SigningKeyID]; ok {
			segKey = k
		} else if k, ok := keyRegistry["*"]; ok {
			segKey = k
		} else {
			return publicProofChain{}, fmt.Errorf("%w: unknown signing key %s for segment %d", constants.ErrPublicFeedProofSegmentInvalid, seg.SigningKeyID, seg.SegmentIndex)
		}

		if err := models.VerifyPublicProofSegment(segments[0].SourceID, seg, segKey); err != nil {
			return publicProofChain{}, fmt.Errorf("public-feed: verify segment %d: %w", seg.SegmentIndex, err)
		}

		for _, w := range seg.WithdrawnArtifactIDs {
			if !listed[w] || withdrawn[w] {
				return publicProofChain{}, fmt.Errorf("%w: withdrawal of inactive artifact %s", constants.ErrPublicFeedProofSegmentInvalid, w)
			}
			withdrawn[w] = true
		}
		for _, entry := range seg.Entries {
			if listed[entry.ArtifactID] {
				return publicProofChain{}, fmt.Errorf("%w: artifact %s repeated", constants.ErrPublicFeedProofSegmentEquivocation, entry.ArtifactID)
			}
			listed[entry.ArtifactID] = true
			allEntries = append(allEntries, orderedEntry{id: entry.ArtifactID, entry: entry})
		}
	}

	chain := publicProofChain{
		Segments: segments,
		Active:   make(map[string]models.PublicProofCatalogEntry, len(allEntries)),
	}
	for _, item := range allEntries {
		if withdrawn[item.id] {
			continue
		}
		chain.Active[item.id] = item.entry
		chain.ActiveIDs = append(chain.ActiveIDs, item.id)
	}
	return chain, nil
}

// publicVerifyProofCmd verifies a chained proof manifest segment log and its
// referenced proof artifacts offline: chain root continuity from zero hash,
// recomputed segment roots, Ed25519 signatures, withdrawal exclusions, and
// active artifact file hashes and sizes.
func publicVerifyProofCmd() *cobra.Command {
	var segmentsPath string
	var artifactsPath string
	var publicKeyHex string
	var keyRegistryPath string

	cmd := &cobra.Command{
		Use:   "verify-proof",
		Short: "Verify a published proof package offline",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(segmentsPath) == "" || strings.TrimSpace(artifactsPath) == "" {
				return constants.ErrMissingRequiredField
			}
			keyRegistry, err := loadPublicProofKeyRegistry(publicKeyHex, keyRegistryPath)
			if err != nil {
				return err
			}
			segments, err := readPublicProofSegments(segmentsPath)
			if err != nil {
				return err
			}
			chain, err := verifyPublicProofChain(segments, keyRegistry)
			if err != nil {
				return err
			}

			for _, artifactID := range chain.ActiveIDs {
				entry := chain.Active[artifactID]
				if entry.ArtifactID == "" || entry.ByteSize < 0 {
					return fmt.Errorf("%w: invalid entry %s", constants.ErrPublicFeedProofCatalogMismatch, entry.ArtifactID)
				}
				artifactPath := filepath.Join(artifactsPath, entry.ArtifactID)
				fi, err := os.Lstat(artifactPath)
				if err != nil {
					return fmt.Errorf("%w: artifact file %s: %w", constants.ErrPublicFeedProofNotFound, entry.ArtifactID, err)
				}
				if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
					return fmt.Errorf("%w: artifact %s is not regular file", constants.ErrPublicFeedProofSymlinkRejected, entry.ArtifactID)
				}
				if fi.Size() != entry.ByteSize {
					return fmt.Errorf("%w: artifact %s size %d != expected %d", constants.ErrPublicFeedProofSizeMismatch, entry.ArtifactID, fi.Size(), entry.ByteSize)
				}
				content, err := os.ReadFile(artifactPath)
				if err != nil {
					return fmt.Errorf("public-feed: read artifact %s: %w", entry.ArtifactID, err)
				}
				h := sha256.Sum256(content)
				computedID := hex.EncodeToString(h[:])
				if computedID != entry.ArtifactID {
					return fmt.Errorf("%w: artifact %s content hash mismatch", constants.ErrPublicFeedProofHashMismatch, entry.ArtifactID)
				}
			}

			last := chain.Segments[len(chain.Segments)-1]
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Proof package verified (%d segments, %d active artifacts, root %s)\n", len(chain.Segments), len(chain.Active), last.ProofRootSHA256)
			return err
		},
	}
	cmd.Flags().StringVar(&segmentsPath, "segments", "", "Path to the chained proof-segments.jsonl file")
	cmd.Flags().StringVar(&artifactsPath, "artifacts", "", "Path to directory containing proof artifact files")
	cmd.Flags().StringVar(&publicKeyHex, "public-key", "", "Hex-encoded Ed25519 public key")
	cmd.Flags().StringVar(&keyRegistryPath, "key-registry", "", "Path to JSON file containing pinned key registry {key_id: public_key_hex}")
	_ = cmd.MarkFlagRequired("segments")
	_ = cmd.MarkFlagRequired("artifacts")
	return cmd
}

// publicVerifyAgentTraceCmd verifies a downloaded assignment_agent_trace proof
// artifact offline: its evidence digest, every receipt signature against the
// trace's pinned actuator anchors, and the sentinel ledger bracket.
func publicVerifyAgentTraceCmd() *cobra.Command {
	var tracePath string
	cmd := &cobra.Command{
		Use:   "verify-agent-trace",
		Short: "Verify a published assignment agent trace offline",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(tracePath) == "" {
				return constants.ErrMissingRequiredField
			}
			body, err := os.ReadFile(tracePath)
			if err != nil {
				return fmt.Errorf("public-feed: read agent trace: %w", err)
			}
			trace, err := evaluation.DecodeAgentTrace(body)
			if err != nil {
				return fmt.Errorf("public-feed: verify agent trace: %w", err)
			}
			if err := evaluation.VerifyAgentTraceEvidence(trace); err != nil {
				return fmt.Errorf("public-feed: verify agent trace: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Agent trace verified (%s, sha256 %s)\n", trace.SchemaVersion, models.SHA256Hex(body))
			return err
		},
	}
	cmd.Flags().StringVar(&tracePath, "trace", "", "Path to the assignment agent trace JSON")
	_ = cmd.MarkFlagRequired("trace")
	return cmd
}

// publicVerifyAssignmentCmd verifies an assignment audit database offline. The
// proof segment log is verified first. The database's SHA-256 must then be an
// active artifact of that chain before the database's own live events are
// checked against their record hashes and the header's run and assignment.
// The database file is read once; the digest and the verified events both come
// from those bytes.
func publicVerifyAssignmentCmd() *cobra.Command {
	var dbPath string
	var segmentsPath string
	var publicKeyHex string
	var keyRegistryPath string

	cmd := &cobra.Command{
		Use:   "verify-assignment",
		Short: "Verify an assignment audit database against its signed proof segments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(dbPath) == "" || strings.TrimSpace(segmentsPath) == "" {
				return constants.ErrMissingRequiredField
			}
			keyRegistry, err := loadPublicProofKeyRegistry(publicKeyHex, keyRegistryPath)
			if err != nil {
				return err
			}
			segments, err := readPublicProofSegments(segmentsPath)
			if err != nil {
				return err
			}
			chain, err := verifyPublicProofChain(segments, keyRegistry)
			if err != nil {
				return err
			}
			dbBytes, err := os.ReadFile(dbPath)
			if err != nil {
				return fmt.Errorf("public-feed: read assignment audit db: %w", err)
			}
			digest := models.SHA256Hex(dbBytes)
			if _, active := chain.Active[digest]; !active {
				return fmt.Errorf("%w: sha256 %s", constants.ErrPublicAssignmentAuditNotInProof, digest)
			}
			header, records, err := assignmentaudit.VerifyDatabase(shared.CommandContext(cmd), dbBytes)
			if err != nil {
				return fmt.Errorf("public-feed: verify assignment audit db: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Assignment audit verified (run %s, assignment %s, %d events, sha256 %s)\n", header.RunID, header.AssignmentID, len(records), digest)
			return err
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "Path to the assignment audit SQLite file")
	cmd.Flags().StringVar(&segmentsPath, "segments", "", "Path to the chained proof-segments.jsonl file")
	cmd.Flags().StringVar(&publicKeyHex, "public-key", "", "Hex-encoded Ed25519 public key")
	cmd.Flags().StringVar(&keyRegistryPath, "key-registry", "", "Path to JSON file containing pinned key registry {key_id: public_key_hex}")
	_ = cmd.MarkFlagRequired("db")
	_ = cmd.MarkFlagRequired("segments")
	return cmd
}

func loadPublicCommandRuntimeUnchecked(cmd *cobra.Command, configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) (fs.RuntimeFileService, models.PublicExportConfig, error) {
	cfg, err := configLoader("")
	if err != nil {
		return nil, models.PublicExportConfig{}, err
	}
	fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
	if err != nil {
		return nil, models.PublicExportConfig{}, fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
	}
	exportConfig, err := gateway.ReadPublicExportConfig(shared.CommandContext(cmd), fileSvc)
	if err != nil {
		return nil, models.PublicExportConfig{}, err
	}
	return fileSvc, exportConfig, nil
}

func loadPublicCommandRuntime(cmd *cobra.Command, configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) (fs.RuntimeFileService, models.PublicExportConfig, error) {
	fileSvc, exportConfig, err := loadPublicCommandRuntimeUnchecked(cmd, configLoader, fileSvcFactory)
	if err != nil {
		return nil, models.PublicExportConfig{}, err
	}
	exists, err := fileSvc.FileExists(shared.CommandContext(cmd), constants.PublicFeedKeyRotationPath)
	if err != nil {
		return nil, models.PublicExportConfig{}, fmt.Errorf("public-feed: inspect key rotation state: %w", err)
	}
	if exists {
		return nil, models.PublicExportConfig{}, constants.ErrPublicFeedKeyRotationPending
	}
	return fileSvc, exportConfig, nil
}

func newPublicPublisherForCommand(cmd *cobra.Command, fileSvc fs.RuntimeFileService, exportConfig models.PublicExportConfig) (*gateway.PublicPublisherService, error) {
	ctx := shared.CommandContext(cmd)
	ks, err := publicKeystore(fileSvc)
	if err != nil {
		return nil, err
	}
	key, err := gateway.ReadPublicSecret(ctx, fileSvc, ks, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired)
	if err != nil {
		return nil, err
	}
	token, err := gateway.ReadPublicSecret(ctx, fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
	if err != nil {
		return nil, err
	}
	publisher := gateway.NewPublicPublisherService(nil, fileSvc, slog.Default(), exportConfig, ed25519.PrivateKey(key), exportConfig.SigningKeyID)
	publisher.SetIngestAuthToken(hex.EncodeToString(token))
	return publisher, nil
}

func publicPublishCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish <records.jsonl>",
		Short: "Publish public-safe records to the configured mirror",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := shared.CommandContext(cmd)
			if gwremote.ShouldPublishViaGateway(ctx, fileSvc) {
				count, err := gwremote.PublishJSONLViaGateway(ctx, fileSvc, cfg, args[0])
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Published %d records through the gateway-owned public mirror\n", count)
				return err
			}
			fileSvc, exportConfig, err := loadPublicCommandRuntime(cmd, configLoader, fileSvcFactory)
			if err != nil {
				return err
			}
			publisher, err := newPublicPublisherForCommand(cmd, fileSvc, exportConfig)
			if err != nil {
				return err
			}
			inputs, err := gwremote.ReadPublicRecordInputs(args[0], exportConfig.BatchMaxRecords, exportConfig.BatchMaxBytes)
			if err != nil {
				return err
			}
			nextSequence := int64(1)
			snapshot, err := publisher.GetSnapshot(shared.CommandContext(cmd))
			if err == nil {
				nextSequence = snapshot.HighWaterSequence + 1
			} else if !errors.Is(err, constants.ErrPublicFeedSnapshotNotFound) {
				return err
			}
			records := make([]models.PublicFeedRecord, len(inputs))
			for index, input := range inputs {
				digest := sha256.Sum256([]byte(input.RecordBytes))
				records[index] = models.PublicFeedRecord{
					Sequence:    nextSequence + int64(index),
					RecordType:  input.RecordType,
					RecordHash:  hex.EncodeToString(digest[:]),
					RecordBytes: input.RecordBytes,
				}
			}
			if err := publisher.ExportBatch(shared.CommandContext(cmd), records); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Published %d records through sequence %d\n", len(records), records[len(records)-1].Sequence)
			return err
		},
	}
	return cmd
}

func publicRepairOutboxCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "repair-outbox",
		Short: "Compact a prefix-pruned public-feed outbox back to the mirror snapshot tip",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := shared.CommandContext(cmd)
			if gwremote.ShouldPublishViaGateway(ctx, fileSvc) {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Gateway-owned public mirror outbox is managed by the Gateway")
				return err
			}
			fileSvc, exportConfig, err := loadPublicCommandRuntime(cmd, configLoader, fileSvcFactory)
			if err != nil {
				return err
			}
			publisher, err := newPublicPublisherForCommand(cmd, fileSvc, exportConfig)
			if err != nil {
				return err
			}
			if err := publisher.RepairOutboxFromSnapshot(ctx); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Public outbox repaired against snapshot")
			return err
		},
	}
}

func publicPushCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Retry the durable public-feed outbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := shared.CommandContext(cmd)
			if gwremote.ShouldPublishViaGateway(ctx, fileSvc) {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Gateway-owned public mirror is already synchronized")
				return err
			}
			fileSvc, exportConfig, err := loadPublicCommandRuntime(cmd, configLoader, fileSvcFactory)
			if err != nil {
				return err
			}
			publisher, err := newPublicPublisherForCommand(cmd, fileSvc, exportConfig)
			if err != nil {
				return err
			}
			if err := publisher.RetransmitOutbox(shared.CommandContext(cmd)); err != nil {
				return err
			}
			if err := publisher.PushProofPackage(shared.CommandContext(cmd)); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Public outbox and proof package delivered")
			return err
		},
	}
}

func publicStatusCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show public-feed publication state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory(cfg.ProjectRoot, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := shared.CommandContext(cmd)
			if gwremote.ShouldPublishViaGateway(ctx, fileSvc) {
				status, err := gwremote.GatewayPublisherStatus(ctx, fileSvc, cfg)
				if err != nil {
					return err
				}
				body, err := json.Marshal(status)
				if err != nil {
					return fmt.Errorf("public-feed: encode status: %w", err)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
				return err
			}
			fileSvc, exportConfig, err := loadPublicCommandRuntime(cmd, configLoader, fileSvcFactory)
			if err != nil {
				return err
			}
			publisher, err := newPublicPublisherForCommand(cmd, fileSvc, exportConfig)
			if err != nil {
				return err
			}
			status := models.PublicPublisherStatus{
				Enabled:       exportConfig.Enabled,
				MirrorOrigin:  exportConfig.MirrorOrigin,
				SourceID:      exportConfig.SourceID,
				SigningKeyID:  exportConfig.SigningKeyID,
				FeedChainHash: constants.PublicFeedZeroHashHex,
			}
			snapshot, err := publisher.GetSnapshot(shared.CommandContext(cmd))
			if err == nil {
				status.HighWaterSequence = snapshot.HighWaterSequence
				status.FeedChainHash = snapshot.FeedChainHash
				status.BatchCount = snapshot.BatchCount
			} else if !errors.Is(err, constants.ErrPublicFeedSnapshotNotFound) {
				return err
			}
			body, err := json.Marshal(status)
			if err != nil {
				return fmt.Errorf("public-feed: encode status: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
			return err
		},
	}
}

// publicKeyRotationBinding binds the sealed rotation key to the key ID its
// state claims, so a sealed key cannot be paired with another key ID.
func publicKeyRotationBinding(newKeyID string) string {
	return constants.PublicFeedKeyRotationPath + "#" + newKeyID
}

// readPublicKeyRotation loads pending rotation state and unseals its new
// signing key. It returns nil state when no rotation is pending.
func readPublicKeyRotation(ctx context.Context, fileSvc fs.RuntimeFileService, ks *keystore.Keystore) (*models.PublicKeyRotationState, ed25519.PrivateKey, error) {
	exists, err := fileSvc.FileExists(ctx, constants.PublicFeedKeyRotationPath)
	if err != nil {
		return nil, nil, fmt.Errorf("public-feed: inspect key rotation state: %w", err)
	}
	if !exists {
		return nil, nil, nil
	}
	data, err := fileSvc.ReadFile(ctx, constants.PublicFeedKeyRotationPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read state: %w", constants.ErrPublicFeedKeyRotationPending, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state models.PublicKeyRotationState
	if err := decoder.Decode(&state); err != nil {
		return nil, nil, fmt.Errorf("%w: decode state: %w", constants.ErrPublicFeedKeyRotationPending, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, nil, fmt.Errorf("%w: trailing JSON", constants.ErrPublicFeedKeyRotationPending)
	}
	privateKeyHex, err := ks.Decrypt(publicKeyRotationBinding(state.NewKeyID), state.SealedNewPrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: unseal new signing key: %w", constants.ErrPublicFeedKeyRotationPending, err)
	}
	privateKey, err := hex.DecodeString(privateKeyHex)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize || state.SourceID == "" || state.OldKeyID == "" || state.NewKeyID == "" || state.OldKeyID == state.NewKeyID {
		return nil, nil, constants.ErrPublicFeedKeyRotationPending
	}
	digest := sha256.Sum256(ed25519.PrivateKey(privateKey).Public().(ed25519.PublicKey))
	if state.NewKeyID != hex.EncodeToString(digest[:]) {
		return nil, nil, constants.ErrPublicFeedKeyRotationPending
	}
	return &state, ed25519.PrivateKey(privateKey), nil
}

func writePublicKeyRotation(ctx context.Context, fileSvc fs.RuntimeFileService, state models.PublicKeyRotationState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("public-feed: encode key rotation state: %w", err)
	}
	if err := fileSvc.WriteFile(ctx, constants.PublicFeedKeyRotationPath, data, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("public-feed: write key rotation state: %w", err)
	}
	return nil
}

func publicKeyRotationOutboxStatus(ctx context.Context, fileSvc fs.RuntimeFileService, rotation models.PublicKeyRotationState) (bool, bool, error) {
	exists, err := fileSvc.FileExists(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return false, false, fmt.Errorf("public-feed: inspect rotation outbox: %w", err)
	}
	if !exists {
		return false, false, nil
	}
	data, err := fileSvc.ReadFile(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return false, false, fmt.Errorf("public-feed: read rotation outbox: %w", err)
	}
	for lineNumber, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry models.PublicOutboxEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return false, false, fmt.Errorf("%w: rotation outbox line %d: %w", constants.ErrPublicFeedOutboxCorrupt, lineNumber+1, err)
		}
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			return false, false, fmt.Errorf("%w: rotation batch line %d: %w", constants.ErrPublicFeedOutboxCorrupt, lineNumber+1, err)
		}
		for _, record := range batch.Records {
			if record.RecordType != models.PublicFeedRecordTypeKeyRevocation {
				continue
			}
			var revocation models.PublicKeyRevocationRecord
			if err := json.Unmarshal([]byte(record.RecordBytes), &revocation); err != nil {
				return false, false, fmt.Errorf("%w: rotation record line %d: %w", constants.ErrPublicFeedOutboxCorrupt, lineNumber+1, err)
			}
			if revocation.SourceID == rotation.SourceID && revocation.RevokedKeyID == rotation.OldKeyID && revocation.NewKeyID == rotation.NewKeyID {
				return true, entry.Status == models.PublicFeedOutboxStatusAcknowledged, nil
			}
		}
	}
	return false, false, nil
}

func finalizePublicKeyRotation(ctx context.Context, fileSvc fs.RuntimeFileService, ks *keystore.Keystore, exportConfig models.PublicExportConfig, rotation models.PublicKeyRotationState, newPrivateKey ed25519.PrivateKey) error {
	if err := gateway.WritePublicSecret(ctx, fileSvc, ks, constants.PublicFeedSigningKeyPath, newPrivateKey); err != nil {
		return fmt.Errorf("public-feed: persist rotated signing key: %w", err)
	}
	exportConfig.SigningKeyID = rotation.NewKeyID
	if err := gateway.WritePublicExportConfig(ctx, fileSvc, exportConfig); err != nil {
		return err
	}
	if err := fileSvc.Remove(ctx, constants.PublicFeedKeyRotationPath); err != nil {
		return fmt.Errorf("public-feed: remove completed key rotation state: %w", err)
	}
	return nil
}

func publicRotateKeyCmdWithConfig(configLoader publicConfigLoader, fileSvcFactory publicFileSvcFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-key",
		Short: "Rotate the public-feed signing key",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileSvc, exportConfig, err := loadPublicCommandRuntimeUnchecked(cmd, configLoader, fileSvcFactory)
			if err != nil {
				return err
			}
			ctx := shared.CommandContext(cmd)
			ks, err := publicKeystore(fileSvc)
			if err != nil {
				return err
			}
			rotation, newPrivateKey, err := readPublicKeyRotation(ctx, fileSvc, ks)
			if err != nil {
				return err
			}
			if rotation == nil {
				publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					return fmt.Errorf("%w: %w", constants.ErrPublicFeedKeyGenFailed, err)
				}
				digest := sha256.Sum256(publicKey)
				newKeyID := hex.EncodeToString(digest[:])
				sealed, err := ks.Encrypt(publicKeyRotationBinding(newKeyID), hex.EncodeToString(privateKey))
				if err != nil {
					return fmt.Errorf("public-feed: seal new signing key: %w", err)
				}
				rotation = &models.PublicKeyRotationState{
					SourceID:            exportConfig.SourceID,
					OldKeyID:            exportConfig.SigningKeyID,
					NewKeyID:            newKeyID,
					SealedNewPrivateKey: sealed,
				}
				newPrivateKey = privateKey
				if err := writePublicKeyRotation(ctx, fileSvc, *rotation); err != nil {
					return err
				}
			}
			if rotation.SourceID != exportConfig.SourceID || (exportConfig.SigningKeyID != rotation.OldKeyID && exportConfig.SigningKeyID != rotation.NewKeyID) {
				return constants.ErrPublicFeedKeyRotationPending
			}
			found, acknowledged, err := publicKeyRotationOutboxStatus(ctx, fileSvc, *rotation)
			if err != nil {
				return err
			}
			if !acknowledged {
				if exportConfig.SigningKeyID != rotation.OldKeyID {
					return constants.ErrPublicFeedKeyRotationPending
				}
				publisher, err := newPublicPublisherForCommand(cmd, fileSvc, exportConfig)
				if err != nil {
					return err
				}
				if found {
					err = publisher.RetransmitOutbox(ctx)
				} else {
					err = publisher.RotateKeyTo(ctx, newPrivateKey, rotation.NewKeyID)
				}
				if err != nil {
					return err
				}
				_, acknowledged, err = publicKeyRotationOutboxStatus(ctx, fileSvc, *rotation)
				if err != nil {
					return err
				}
				if !acknowledged {
					return constants.ErrPublicFeedKeyRotationPending
				}
			}
			if err := finalizePublicKeyRotation(ctx, fileSvc, ks, exportConfig, *rotation, newPrivateKey); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Public signing key rotated to %s\n", rotation.NewKeyID)
			return err
		},
	}
}
