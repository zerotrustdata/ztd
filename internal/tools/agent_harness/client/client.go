// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package client is Agent Harness's thin, faithful client for a real ztd Gateway.
// It speaks the actual wire surfaces (health/state-root, MCP & A2A JSON-RPC,
// the governance envelope admission API, the OOB approve flow, and audit
// receipts) and records every exchange so the run can be audited in detail.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Persona is the identity Agent Harness wears for a given exchange. This is the ONLY
// thing Agent Harness fakes: it pretends to be whatever AI tool/agent we point at the
// Gateway. The Gateway and Operator are real and treat it like any BYO client.
type Persona struct {
	// ID is a stable handle, e.g. "claude-desktop", "cursor", "langchain-agent".
	ID string
	// UserAgent is sent on the wire so the Gateway/audit log attributes the call.
	UserAgent string
	// OperatorSessionID is the Operator session ID for Bearer token authentication.
	OperatorSessionID string
	// CLISessionID is the CLI session ID. It is sent as X-ZTD-CLI-Session-ID only on requests
	// made with the CLI certificate; requests made with an Operator certificate never carry it.
	CLISessionID string
	// UserID is the user ID for session binding.
	UserID string
	// OperatorID is the operator ID for session binding.
	OperatorID string
}

// Exchange is a single recorded HTTP round-trip. Slices of these are the spine
// of the detailed audit report.
type Exchange struct {
	Persona   string          `json:"persona"`
	Method    string          `json:"method"`
	URL       string          `json:"url"`
	ReqBody   json.RawMessage `json:"request,omitempty"`
	ReqRaw    string          `json:"request_raw,omitempty"`
	Status    int             `json:"status"`
	RespBody  json.RawMessage `json:"response,omitempty"`
	RespRaw   string          `json:"response_raw,omitempty"`
	LatencyMS int64           `json:"latency_ms"`
	Err       string          `json:"error,omitempty"`
	At        time.Time       `json:"at"`
}

// Client wraps an mTLS-capable http.Client plus the recorder.
type Client struct {
	cfg  config.Config
	http *http.Client // Operator identity (Auth)
	// cliHTTP presents the CLI identity (CLIAuth). It is the Operator client itself when both name
	// the same certificate, and nil when no CLI certificate is configured.
	cliHTTP *http.Client
	// cliTLS is the TLS config of the CLI identity; nil when no CLI certificate is configured.
	cliTLS *tls.Config
	// operatorIsCLI is set when CLIAuth names the Operator certificate, so requests made with the
	// Operator client present the CLI identity and carry the persona's CLI session.
	operatorIsCLI bool
	rec           *[]Exchange // optional sink for the current scenario
}

