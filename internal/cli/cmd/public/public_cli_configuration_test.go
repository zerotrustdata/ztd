// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package public

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type publicFailOnceFileSvc struct {
	fs.RuntimeFileService
	path      string
	remaining int
}

func (s *publicFailOnceFileSvc) WriteFile(ctx context.Context, relPath string, data []byte, mode os.FileMode) error {
	if relPath == s.path && s.remaining > 0 {
		s.remaining--
		return fmt.Errorf("injected write failure")
	}
	return s.RuntimeFileService.WriteFile(ctx, relPath, data, mode)
}

func validPublicProjectionBytes(campaignID string) string {
	return fmt.Sprintf(`{"schema_version":"1.3.0","kind":"catalog_snapshot","dataset_id":"%s","quality_state":"live_in_progress","observed_at":"2026-09-21T00:00:00Z"}`, campaignID)
}

func useTestKeystore(t *testing.T, fileSvc fs.RuntimeFileService) *keystore.Keystore {
	t.Helper()
	keyring := keystoretest.NewMemoryKeyring()
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	original := openPublicKeystore
	openPublicKeystore = func(_ fs.RuntimeFileService) (*keystore.Keystore, error) {
		return ks, nil
	}
	t.Cleanup(func() {
		openPublicKeystore = original
	})
	return ks
}

func TestPublicCmd_ExposesProductionSurface(t *testing.T) {
	cmd := Cmd()
	expected := []string{"config", "init", "publish", "push", "repair-outbox", "restore", "rotate-key", "source", "status", "verify-agent-trace", "verify-assignment", "verify-proof"}
	require.Len(t, cmd.Commands(), len(expected))
	for _, name := range expected {
		child, _, err := cmd.Find([]string{name})
		require.NoError(t, err)
		assert.Equal(t, name, child.Name())
	}
}

func TestPublicRepairOutboxCmd_UsesGatewayOwnedStateWhenHostConfigIsAbsent(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	gwremote.WithConfiguredGatewayHealthCheck(t, true)

	var output bytes.Buffer
	cmd := publicRepairOutboxCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetOut(&output)

	require.NoError(t, cmd.Execute())
	assert.Contains(t, output.String(), "Gateway-owned public mirror outbox is managed by the Gateway")
}

func TestPublicInitCmd_PersistsPrivateConfigurationAndSecrets(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	cmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", "https://mirror.example"})

	require.NoError(t, cmd.Execute())

	configBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedExportConfigPath)
	require.NoError(t, err)
	var exportConfig models.PublicExportConfig
	require.NoError(t, json.Unmarshal(configBytes, &exportConfig))
	assert.True(t, exportConfig.Enabled)
	assert.Equal(t, "deployment-a", exportConfig.SourceID)
	assert.Equal(t, "https://mirror.example", exportConfig.MirrorOrigin)
	assert.NotEmpty(t, exportConfig.SigningKeyID)

	keyBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired)
	require.NoError(t, err)
	assert.Len(t, keyBytes, ed25519.PrivateKeySize)

	tokenBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
	require.NoError(t, err)
	assert.Len(t, tokenBytes, constants.PublicFeedIngestTokenBytes)

	// Ensure secrets on disk are sealed and not stored in the clear
	rawKey, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedSigningKeyPath)
	require.NoError(t, err)
	assert.NotEqual(t, hex.EncodeToString(keyBytes), string(rawKey))

	rawToken, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedIngestTokenPath)
	require.NoError(t, err)
	assert.NotEqual(t, hex.EncodeToString(tokenBytes), string(rawToken))

	for _, relPath := range []string{constants.PublicFeedExportConfigPath, constants.PublicFeedSigningKeyPath, constants.PublicFeedIngestTokenPath} {
		info, statErr := fileSvc.Stat(context.Background(), relPath)
		require.NoError(t, statErr)
		assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, info.IsDir()), info.Mode().Perm())
	}
}

func TestPublicInitCmd_RejectsExistingFeedWithoutChangingGeneration(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	ctx := testutil.Context(t)
	initialize := func(sourceID string) error {
		cmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
		cmd.SetArgs([]string{"--source-id", sourceID, "--mirror-origin", "https://mirror.example"})
		return cmd.Execute()
	}
	require.NoError(t, initialize("deployment-a"))
	paths := []string{constants.PublicFeedExportConfigPath, constants.PublicFeedSigningKeyPath, constants.PublicFeedIngestTokenPath}
	original := make([][]byte, len(paths))
	for index, path := range paths {
		var err error
		original[index], err = fileSvc.ReadFile(ctx, path)
		require.NoError(t, err)
	}

	require.ErrorIs(t, initialize("deployment-b"), constants.ErrPublicFeedConfigExists)
	for index, path := range paths {
		actual, err := fileSvc.ReadFile(ctx, path)
		require.NoError(t, err)
		assert.Equal(t, original[index], actual)
	}
}

func TestPublicInitCmd_RejectsOrphanedOutboxBeforeWritingSecrets(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	ctx := testutil.Context(t)
	const outbox = "orphaned-generation\n"
	require.NoError(t, fileSvc.WriteFile(ctx, constants.PublicFeedOutboxPath, []byte(outbox), constants.PermFilePrivate))
	cmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", "https://mirror.example"})

	require.ErrorIs(t, cmd.Execute(), constants.ErrPublicFeedOrphanedGeneration)
	for _, path := range []string{constants.PublicFeedExportConfigPath, constants.PublicFeedSigningKeyPath, constants.PublicFeedIngestTokenPath} {
		exists, err := fileSvc.FileExists(ctx, path)
		require.NoError(t, err)
		assert.False(t, exists, path)
	}
	actual, err := fileSvc.ReadFile(ctx, constants.PublicFeedOutboxPath)
	require.NoError(t, err)
	assert.Equal(t, outbox, string(actual))
}

