// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package mcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestTLSCertInspectTool_Execute_Validation(t *testing.T) {
	tool := &TLSCertInspectTool{}
	ctx := context.Background()

	// Empty arguments
	_, err := tool.Execute(ctx, json.RawMessage(`{}`))
	assert.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPTLSCertInspectRequired))

	// Invalid JSON
	_, err = tool.Execute(ctx, json.RawMessage(`{invalid`))
	assert.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrMCPUnmarshalArguments))

	// Invalid path
	_, err = tool.Execute(ctx, json.RawMessage(`{"cert_path": "/etc/shadow"}`))
	assert.Error(t, err)
	// Note: validation.go likely rejects this
}

func TestTLSCertInspectTool_Execute_File(t *testing.T) {
	tmpDir := testutil.TempDir(t)

	certPath := filepath.Join(tmpDir, "test.crt")
	certPEM, keyPEM := generateTestCert(t)
	require.NotEmpty(t, keyPEM)
	err := os.WriteFile(certPath, certPEM, 0o644)
	require.NoError(t, err)

	tool := &TLSCertInspectTool{}
	args := json.RawMessage(fmt.Sprintf(`{"cert_path": %q}`, certPath))
	result, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)
	assert.Len(t, result.Content, 1)

	var inspectResult certInspectResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &inspectResult)
	require.NoError(t, err)
	assert.Equal(t, "test-cert", inspectResult.Subject)
}

func TestTLSCertInspectTool_Execute_Host(t *testing.T) {
	certPEM, keyPEM := generateTestCert(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				tlsConn := conn.(*tls.Conn)
				_ = tlsConn.Handshake()
				tlsConn.Close()
			}()
		}
	}()

	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	tool := &TLSCertInspectTool{}
	args := json.RawMessage(fmt.Sprintf(`{"host": %q, "port": %d}`, host, port))
	result, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)

	var inspectResult certInspectResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &inspectResult)
	require.NoError(t, err)
	assert.Equal(t, "test-cert", inspectResult.Subject)
}

func generateTestCert(t *testing.T) ([]byte, []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-cert",
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	return certPEM, keyPEM
}
