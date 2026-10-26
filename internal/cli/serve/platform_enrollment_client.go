// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/netutil"
	"github.com/zerotrustdata/ztd/internal/services/auth"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Bootstrap redial backoff after a dropped socket or a transient Gateway
// error. The request, token, and keys are reused on every redial.
const (
	operatorBootstrapRedialBase = time.Second
	operatorBootstrapRedialMax  = 30 * time.Second
)

// OperatorIdentity is the enrolled Operator's identity and runtime grant. It
// lives in process memory only and is never written to disk: a stop or reboot
// loses it, and the Operator must enroll again.
type OperatorIdentity struct {
	Certificate        tls.Certificate
	CertificatePEM     []byte
	TrustBundlePEM     []byte
	OperatorID         string
	OperatorSessionID  string
	Posture            constants.Posture
	SystemFingerprint  string
	MaxConcurrentTasks int
	MaxMemoryMB        int
	// L2Trust is the Gateway's consensus policy and member keys the Operator
	// verifies L2 votes against; nil when the Gateway has no consensus set.
	L2Trust *models.OperatorL2Trust
}

// OperatorBootstrapClientConfig configures one Operator enrollment.
type OperatorBootstrapClientConfig struct {
	// URL is the Gateway bootstrap websocket (OperatorBootstrapURL).
	URL        string
	InstanceID string
	Hostname   string
	// RuntimeConfig is the canonical JSON OperatorRuntimeConfig the Gateway
	// records for the issued Operator (operatorRuntimeConfig).
	RuntimeConfig      json.RawMessage
	FingerprintOptions auth.FingerprintOptions
	// ActuatorPublicKey is the hex Ed25519 public key of this Operator's L5
	// Actuator. The Gateway binds it to the Operator at enrollment.
	ActuatorPublicKey string
	// AuditorPublicKey is the hex Ed25519 public key of this Operator's
	// Auditor. The Gateway binds it to the Operator at enrollment.
	AuditorPublicKey string
	// TrustBundlePEM, when set, is the owner-supplied CA a wss:// bootstrap
	// URL must chain to; otherwise the system roots apply.
	TrustBundlePEM []byte
	// TLSServerName is the name the Gateway certificate is verified against
	// when TrustBundlePEM is set (config.TLSServerName for the endpoint).
	TLSServerName string
	Deployment    *OperatorDeploymentRecorder
	Logger        *slog.Logger
}

// OperatorBootstrapClient enrolls the Operator over the Gateway bootstrap
// websocket (models.OperatorBootstrapFrameType). It is the Operator's only
// enrollment path: no HTTP request is made, and nothing it generates or
// receives touches disk.
type OperatorBootstrapClient struct {
	cfg    OperatorBootstrapClientConfig
	dialer *websocket.Dialer
}

func NewOperatorBootstrapClient(cfg OperatorBootstrapClientConfig) (*OperatorBootstrapClient, error) {
	if cfg.URL == "" || cfg.InstanceID == "" || cfg.Hostname == "" || len(cfg.RuntimeConfig) == 0 ||
		cfg.Deployment == nil || cfg.Logger == nil {
		return nil, fmt.Errorf("%w: operator bootstrap requires a URL, instance ID, hostname, runtime config, deployment recorder, and logger", constants.ErrInternal)
	}
	if _, err := models.DecodeActuatorPublicKey(cfg.ActuatorPublicKey); err != nil {
		return nil, fmt.Errorf("operator bootstrap: actuator key: %w", err)
	}
	if _, err := models.DecodeAuditorPublicKey(cfg.AuditorPublicKey); err != nil {
		return nil, fmt.Errorf("operator bootstrap: auditor key: %w", err)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if len(cfg.TrustBundlePEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.TrustBundlePEM) {
			return nil, fmt.Errorf("%w: operator bootstrap trust bundle", constants.ErrCAParseFailed)
		}
		tlsCfg.RootCAs = pool
		tlsCfg.ServerName = cfg.TLSServerName
	}
	return &OperatorBootstrapClient{cfg: cfg, dialer: httpclient.WebSocketDialerWithTLS(tlsCfg)}, nil
}