func TestPublicPublishAndRotateKeyCommands_DeriveDurableSequenceAndAuthenticateMirror(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	var received []models.PublicFeedBatch
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
		assert.NoError(t, err)
		assert.Equal(t, "Bearer "+hex.EncodeToString(tokenBytes), r.Header.Get("Authorization"))
		if r.URL.Path == "/keys/register" {
			assert.NoError(t, json.NewEncoder(w).Encode(models.PublicKeyRegistrationResponse{Accepted: true}))
			return
		}
		var request models.PublicIngestRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		received = append(received, request.Batch)
		assert.NoError(t, json.NewEncoder(w).Encode(models.PublicIngestResponse{
			Accepted:          true,
			HighWaterSequence: request.Batch.LastSequence,
			FeedChainHash:     request.Batch.ContentHash,
		}))
	}))
	t.Cleanup(mirror.Close)

	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", mirror.URL})
	require.NoError(t, initCmd.Execute())

	input := models.PublicFeedRecordInput{
		RecordType:  models.PublicFeedRecordTypeProjection,
		RecordBytes: validPublicProjectionBytes("campaign-a"),
	}
	inputBytes, err := json.Marshal(input)
	require.NoError(t, err)
	inputBytes = append(inputBytes, '\n')
	inputPath := filepath.Join(cfg.ProjectRoot, constants.TestPublicFeedRecordsFilename)
	require.NoError(t, os.WriteFile(inputPath, inputBytes, constants.PermFilePrivate))

	publishCmd := publicPublishCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	publishCmd.SetArgs([]string{inputPath})
	require.NoError(t, publishCmd.Execute())
	require.Len(t, received, 1)
	assert.Equal(t, int64(1), received[0].FirstSequence)
	assert.Equal(t, int64(1), received[0].LastSequence)
	require.Len(t, received[0].Records, 1)
	assert.Equal(t, int64(1), received[0].Records[0].Sequence)

	keyBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedSigningKeyPath)
	require.NoError(t, err)
	tokenBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedIngestTokenPath)
	require.NoError(t, err)
	var output bytes.Buffer
	statusCmd := publicStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	statusCmd.SetOut(&output)
	require.NoError(t, statusCmd.Execute())
	assert.NotContains(t, output.String(), string(keyBytes))
	assert.NotContains(t, output.String(), string(tokenBytes))
	assert.Contains(t, output.String(), `"high_water_sequence":1`)

	configBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedExportConfigPath)
	require.NoError(t, err)
	var beforeRotation models.PublicExportConfig
	require.NoError(t, json.Unmarshal(configBytes, &beforeRotation))
	rotateCmd := publicRotateKeyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	require.NoError(t, rotateCmd.Execute())
	require.Len(t, received, 2)
	assert.Equal(t, int64(2), received[1].FirstSequence)
	assert.Equal(t, models.PublicFeedRecordTypeKeyRevocation, received[1].Records[0].RecordType)

	configBytes, err = fileSvc.ReadFile(context.Background(), constants.PublicFeedExportConfigPath)
	require.NoError(t, err)
	var afterRotation models.PublicExportConfig
	require.NoError(t, json.Unmarshal(configBytes, &afterRotation))
	assert.NotEqual(t, beforeRotation.SigningKeyID, afterRotation.SigningKeyID)
	rotatedKeyBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedSigningKeyPath)
	require.NoError(t, err)
	assert.NotEqual(t, keyBytes, rotatedKeyBytes)
}

func TestPublicSourceTransitionCmd_RequiresConfirmationAndArchivesOldSource(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	_ = useTestKeystore(t, fileSvc)
	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-old", "--mirror-origin", "https://mirror.example"})
	require.NoError(t, initCmd.Execute())
	require.NoError(t, fileSvc.WriteFile(context.Background(), constants.PublicFeedSnapshotPath, []byte("old-snapshot"), constants.PermFilePrivate))

	transitionCmd := publicSourceTransitionCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	transitionCmd.SetArgs([]string{"--source-id", "deployment-new"})
	err := transitionCmd.Execute()
	require.ErrorIs(t, err, constants.ErrPublicFeedSourceTransitionConfirmation)

	var output bytes.Buffer
	transitionCmd = publicSourceTransitionCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	transitionCmd.SetOut(&output)
	transitionCmd.SetArgs([]string{"--source-id", "deployment-new", "--yes"})
	require.NoError(t, transitionCmd.Execute())
	assert.Contains(t, output.String(), "deployment-new")

	activeConfig, err := gateway.ReadPublicExportConfig(context.Background(), fileSvc)
	require.NoError(t, err)
	assert.Equal(t, "deployment-new", activeConfig.SourceID)
	archivedPaths, err := gateway.PublicFeedArchivePathsFor("deployment-old")
	require.NoError(t, err)
	archivedConfigBytes, err := fileSvc.ReadFile(context.Background(), archivedPaths.ExportConfig)
	require.NoError(t, err)
	var archivedConfig models.PublicExportConfig
	require.NoError(t, json.Unmarshal(archivedConfigBytes, &archivedConfig))
	assert.Equal(t, "deployment-old", archivedConfig.SourceID)
	archivedSnapshot, err := fileSvc.ReadFile(context.Background(), archivedPaths.Snapshot)
	require.NoError(t, err)
	assert.Equal(t, "old-snapshot", string(archivedSnapshot))
}

