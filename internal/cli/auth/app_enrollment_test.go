// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"google.golang.org/protobuf/proto"
)

// generateTestCertificateWithSPIFFE generates a test certificate with a SPIFFE URI SAN.
func generateTestCertificateWithSPIFFE(t *testing.T, agentName string, notAfter time.Time) (certPEM string, keyPEM string) {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	spiffeID := "spiffe://ztd.local/app/" + agentName
	uri, err := url.Parse(spiffeID)
	require.NoError(t, err)

	now := time.Now().Truncate(time.Second)
	notAfter = notAfter.Truncate(time.Second)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: agentName,
		},
		NotBefore: now,
		NotAfter:  notAfter,
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		URIs:                  []*url.URL{uri},
		BasicConstraintsValid: true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	certPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	}))

	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	require.NoError(t, err)

	keyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	}))

	return certPEM, keyPEM
}

// writeTestCLICert generates a self-signed CLI cert and writes it to cfg.CLICertFile()/CLIKeyFile().
func writeTestCLICert(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) {
	t.Helper()

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test-cli"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	certRel, err := fileSvc.RelFromAbs(cfg.CLICertFile())
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), certRel, certPEM, constants.PermFilePrivate))
	sealTestKey(t, fileSvc, cfg, cfg.CLIKeyFile(), keyPEM)
}

func TestValidateAppName(t *testing.T) {
	assert.ErrorIs(t, validateAppName(""), constants.ErrPlatformEnrollmentAppNameRequired)
	assert.ErrorIs(t, validateAppName("ztd"), constants.ErrPlatformEnrollmentInvalidAppName)
	assert.ErrorIs(t, validateAppName("ztdo"), constants.ErrPlatformEnrollmentInvalidAppName)
	assert.ErrorIs(t, validateAppName("ztde"), constants.ErrPlatformEnrollmentInvalidAppName)
	assert.ErrorIs(t, validateAppName("operator"), constants.ErrPlatformEnrollmentInvalidAppName)
	assert.ErrorIs(t, validateAppName("INVALID-UPPER"), constants.ErrPlatformEnrollmentInvalidAppName)
	assert.ErrorIs(t, validateAppName("app/slash"), constants.ErrPlatformEnrollmentInvalidAppName)
	assert.NoError(t, validateAppName("ztd-eval"))
	assert.NoError(t, validateAppName("my-agent-1"))
	assert.NoError(t, validateAppName("custom.app"))
}

func TestLoadAppIdentity_Missing(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	cert, err := LoadAppIdentity(fileSvc, cfg, "my-agent")
	assert.Nil(t, cert)
	assert.ErrorIs(t, err, constants.ErrAppIdentityNotFound)
	assert.False(t, HasValidAppIdentity(fileSvc, cfg, "my-agent"))
}

func TestLoadAppIdentity_Expired(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	appName := "my-agent"
	certPEM, keyPEM := generateTestCertificateWithSPIFFE(t, appName, time.Now().Add(-1*time.Hour))

	certRel, err := fileSvc.RelFromAbs(cfg.AppCertFile(appName))
	require.NoError(t, err)

	require.NoError(t, fileSvc.WriteFile(context.Background(), certRel, []byte(certPEM), constants.PermFilePublic))
	sealTestKey(t, fileSvc, cfg, cfg.AppKeyFile(appName), []byte(keyPEM))

	cert, err := LoadAppIdentity(fileSvc, cfg, appName)
	assert.Nil(t, cert)
	assert.ErrorIs(t, err, constants.ErrAppIdentityExpired)
	assert.False(t, HasValidAppIdentity(fileSvc, cfg, appName))
}

