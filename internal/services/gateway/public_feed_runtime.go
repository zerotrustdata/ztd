// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

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
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

const (
	defaultPublicSpectatorSourceID = "opendevops-local"
)

// ReadPublicExportConfig loads the durable public-feed export configuration.
func ReadPublicExportConfig(ctx context.Context, fileSvc fs.RuntimeFileService) (models.PublicExportConfig, error) {
	data, err := fileSvc.ReadFile(ctx, constants.PublicFeedExportConfigPath)
	if err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("%w: %w", constants.ErrPublicFeedConfigRequired, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var exportConfig models.PublicExportConfig
	if err := decoder.Decode(&exportConfig); err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("%w: decode: %w", constants.ErrPublicFeedConfigRequired, err)
	}
	if err := rejectTrailingPublicJSON(decoder); err != nil {
		return models.PublicExportConfig{}, err
	}
	if err := ValidatePublicExportConfig(exportConfig); err != nil {
		return models.PublicExportConfig{}, err
	}
	return exportConfig, nil
}

func ValidatePublicFeedSourceID(sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return constants.ErrPublicFeedSourceIDRequired
	}
	if len(sourceID) > 128 || sourceID == "." || sourceID == ".." {
		return constants.ErrPublicFeedSourceIDInvalid
	}
	for _, char := range sourceID {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.' {
			continue
		}
		return constants.ErrPublicFeedSourceIDInvalid
	}
	return nil
}

// ValidatePublicExportConfig checks required public-feed publisher fields.
func ValidatePublicExportConfig(exportConfig models.PublicExportConfig) error {
	if err := ValidatePublicFeedSourceID(exportConfig.SourceID); err != nil {
		return err
	}
	if strings.TrimSpace(exportConfig.SigningKeyID) == "" {
		return constants.ErrPublicFeedSigningKeyIDRequired
	}
	if exportConfig.BatchMaxRecords <= 0 || exportConfig.BatchMaxRecords > constants.PublicFeedBatchMaxRecords || exportConfig.BatchMaxBytes <= 0 || exportConfig.BatchMaxBytes > constants.PublicFeedBatchMaxBytes || exportConfig.RetryMaxAttempts <= 0 || exportConfig.RetryInitialBackoffSecs < 0 || exportConfig.RetryMaxBackoffSecs < exportConfig.RetryInitialBackoffSecs || exportConfig.AckWindowSecs <= 0 {
		return constants.ErrPublicFeedConfigRequired
	}
	return ValidatePublicMirrorOrigin(exportConfig.MirrorOrigin)
}

// ValidatePublicMirrorOrigin ensures the mirror origin is a bare loopback or
// hosted origin without path or credentials.
func ValidatePublicMirrorOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return constants.ErrPublicFeedMirrorOriginRequired
	}
	return nil
}

// ValidatePublicMirrorListenAddresses enforces loopback-only private/public
// mirror listeners with distinct addresses. When allowContainerBind is true,
// 0.0.0.0 is also accepted so a container network can reach the listener.
func ValidatePublicMirrorListenAddresses(privateAddress, publicAddress string, allowContainerBind bool) error {
	for _, address := range []string{privateAddress, publicAddress} {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %q: %v", constants.ErrPublicFeedListenAddress, address, err)
		}
		if !isAllowedPublicMirrorListenHost(host, allowContainerBind) {
			return fmt.Errorf("%w: %q", constants.ErrPublicFeedListenAddress, address)
		}
	}
	if _, port, err := net.SplitHostPort(privateAddress); err == nil && port != "0" && sameListenAddress(privateAddress, publicAddress) {
		return fmt.Errorf("%w: duplicate address %q", constants.ErrPublicFeedListenAddress, privateAddress)
	}
	return nil
}

func isAllowedPublicMirrorListenHost(host string, allowContainerBind bool) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	return allowContainerBind && host == "0.0.0.0"
}

// ValidatePublicExplorerListenAddress enforces loopback-only explorer listeners.
// The public mirror address is allowed because its handler combines the mirror
// API and explorer assets on one listener; a dedicated listener must differ from
// the authenticated private ingest address.
func ValidatePublicExplorerListenAddress(explorerAddress, privateAddress, publicAddress string, allowContainerBind bool) error {
	explorerAddress = strings.TrimSpace(explorerAddress)
	if explorerAddress == "" {
		return nil
	}
	if sameListenAddress(explorerAddress, publicAddress) {
		return nil
	}
	if sameListenAddress(explorerAddress, privateAddress) {
		return fmt.Errorf("%w: duplicate address %q", constants.ErrPublicFeedListenAddress, explorerAddress)
	}
	host, _, err := net.SplitHostPort(explorerAddress)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", constants.ErrPublicFeedListenAddress, explorerAddress, err)
	}
	if !isAllowedPublicMirrorListenHost(host, allowContainerBind) {
		return fmt.Errorf("%w: %q", constants.ErrPublicFeedListenAddress, explorerAddress)
	}
	return nil
}