func TestPublicRotateKeyCmd_PreRegistersNewKeyAndDurablyRevokesOldKey(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", "https://mirror.example"})
	require.NoError(t, initCmd.Execute())

	configBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedExportConfigPath)
	require.NoError(t, err)
	var oldConfig models.PublicExportConfig
	require.NoError(t, json.Unmarshal(configBytes, &oldConfig))
	oldPrivateKeyBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired)
	require.NoError(t, err)
	tokenBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
	require.NoError(t, err)

	store := openTestPublicMirrorStore(t, fileSvc)
	mirror, err := gateway.NewPublicMirrorServer(slog.Default(), store, gateway.PublicMirrorConfig{})
	require.NoError(t, err)
	mirror.SetIngestAuthToken(hex.EncodeToString(tokenBytes))
	require.NoError(t, mirror.RegisterSourceKey(context.Background(), oldConfig.SourceID, oldConfig.SigningKeyID, ed25519.PrivateKey(oldPrivateKeyBytes).Public().(ed25519.PublicKey)))
	server := httptest.NewServer(mirror.IngestHandler())

	setCmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	setCmd.SetArgs([]string{"--mirror-origin", server.URL})
	require.NoError(t, setCmd.Execute())
	inputPath := filepath.Join(cfg.ProjectRoot, constants.TestPublicFeedRecordsFilename)
	input := models.PublicFeedRecordInput{RecordType: models.PublicFeedRecordTypeProjection, RecordBytes: validPublicProjectionBytes("campaign-a")}
	inputBytes, err := json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(inputPath, append(inputBytes, '\n'), constants.PermFilePrivate))
	publishCmd := publicPublishCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	publishCmd.SetArgs([]string{inputPath})
	require.NoError(t, publishCmd.Execute())

	rotateCmd := publicRotateKeyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	require.NoError(t, rotateCmd.Execute())
	server.Close()
	require.NoError(t, store.Close())

	store = openTestPublicMirrorStore(t, fileSvc)
	keys, err := store.Keys(context.Background())
	require.NoError(t, err)
	require.Len(t, keys.Registry, 2)
	_, revoked := keys.Revoked[oldConfig.SourceID+":"+oldConfig.SigningKeyID]
	assert.True(t, revoked)

	restartedMirror, err := gateway.NewPublicMirrorServer(slog.Default(), store, gateway.PublicMirrorConfig{})
	require.NoError(t, err)
	restartedMirror.SetIngestAuthToken(hex.EncodeToString(tokenBytes))
	restartedServer := httptest.NewServer(restartedMirror.IngestHandler())
	t.Cleanup(restartedServer.Close)
	setCmd = publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	setCmd.SetArgs([]string{"--mirror-origin", restartedServer.URL})
	require.NoError(t, setCmd.Execute())
	input.RecordBytes = validPublicProjectionBytes("campaign-b")
	inputBytes, err = json.Marshal(input)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(inputPath, append(inputBytes, '\n'), constants.PermFilePrivate))
	publishCmd = publicPublishCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	publishCmd.SetArgs([]string{inputPath})
	require.NoError(t, publishCmd.Execute())

	oldFileSvc, _ := cmdtest.NewCmdTestEnv(t)
	oldConfig.MirrorOrigin = restartedServer.URL
	oldPublisher := gateway.NewPublicPublisherService(nil, oldFileSvc, slog.Default(), oldConfig, ed25519.PrivateKey(oldPrivateKeyBytes), oldConfig.SigningKeyID)
	oldPublisher.SetIngestAuthToken(hex.EncodeToString(tokenBytes))
	oldRecordBytes := validPublicProjectionBytes("old-key")
	oldRecordHash := sha256.Sum256([]byte(oldRecordBytes))
	err = oldPublisher.ExportBatch(context.Background(), []models.PublicFeedRecord{{
		Sequence:    1,
		RecordType:  models.PublicFeedRecordTypeProjection,
		RecordHash:  hex.EncodeToString(oldRecordHash[:]),
		RecordBytes: oldRecordBytes,
	}})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedMirrorRejected)
}

func TestPublicInitCmd_RollsBackPartialSecretStateWhenConfigurationWriteFails(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	_ = useTestKeystore(t, fileSvc)
	failingFileSvc := &publicFailOnceFileSvc{RuntimeFileService: fileSvc, path: constants.PublicFeedExportConfigPath, remaining: 1}
	cmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(failingFileSvc))
	cmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", "https://mirror.example"})

	err := cmd.Execute()
	require.Error(t, err)
	for _, relPath := range []string{constants.PublicFeedExportConfigPath, constants.PublicFeedSigningKeyPath, constants.PublicFeedIngestTokenPath} {
		exists, existsErr := fileSvc.FileExists(context.Background(), relPath)
		require.NoError(t, existsErr)
		assert.False(t, exists)
	}
}