func TestLoadAppIdentity_Valid(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	appName := "my-agent"
	certPEM, keyPEM := generateTestCertificateWithSPIFFE(t, appName, time.Now().Add(24*time.Hour))

	certRel, err := fileSvc.RelFromAbs(cfg.AppCertFile(appName))
	require.NoError(t, err)

	require.NoError(t, fileSvc.WriteFile(context.Background(), certRel, []byte(certPEM), constants.PermFilePublic))
	sealTestKey(t, fileSvc, cfg, cfg.AppKeyFile(appName), []byte(keyPEM))

	cert, err := LoadAppIdentity(fileSvc, cfg, appName)
	require.NoError(t, err)
	assert.NotNil(t, cert)
	assert.True(t, HasValidAppIdentity(fileSvc, cfg, appName))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAppPlatformEnrollmentClient_SigningIdentitySurvivesResume(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	var submitted models.PlatformEnrollmentCreateRequest
	var approved models.PlatformEnrollmentDecisionTarget
	createCalls := 0
	completeCalls := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		switch r.URL.Path {
		case constants.APIPaths.AuthPlatformEnrollmentRequest:
			createCalls++
			require.NoError(t, json.NewDecoder(r.Body).Decode(&submitted))
			rec.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(rec).Encode(models.PlatformEnrollmentCreateResponse{RequestID: "signing-request", ExpiresAt: time.Now().Add(time.Hour)}))
		case constants.APIPaths.AuthPlatformEnrollmentStatus:
			require.Equal(t, "true", r.URL.Query().Get("wait"))
			require.NoError(t, json.NewEncoder(rec).Encode(models.PlatformEnrollmentStatusResponse{State: models.PlatformEnrollmentStateApproved}))
		case constants.APIPaths.AuthPlatformEnrollmentComplete:
			completeCalls++
			var completion models.PlatformEnrollmentCompleteRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&completion))
			publicKey, err := submitted.App.SigningKey()
			require.NoError(t, err)
			require.Len(t, publicKey, ed25519.PublicKeySize)
			fingerprint := sha256.Sum256(publicKey)
			require.Equal(t, hex.EncodeToString(fingerprint[:]), approved.Fingerprints.SigningKey)
			transcript, err := proto.MarshalOptions{Deterministic: true}.Marshal(&commonv1.PlatformEnrollmentCompletionTranscript{
				ProtocolVersion: constants.PlatformEnrollmentProtocolVersion,
				RequestId:       approved.RequestID, TokenHash: submitted.TokenHash,
				ComponentKind: commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_APPLICATION,
				InstanceId:    submitted.InstanceID,
				Fingerprints:  &commonv1.PlatformEnrollmentFingerprints{App: approved.Fingerprints.App, SigningKey: approved.Fingerprints.SigningKey},
			})
			require.NoError(t, err)
			digest := sha256.Sum256(transcript)
			signature, err := base64.RawURLEncoding.DecodeString(completion.Proofs.SigningKey)
			require.NoError(t, err)
			require.True(t, ed25519.Verify(publicKey, digest[:], signature))
			block, _ := pem.Decode([]byte(submitted.App.CSRPEM))
			csr, err := x509.ParseCertificateRequest(block.Bytes)
			require.NoError(t, err)
			transportProof, err := base64.RawURLEncoding.DecodeString(completion.Proofs.App)
			require.NoError(t, err)
			require.True(t, ecdsa.VerifyASN1(csr.PublicKey.(*ecdsa.PublicKey), digest[:], transportProof))
			if completeCalls == 1 {
				return nil, constants.ErrInternal
			}
			rec.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(rec).Encode(models.PlatformEnrollmentCompleteResponse{App: &models.PlatformEnrollmentAppCredentials{AppID: "reviewer", AppCert: "test-cert"}}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return rec.Result(), nil
	})
	opts := AppEnrollmentOptions{GenerateSigningKey: true, InstanceID: "original-instance", HTTPClient: &http.Client{Transport: transport}, Approver: func(_ context.Context, target models.PlatformEnrollmentDecisionTarget) error {
		approved = target
		return nil
	}}
	client, err := NewAppPlatformEnrollmentClient("reviewer", fileSvc, cfg, nil, opts)
	require.NoError(t, err)
	_, err = client.Enroll(t.Context(), nil)
	require.ErrorIs(t, err, constants.ErrInternal)
	pending, err := client.loadPendingState(client.pendingStatePath())
	require.NoError(t, err)
	require.Len(t, pending.SigningSeed, ed25519.SeedSize)
	sealed, err := fileSvc.ReadFile(t.Context(), client.pendingStatePath())
	require.NoError(t, err)
	require.NotContains(t, string(sealed), base64.StdEncoding.EncodeToString(pending.SigningSeed))
	opts.InstanceID = "changed-instance"
	opts.GenerateSigningKey = false // Resuming must retain the original intent even without the flag.
	client, err = NewAppPlatformEnrollmentClient("reviewer", fileSvc, cfg, nil, opts)
	require.NoError(t, err)
	_, err = client.Enroll(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, createCalls)
	key, err := LoadAppSigningKey(t.Context(), fileSvc, cfg, "reviewer")
	require.NoError(t, err)
	require.Equal(t, submitted.App.SigningPublicKey, hex.EncodeToString(key.Public().(ed25519.PublicKey)))
	pending, err = client.loadPendingState(client.pendingStatePath())
	require.NoError(t, err)
	require.Nil(t, pending)
}

func TestAppPlatformEnrollmentClient_RejectsInvalidPendingSigningIdentity(t *testing.T) {
	for _, seed := range [][]byte{nil, {1, 2, 3}} {
		fileSvc, cfg := newAuthTestEnv(t)
		client, err := NewAppPlatformEnrollmentClient("reviewer", fileSvc, cfg, nil, AppEnrollmentOptions{
			GenerateSigningKey: true,
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid pending identity must fail before contacting the Gateway")
				return nil, constants.ErrInternal
			})},
		})
		require.NoError(t, err)
		require.NoError(t, client.persistPendingState(client.pendingStatePath(), &appPendingState{Token: "pending-token", SigningSeed: seed}))
		_, err = client.Enroll(t.Context(), nil)
		require.ErrorIs(t, err, constants.ErrAppSigningKeyInvalid)
	}
}