// operatorBootstrapAttempt is the in-memory enrollment request, reused on
// every redial so the Gateway resumes it instead of creating another.
type operatorBootstrapAttempt struct {
	request      models.OperatorBootstrapRequest
	operatorKey  *ecdsa.PrivateKey
	cliKey       *ecdsa.PrivateKey
	fingerprints models.PlatformEnrollmentCSRFingerprints
	requestID    string
}

// operatorBootstrapRetry marks a failure the same attempt survives by
// redialing: a dropped socket, an unreachable Gateway, or a transient
// Gateway error frame.
type operatorBootstrapRetry struct{ err error }

func (e *operatorBootstrapRetry) Error() string { return e.err.Error() }
func (e *operatorBootstrapRetry) Unwrap() error { return e.err }

// Enroll creates the enrollment request, holds the bootstrap socket through
// the owner's decision, proves possession of both keys, and returns the
// delivered identity. Only the context, a denial, an expiry, or a permanent
// Gateway error ends it; everything else redials with the same request.
func (c *OperatorBootstrapClient) Enroll(ctx context.Context) (*OperatorIdentity, error) {
	attempt, err := c.newAttempt()
	if err != nil {
		return nil, err
	}
	// The launch ID is not a secret, so the deploying CLI approves only the
	// request carrying these keys; record them before anything is announced.
	if err := c.cfg.Deployment.Record(ctx, models.OperatorDeploymentState{Phase: models.OperatorDeploymentPhaseRequesting, Fingerprints: attempt.fingerprints}); err != nil {
		return nil, err
	}
	delay := operatorBootstrapRedialBase
	for {
		identity, err := c.exchange(ctx, attempt)
		if err == nil {
			return identity, nil
		}
		var retry *operatorBootstrapRetry
		if ctx.Err() != nil || !errors.As(err, &retry) {
			return nil, err
		}
		c.cfg.Logger.Warn("operator enrollment: bootstrap socket ended; redialing",
			"request_id", attempt.requestID, "retry_in", delay, "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, operatorBootstrapRedialMax)
	}
}

func (c *OperatorBootstrapClient) newAttempt() (*operatorBootstrapAttempt, error) {
	operatorCSR, operatorKey, err := GenerateCSR("ztd-operator-" + c.cfg.Hostname)
	if err != nil {
		return nil, err
	}
	cliCSR, cliKey, err := GenerateCSR("ztd-cli-" + c.cfg.Hostname)
	if err != nil {
		return nil, err
	}
	operatorFP, err := auth.CSRFingerprint(operatorCSR)
	if err != nil {
		return nil, fmt.Errorf("operator enrollment: operator csr fingerprint: %w", err)
	}
	cliFP, err := auth.CSRFingerprint(cliCSR)
	if err != nil {
		return nil, fmt.Errorf("operator enrollment: cli csr fingerprint: %w", err)
	}
	systemFingerprint, err := auth.GenerateOperatorFingerprint(c.cfg.Logger, c.cfg.FingerprintOptions)
	if err != nil {
		return nil, fmt.Errorf("operator enrollment: system fingerprint: %w", err)
	}
	token, err := models.NewPlatformEnrollmentToken()
	if err != nil {
		return nil, fmt.Errorf("operator enrollment: generate request token: %w", err)
	}
	return &operatorBootstrapAttempt{
		request: models.OperatorBootstrapRequest{
			Token: token,
			Enrollment: models.PlatformEnrollmentCreateRequest{
				ComponentKind:     models.PlatformComponentOperator,
				InstanceID:        c.cfg.InstanceID,
				Hostname:          c.cfg.Hostname,
				SystemFingerprint: systemFingerprint.Fingerprint,
				DeploymentID:      c.cfg.Deployment.LaunchID(),
				TokenHash:         models.PlatformEnrollmentTokenHash(token),
				Operator: &models.PlatformOperatorCSRPayload{
					OperatorCSRPEM:    operatorCSR,
					CLICSRPEM:         cliCSR,
					ActuatorPublicKey: c.cfg.ActuatorPublicKey,
					AuditorPublicKey:  c.cfg.AuditorPublicKey,
					AuditorKeyID:      c.cfg.AuditorPublicKey,
				},
			},
			RuntimeConfig: c.cfg.RuntimeConfig,
		},
		operatorKey:  operatorKey,
		cliKey:       cliKey,
		fingerprints: models.PlatformEnrollmentCSRFingerprints{Operator: operatorFP, CLI: cliFP},
	}, nil
}

// exchange runs one bootstrap socket to the bundle. Transport failures are
// retryable; the Gateway's error frame says whether its failure is.
func (c *OperatorBootstrapClient) exchange(ctx context.Context, attempt *operatorBootstrapAttempt) (*OperatorIdentity, error) {
	conn, resp, err := c.dialer.DialContext(ctx, c.cfg.URL, nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if errors.Is(err, websocket.ErrBadHandshake) {
		return nil, fmt.Errorf("operator enrollment: Gateway refused the bootstrap websocket (HTTP %d): %w", resp.StatusCode, err)
	}
	if err != nil {
		return nil, &operatorBootstrapRetry{fmt.Errorf("operator enrollment: dial %s: %w", c.cfg.URL, err)}
	}
	defer conn.Close()
	// The socket is held for the owner's decision; cancellation closes it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := conn.WriteJSON(models.OperatorBootstrapFrame{Type: models.OperatorBootstrapFrameRequest, Request: &attempt.request}); err != nil {
		return nil, &operatorBootstrapRetry{fmt.Errorf("operator enrollment: send request: %w", err)}
	}

	created, err := readOperatorBootstrapFrame(conn, models.OperatorBootstrapFrameCreated)
	if err != nil {
		return nil, err
	}
	if err := c.observeCreated(ctx, attempt, created.Created); err != nil {
		return nil, err
	}

	// Proof of possession goes out as soon as the request ID it signs is
	// known, so the Gateway issues the moment the owner's approval commits.
	proofs, err := attempt.proofs()
	if err != nil {
		return nil, err
	}
	if err := conn.WriteJSON(models.OperatorBootstrapFrame{Type: models.OperatorBootstrapFrameComplete, Complete: proofs}); err != nil {
		return nil, &operatorBootstrapRetry{fmt.Errorf("operator enrollment: send completion: %w", err)}
	}

	decided, err := readOperatorBootstrapFrame(conn, models.OperatorBootstrapFrameDecision)
	if err != nil {
		return nil, err
	}
	if decided.Decision == nil {
		return nil, fmt.Errorf("operator enrollment: decision frame missing its decision")
	}
	switch decided.Decision.State {
	case models.PlatformEnrollmentStateApproved, models.PlatformEnrollmentStateIssuing, models.PlatformEnrollmentStateCompleted:
	case models.PlatformEnrollmentStateDenied:
		return nil, fmt.Errorf("operator enrollment: request %s was denied by the owner", attempt.requestID)
	case models.PlatformEnrollmentStateExpired:
		return nil, fmt.Errorf("operator enrollment: request %s expired before approval", attempt.requestID)
	default:
		return nil, fmt.Errorf("operator enrollment: request %s ended in state %s", attempt.requestID, decided.Decision.State)
	}

	delivered, err := readOperatorBootstrapFrame(conn, models.OperatorBootstrapFrameBundle)
	if err != nil {
		return nil, err
	}
	identity, err := attempt.identity(delivered.Bundle)
	if err != nil {
		return nil, err
	}
	c.cfg.Logger.Info("operator enrollment: completed",
		"request_id", attempt.requestID,
		"operator_id", identity.OperatorID,
		"operator_session_id", identity.OperatorSessionID)
	if err := c.cfg.Deployment.Record(ctx, models.OperatorDeploymentState{Phase: models.OperatorDeploymentPhaseEnrolled, OperatorSessionID: identity.OperatorSessionID}); err != nil {
		return nil, err
	}
	return identity, nil
}

// observeCreated announces the request once; a redial resumes the same one.
func (c *OperatorBootstrapClient) observeCreated(ctx context.Context, attempt *operatorBootstrapAttempt, created *models.PlatformEnrollmentCreateResponse) error {
	if created == nil || created.RequestID == "" {
		return fmt.Errorf("operator enrollment: created frame missing its request")
	}
	if created.Fingerprints.Operator != attempt.fingerprints.Operator || created.Fingerprints.CLI != attempt.fingerprints.CLI {
		return fmt.Errorf("operator enrollment: request %s does not carry this Operator's keys", created.RequestID)
	}
	if attempt.requestID == created.RequestID {
		return nil
	}
	if attempt.requestID != "" {
		return fmt.Errorf("operator enrollment: Gateway resumed request %s, expected %s", created.RequestID, attempt.requestID)
	}
	attempt.requestID = created.RequestID
	c.cfg.Logger.Info("operator enrollment: request submitted",
		"request_id", created.RequestID,
		"operator_fingerprint", attempt.fingerprints.Operator,
		"cli_fingerprint", attempt.fingerprints.CLI,
		"approval_url", created.ApprovalURL,
		"expires_at", created.ExpiresAt)
	renderOperatorEnrollmentInstructions(os.Stderr, created)
	return c.cfg.Deployment.Record(ctx, models.OperatorDeploymentState{Phase: models.OperatorDeploymentPhasePendingApproval, RequestID: created.RequestID})
}

func renderOperatorEnrollmentInstructions(w io.Writer, created *models.PlatformEnrollmentCreateResponse) {
	if w == nil || created == nil {
		return
	}
	fmt.Fprintf(w, "Approve on the Gateway host with:\n%s auth enroll approve %s\n", constants.LocalBinaryName, created.RequestID)
	if url := strings.TrimSpace(created.ApprovalURL); url != "" {
		fmt.Fprintf(w, "Passkey approval URL: %s\n", url)
	}
}

// readOperatorBootstrapFrame reads the next frame and requires want. A read
// failure is a dropped socket (retryable); an error frame carries the
// Gateway's own verdict.
func readOperatorBootstrapFrame(conn *websocket.Conn, want models.OperatorBootstrapFrameType) (models.OperatorBootstrapFrame, error) {
	var frame models.OperatorBootstrapFrame
	if err := conn.ReadJSON(&frame); err != nil {
		return frame, &operatorBootstrapRetry{fmt.Errorf("operator enrollment: await %s: %w", want, err)}
	}
	if frame.Type == models.OperatorBootstrapFrameError {
		if frame.Error == nil {
			return frame, fmt.Errorf("operator enrollment: Gateway ended the exchange without a reason")
		}
		err := fmt.Errorf("operator enrollment: Gateway: %s", frame.Error.Message)
		if frame.Error.Retryable {
			return frame, &operatorBootstrapRetry{err}
		}
		return frame, err
	}
	if frame.Type != want {
		return frame, fmt.Errorf("operator enrollment: expected %s frame, got %q", want, frame.Type)
	}
	return frame, nil
}

// proofs signs the canonical completion transcript with both private keys.
func (a *operatorBootstrapAttempt) proofs() (*models.PlatformEnrollmentProofs, error) {
	transcript, err := buildOperatorCompletionTranscript(a.requestID, a.request.Enrollment.TokenHash,
		a.request.Enrollment.InstanceID, a.fingerprints.Operator, a.fingerprints.CLI)
	if err != nil {
		return nil, err
	}
	operatorProof, err := signTranscript(a.operatorKey, transcript)
	if err != nil {
		return nil, fmt.Errorf("operator enrollment: sign operator proof: %w", err)
	}
	cliProof, err := signTranscript(a.cliKey, transcript)
	if err != nil {
		return nil, fmt.Errorf("operator enrollment: sign cli proof: %w", err)
	}
	return &models.PlatformEnrollmentProofs{Operator: operatorProof, CLI: cliProof}, nil
}

// identity pairs the delivered Operator certificate with the in-memory key.
// The companion CLI certificate is not retained: the Operator never uses it.
func (a *operatorBootstrapAttempt) identity(bundle *models.OperatorBootstrapBundle) (*OperatorIdentity, error) {
	if bundle == nil || bundle.Credentials.OperatorCert == "" || bundle.Credentials.HubTrustBundle == "" ||
		bundle.Credentials.OperatorID == "" || bundle.Credentials.OperatorSessionID == "" {
		return nil, fmt.Errorf("operator enrollment: bundle missing the Operator certificate, trust, or session")
	}
	creds := bundle.Credentials
	if !creds.Posture.Valid() {
		return nil, fmt.Errorf("operator enrollment: bundle posture %q: %w", creds.Posture, constants.ErrInvalidPosture)
	}
	cert, certPEM, err := operatorKeyPair(creds.OperatorCert, creds.OperatorCertChain, a.operatorKey)
	if err != nil {
		return nil, err
	}
	return &OperatorIdentity{
		Certificate:        cert,
		CertificatePEM:     certPEM,
		TrustBundlePEM:     []byte(creds.HubTrustBundle),
		OperatorID:         creds.OperatorID,
		OperatorSessionID:  creds.OperatorSessionID,
		Posture:            creds.Posture,
		SystemFingerprint:  a.request.Enrollment.SystemFingerprint,
		MaxConcurrentTasks: bundle.MaxConcurrentTasks,
		MaxMemoryMB:        bundle.MaxMemoryMB,
		L2Trust:            bundle.L2Trust,
	}, nil
}

// operatorKeyPair pairs an issued Operator leaf and its chain with the key
// the CSR was generated from. It returns the pair and the PEM it was built
// from.
func operatorKeyPair(certPEM, chainPEM string, key *ecdsa.PrivateKey) (tls.Certificate, []byte, error) {
	if chainPEM != "" {
		certPEM += "\n" + chainPEM
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("operator identity: encode operator key: %w", err)
	}
	cert, err := tls.X509KeyPair([]byte(certPEM), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("%w: %w", constants.ErrLoadCertKeyPair, err)
	}
	return cert, []byte(certPEM), nil
}

// operatorRuntimeConfig is the runtime configuration the Operator declares
// at enrollment; the Gateway records it for the issued Operator.
func operatorRuntimeConfig(cfg *config.Config) (json.RawMessage, error) {
	data, err := models.MarshalOperatorRuntimeConfig(&operatorv1.OperatorRuntimeConfig{
		CloudMode:     cfg.CloudMode,
		CloudProvider: cfg.CloudProvider,
		LogLevel:      cfg.LogLevel,

		HttpPort:     int32(cfg.HTTPPort),
		Capabilities: models.OperatorCapabilitiesToProto(cfg.EffectiveOperatorCapabilities()),
		LocalDir:     cfg.WorkDir,
		Account:      auth.ResolveCurrentAccount(),

		InferenceOllamaEndpoint:              cfg.Inference.OllamaEndpoint,
		InferenceLlamacppEndpoint:            cfg.Inference.LlamaCppEndpoint,
		InferenceLlamacppModelRoot:           cfg.Inference.LlamaCppModelRoot,
		InferenceKeepAlive:                   cfg.Inference.KeepAlive,
		InferenceAllowHostedDecision:         cfg.Inference.AllowHostedDecision,
		InferenceDecisionModels:              cfg.Inference.DecisionModels,
		InferenceDecisionOnly:                cfg.Inference.DecisionOnly,
		InferenceDecisionBackend:             inferenceDecisionBackendKind(cfg),
		ProvenanceOperatorId:                 cfg.ProvenanceOperator.OperatorID,
		ProvenanceOperatorModelStorageRoot:   cfg.ProvenanceOperator.ModelStorageRoot,
		ProvenanceOperatorModelStorageFormat: cfg.ProvenanceOperator.ModelStorageFormat,
		ProviderBoundaryObserverId:           cfg.ProviderBoundaryObserver.ObserverID,
		Platform:                             runtime.GOOS,
		HeartbeatIntervalMs:                  uint32(cfg.HeartbeatInterval.Milliseconds()),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrOperatorRuntimeConfigMarshal, err)
	}
	return data, nil
}

// operatorBootstrapDialURL returns where enrollment dials. With an
// owner-pinned Gateway CA the bootstrap runs over wss:// on the Gateway HTTPS
// port (the pub/sub listener), so the Gateway is authenticated before the
// request is announced; otherwise it is the plain HTTP bootstrap URL.
func operatorBootstrapDialURL(cfg *config.Config, endpoint string, httpPort int, pinned bool) string {
	if pinned {
		return strings.TrimRight(cfg.PubSubURL, "/") + constants.APIPaths.AuthOperatorBootstrapWebSocket
	}
	return OperatorBootstrapURL(endpoint, httpPort)
}

// OperatorBootstrapURL returns the Gateway bootstrap websocket URL for an
// endpoint flag value. An http(s) scheme maps to ws(s); an endpoint port wins,
// otherwise httpPort (or the Operator HTTP default when zero) is used.
func OperatorBootstrapURL(endpoint string, httpPort int) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	var base string
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		base = "wss://" + strings.TrimPrefix(endpoint, "https://")
	case strings.HasPrefix(endpoint, "http://"):
		base = "ws://" + strings.TrimPrefix(endpoint, "http://")
	case strings.Contains(endpoint, "://"):
		base = endpoint
	default:
		if host, port, err := netutil.SplitEndpoint(endpoint); err == nil && port == 0 {
			if httpPort == 0 {
				httpPort = constants.Ports.OperatorHttp
			}
			endpoint = net.JoinHostPort(host, strconv.Itoa(httpPort))
		}
		base = "ws://" + endpoint
	}
	return strings.TrimRight(base, "/") + constants.APIPaths.AuthOperatorBootstrapWebSocket
}