func TestPublicRotateKeyCmd_RecoversAfterLocalConfigurationPersistenceFailure(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", "https://mirror.example"})
	require.NoError(t, initCmd.Execute())
	configBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedExportConfigPath)
	require.NoError(t, err)
	var oldConfig models.PublicExportConfig
	require.NoError(t, json.Unmarshal(configBytes, &oldConfig))
	privateKeyBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired)
	require.NoError(t, err)
	tokenBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
	require.NoError(t, err)
	store := openTestPublicMirrorStore(t, fileSvc)
	mirror, err := gateway.NewPublicMirrorServer(slog.Default(), store, gateway.PublicMirrorConfig{})
	require.NoError(t, err)
	mirror.SetIngestAuthToken(hex.EncodeToString(tokenBytes))
	require.NoError(t, mirror.RegisterSourceKey(context.Background(), oldConfig.SourceID, oldConfig.SigningKeyID, ed25519.PrivateKey(privateKeyBytes).Public().(ed25519.PublicKey)))
	server := httptest.NewServer(mirror.IngestHandler())
	t.Cleanup(server.Close)
	setCmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	setCmd.SetArgs([]string{"--mirror-origin", server.URL})
	require.NoError(t, setCmd.Execute())

	failingFileSvc := &publicFailOnceFileSvc{RuntimeFileService: fileSvc, path: constants.PublicFeedExportConfigPath, remaining: 1}
	rotateCmd := publicRotateKeyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(failingFileSvc))
	err = rotateCmd.Execute()
	require.Error(t, err)
	exists, err := fileSvc.FileExists(context.Background(), constants.PublicFeedKeyRotationPath)
	require.NoError(t, err)
	assert.True(t, exists)

	// Assert rotation state on disk has SealedNewPrivateKey and does not contain raw key hex
	rotationBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedKeyRotationPath)
	require.NoError(t, err)
	var rotationState models.PublicKeyRotationState
	require.NoError(t, json.Unmarshal(rotationBytes, &rotationState))
	assert.NotEmpty(t, rotationState.SealedNewPrivateKey)
	unsealedHex, err := ks.Decrypt(publicKeyRotationBinding(rotationState.NewKeyID), rotationState.SealedNewPrivateKey)
	require.NoError(t, err)
	assert.NotEmpty(t, unsealedHex)
	assert.NotContains(t, string(rotationBytes), unsealedHex)

	summary, found, err := store.SourceSummary(context.Background(), oldConfig.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, summary.BatchCount)
	keys, err := store.Keys(context.Background())
	require.NoError(t, err)
	_, revoked := keys.Revoked[oldConfig.SourceID+":"+oldConfig.SigningKeyID]
	assert.True(t, revoked)

	rotateCmd = publicRotateKeyCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	require.NoError(t, rotateCmd.Execute())
	exists, err = fileSvc.FileExists(context.Background(), constants.PublicFeedKeyRotationPath)
	require.NoError(t, err)
	assert.False(t, exists)
	summary, found, err = store.SourceSummary(context.Background(), oldConfig.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 1, summary.BatchCount)
}

func TestPublicCommands_RejectMalformedTrailingUnknownAndInvalidConfiguration(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	valid := models.DefaultPublicExportConfig()
	valid.Enabled = true
	valid.SourceID = "deployment-a"
	valid.MirrorOrigin = "https://mirror.example"
	valid.SigningKeyID = "key-a"
	validBytes, err := json.Marshal(valid)
	require.NoError(t, err)
	unknown := append([]byte(nil), validBytes[:len(validBytes)-1]...)
	unknown = append(unknown, []byte(`,"unexpected":true}`)...)
	invalid := valid
	invalid.BatchMaxRecords = 0
	invalidBytes, err := json.Marshal(invalid)
	require.NoError(t, err)
	tests := []struct {
		name string
		body []byte
	}{
		{name: "malformed JSON", body: []byte(`{`)},
		{name: "trailing JSON", body: append(append([]byte(nil), validBytes...), []byte(`}`)...)},
		{name: "unknown field", body: unknown},
		{name: "invalid limits", body: invalidBytes},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			require.NoError(t, fileSvc.WriteFile(context.Background(), constants.PublicFeedExportConfigPath, testCase.body, constants.PermFilePrivate))
			cmd := publicStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
			err := cmd.Execute()
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrPublicFeedConfigRequired)
		})
	}
}

func TestPublicPushCmd_RetriesDurableOutboxAfterCommandRestart(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	failedMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", failedMirror.URL})
	require.NoError(t, initCmd.Execute())
	configBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedExportConfigPath)
	require.NoError(t, err)
	var exportConfig models.PublicExportConfig
	require.NoError(t, json.Unmarshal(configBytes, &exportConfig))
	exportConfig.RetryMaxAttempts = 1
	require.NoError(t, gateway.WritePublicExportConfig(context.Background(), fileSvc, exportConfig))
	input := models.PublicFeedRecordInput{RecordType: models.PublicFeedRecordTypeProjection, RecordBytes: validPublicProjectionBytes("campaign-a")}
	inputBytes, err := json.Marshal(input)
	require.NoError(t, err)
	inputPath := filepath.Join(cfg.ProjectRoot, constants.TestPublicFeedRecordsFilename)
	require.NoError(t, os.WriteFile(inputPath, append(inputBytes, '\n'), constants.PermFilePrivate))
	publishCmd := publicPublishCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	publishCmd.SetArgs([]string{inputPath})
	require.Error(t, publishCmd.Execute())
	failedMirror.Close()

	privateKeyBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired)
	require.NoError(t, err)
	tokenBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
	require.NoError(t, err)
	store := openTestPublicMirrorStore(t, fileSvc)
	mirror, err := gateway.NewPublicMirrorServer(slog.Default(), store, gateway.PublicMirrorConfig{})
	require.NoError(t, err)
	mirror.SetIngestAuthToken(hex.EncodeToString(tokenBytes))
	require.NoError(t, mirror.RegisterSourceKey(context.Background(), exportConfig.SourceID, exportConfig.SigningKeyID, ed25519.PrivateKey(privateKeyBytes).Public().(ed25519.PublicKey)))
	server := httptest.NewServer(mirror.IngestHandler())
	t.Cleanup(server.Close)
	setCmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	setCmd.SetArgs([]string{"--mirror-origin", server.URL})
	require.NoError(t, setCmd.Execute())
	pushCmd := publicPushCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	require.NoError(t, pushCmd.Execute())
	summary, found, err := store.SourceSummary(context.Background(), exportConfig.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), summary.HighWaterSequence)
	assert.Equal(t, 1, summary.BatchCount)
}

