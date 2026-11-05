// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newBucketConfigTestFileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	return fileSvc
}

func bucketConfigTestExport(mirrorOrigin string, bucket *models.PublicBucketConfig) models.PublicExportConfig {
	exportConfig := models.DefaultPublicExportConfig()
	exportConfig.Enabled = true
	exportConfig.SourceID = "deployment-a"
	exportConfig.SigningKeyID = "key-a"
	exportConfig.MirrorOrigin = mirrorOrigin
	exportConfig.Bucket = bucket
	return exportConfig
}

func validBucketConfigForTest() models.PublicBucketConfig {
	return models.PublicBucketConfig{
		Name:         "feed-bucket",
		Endpoint:     "https://acct.r2.example",
		PublicOrigin: "https://feed.example",
		ZoneID:       "zone-a",
	}
}

func validBucketCredentialsForTest() models.PublicBucketCredentials {
	return models.PublicBucketCredentials{
		AccessKeyID:     "access-key-value-0001",
		SecretAccessKey: "secret-value-0001",
		PurgeToken:      "purge-token-value-0001",
	}
}

func TestPublicExportConfig_WithoutBucketStillLoadsAndValidates(t *testing.T) {
	fileSvc := newBucketConfigTestFileSvc(t)
	ctx := context.Background()
	exportConfig := bucketConfigTestExport("https://mirror.example", nil)

	require.NoError(t, ValidatePublicExportConfig(exportConfig))
	require.NoError(t, WritePublicExportConfig(ctx, fileSvc, exportConfig))
	loaded, err := ReadPublicExportConfig(ctx, fileSvc)
	require.NoError(t, err)
	assert.Nil(t, loaded.Bucket)
}

func TestPublicExportConfig_RefusesBucketOnSeparatelyHostedMirror(t *testing.T) {
	bucket := validBucketConfigForTest()

	err := ValidatePublicExportConfig(bucketConfigTestExport("https://mirror.example", &bucket))

	require.ErrorIs(t, err, constants.ErrPublicBucketRequiresLocalMirror)
}

func TestPublicExportConfig_AcceptsBucketOnLoopbackMirror(t *testing.T) {
	bucket := validBucketConfigForTest()

	require.NoError(t, ValidatePublicExportConfig(bucketConfigTestExport("http://127.0.0.1:8443", &bucket)))
}

func TestValidatePublicBucketConfig_AcceptsBareOriginsAndLoopbackHTTP(t *testing.T) {
	bucket := models.PublicBucketConfig{
		Name:         "feed-bucket",
		Endpoint:     "http://127.0.0.1:9000",
		PublicOrigin: "http://localhost:8080",
		ZoneID:       "zone-a",
	}

	require.NoError(t, ValidatePublicBucketConfig(bucket))
}

func TestValidatePublicBucketConfig_RejectsInvalidFieldsWithTypedError(t *testing.T) {
	cases := map[string]func(*models.PublicBucketConfig){
		"missing name":                 func(b *models.PublicBucketConfig) { b.Name = "" },
		"missing endpoint":             func(b *models.PublicBucketConfig) { b.Endpoint = "" },
		"missing public origin":        func(b *models.PublicBucketConfig) { b.PublicOrigin = "" },
		"missing zone id":              func(b *models.PublicBucketConfig) { b.ZoneID = " " },
		"endpoint with path":           func(b *models.PublicBucketConfig) { b.Endpoint = "https://acct.r2.example/bucket" },
		"endpoint with trailing slash": func(b *models.PublicBucketConfig) { b.Endpoint = "https://acct.r2.example/" },
		"endpoint with query":          func(b *models.PublicBucketConfig) { b.Endpoint = "https://acct.r2.example?x=1" },
		"endpoint with fragment":       func(b *models.PublicBucketConfig) { b.Endpoint = "https://acct.r2.example#x" },
		"endpoint with user info":      func(b *models.PublicBucketConfig) { b.Endpoint = "https://user:pass@acct.r2.example" },
		"public origin with path":      func(b *models.PublicBucketConfig) { b.PublicOrigin = "https://feed.example/feed" },
		"http endpoint on public host": func(b *models.PublicBucketConfig) { b.Endpoint = "http://acct.r2.example" },
		"http public origin on public": func(b *models.PublicBucketConfig) { b.PublicOrigin = "http://feed.example" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bucket := validBucketConfigForTest()
			mutate(&bucket)

			require.ErrorIs(t, ValidatePublicBucketConfig(bucket), constants.ErrPublicBucketConfigInvalid)
		})
	}
}

func TestPublicBucketCredentials_RoundTripThroughSealedFile(t *testing.T) {
	fileSvc := newBucketConfigTestFileSvc(t)
	ks := newTestKeystore(t, fileSvc, testutil.NewTestLogger())
	ctx := context.Background()
	credentials := validBucketCredentialsForTest()

	require.NoError(t, WritePublicBucketCredentials(ctx, fileSvc, ks, credentials))

	loaded, err := ReadPublicBucketCredentials(ctx, fileSvc, ks)
	require.NoError(t, err)
	assert.Equal(t, credentials, loaded)

	raw, err := fileSvc.ReadFile(ctx, constants.PublicFeedBucketCredentialsPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), credentials.SecretAccessKey)
	assert.NotContains(t, string(raw), credentials.PurgeToken)
	assert.NotContains(t, string(raw), credentials.AccessKeyID)
}

func TestPublicBucketCredentials_RequireEveryField(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*models.PublicBucketCredentials){
		"missing access key id":     func(c *models.PublicBucketCredentials) { c.AccessKeyID = "" },
		"missing secret access key": func(c *models.PublicBucketCredentials) { c.SecretAccessKey = "" },
		"missing purge token":       func(c *models.PublicBucketCredentials) { c.PurgeToken = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fileSvc := newBucketConfigTestFileSvc(t)
			ks := newTestKeystore(t, fileSvc, testutil.NewTestLogger())
			credentials := validBucketCredentialsForTest()
			mutate(&credentials)

			require.ErrorIs(t, WritePublicBucketCredentials(ctx, fileSvc, ks, credentials), constants.ErrPublicBucketCredentialsRequired)
			exists, err := fileSvc.FileExists(ctx, constants.PublicFeedBucketCredentialsPath)
			require.NoError(t, err)
			assert.False(t, exists)
		})
	}

	t.Run("stored credentials missing purge token", func(t *testing.T) {
		fileSvc := newBucketConfigTestFileSvc(t)
		ks := newTestKeystore(t, fileSvc, testutil.NewTestLogger())
		data, err := json.Marshal(models.PublicBucketCredentials{AccessKeyID: "a", SecretAccessKey: "s"})
		require.NoError(t, err)
		require.NoError(t, WritePublicSecret(ctx, fileSvc, ks, constants.PublicFeedBucketCredentialsPath, data))

		_, err = ReadPublicBucketCredentials(ctx, fileSvc, ks)
		require.ErrorIs(t, err, constants.ErrPublicBucketCredentialsRequired)
	})

	t.Run("read without stored credentials", func(t *testing.T) {
		fileSvc := newBucketConfigTestFileSvc(t)
		ks := newTestKeystore(t, fileSvc, testutil.NewTestLogger())

		_, err := ReadPublicBucketCredentials(ctx, fileSvc, ks)
		require.ErrorIs(t, err, constants.ErrPublicBucketCredentialsRequired)
	})
}