// buildOperatorCompletionTranscript constructs the canonical
// PlatformEnrollmentCompletionTranscript as deterministic protobuf,
// matching the gateway's platformEnrollmentCompletionTranscript. The
// operator transcript includes both the operator and CLI fingerprints.
func buildOperatorCompletionTranscript(requestID, tokenHash, instanceID, operatorFP, cliFP string) ([]byte, error) {
	message := &commonv1.PlatformEnrollmentCompletionTranscript{
		ProtocolVersion: constants.PlatformEnrollmentProtocolVersion,
		RequestId:       requestID,
		TokenHash:       tokenHash,
		ComponentKind:   commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_OPERATOR,
		InstanceId:      instanceID,
		Fingerprints: &commonv1.PlatformEnrollmentFingerprints{
			Operator: operatorFP,
			Cli:      cliFP,
		},
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("operator enrollment: marshal completion transcript: %w", err)
	}
	return encoded, nil
}

// signTranscript signs the SHA-256 digest of the transcript with the
// private key and returns the base64url-encoded ASN.1 DER signature.
// Go's ecdsa.SignASN1 produces ASN.1 DER directly (no raw R||S
// conversion needed, unlike WebCrypto's raw R||S signatures).
func signTranscript(privateKey *ecdsa.PrivateKey, transcript []byte) (string, error) {
	digest := sha256.Sum256(transcript)
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign transcript: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(signature), nil
}

func inferenceDecisionBackendKind(cfg *config.Config) string {
	if !cfg.Inference.Enabled {
		return ""
	}
	kind := cfg.Inference.Provider + "-systemone"
	if cfg.Inference.AllowHostedDecision {
		return kind + ",typesafe"
	}
	return kind
}