func TestPublicPushCmd_AuthenticatesAndPublishesProofPackage(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", "https://mirror.example"})
	require.NoError(t, initCmd.Execute())
	exportConfig, err := gateway.ReadPublicExportConfig(context.Background(), fileSvc)
	require.NoError(t, err)
	privateKeyBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired)
	require.NoError(t, err)
	tokenBytes, err := gateway.ReadPublicSecret(context.Background(), fileSvc, ks, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
	require.NoError(t, err)
	store := openTestPublicMirrorStore(t, fileSvc)
	mirror, err := gateway.NewPublicMirrorServer(slog.Default(), store, gateway.PublicMirrorConfig{})
	require.NoError(t, err)
	mirror.SetIngestAuthToken(hex.EncodeToString(tokenBytes))
	require.NoError(t, mirror.RegisterSourceKey(context.Background(), exportConfig.SourceID, exportConfig.SigningKeyID, ed25519.PrivateKey(privateKeyBytes).Public().(ed25519.PublicKey)))
	server := httptest.NewServer(mirror.IngestHandler())
	t.Cleanup(server.Close)
	publicServer := httptest.NewServer(mirror.PublicHandler())
	t.Cleanup(publicServer.Close)
	exportConfig.MirrorOrigin = server.URL
	require.NoError(t, gateway.WritePublicExportConfig(context.Background(), fileSvc, exportConfig))
	pushCmd := publicPushCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	publisher, err := newPublicPublisherForCommand(pushCmd, fileSvc, exportConfig)
	require.NoError(t, err)
	_, err = publisher.BuildProofPackage(context.Background(), "campaign-a", "revision-a", "verified-index-hash-a", true, []gateway.ProofArtifactInput{{
		Filename:   "proof-a.json",
		MediaType:  "application/json",
		Content:    []byte(`{"campaign_id":"campaign-a"}`),
		CampaignID: "campaign-a",
	}})
	require.NoError(t, err)
	manifest, err := publisher.BuildProofPackage(context.Background(), "campaign-b", "revision-b", "verified-index-hash-b", true, []gateway.ProofArtifactInput{{
		Filename:   "proof-b.json",
		MediaType:  "application/json",
		Content:    []byte(`{"campaign_id":"campaign-b"}`),
		CampaignID: "campaign-b",
	}})
	require.NoError(t, err)
	require.NoError(t, pushCmd.Execute())
	stored, found, err := store.ProofManifest(context.Background(), exportConfig.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, manifest.ProofRootSHA256, stored.ProofRootSHA256)
	artifactCount, err := store.ProofArtifactCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, artifactCount)

	// Explicit internal exports remain verifiable without anonymous proof routes.
	segments, err := store.ProofSegments(t.Context(), exportConfig.SourceID)
	require.NoError(t, err)
	require.Len(t, segments, 2)
	var log bytes.Buffer
	for _, segment := range segments {
		require.NoError(t, json.NewEncoder(&log).Encode(segment))
	}
	const exportedLog = "downloaded-proof-segments.jsonl"
	require.NoError(t, fileSvc.WriteFile(t.Context(), exportedLog, log.Bytes(), constants.PermFilePrivate))
	server.Close()
	verify := publicVerifyProofCmd()
	var output bytes.Buffer
	verify.SetOut(&output)
	verify.SetArgs([]string{"--segments", fileSvc.Resolve(exportedLog), "--artifacts", fileSvc.Resolve(constants.PublicProofsDirname), "--public-key", hex.EncodeToString(ed25519.PrivateKey(privateKeyBytes).Public().(ed25519.PublicKey))})
	require.NoError(t, verify.Execute())
	assert.Contains(t, output.String(), "2 segments, 2 active artifacts")
}

// openTestPublicMirrorStore opens the durable mirror store under fileSvc and
// closes it when the test ends.
func openTestPublicMirrorStore(t *testing.T, fileSvc fs.RuntimeFileService) *gateway.PublicMirrorStore {
	t.Helper()
	store, err := gateway.OpenPublicMirrorStore(context.Background(), fileSvc, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPublicConfigSetCmd_UpdatesOnlyExplicitFields(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	initial := models.DefaultPublicExportConfig()
	initial.Enabled = true
	initial.SourceID = "deployment-a"
	initial.MirrorOrigin = "https://old.example"
	initial.SigningKeyID = "key-a"
	body, err := json.Marshal(initial)
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), constants.PublicFeedExportConfigPath, body, constants.PermFilePrivate))

	cmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{"--mirror-origin", "https://new.example"})
	require.NoError(t, cmd.Execute())

	updatedBytes, err := fileSvc.ReadFile(context.Background(), constants.PublicFeedExportConfigPath)
	require.NoError(t, err)
	var updated models.PublicExportConfig
	require.NoError(t, json.Unmarshal(updatedBytes, &updated))
	assert.Equal(t, "deployment-a", updated.SourceID)
	assert.Equal(t, "https://new.example", updated.MirrorOrigin)
	assert.Equal(t, "key-a", updated.SigningKeyID)
}

