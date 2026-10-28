// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// httpTimeout is the default timeout for all HTTP clients in the auth package.
const httpTimeout = 30 * time.Second

// ReadTrustBundle reads the trust bundle using fileSvc for the default runtime
// path, or os.ReadFile for a custom external path.
func ReadTrustBundle(fileSvc fs.RuntimeFileService, cfg *config.Config) ([]byte, error) {
	if custom := cfg.CustomTrustBundlePath(); custom != "" {
		return os.ReadFile(custom)
	}
	return fileSvc.ReadFile(context.Background(), cfg.DefaultTrustBundleRelPath())
}

// WriteTrustBundleFS writes the trust bundle using fileSvc for the default runtime
// path, or os.* for a custom external path.
func WriteTrustBundleFS(fileSvc fs.RuntimeFileService, cfg *config.Config, data []byte, mode os.FileMode) error {
	if custom := cfg.CustomTrustBundlePath(); custom != "" {
		if err := os.MkdirAll(filepath.Dir(custom), constants.PermDirPrivate); err != nil {
			return fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
		}
		if err := os.WriteFile(custom, data, mode); err != nil {
			return fmt.Errorf("%w: %w", constants.ErrFileWriteFailed, err)
		}
		return nil
	}
	return fileSvc.WriteFile(context.Background(), cfg.DefaultTrustBundleRelPath(), data, mode)
}

// isNotFound checks if an error indicates the file does not exist.
func isNotFound(err error) bool {
	return errors.Is(err, constants.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

type Credentials struct {
	OperatorSessionID string `json:"operator_session_id"`
	UserID            string `json:"user_id"`
	OperatorID        string `json:"operator_id"`
	CLISessionID      string `json:"cli_session_id"`
}

type ClientAuthContext struct {
	OperatorSessionID string `json:"operator_session_id"`
	CLISessionID      string `json:"cli_session_id"`
	UserID            string `json:"user_id"`
	OperatorID        string `json:"operator_id"`
	ClientCert        string `json:"client_cert"`
	ClientKey         string `json:"client_key"`
	// Certificate is the CLI mTLS identity with its sealed key unsealed in
	// memory only. It is never serialized.
	Certificate *tls.Certificate `json:"-"`
}

func LoadClientAuthContext(fileSvc fs.RuntimeFileService, cfg *config.Config) (*ClientAuthContext, error) {
	creds, err := LoadCredentials(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, fmt.Errorf("%w: local CLI credentials are absent\nEnroll first with:\n./ztd auth enroll user", constants.ErrNotAuthenticated)
	}
	missing := make([]string, 0, 2)
	if creds.CLISessionID == "" {
		missing = append(missing, "cli_session_id")
	}
	if creds.UserID == "" {
		missing = append(missing, "user_id")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: local CLI credentials are missing %s\nEnroll with:\n./ztd auth enroll user\nor refresh with:\n./ztd auth refresh", constants.ErrNotAuthenticated, strings.Join(missing, ", "))
	}
	paths := []string{cfg.CLICertFile(), cfg.CLIKeyFile()}
	for _, path := range paths {
		relPath, err := fileSvc.RelFromAbs(path)
		if err != nil {
			return nil, fmt.Errorf("auth: resolve client identity path: %w", err)
		}
		exists, err := fileSvc.FileExists(context.Background(), relPath)
		if err != nil {
			return nil, fmt.Errorf("auth: inspect client identity path: %w", err)
		}
		if !exists {
			return nil, fmt.Errorf("%w: incomplete local CLI identity\nEnroll with:\n./ztd auth enroll user", constants.ErrNotAuthenticated)
		}
	}
	cert, err := LoadCLICertificate(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	return &ClientAuthContext{
		OperatorSessionID: creds.OperatorSessionID,
		CLISessionID:      creds.CLISessionID,
		UserID:            creds.UserID,
		OperatorID:        creds.OperatorID,
		ClientCert:        cfg.CLICertFile(),
		ClientKey:         cfg.CLIKeyFile(),
		Certificate:       &cert,
	}, nil
}

// getLocalOSUser retrieves the current OS user information.
func getLocalOSUser() *models.LocalOSUser {
	return models.CurrentLocalOSUser()
}

func GenerateCSR(commonName string) (csrPEM string, privKey *ecdsa.PrivateKey, err error) {
	privKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", constants.ErrCSRGenerationFailed, err)
	}

	template := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{constants.BinaryImageName},
		},
		DNSNames: []string{"localhost", constants.GatewayInternalHostname},
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, privKey)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", constants.ErrCSRGenerationFailed, err)
	}

	csrPEMBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrBytes,
	})

	return string(csrPEMBytes), privKey, nil
}

func SaveCredentials(fileSvc fs.RuntimeFileService, cfg *config.Config, creds *Credentials) error {
	credsData, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONBody, err)
	}

	relPath, err := fileSvc.RelFromAbs(cfg.CredentialsFile())
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrFileWriteFailed, err)
	}
	if err := fileSvc.WriteFile(context.Background(), relPath, credsData, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrFileWriteFailed, err)
	}

	return nil
}

func LoadCredentials(fileSvc fs.RuntimeFileService, cfg *config.Config) (*Credentials, error) {
	relPath, err := fileSvc.RelFromAbs(cfg.CredentialsFile())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrFailedToLoadCredentials, err)
	}
	credsData, err := fileSvc.ReadFile(context.Background(), relPath)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %w", constants.ErrFailedToLoadCredentials, err)
	}

	var creds Credentials
	if err := json.Unmarshal(credsData, &creds); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONBody, err)
	}

	return &creds, nil
}

func CheckOperatorRunning(cfg *config.Config) error {
	return CheckOperatorRunningAtURL(cfg.OperatorDiscoveryURL())
}

func CheckOperatorRunningAtURL(operatorURL string) error {
	// Parse the URL to extract host:port
	parsedURL, err := url.Parse(operatorURL)
	if err != nil {
		return fmt.Errorf("%w: %s", constants.ErrGatewayURLRequired, operatorURL)
	}

	hostPort := parsedURL.Host
	if hostPort == "" {
		return fmt.Errorf("%w: %s", constants.ErrGatewayURLRequired, operatorURL)
	}
	// Force IPv4 by replacing localhost with 127.0.0.1 to prevent IPv6 resolution
	if strings.HasPrefix(hostPort, "localhost:") {
		hostPort = "127.0.0.1" + hostPort[9:]
	}
	// Try to connect to the port
	conn, err := net.DialTimeout(string(constants.NetworkProtocolTCP), hostPort, 5*time.Second)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrServiceUnavailable, err)
	}
	conn.Close()

	return nil
}