// New builds a Client with mTLS material loaded per config. The MCP/A2A surface
// also accepts an API key; both are attached automatically when present.
func New(cfg config.Config) (*Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}

	if cfg.Auth.CABundle != "" {
		if pem, err := os.ReadFile(cfg.Auth.CABundle); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				tlsCfg.RootCAs = pool
			}
		}
	}
	if cfg.Auth.Certificate != nil {
		tlsCfg.Certificates = []tls.Certificate{*cfg.Auth.Certificate}
	} else if cfg.Auth.ClientCert != "" && cfg.Auth.ClientKey != "" {
		if _, err := os.Stat(cfg.Auth.ClientCert); err == nil {
			if _, err := os.Stat(cfg.Auth.ClientKey); err == nil {
				crt, err := tls.LoadX509KeyPair(cfg.Auth.ClientCert, cfg.Auth.ClientKey)
				if err != nil {
					return nil, fmt.Errorf("load client cert: %w", err)
				}
				tlsCfg.Certificates = []tls.Certificate{crt}
			}
		}
	}

	c := &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}

	// The CLI identity carries notary-scenario submits and approval streams, so
	// handleCLIAuth can stamp the host user onto the suspended transaction. When
	// CLIAuth names the Operator certificate, the Operator client already presents
	// the CLI identity and serves both. Otherwise CLIAuth gets its own client.
	if cfg.CLIAuth.Certificate == nil && (cfg.CLIAuth.ClientCert == "" || cfg.CLIAuth.ClientKey == "") {
		return c, nil
	}
	if cfg.CLIAuth.Certificate == cfg.Auth.Certificate && cfg.CLIAuth.ClientCert == cfg.Auth.ClientCert && cfg.CLIAuth.ClientKey == cfg.Auth.ClientKey {
		c.operatorIsCLI = true
		c.cliHTTP = c.http
		c.cliTLS = tlsCfg
		return c, nil
	}
	cliTLS := &tls.Config{MinVersion: tls.VersionTLS13}
	if cfg.CLIAuth.CABundle != "" {
		if pem, err := os.ReadFile(cfg.CLIAuth.CABundle); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				cliTLS.RootCAs = pool
			}
		}
	}
	if cfg.CLIAuth.Certificate != nil {
		cliTLS.Certificates = []tls.Certificate{*cfg.CLIAuth.Certificate}
	} else if _, err := os.Stat(cfg.CLIAuth.ClientCert); err == nil {
		if _, err := os.Stat(cfg.CLIAuth.ClientKey); err == nil {
			crt, err := tls.LoadX509KeyPair(cfg.CLIAuth.ClientCert, cfg.CLIAuth.ClientKey)
			if err != nil {
				return nil, fmt.Errorf("load CLI client cert: %w", err)
			}
			cliTLS.Certificates = []tls.Certificate{crt}
		}
	}
	c.cliTLS = cliTLS
	c.cliHTTP = &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: cliTLS},
	}

	return c, nil
}

// Config exposes the live config (scenarios need TTLs, key ids, etc.).
func (c *Client) Config() config.Config { return c.cfg }

// Record points the client at a sink; every subsequent call appends an Exchange.
func (c *Client) Record(sink *[]Exchange) { c.rec = sink }

// doWithCLI sends a request with the CLI identity and its CLI session. It is used for
// notary-scenario transaction submits so handleCLIAuth stamps the host user's identity
// onto the suspended transaction. It fails closed without a CLI certificate: only a CLI
// certificate establishes a CLI session, so the request must not fall back to the
// Operator identity.
func (c *Client) doWithCLI(ctx context.Context, p Persona, method, url string, body []byte) (int, []byte, error) {
	if c.cliHTTP == nil {
		return 0, nil, fmt.Errorf("agent harness: %s %s needs the CLI certificate: %w", method, url, constants.ErrMissingCertificate)
	}
	return c.doWithClient(ctx, p, method, url, body, c.cliHTTP, "application/json", true)
}

// do executes a JSON request against the mTLS surface with the Operator identity and
// records it. The persona's CLI session is sent only when the Operator identity is the
// CLI identity.
func (c *Client) do(ctx context.Context, p Persona, method, url string, body []byte) (int, []byte, error) {
	return c.doWithClient(ctx, p, method, url, body, c.http, "application/json", c.operatorIsCLI)
}

// doWithClient is the core request executor. It is shared by do (Operator identity),
// doWithCLI (CLI identity), and the application client in Deliberate, so every path gets
// identical header/recording logic. cliIdentity reports whether hc presents the CLI
// certificate; only then is the persona's CLI session sent.
func (c *Client) doWithClient(ctx context.Context, p Persona, method, url string, body []byte, hc *http.Client, contentType string, cliIdentity bool) (int, []byte, error) {
	start := time.Now()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if p.UserAgent != "" {
		req.Header.Set("User-Agent", p.UserAgent)
		// Surface the impersonated identity for audit attribution too.
		req.Header.Set("X-ZTD-Client-Persona", p.ID)
	}
	if p.OperatorSessionID != "" {
		req.Header.Set("Authorization", "Bearer "+p.OperatorSessionID)
	}
	if cliIdentity && p.CLISessionID != "" {
		req.Header.Set(constants.HeaderCLISessionID, p.CLISessionID)
	}
	if p.UserID != "" {
		req.Header.Set(constants.HeaderUserID, p.UserID)
	}
	if p.OperatorID != "" {
		req.Header.Set(constants.HeaderOperatorID, p.OperatorID)
	}
	if p.OperatorSessionID != "" {
		req.Header.Set(constants.HeaderOperatorSessionID, p.OperatorSessionID)
	}

	resp, err := hc.Do(req)
	ex := Exchange{Persona: p.ID, Method: method, URL: url, At: start}
	attachBody(&ex.ReqBody, &ex.ReqRaw, body)

	if err != nil {
		ex.Err = err.Error()
		ex.LatencyMS = time.Since(start).Milliseconds()
		c.append(ex, c.cfg.Verbose)
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)

	ex.Status = resp.StatusCode
	ex.LatencyMS = time.Since(start).Milliseconds()
	attachBody(&ex.RespBody, &ex.RespRaw, out)
	c.append(ex, c.cfg.Verbose)
	return resp.StatusCode, out, nil
}