const (
	testBucketAccessKeyID = "access-key-value-0001"
	testBucketSecret      = "secret-value-0001"
	testBucketPurgeToken  = "purge-token-value-0001"
)

func testBucketCredentials() models.PublicBucketCredentials {
	return models.PublicBucketCredentials{AccessKeyID: testBucketAccessKeyID, SecretAccessKey: testBucketSecret, PurgeToken: testBucketPurgeToken}
}

func writeBucketTestFeedConfig(t *testing.T, fileSvc fs.RuntimeFileService, mirrorOrigin string) {
	t.Helper()
	exportConfig := models.DefaultPublicExportConfig()
	exportConfig.Enabled = true
	exportConfig.SourceID = "deployment-a"
	exportConfig.MirrorOrigin = mirrorOrigin
	exportConfig.SigningKeyID = "key-a"
	body, err := json.Marshal(exportConfig)
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), constants.PublicFeedExportConfigPath, body, constants.PermFilePrivate))
}

func writeBucketCredentialsFile(t *testing.T, credentials models.PublicBucketCredentials) string {
	t.Helper()
	data, err := json.Marshal(credentials)
	require.NoError(t, err)
	path := filepath.Join(testutil.TempDir(t), "bucket-credentials.json")
	require.NoError(t, os.WriteFile(path, data, constants.PermFilePrivate))
	return path
}

func fullBucketSetArgs(credentialsPath string) []string {
	return []string{
		"--bucket-name", "feed-bucket",
		"--bucket-endpoint", "https://acct.r2.example",
		"--bucket-public-origin", "https://feed.example",
		"--bucket-zone-id", "zone-a",
		"--bucket-credentials-file", credentialsPath,
	}
}

func bucketConfigOnDisk(t *testing.T, fileSvc fs.RuntimeFileService) models.PublicExportConfig {
	t.Helper()
	exportConfig, err := gateway.ReadPublicExportConfig(context.Background(), fileSvc)
	require.NoError(t, err)
	return exportConfig
}

func TestPublicConfigSetCmd_BucketFieldsPersistWithSealedCredentials(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	writeBucketTestFeedConfig(t, fileSvc, "http://127.0.0.1:8443")
	ctx := context.Background()

	var output bytes.Buffer
	cmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetOut(&output)
	cmd.SetArgs(fullBucketSetArgs(writeBucketCredentialsFile(t, testBucketCredentials())))
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "Public feed configuration updated\n", output.String())

	exportConfig := bucketConfigOnDisk(t, fileSvc)
	require.NotNil(t, exportConfig.Bucket)
	assert.Equal(t, models.PublicBucketConfig{Name: "feed-bucket", Endpoint: "https://acct.r2.example", PublicOrigin: "https://feed.example", ZoneID: "zone-a"}, *exportConfig.Bucket)
	assert.Equal(t, "http://127.0.0.1:8443", exportConfig.MirrorOrigin)

	credentials, err := gateway.ReadPublicBucketCredentials(ctx, fileSvc, ks)
	require.NoError(t, err)
	assert.Equal(t, testBucketCredentials(), credentials)

	raw, err := fileSvc.ReadFile(ctx, constants.PublicFeedBucketCredentialsPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), testBucketSecret)
	assert.NotContains(t, string(raw), testBucketPurgeToken)
	assert.NotContains(t, string(raw), testBucketAccessKeyID)

	// A later partial update overwrites only the flag given.
	partial := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	partial.SetArgs([]string{"--bucket-zone-id", "zone-b"})
	require.NoError(t, partial.Execute())
	exportConfig = bucketConfigOnDisk(t, fileSvc)
	require.NotNil(t, exportConfig.Bucket)
	assert.Equal(t, models.PublicBucketConfig{Name: "feed-bucket", Endpoint: "https://acct.r2.example", PublicOrigin: "https://feed.example", ZoneID: "zone-b"}, *exportConfig.Bucket)
}

func TestPublicConfigSetCmd_ReadsBucketCredentialsFromStdin(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	writeBucketTestFeedConfig(t, fileSvc, "http://127.0.0.1:8443")
	data, err := json.Marshal(testBucketCredentials())
	require.NoError(t, err)

	cmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetIn(strings.NewReader(string(data)))
	cmd.SetArgs(fullBucketSetArgs("-"))
	require.NoError(t, cmd.Execute())

	credentials, err := gateway.ReadPublicBucketCredentials(context.Background(), fileSvc, ks)
	require.NoError(t, err)
	assert.Equal(t, testBucketCredentials(), credentials)
}