func sameListenAddress(a, b string) bool {
	return normalizeListenAddress(a) == normalizeListenAddress(b)
}

func normalizeListenAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// InitializeLocalPublicFeed creates a new local publisher generation. It
// requires an unused feed state and rolls back paths it created if a later
// write fails. Callers that want read-if-present behavior must make that choice
// explicitly before calling this function.
func InitializeLocalPublicFeed(ctx context.Context, fileSvc fs.RuntimeFileService, ks *keystore.Keystore, sourceID, mirrorOrigin string) (models.PublicExportConfig, error) {
	if ks == nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: %w: keystore", constants.ErrMissingRequiredField)
	}
	if err := ValidatePublicFeedSourceID(sourceID); err != nil {
		return models.PublicExportConfig{}, err
	}
	if err := ValidatePublicMirrorOrigin(mirrorOrigin); err != nil {
		return models.PublicExportConfig{}, err
	}
	// A new signing key starts a new feed generation. State signed under a
	// previous generation's key (outbox, proof log, mirror checkpoint) can
	// never verify under the new key, so genesis fails closed instead of
	// adopting it; the operator archives or discards that generation first.
	for _, generationPath := range archivePathPairs(PublicFeedArchivePathSet{}) {
		exists, existsErr := fileSvc.FileExists(ctx, generationPath.active)
		if existsErr != nil {
			return models.PublicExportConfig{}, fmt.Errorf("public-feed: inspect initialization path: %w", existsErr)
		}
		if !exists {
			continue
		}
		switch generationPath.active {
		case constants.PublicFeedExportConfigPath, constants.PublicFeedSigningKeyPath, constants.PublicFeedIngestTokenPath:
			return models.PublicExportConfig{}, constants.ErrPublicFeedConfigExists
		default:
			return models.PublicExportConfig{}, fmt.Errorf("%w: %s", constants.ErrPublicFeedOrphanedGeneration, generationPath.active)
		}
	}
	if err := fileSvc.MkdirAll(ctx, constants.PublicFeedDirname, constants.PermDirPrivate); err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: create runtime directory: %w", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("%w: %w", constants.ErrPublicFeedKeyGenFailed, err)
	}
	token := make([]byte, constants.PublicFeedIngestTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: generate ingest token: %w", err)
	}
	keyDigest := sha256.Sum256(publicKey)
	exportConfig := models.DefaultPublicExportConfig()
	exportConfig.Enabled = true
	exportConfig.SourceID = sourceID
	exportConfig.MirrorOrigin = mirrorOrigin
	exportConfig.SigningKeyID = hex.EncodeToString(keyDigest[:])
	createdPaths := make([]string, 0, 3)
	rollback := func(cause error) error {
		result := cause
		for index := len(createdPaths) - 1; index >= 0; index-- {
			if removeErr := fileSvc.Remove(ctx, createdPaths[index]); removeErr != nil {
				result = errors.Join(result, fmt.Errorf("public-feed: roll back initialization path %s: %w", createdPaths[index], removeErr))
			}
		}
		return result
	}
	if err := WritePublicSecret(ctx, fileSvc, ks, constants.PublicFeedSigningKeyPath, privateKey); err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: write signing key: %w", err)
	}
	createdPaths = append(createdPaths, constants.PublicFeedSigningKeyPath)
	if err := WritePublicSecret(ctx, fileSvc, ks, constants.PublicFeedIngestTokenPath, token); err != nil {
		return models.PublicExportConfig{}, rollback(fmt.Errorf("public-feed: write ingest token: %w", err))
	}
	createdPaths = append(createdPaths, constants.PublicFeedIngestTokenPath)
	if err := WritePublicExportConfig(ctx, fileSvc, exportConfig); err != nil {
		return models.PublicExportConfig{}, rollback(err)
	}
	return exportConfig, nil
}

type publicFeedArchivePath struct {
	active  string
	archive string
}

type PublicFeedArchivePathSet struct {
	Root            string
	ExportConfig    string
	SigningKey      string
	IngestToken     string
	Outbox          string
	Snapshot        string
	KeyRotation     string
	Proofs          string
	ProofCatalog    string
	ProofManifest   string
	ProofSegments   string
	ProofMirrorSync string
}