func TestLoadAppSigningKey_RejectsPlaintextAndMalformedSeed(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	rel, err := fileSvc.RelFromAbs(cfg.AppSigningKeyFile("reviewer"))
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(t.Context(), rel, bytes.Repeat([]byte{1}, ed25519.SeedSize), constants.PermFilePrivate))
	_, err = LoadAppSigningKey(t.Context(), fileSvc, cfg, "reviewer")
	require.ErrorIs(t, err, constants.ErrIdentityFileUnseal)
	require.NoError(t, sealIdentityFile(t.Context(), fileSvc, cfg, rel, []byte{1, 2, 3}))
	_, err = LoadAppSigningKey(t.Context(), fileSvc, cfg, "reviewer")
	require.ErrorIs(t, err, constants.ErrAppSigningKeyInvalid)
}

func TestAppPlatformEnrollmentClient_Enroll_ApproverL3RequiredPrintsSuspensionAndAwaitsDecision(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	appName := "claude"

	expiresAt := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	approverCalled := false

	mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		switch r.URL.Path {
		case constants.APIPaths.AuthPlatformEnrollmentRequest:
			rec.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rec).Encode(models.PlatformEnrollmentCreateResponse{
				RequestID: "req-l3-1",
				ExpiresAt: expiresAt,
			})
		case constants.APIPaths.AuthPlatformEnrollmentStatus:
			rec.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(rec).Encode(models.PlatformEnrollmentStatusResponse{
				RequestID: "req-l3-1",
				State:     models.PlatformEnrollmentStateApproved,
			})
		case constants.APIPaths.AuthPlatformEnrollmentComplete:
			rec.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rec).Encode(models.PlatformEnrollmentCompleteResponse{
				App: &models.PlatformEnrollmentAppCredentials{
					AppID:   "spiffe://ztd.local/app/claude",
					AppCert: "dummy-cert",
				},
			})
		default:
			http.NotFound(rec, r)
		}
		return rec.Result(), nil
	})

	client, err := NewAppPlatformEnrollmentClient(appName, fileSvc, cfg, nil, AppEnrollmentOptions{
		GatewayHTTPURL: "http://localhost:8080",
		HTTPClient:     &http.Client{Transport: mockTransport},
		Approver: func(_ context.Context, target models.PlatformEnrollmentDecisionTarget) error {
			approverCalled = true
			assert.Equal(t, "req-l3-1", target.RequestID)
			return &models.PlatformEnrollmentL3RequiredError{
				Suspension: models.PlatformEnrollmentL3SuspensionResponse{
					Status:      models.PlatformEnrollmentStatusL3Required,
					Posture:     constants.PostureRatify,
					TxHash:      "tx-ratify-123",
					ApprovalURL: "https://localhost:8443/console/approve/tx-ratify-123",
					ExpiresAt:   expiresAt,
					Message:     constants.PostureRatify.HumanApprovalReason(),
				},
			}
		},
	})
	require.NoError(t, err)

	var out bytes.Buffer
	resp, err := client.Enroll(context.Background(), &out)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, approverCalled)

	output := out.String()
	assert.Contains(t, output, constants.PostureRatify.HumanApprovalReason())
	assert.Contains(t, output, "https://localhost:8443/console/approve/tx-ratify-123")
	assert.Contains(t, output, "tx-ratify-123")
	assert.Contains(t, output, expiresAt.Format(time.RFC3339))
}

func TestAppPlatformEnrollmentClient_SubmitRequest_RejectsUnrelated403ContainingBootstrappedSubstring(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	var hits int
	mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(http.StatusForbidden)
		_, _ = rec.WriteString(`{"error":"forbidden: custom policy on bootstrapped node"}`)
		return rec.Result(), nil
	})

	client, err := NewAppPlatformEnrollmentClient("claude", fileSvc, cfg, nil, AppEnrollmentOptions{
		GatewayHTTPURL: "http://localhost:8080",
		HTTPClient:     &http.Client{Transport: mockTransport},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = client.Enroll(ctx, nil)
	require.Error(t, err)
	assert.Equal(t, 1, hits, "unrelated 403 containing 'bootstrapped' substring must not trigger bootstrap retry loop")
}

func TestNewAppPlatformEnrollmentClient_UsesConfiguredGatewayDiscoveryEndpoint(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	cfg.Paths.Host = "localhost"
	config.SetHTTPEndpointOverride("http://gateway-fixture.example:19080")
	t.Cleanup(func() { config.SetEndpointOverride("") })

	client, err := NewAppPlatformEnrollmentClient("ztd-eval", fileSvc, cfg, nil)
	require.NoError(t, err)
	assert.Equal(t, "http://gateway-fixture.example:19080", client.gatewayHTTPURL)
}