func TestPublicConfigSetCmd_RefusesBucketOnSeparatelyHostedMirror(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	writeBucketTestFeedConfig(t, fileSvc, "https://mirror.example")
	ctx := context.Background()

	cmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs(fullBucketSetArgs(writeBucketCredentialsFile(t, testBucketCredentials())))

	require.ErrorIs(t, cmd.Execute(), constants.ErrPublicBucketRequiresLocalMirror)
	assert.Nil(t, bucketConfigOnDisk(t, fileSvc).Bucket)
	exists, err := fileSvc.FileExists(ctx, constants.PublicFeedBucketCredentialsPath)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestPublicConfigSetCmd_RefusesNonHTTPSBucketEndpointOnPublicHost(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	writeBucketTestFeedConfig(t, fileSvc, "http://127.0.0.1:8443")

	cmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs([]string{"--bucket-name", "feed-bucket", "--bucket-endpoint", "http://acct.r2.example", "--bucket-public-origin", "https://feed.example", "--bucket-zone-id", "zone-a"})

	require.ErrorIs(t, cmd.Execute(), constants.ErrPublicBucketConfigInvalid)
	assert.Nil(t, bucketConfigOnDisk(t, fileSvc).Bucket)
}

func TestPublicConfigSetCmd_RefusesBucketCredentialsMissingPurgeToken(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	writeBucketTestFeedConfig(t, fileSvc, "http://127.0.0.1:8443")
	credentials := testBucketCredentials()
	credentials.PurgeToken = ""

	cmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs(fullBucketSetArgs(writeBucketCredentialsFile(t, credentials)))

	require.ErrorIs(t, cmd.Execute(), constants.ErrPublicBucketCredentialsRequired)
	assert.Nil(t, bucketConfigOnDisk(t, fileSvc).Bucket)
}

func TestPublicConfigSetCmd_RefusesCredentialsWithoutKeystore(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	writeBucketTestFeedConfig(t, fileSvc, "http://127.0.0.1:8443")
	errKeystore := errors.New("keystore unavailable")
	original := openPublicKeystore
	openPublicKeystore = func(fs.RuntimeFileService) (*keystore.Keystore, error) { return nil, errKeystore }
	t.Cleanup(func() { openPublicKeystore = original })

	cmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetArgs(fullBucketSetArgs(writeBucketCredentialsFile(t, testBucketCredentials())))

	require.ErrorIs(t, cmd.Execute(), errKeystore)
	assert.Nil(t, bucketConfigOnDisk(t, fileSvc).Bucket)
}

func TestPublicConfigSetCmd_BucketRemoveClearsConfigAndKeepsCredentials(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	ks := useTestKeystore(t, fileSvc)
	writeBucketTestFeedConfig(t, fileSvc, "http://127.0.0.1:8443")
	setCmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	setCmd.SetArgs(fullBucketSetArgs(writeBucketCredentialsFile(t, testBucketCredentials())))
	require.NoError(t, setCmd.Execute())

	removeCmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	removeCmd.SetArgs([]string{"--bucket-remove"})
	require.NoError(t, removeCmd.Execute())

	assert.Nil(t, bucketConfigOnDisk(t, fileSvc).Bucket)
	credentials, err := gateway.ReadPublicBucketCredentials(context.Background(), fileSvc, ks)
	require.NoError(t, err)
	assert.Equal(t, testBucketCredentials(), credentials)
}

func TestPublicConfigSetCmd_BucketRemoveRejectsCombinedBucketFlags(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	writeBucketTestFeedConfig(t, fileSvc, "http://127.0.0.1:8443")
	setCmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	setCmd.SetArgs([]string{"--bucket-name", "feed-bucket", "--bucket-endpoint", "https://acct.r2.example", "--bucket-public-origin", "https://feed.example", "--bucket-zone-id", "zone-a"})
	require.NoError(t, setCmd.Execute())

	for _, args := range [][]string{
		{"--bucket-remove", "--bucket-zone-id", "zone-b"},
		{"--bucket-remove", "--bucket-credentials-file", writeBucketCredentialsFile(t, testBucketCredentials())},
	} {
		removeCmd := publicConfigSetCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
		removeCmd.SetArgs(args)
		require.ErrorIs(t, removeCmd.Execute(), constants.ErrValidationFailed)
	}
	require.NotNil(t, bucketConfigOnDisk(t, fileSvc).Bucket)
}

// Each row of a stored verification report against the feed's verifier state. Plain
// verify stays local by design, so disagreement has to be visible in the status.
func TestPublicStatusCmd_ReportsFeedDriftForEachDisagreement(t *testing.T) {
	const pass, fail = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	rows := []struct {
		runID string
		local evalv1.EvaluationVerdictStatus
		feed  string
		want  string
	}{
		{runID: "run-agree-pass", local: pass, feed: "passed"},
		{runID: "run-agree-fail", local: fail, feed: "failed"},
		{runID: "run-no-report", feed: "not_run"},
		{runID: "run-pass-not-run", local: pass, feed: "not_run", want: "verified"},
		{runID: "run-pass-failed", local: pass, feed: "failed", want: "verified"},
		{runID: "run-fail-passed", local: fail, feed: "passed", want: "verify_failed"},
		{runID: "run-fail-not-run", local: fail, feed: "not_run", want: "verify_failed"},
	}
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	store := evaluation.NewStore(fileSvc)
	bootstrap := models.PublicFeedBootstrap{}
	var want []models.PublicFeedVerificationDrift
	for _, row := range rows {
		require.NoError(t, store.SaveRun(t.Context(), &evalv1.EvaluationRun{SchemaVersion: evaluation.CampaignSchemaVersion, RunId: row.runID, CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: "campaign-a"}}))
		if row.local != evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSPECIFIED {
			require.NoError(t, store.SaveCampaignVerification(t.Context(), row.runID, &evalv1.EvaluationVerificationReport{RunId: row.runID, Status: row.local}))
		}
		bootstrap.RecentProjections = append(bootstrap.RecentProjections, models.NewPublicFeedObject(map[string]string{"kind": "evaluation_summary", "run_id": row.runID, "verifier_state": row.feed}))
		if row.want != "" {
			want = append(want, models.PublicFeedVerificationDrift{RunID: row.runID, LocalStatus: row.want, FeedVerifierState: row.feed})
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].RunID < want[j].RunID })
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(bootstrap)
	}))
	t.Cleanup(mirror.Close)
	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", mirror.URL})
	require.NoError(t, initCmd.Execute())

	var stdout, stderr bytes.Buffer
	cmd := publicStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--mirror-origin", mirror.URL})
	require.NoError(t, cmd.Execute())
	var status models.PublicPublisherStatus
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &status))
	assert.True(t, status.FeedDriftChecked)
	assert.Equal(t, want, status.FeedDrift)
	assert.Contains(t, stderr.String(), "run run-fail-passed is verify_failed locally but the feed says passed")
	assert.NotContains(t, stderr.String(), "run-agree-pass")

	// A mirror that cannot be read must not look like agreement.
	mirror.Close()
	stdout.Reset()
	stderr.Reset()
	require.NoError(t, cmd.Execute())
	status = models.PublicPublisherStatus{}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &status))
	assert.False(t, status.FeedDriftChecked)
	assert.Empty(t, status.FeedDrift)
	assert.Contains(t, stderr.String(), "feed drift not checked")
}

