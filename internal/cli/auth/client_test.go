// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"crypto/elliptic"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"os"
	"runtime"
	"testing"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestGenerateCSR(t *testing.T) {
	tests := []struct {
		name       string
		commonName string
		wantErr    bool
	}{
		{
			name:       "valid CSR generation",
			commonName: "test-operator",
			wantErr:    false,
		},
		{
			name:       "CSR with special characters",
			commonName: "test-operator-123",
			wantErr:    false,
		},
		{
			name:       "CSR with domain-style name",
			commonName: "operator.example.com",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csrPEM, privKey, err := GenerateCSR(tt.commonName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateCSR() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if csrPEM == "" {
					t.Error("GenerateCSR() returned empty CSR PEM")
				}

				if privKey == nil {
					t.Error("GenerateCSR() returned nil private key")
					return
				}

				// Verify CSR PEM format
				block, _ := pem.Decode([]byte(csrPEM))
				if block == nil {
					t.Error("Generated CSR is not valid PEM")
					return
				}
				if block.Type != "CERTIFICATE REQUEST" {
					t.Errorf("CSR PEM block type is %s, want CERTIFICATE REQUEST", block.Type)
				}

				// Verify private key is ECDSA P-256
				if privKey.Curve != elliptic.P256() {
					t.Errorf("Private key curve is %v, want P-256", privKey.Curve)
				}
			}
		})
	}
}

func TestSaveCredentials(t *testing.T) {
	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")

	cfg := &config.Config{}
	cfg.RuntimeDir = runtimeDir

	creds := &Credentials{
		OperatorSessionID: "test-session-id",
		UserID:            "test-user-id",
		OperatorID:        "test-operator-id",
		CLISessionID:      "test-cli-session-id",
	}

	err := SaveCredentials(fileSvc, cfg, creds)
	if err != nil {
		t.Fatalf("SaveCredentials() failed: %v", err)
	}

	// Verify file was created using the config's CredentialsFile method
	credsFile := cfg.CredentialsFile()
	data, err := os.ReadFile(credsFile)
	if err != nil {
		t.Fatalf("Failed to read credentials file: %v", err)
	}

	// Verify file permissions (should be PermFilePrivate)
	info, err := os.Stat(credsFile)
	if err != nil {
		t.Fatalf("Failed to stat credentials file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != constants.PermFilePrivate {
		t.Errorf("Credentials file permissions = %v, want %v", info.Mode().Perm(), constants.PermFilePrivate)
	}

	// Verify content
	var loadedCreds Credentials
	if err := json.Unmarshal(data, &loadedCreds); err != nil {
		t.Fatalf("Failed to unmarshal credentials: %v", err)
	}

	if loadedCreds.OperatorSessionID != creds.OperatorSessionID {
		t.Errorf("OperatorSessionID = %v, want %v", loadedCreds.OperatorSessionID, creds.OperatorSessionID)
	}
	if loadedCreds.UserID != creds.UserID {
		t.Errorf("UserID = %v, want %v", loadedCreds.UserID, creds.UserID)
	}
}

func TestLoadCredentials(t *testing.T) {
	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")

	cfg := &config.Config{}
	cfg.RuntimeDir = runtimeDir

	t.Run("non-existent file returns nil", func(t *testing.T) {
		creds, err := LoadCredentials(fileSvc, cfg)
		if err != nil {
			t.Errorf("LoadCredentials() error = %v, want nil", err)
		}
		if creds != nil {
			t.Error("LoadCredentials() returned non-nil for non-existent file")
		}
	})

	t.Run("load existing credentials", func(t *testing.T) {
		creds := &Credentials{
			OperatorSessionID: "test-session-id",
			UserID:            "test-user-id",
			OperatorID:        "test-operator-id",
			CLISessionID:      "test-cli-session-id",
		}

		if err := SaveCredentials(fileSvc, cfg, creds); err != nil {
			t.Fatalf("Failed to save credentials: %v", err)
		}

		loaded, err := LoadCredentials(fileSvc, cfg)
		if err != nil {
			t.Fatalf("LoadCredentials() failed: %v", err)
		}

		if loaded.OperatorSessionID != creds.OperatorSessionID {
			t.Errorf("OperatorSessionID = %v, want %v", loaded.OperatorSessionID, creds.OperatorSessionID)
		}
		if loaded.UserID != creds.UserID {
			t.Errorf("UserID = %v, want %v", loaded.UserID, creds.UserID)
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		credsFile := cfg.CredentialsFile()
		credsRel, err := fileSvc.RelFromAbs(credsFile)
		if err != nil {
			t.Fatalf("Failed to get relative path: %v", err)
		}
		if err := fileSvc.WriteFile(context.Background(), credsRel, []byte("invalid json"), constants.PermFilePrivate); err != nil {
			t.Fatalf("Failed to write invalid JSON: %v", err)
		}

		_, err = LoadCredentials(fileSvc, cfg)
		if err == nil {
			t.Error("LoadCredentials() should return error for invalid JSON")
		}
	})
}

// Helper functions

func newAuthTestFileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	baseDir := testutil.TempDir(t)
	svc, err := fs.NewRuntimeFileService(baseDir, slog.Default())
	if err != nil {
		t.Fatalf("Failed to create file service: %v", err)
	}
	if err := svc.CreateRuntimeTree(context.Background()); err != nil {
		t.Fatalf("Failed to create runtime tree: %v", err)
	}
	return svc
}

func newAuthTestEnv(t *testing.T) (fs.RuntimeFileService, *config.Config) {
	t.Helper()
	fileSvc := newAuthTestFileSvc(t)
	runtimeDir := fileSvc.Resolve("")
	cfg := &config.Config{
		ProjectRoot: runtimeDir,
		RuntimeDir:  runtimeDir,
		Paths:       &config.PathsConfig{Host: "localhost"},
		Keyring:     keystoretest.NewMemoryKeyring(),
	}
	return fileSvc, cfg
}
