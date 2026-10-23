// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
)

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name: "minimal config",
			cfg: config.Config{
				Auth: config.Auth{},
			},
			wantErr: false,
		},
		{
			name: "missing CA bundle fails closed",
			cfg: config.Config{
				Auth: config.Auth{
					CABundle: "nonexistent.pem",
				},
			},
			wantErr: true,
		},
		{
			name: "missing client cert/key fails closed",
			cfg: config.Config{
				Auth: config.Auth{
					ClientCert: "nonexistent.crt",
					ClientKey:  "nonexistent.key",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := New(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && client == nil {
				t.Error("New() returned nil client")
			}
		})
	}
}

func TestNew_WithValidCerts(t *testing.T) {
	t.Parallel()
	caPEM, _ := testutil.GenerateTestCertificate(t, "test-ca")
	caPath := filepath.Join(testutil.TempDir(t), "ca.pem")
	if err := os.WriteFile(caPath, []byte(caPEM), 0600); err != nil {
		t.Fatalf("failed to write CA: %v", err)
	}
	identity := writeIdentity(t, "client")

	cfg := config.Config{
		Auth: config.Auth{
			CABundle:   caPath,
			ClientCert: identity.ClientCert,
			ClientKey:  identity.ClientKey,
		},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if client == nil {
		t.Fatal("New() returned nil client")
	}
}

// writeIdentity writes a generated client certificate and key as PEM files in a fresh
// test directory and returns the Auth that names them. Tests use real files because New
// fails closed on a configured path it cannot load.
func writeIdentity(t *testing.T, name string) config.Auth {
	t.Helper()
	dir := testutil.TempDir(t)
	certPEM, keyPEM := testutil.GenerateTestCertificate(t, name)
	auth := config.Auth{
		ClientCert: filepath.Join(dir, name+".crt"),
		ClientKey:  filepath.Join(dir, name+".key"),
	}
	require.NoError(t, os.WriteFile(auth.ClientCert, []byte(certPEM), 0600))
	require.NoError(t, os.WriteFile(auth.ClientKey, []byte(keyPEM), 0600))
	return auth
}

// TestNew_FailsClosedOnConfiguredMaterial pins the rule that material the config names but
// cannot be loaded is an error, never a client that silently runs without its identity or
// trust anchor.
func TestNew_FailsClosedOnConfiguredMaterial(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	identity := writeIdentity(t, "valid")
	noPEM := filepath.Join(dir, "no-certs.pem")
	require.NoError(t, os.WriteFile(noPEM, []byte("not a certificate"), 0600))

	t.Run("missing client certificate file", func(t *testing.T) {
		t.Parallel()
		_, err := New(config.Config{Auth: config.Auth{
			ClientCert: filepath.Join(dir, "missing.crt"),
			ClientKey:  identity.ClientKey,
		}})
		require.ErrorIs(t, err, constants.ErrFailedToLoadClientCertificate)
		require.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("missing CLI client certificate file", func(t *testing.T) {
		t.Parallel()
		_, err := New(config.Config{CLIAuth: config.Auth{
			ClientCert: filepath.Join(dir, "missing-cli.crt"),
			ClientKey:  identity.ClientKey,
		}})
		require.ErrorIs(t, err, constants.ErrFailedToLoadClientCertificate)
		require.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("unreadable CA bundle", func(t *testing.T) {
		t.Parallel()
		_, err := New(config.Config{Auth: config.Auth{CABundle: filepath.Join(dir, "missing-ca.pem")}})
		require.ErrorIs(t, err, constants.ErrFailedToReadTrustBundle)
		require.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("CA bundle with no PEM certificates", func(t *testing.T) {
		t.Parallel()
		_, err := New(config.Config{Auth: config.Auth{CABundle: noPEM}})
		require.ErrorIs(t, err, constants.ErrFailedToParseTrustBundle)
	})

	t.Run("certificate named without its key", func(t *testing.T) {
		t.Parallel()
		_, err := New(config.Config{Auth: config.Auth{ClientCert: identity.ClientCert}})
		require.ErrorIs(t, err, constants.ErrIncompleteCredentialPair)
	})
}

func TestNew_InvalidKeyPair(t *testing.T) {
	t.Parallel()
	tempDir := testutil.TempDir(t)
	certPath := filepath.Join(tempDir, "client.crt")
	keyPath := filepath.Join(tempDir, "client.key")

	// Write invalid cert/key pair
	if err := os.WriteFile(certPath, []byte("invalid cert"), 0600); err != nil {
		t.Fatalf("failed to write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("invalid key"), 0600); err != nil {
		t.Fatalf("failed to write key: %v", err)
	}

	cfg := config.Config{
		Auth: config.Auth{
			ClientCert: certPath,
			ClientKey:  keyPath,
		},
	}

	_, err := New(cfg)
	if err == nil {
		t.Error("New() should fail with invalid cert/key pair")
	}
}

func TestClient_Config(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		MTLSBaseURL: "https://example.com",
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	retrievedCfg := client.Config()
	if retrievedCfg.MTLSBaseURL != cfg.MTLSBaseURL {
		t.Errorf("Config() returned different MTLSBaseURL")
	}
}

func TestClient_Record(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Auth: config.Auth{}}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	var exchanges []Exchange
	client.Record(&exchanges)

	if client.rec == nil {
		t.Error("Record() did not set rec")
	}
	if client.rec != &exchanges {
		t.Error("Record() did not set rec to the provided sink")
	}
}

func TestAttachBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    []byte
		wantJSON bool
		wantRaw  bool
	}{
		{
			name:     "nil input",
			input:    nil,
			wantJSON: false,
			wantRaw:  false,
		},
		{
			name:     "empty input",
			input:    []byte{},
			wantJSON: false,
			wantRaw:  false,
		},
		{
			name:     "valid JSON",
			input:    []byte(`{"test": "value"}`),
			wantJSON: true,
			wantRaw:  false,
		},
		{
			name:     "invalid JSON",
			input:    []byte(`not json`),
			wantJSON: false,
			wantRaw:  true,
		},
		{
			name:     "plain text",
			input:    []byte(`plain text response`),
			wantJSON: false,
			wantRaw:  true,
		},
		{
			name:     "JSON array",
			input:    []byte(`[1, 2, 3]`),
			wantJSON: true,
			wantRaw:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var j json.RawMessage
			var raw string

			attachBody(&j, &raw, tt.input)

			hasJSON := len(j) > 0
			hasRaw := raw != ""

			if hasJSON != tt.wantJSON {
				t.Errorf("attachBody() JSON = %v, want %v", hasJSON, tt.wantJSON)
			}
			if hasRaw != tt.wantRaw {
				t.Errorf("attachBody() raw = %v, want %v", hasRaw, tt.wantRaw)
			}
		})
	}
}

func TestExchange_Marshal(t *testing.T) {
	t.Parallel()
	ex := Exchange{
		Persona:   "test-client",
		Method:    "GET",
		URL:       "http://example.com/test",
		ReqBody:   json.RawMessage(`{"test": "request"}`),
		Status:    200,
		RespBody:  json.RawMessage(`{"result": "ok"}`),
		LatencyMS: 100,
		At:        time.Now(),
	}

	data, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("failed to marshal Exchange: %v", err)
	}

	var decoded Exchange
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Exchange: %v", err)
	}

	if decoded.Persona != ex.Persona {
		t.Errorf("Persona mismatch: got %s, want %s", decoded.Persona, ex.Persona)
	}
	if decoded.Method != ex.Method {
		t.Errorf("Method mismatch: got %s, want %s", decoded.Method, ex.Method)
	}
	if decoded.Status != ex.Status {
		t.Errorf("Status mismatch: got %d, want %d", decoded.Status, ex.Status)
	}
}

func TestPersona(t *testing.T) {
	t.Parallel()
	p := Persona{
		ID:                "test-id",
		UserAgent:         "TestAgent/1.0",
		OperatorSessionID: "session-123",
		CLISessionID:      "cli-session-123",
		UserID:            "user-123",
		OperatorID:        "operator-123",
	}

	if p.ID != "test-id" {
		t.Errorf("ID mismatch")
	}
	if p.UserAgent != "TestAgent/1.0" {
		t.Errorf("UserAgent mismatch")
	}
	if p.OperatorSessionID != "session-123" {
		t.Errorf("OperatorSessionID mismatch")
	}
	if p.CLISessionID != "cli-session-123" {
		t.Errorf("CLISessionID mismatch")
	}
	if p.UserID != "user-123" {
		t.Errorf("UserID mismatch")
	}
	if p.OperatorID != "operator-123" {
		t.Errorf("OperatorID mismatch")
	}
}

func TestNew_CLIAuthBuildsSecondClient(t *testing.T) {
	t.Parallel()
	t.Run("cliHTTP is nil when CLIAuth equals Auth", func(t *testing.T) {
		t.Parallel()
		cfg := config.Config{Auth: config.Auth{}}
		c, err := New(cfg)
		require.NoError(t, err)
		assert.Nil(t, c.cliHTTP, "cliHTTP should be nil when CLIAuth is not set")
		assert.Nil(t, c.cliTLS, "cliTLS should be nil when CLIAuth is not set")
	})

	t.Run("the CLI identity is the Operator client when CLIAuth matches Auth", func(t *testing.T) {
		t.Parallel()
		same := writeIdentity(t, "same")
		c, err := New(config.Config{Auth: same, CLIAuth: same})
		require.NoError(t, err)
		assert.Same(t, c.http, c.cliHTTP, "the CLI identity is the Operator client")
		assert.Same(t, c.http.Transport.(*http.Transport).TLSClientConfig, c.cliTLS, "the CLI identity shares the Operator TLS config")
	})

	t.Run("cliHTTP is a separate client when CLIAuth differs from Auth", func(t *testing.T) {
		t.Parallel()
		c, err := New(config.Config{Auth: writeIdentity(t, "operator"), CLIAuth: writeIdentity(t, "cli")})
		require.NoError(t, err)
		assert.NotNil(t, c.cliHTTP, "cliHTTP should be built when CLIAuth differs from Auth")
		assert.NotNil(t, c.cliTLS, "cliTLS should be built when CLIAuth differs from Auth")
		assert.NotSame(t, c.http, c.cliHTTP, "the Operator identity is not the CLI identity when they differ")
	})

	t.Run("the CLI identity keeps its own trust anchors when only its CA bundle differs", func(t *testing.T) {
		t.Parallel()
		operator := writeIdentity(t, "shared")
		cli := operator
		cli.CABundle = operator.ClientCert
		c, err := New(config.Config{Auth: operator, CLIAuth: cli})
		require.NoError(t, err)
		assert.NotSame(t, c.http, c.cliHTTP, "a pinned CLI bundle is not replaced by the Operator trust anchors")
		require.NotNil(t, c.cliTLS)
		assert.NotNil(t, c.cliTLS.RootCAs, "the CLI client verifies the Gateway against its configured bundle")
	})
}

func TestClient_SubscribeApprovalsRequiresCLISession(t *testing.T) {
	t.Parallel()
	c, err := New(config.Config{
		PublicBaseURL: "https://127.0.0.1:1",
		Auth:          writeIdentity(t, "operator"),
		CLIAuth:       writeIdentity(t, "cli"),
	})
	require.NoError(t, err)

	_, err = c.SubscribeApprovals(context.Background(), Persona{ID: "test"})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}