// statusFeedEnv initializes a local feed with an optional bucket and returns the
// runtime, and the mirror origin that answers feed drift with no runs.
func statusFeedEnv(t *testing.T, bucket *models.PublicBucketConfig) (fs.RuntimeFileService, *config.Config, string) {
	t.Helper()
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	useTestKeystore(t, fileSvc)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(models.PublicFeedBootstrap{})
	}))
	t.Cleanup(mirror.Close)
	initCmd := publicInitCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	initCmd.SetArgs([]string{"--source-id", "deployment-a", "--mirror-origin", mirror.URL})
	require.NoError(t, initCmd.Execute())
	if bucket != nil {
		exportConfig, err := gateway.ReadPublicExportConfig(t.Context(), fileSvc)
		require.NoError(t, err)
		exportConfig.Bucket = bucket
		require.NoError(t, gateway.WritePublicExportConfig(t.Context(), fileSvc, exportConfig))
	}
	return fileSvc, cfg, mirror.URL
}

// statusBucket is the bucket the status tests configure.
func statusBucket() *models.PublicBucketConfig {
	return &models.PublicBucketConfig{Name: "feed-bucket", Endpoint: "https://acct.r2.example", PublicOrigin: "https://feed.example", ZoneID: "zone-a"}
}

// writeStatusBucketJournal writes journal lines as the bucket publisher leaves them.
func writeStatusBucketJournal(t *testing.T, fileSvc fs.RuntimeFileService, entries ...models.PublicBucketJournalEntry) {
	t.Helper()
	var data []byte
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		require.NoError(t, err)
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	require.NoError(t, fileSvc.WriteFile(t.Context(), constants.PublicFeedBucketJournalPath, data, constants.PermFilePrivate))
}

// runStatusForBucket runs ztd public status against the feed and returns its
// stdout and stderr.
func runStatusForBucket(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config, mirrorOrigin string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := publicStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--mirror-origin", mirrorOrigin})
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// TestPublicStatusCmd_ReportsBucketPointerAndPendingWork covers both outputs: the
// bucket object in the JSON status and the text line on stderr. The mirror has no
// records here, so the lag is floored at zero.
func TestPublicStatusCmd_ReportsBucketPointerAndPendingWork(t *testing.T) {
	fileSvc, cfg, mirrorOrigin := statusFeedEnv(t, statusBucket())
	writeStatusBucketJournal(t, fileSvc,
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPointer, Revision: 7, Sequence: 3},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: "proofs/a"},
		models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurge, Key: "https://feed.example/pointer.json"},
	)

	stdout, stderr, err := runStatusForBucket(t, fileSvc, cfg, mirrorOrigin)
	require.NoError(t, err)
	var status models.PublicPublisherStatus
	require.NoError(t, json.Unmarshal([]byte(stdout), &status))
	assert.Equal(t, &models.PublicBucketStatus{Name: "feed-bucket", PointerRevision: 7, PointerSequence: 3, PendingDeletes: 1, PendingPurges: 1}, status.Bucket)
	assert.Contains(t, stderr, "Bucket: feed-bucket pointer revision 7 at sequence 3, 0 behind, 1 deletes and 1 purges pending\n")
}

// TestPublicStatusCmd_OmitsBucketWhenOff keeps the status output unchanged for a
// feed with no bucket: no bucket key in the JSON and no bucket line on stderr.
func TestPublicStatusCmd_OmitsBucketWhenOff(t *testing.T) {
	fileSvc, cfg, mirrorOrigin := statusFeedEnv(t, nil)

	stdout, stderr, err := runStatusForBucket(t, fileSvc, cfg, mirrorOrigin)
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(stdout), &raw))
	_, present := raw["bucket"]
	assert.False(t, present, "a feed without a bucket reports no bucket object")
	assert.NotContains(t, stderr, "Bucket:")
}

// TestPublicStatusCmd_UnreadableBucketJournalWarnsWithoutFailing reports a journal
// that cannot be read as a warning, and the status still prints.
func TestPublicStatusCmd_UnreadableBucketJournalWarnsWithoutFailing(t *testing.T) {
	fileSvc, cfg, mirrorOrigin := statusFeedEnv(t, statusBucket())
	require.NoError(t, fileSvc.WriteFile(t.Context(), constants.PublicFeedBucketJournalPath, []byte("not json\n"), constants.PermFilePrivate))

	stdout, stderr, err := runStatusForBucket(t, fileSvc, cfg, mirrorOrigin)
	require.NoError(t, err, "a bucket read failure must not fail the status command")
	assert.Contains(t, stderr, "warning: bucket status not read")
	var status models.PublicPublisherStatus
	require.NoError(t, json.Unmarshal([]byte(stdout), &status))
	assert.Nil(t, status.Bucket)
}