func PublicFeedArchivePathsFor(sourceID string) (PublicFeedArchivePathSet, error) {
	if err := ValidatePublicFeedSourceID(sourceID); err != nil {
		return PublicFeedArchivePathSet{}, err
	}
	root := path.Join(constants.PublicFeedArchiveGenerationsDirname, strings.TrimSpace(sourceID))
	return PublicFeedArchivePathSet{
		Root:            root,
		ExportConfig:    path.Join(root, constants.PublicFeedExportConfigFilename),
		SigningKey:      path.Join(root, constants.PublicFeedSigningKeyFilename),
		IngestToken:     path.Join(root, constants.PublicFeedIngestTokenFilename),
		Outbox:          path.Join(root, constants.PublicFeedOutboxFilename),
		Snapshot:        path.Join(root, constants.PublicFeedSnapshotFilename),
		KeyRotation:     path.Join(root, constants.PublicFeedKeyRotationFilename),
		Proofs:          path.Join(root, constants.PublicProofsDirname),
		ProofCatalog:    path.Join(root, constants.PublicProofCatalogFilename),
		ProofManifest:   path.Join(root, constants.PublicProofManifestFilename),
		ProofSegments:   path.Join(root, constants.PublicProofSegmentsFilename),
		ProofMirrorSync: path.Join(root, constants.PublicProofMirrorSyncFilename),
	}, nil
}

func archivePathPairs(archive PublicFeedArchivePathSet) []publicFeedArchivePath {
	return []publicFeedArchivePath{
		{active: constants.PublicFeedExportConfigPath, archive: archive.ExportConfig},
		{active: constants.PublicFeedSigningKeyPath, archive: archive.SigningKey},
		{active: constants.PublicFeedIngestTokenPath, archive: archive.IngestToken},
		{active: constants.PublicFeedOutboxPath, archive: archive.Outbox},
		{active: constants.PublicFeedSnapshotPath, archive: archive.Snapshot},
		{active: constants.PublicFeedKeyRotationPath, archive: archive.KeyRotation},
		{active: constants.PublicProofsDirname, archive: archive.Proofs},
		{active: constants.PublicProofCatalogFilename, archive: archive.ProofCatalog},
		{active: constants.PublicProofManifestFilename, archive: archive.ProofManifest},
		{active: constants.PublicProofSegmentsFilename, archive: archive.ProofSegments},
		{active: constants.PublicProofMirrorSyncFilename, archive: archive.ProofMirrorSync},
	}
}

func TransitionLocalPublicFeed(ctx context.Context, fileSvc fs.RuntimeFileService, ks *keystore.Keystore, sourceID string) (models.PublicExportConfig, error) {
	if ks == nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: %w: keystore", constants.ErrMissingRequiredField)
	}
	sourceID = strings.TrimSpace(sourceID)
	if err := ValidatePublicFeedSourceID(sourceID); err != nil {
		return models.PublicExportConfig{}, err
	}
	oldConfig, err := ReadPublicExportConfig(ctx, fileSvc)
	if err != nil {
		return models.PublicExportConfig{}, err
	}
	if sourceID == oldConfig.SourceID {
		return models.PublicExportConfig{}, fmt.Errorf("%w: source is unchanged", constants.ErrValidationFailed)
	}
	requestedGeneration, err := PublicFeedArchivePathsFor(sourceID)
	if err != nil {
		return models.PublicExportConfig{}, err
	}
	requestedGenerationExists, err := fileSvc.FileExists(ctx, requestedGeneration.Root)
	if err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: inspect requested archive generation: %w", err)
	}
	if requestedGenerationExists {
		return models.PublicExportConfig{}, constants.ErrPublicFeedArchiveGenerationExists
	}
	generation, err := PublicFeedArchivePathsFor(oldConfig.SourceID)
	if err != nil {
		return models.PublicExportConfig{}, err
	}
	generationExists, err := fileSvc.FileExists(ctx, generation.Root)
	if err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: inspect archive generation: %w", err)
	}
	if generationExists {
		return models.PublicExportConfig{}, constants.ErrPublicFeedArchiveGenerationExists
	}
	if _, err := ReadPublicSecret(ctx, fileSvc, ks, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired); err != nil {
		return models.PublicExportConfig{}, err
	}
	if _, err := ReadPublicSecret(ctx, fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired); err != nil {
		return models.PublicExportConfig{}, err
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("%w: %v", constants.ErrPublicFeedKeyGenFailed, err)
	}
	token := make([]byte, constants.PublicFeedIngestTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: generate ingest token: %w", err)
	}
	keyDigest := sha256.Sum256(publicKey)
	newConfig := oldConfig
	newConfig.SourceID = sourceID
	newConfig.SigningKeyID = hex.EncodeToString(keyDigest[:])

	if err := fileSvc.MkdirAll(ctx, generation.Root, constants.PermDirPrivate); err != nil {
		return models.PublicExportConfig{}, fmt.Errorf("public-feed: create archive generation: %w", err)
	}
	paths := archivePathPairs(generation)
	moved := make([]publicFeedArchivePath, 0, len(paths))
	rollback := func(cause error) error {
		result := cause
		for _, relPath := range []string{constants.PublicFeedExportConfigPath, constants.PublicFeedSigningKeyPath, constants.PublicFeedIngestTokenPath} {
			if removeErr := fileSvc.Remove(ctx, relPath); removeErr != nil {
				result = errors.Join(result, fmt.Errorf("public-feed: roll back new source path %s: %w", relPath, removeErr))
			}
		}
		for index := len(moved) - 1; index >= 0; index-- {
			if renameErr := fileSvc.Rename(ctx, moved[index].archive, moved[index].active); renameErr != nil {
				result = errors.Join(result, fmt.Errorf("public-feed: restore archived path %s: %w", moved[index].active, renameErr))
			}
		}
		if removeErr := fileSvc.RemoveAll(ctx, generation.Root); removeErr != nil {
			result = errors.Join(result, fmt.Errorf("public-feed: remove rolled back generation: %w", removeErr))
		}
		return result
	}
	for _, archivePath := range paths {
		exists, existsErr := fileSvc.FileExists(ctx, archivePath.active)
		if existsErr != nil {
			return models.PublicExportConfig{}, rollback(fmt.Errorf("public-feed: inspect source path %s: %w", archivePath.active, existsErr))
		}
		if !exists {
			continue
		}
		if err := fileSvc.Rename(ctx, archivePath.active, archivePath.archive); err != nil {
			return models.PublicExportConfig{}, rollback(fmt.Errorf("public-feed: archive path %s: %w", archivePath.active, err))
		}
		moved = append(moved, archivePath)
	}
	if err := WritePublicSecret(ctx, fileSvc, ks, constants.PublicFeedSigningKeyPath, privateKey); err != nil {
		return models.PublicExportConfig{}, rollback(fmt.Errorf("public-feed: write new signing key: %w", err))
	}
	if err := WritePublicSecret(ctx, fileSvc, ks, constants.PublicFeedIngestTokenPath, token); err != nil {
		return models.PublicExportConfig{}, rollback(fmt.Errorf("public-feed: write new ingest token: %w", err))
	}
	if err := WritePublicExportConfig(ctx, fileSvc, newConfig); err != nil {
		return models.PublicExportConfig{}, rollback(err)
	}
	return newConfig, nil
}