func (c *Client) append(ex Exchange, echo bool) {
	if c.rec != nil {
		*c.rec = append(*c.rec, ex)
	}
	if echo {
		fmt.Fprintf(os.Stderr, "  %-3s %d %dms %s\n", ex.Method, ex.Status, ex.LatencyMS, ex.URL)
		if ex.Err != "" {
			fmt.Fprintf(os.Stderr, "      err: %s\n", ex.Err)
		}
	}
}

// attachBody stores valid JSON as structured, anything else as raw text.
func attachBody(j *json.RawMessage, raw *string, b []byte) {
	if len(b) == 0 {
		return
	}
	if json.Valid(b) {
		*j = append(json.RawMessage(nil), b...)
		return
	}
	*raw = string(b)
}

// ---- Gateway surfaces -------------------------------------------------------

// StateRoot fetches the current state_merkle_root from /state on the public surface.
// Maximal envelopes must bind to this exact root or the Operator drops them (TOCTOU gap).
func (c *Client) StateRoot(ctx context.Context) (string, error) {
	return c.stateRoot(ctx, c.cfg.PublicBaseURL)
}

// StateRootFromMTLS fetches the current state_merkle_root from /state on the mTLS surface.
// Use this when the gateway is running in full cert mode where all ports require mTLS.
func (c *Client) StateRootFromMTLS(ctx context.Context) (string, error) {
	return c.stateRoot(ctx, c.cfg.MTLSBaseURL)
}

func (c *Client) stateRoot(ctx context.Context, baseURL string) (string, error) {
	_, body, err := c.do(ctx, Persona{ID: "agent-harness"}, http.MethodGet, baseURL+constants.APIPaths.State, nil)
	if err != nil {
		return "", err
	}
	var h models.StateResponse
	if err := json.Unmarshal(body, &h); err != nil {
		return "", fmt.Errorf("state root: decode response: %w", err)
	}
	if h.StateMerkleRoot == "" {
		return "", fmt.Errorf("state root: %w", constants.ErrMissingRequiredField)
	}
	return h.StateMerkleRoot, nil
}

// RegisterSigner registers an Ed25519 public key as a trusted signer so
// consensus/notary postures will accept Agent Harness's proofs. The body is the
// Gateway's models.TrustedSigner wire type, decoded by handleGovernanceSigners.
// A non-2xx response is returned as an error.
func (c *Client) RegisterSigner(ctx context.Context, keyID, pubHex string) error {
	payload, err := json.Marshal(models.TrustedSigner{
		ID:        keyID,
		PublicKey: pubHex,
		Enabled:   true,
	})
	if err != nil {
		return fmt.Errorf("register signer %q: %w", keyID, err)
	}
	status, _, err := c.do(ctx, Persona{ID: "agent-harness"}, http.MethodPost,
		c.cfg.MTLSBaseURL+constants.APIPaths.GovernanceSigners, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("register signer %q: status %d", keyID, status)
	}
	return nil
}