func WritePublicExportConfig(ctx context.Context, fileSvc fs.RuntimeFileService, exportConfig models.PublicExportConfig) error {
	if err := ValidatePublicExportConfig(exportConfig); err != nil {
		return err
	}
	data, err := json.MarshalIndent(exportConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("public-feed: encode export config: %w", err)
	}
	data = append(data, '\n')
	if err := fileSvc.WriteFile(ctx, constants.PublicFeedExportConfigPath, data, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("public-feed: write export config: %w", err)
	}
	return nil
}

// WritePublicSecret seals secret under the keystore master key and writes the
// ciphertext to relPath. Public-feed secrets never touch disk in the clear.
func WritePublicSecret(ctx context.Context, fileSvc fs.RuntimeFileService, ks *keystore.Keystore, relPath string, secret []byte) error {
	if ks == nil {
		return fmt.Errorf("public-feed: %w: keystore", constants.ErrMissingRequiredField)
	}
	sealed, err := ks.Encrypt(relPath, hex.EncodeToString(secret))
	if err != nil {
		return fmt.Errorf("public-feed: seal %s: %w", relPath, err)
	}
	if err := fileSvc.WriteFile(ctx, relPath, []byte(sealed), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("public-feed: write %s: %w", relPath, err)
	}
	return nil
}

// ReadPublicSecret reads and unseals a secret written by WritePublicSecret and
// checks its length. Any read, unseal, or decode failure returns missingErr.
func ReadPublicSecret(ctx context.Context, fileSvc fs.RuntimeFileService, ks *keystore.Keystore, relPath string, expectedBytes int, missingErr error) ([]byte, error) {
	if ks == nil {
		return nil, fmt.Errorf("%w: keystore unavailable", missingErr)
	}
	data, err := fileSvc.ReadFile(ctx, relPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", missingErr, err)
	}
	plaintext, err := ks.Decrypt(relPath, strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: unseal: %v", missingErr, err)
	}
	decoded, err := hex.DecodeString(plaintext)
	if err != nil || len(decoded) != expectedBytes {
		return nil, missingErr
	}
	return decoded, nil
}

func rejectTrailingPublicJSON(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", constants.ErrPublicFeedConfigRequired)
	}
	return nil
}

func newPublicMirrorHTTPServer(address string, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