// SubscribeApprovals opens the approval subscription for the persona's CLI session over
// the CLI identity, so the stream is authenticated with the same identity as the notary
// submit. It fails closed when no CLI certificate is configured or the persona has no CLI
// session: the Operator identity cannot receive a CLI session's approvals. It returns once
// the gateway has registered the listener, so it must run before the attended call is
// submitted.
func (c *Client) SubscribeApprovals(ctx context.Context, p Persona) (*auth.ApprovalSubscription, error) {
	if c.cliTLS == nil {
		return nil, fmt.Errorf("agent harness: subscribe approvals needs the CLI certificate: %w", constants.ErrMissingCertificate)
	}
	if p.CLISessionID == "" {
		return nil, fmt.Errorf("agent harness: subscribe approvals needs a CLI session: %w", constants.ErrMissingRequiredField)
	}
	sseHTTPClient := &http.Client{
		Timeout:   0,
		Transport: &http.Transport{TLSClientConfig: c.cliTLS.Clone()},
	}
	return auth.SubscribeApprovals(ctx, sseHTTPClient, c.cfg.PublicBaseURL, p.CLISessionID)
}

// ApprovalDecision is the decision the banner asks the human approver to make.
type ApprovalDecision int

const (
	// ApprovalDecisionApprove asks the human to approve the transaction.
	ApprovalDecisionApprove ApprovalDecision = iota
	// ApprovalDecisionDeny asks the human to deny the transaction.
	ApprovalDecisionDeny
)

// AnnounceApproval prints the human approval banner asking for decision. A
// non-empty txHash adds the transaction and its direct approval link. An empty
// txHash announces an approval whose transaction does not exist yet: a governed
// dispatch holds its request open and the Gateway creates the transaction only
// when the call is suspended, so the approver finds it pending in the console.
func (c *Client) AnnounceApproval(txHash string, decision ApprovalDecision) {
	// The human approver reaches the gateway from the host, which uses different
	// host/ports than the harness process (container-internal). Prefer the
	// host-reachable display URL for the printed link; fall back to PublicBaseURL
	// so non-demo invocations keep working.
	cfg := c.cfg
	displayBase := cfg.ApprovalDisplayURL
	if displayBase == "" {
		displayBase = cfg.PublicBaseURL
	}
	consoleURL := displayBase + constants.APIPaths.ConsolePrefix

	fmt.Fprintf(os.Stderr, "\n  ╔════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(os.Stderr, "  ║  HUMAN APPROVAL REQUIRED                                    ║\n")
	if decision == ApprovalDecisionDeny {
		fmt.Fprintf(os.Stderr, "  ║  DENY this transaction in your console (pushed via SSE):    ║\n")
	} else {
		fmt.Fprintf(os.Stderr, "  ║  Approve in your console (approval pushed via SSE):        ║\n")
	}
	fmt.Fprintf(os.Stderr, "  ║  %s\n", consoleURL)
	if txHash != "" {
		fmt.Fprintf(os.Stderr, "  ║  Or open this direct link as an alternative:                ║\n")
		fmt.Fprintf(os.Stderr, "  ║  %s\n", displayBase+constants.APIPaths.ApprovePagePrefix+txHash)
		fmt.Fprintf(os.Stderr, "  ║  Transaction: %s\n", txHash)
	}
	fmt.Fprintf(os.Stderr, "  ╚════════════════════════════════════════════════════════════╝\n\n")
}

// WaitForApproval prints the human approval banner for txHash, asking for decision,
// waits on sub for the decision, and returns the signed receipt: the resumed
// receipt on approve, or the L3_DENIED rejection receipt on deny.
func (c *Client) WaitForApproval(ctx context.Context, sub *auth.ApprovalSubscription, txHash, userID string, decision ApprovalDecision) (*operatorv1.ActionReceipt, error) {
	c.AnnounceApproval(txHash, decision)

	receipt, err := sub.Wait(ctx, txHash, userID)
	if err != nil {
		return nil, err
	}
	if receipt.GetRejectionCode() == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED {
		fmt.Fprintf(os.Stderr, "  ✗ Human denied transaction %s (signed L3_DENIED receipt)\n\n", txHash)
		return receipt, nil
	}
	fmt.Fprintf(os.Stderr, "  ✓ Human approval confirmed for transaction %s\n\n", txHash)
	return receipt, nil
}
