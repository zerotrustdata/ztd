// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package mcp provides Model Context Protocol (MCP) and Agent-to-Agent (A2A) services.
//
// This package contains GatewayService, which is a shared service used in both gateway mode
// and outbound mode for MCP/A2A protocol translation and downstream dispatch. The service is
// truly polymorphic - the same implementation is used in both modes.
//
// For more information on service modes, see docs/architecture/service_modes.md.
package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/uuid"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ApprovalRequestTTL is the lifetime of a human-approval request created when a
// state-changing action is paused under an L3-enforcing posture. It is deliberately
// short: L3 enforces *proof of human presence*, so the passkey (WebAuthn)
// authorization must be completed close in time to the action it authorizes.
// Once this window lapses the suspended transaction expires (the store filters on
// expires_at) and a fresh approval must be requested by retrying the action.
const ApprovalRequestTTL = 2 * time.Minute

// approvalPausedMessage builds the directive returned to the calling agent when a
// mutation is paused awaiting human passkey authorization under an L3-enforcing posture.
// It is phrased as an instruction to the AI: what happened, what a human must do,
// the short deadline, and—critically—what to do once that deadline passes (retry
// the call to open a fresh approval request).
func approvalPausedMessage(posture constants.Posture, approvalURL string) string {
	return fmt.Sprintf("Execution paused: this is a state-changing action. %s "+
		"A human must approve this exact change at %s within %d minutes. "+
		"Wait for the human to approve, then retry this identical tool call to proceed. "+
		"This window is intentionally short so the approval proves a human was present for this specific action. "+
		"If it lapses before approval the request is voided for security — call this tool again to open a fresh approval request.",
		posture.HumanApprovalReason(), approvalURL, int(ApprovalRequestTTL.Minutes()))
}

// L2ConsensusDeliberator sends an envelope to an L2 consensus service for deliberation.
// The consensus service collects signed votes from its members and returns the envelope
// with L2 metadata populated. This interface is implemented by an HTTP client that calls
// the consensus service's /consensus/v1/deliberate endpoint, or by an in-process adapter.
type L2ConsensusDeliberator interface {
	Deliberate(ctx context.Context, envelopeBytes []byte) ([]byte, error)
}

// EmbeddedSessionResolver reads the claimed embedded Operator's session ID.
// It is read-only: an unclaimed embedded Operator yields
// constants.ErrEmbeddedOperatorUnclaimed. *embedded.Service satisfies it.
type EmbeddedSessionResolver interface {
	OperatorSessionID(ctx context.Context) (string, error)
}

// GatewayService handles MCP/A2A protocol translation and downstream dispatch.
// This service is shared across both gateway mode and outbound mode - the same
// implementation is used in both contexts (truly polymorphic).
//
// In gateway mode, it is created by GatewayModeService as field mcpGateway.
// In outbound mode, it is used by OperatorPubSubService as field mcpGateway.
type GatewayService struct {
	logger            *slog.Logger
	responder         *response.Writer
	suspendedStore    storage.SuspendedTransactionStore
	fieldPathRegistry *FieldPathRegistry
	auditStore        AuditEventRecorder
	nativeToolHandler *NativeToolHandler
	scrubbingService  *scrubbing.ScrubbingService
	threatScanner     ThreatScanner
	posture           constants.Posture // validated once in NewGatewayService
	a2aDownstreamURL  string            // construction-phase (immutable after NewGatewayService)
	publicBaseURL     string            // construction-phase (immutable after NewGatewayService)
	maxPayloadBytes   int64

	envProc                governance.EnvelopeProcessor
	stateRootProvider      governance.StateRootProvider
	signingKey             ed25519.PrivateKey
	keyID                  string
	downstreamURL          string
	downstreamCmd          string
	downstreamArgs         []string
	subprocessDownstream   *SubprocessDownstream
	dbService              FieldReader
	sessionValidator       SessionValidator
	auditLogger            AuditLogger
	l2ConsensusDeliberator L2ConsensusDeliberator
	embeddedSession        EmbeddedSessionResolver

	// Circuit breaker state
	mu               sync.RWMutex
	failureCount     int
	lastFailure      time.Time
	circuitOpen      bool
	cooldownDuration time.Duration
	maxFailures      int
}

// FieldReader provides read access to individual document fields, backing the
// MCP gateway's read_field operation. It is implemented by the gateway's
// document store (DocumentStoreService).
type FieldReader interface {
	GetField(ctx context.Context, collection, id, fieldPath string) (FieldValue, error)
}

// SessionValidator validates Operator sessions for L3 authorization
type SessionValidator interface {
	ValidateSession(operatorSessionID string) (bool, error)
}

// AuditLogger logs field read operations to the audit vault
type AuditLogger interface {
	LogFieldRead(operatorSessionID, collection, documentID, fieldPath string, value FieldValue) error
}

// ThreatScanner scans input strings for security threats using L1 doctrine patterns.
// Implemented by governance.L1Doctrine.
type ThreatScanner interface {
	AnalyzeCommand(input string) []governance.ThreatSignal
}

// AuditEventRecorder records audit events. Implemented by storage.SQLAuditStore
// in production. NewGatewayService rejects a nil AuditStore at construction
// (constants.ErrAuditStoreRequired) — a missing audit store is a wiring bug
// and fail-open on a security control, not a no-op condition. There is no
// production no-op recorder.
type AuditEventRecorder interface {
	RecordEvent(event *storage.Event) (int64, error)

	// RecordEventOnce records the event unless an event of the same type already exists
	// for its TransactionID, reporting whether it wrote. The expiry sweep uses it so a
	// retry after a failed delete cannot record the same expiry twice.
	RecordEventOnce(event *storage.Event) (eventID int64, recorded bool, err error)
}

// Dependencies groups all dependencies for NewGatewayService.
type Dependencies struct {
	Logger           *slog.Logger
	Responder        *response.Writer
	SuspendedStore   storage.SuspendedTransactionStore
	ScrubbingService *scrubbing.ScrubbingService
	ThreatScanner    ThreatScanner
	MaxPayloadBytes  int64
	Posture          constants.Posture
	A2ADownstreamURL string // A2A downstream server URL
	PublicBaseURL    string // Public base URL for approval links

	// AuditStore records audit events for suspended transactions and downstream
	// MCP calls. Required: NewGatewayService returns constants.ErrAuditStoreRequired
	// when nil — a missing audit store is a wiring bug, not a no-op condition.
	AuditStore AuditEventRecorder

	// AuditReceiptQuery provides read access to the operator audit vault for
	// the governed audit_receipt_list and audit_receipt_get native tools.
	// Optional: when nil, those tools fail closed with
	// constants.ErrMCPAuditReceiptQueryNotConfigured. Production wires
	// *storage.SQLAuditStore, which implements both AuditEventRecorder and
	// AuditReceiptQuery.
	AuditReceiptQuery AuditReceiptQuery

	// EmbeddedSession resolves the claimed embedded Operator's session ID,
	// which App-originated Gateway-local envelopes are stamped with so their
	// approval, expiry, and execution evidence lands on the runtime that
	// executes them. Required: NewGatewayService returns
	// constants.ErrEmbeddedSessionResolverRequired when nil.
	EmbeddedSession EmbeddedSessionResolver

	// FieldPathRegistryFactory overrides the default NewFieldPathRegistry constructor.
	// When nil, NewFieldPathRegistry is used. This allows tests to inject a failing
	// factory to verify error handling in NewGatewayService.
	FieldPathRegistryFactory func(*slog.Logger) (*FieldPathRegistry, error)

	EnvProc                governance.EnvelopeProcessor
	StateRootProvider      governance.StateRootProvider
	SigningKey             ed25519.PrivateKey
	KeyID                  string
	DownstreamURL          string
	DownstreamCmd          string
	DownstreamArgs         []string
	DBService              FieldReader
	SessionValidator       SessionValidator
	AuditLogger            AuditLogger
	L2ConsensusDeliberator L2ConsensusDeliberator
}

func NewGatewayService(deps Dependencies) (*GatewayService, error) {
	// An empty or unknown posture fails construction: every envelope is stamped
	// with this posture and L2/L3 gating reads it.
	if !deps.Posture.Valid() {
		return nil, fmt.Errorf("gateway: posture: %w: %q", constants.ErrInvalidPosture, deps.Posture)
	}

	if deps.DownstreamURL != "" && deps.DownstreamCmd != "" {
		return nil, fmt.Errorf("gateway: cannot configure both HTTP downstream (%s) and subprocess downstream (%s): %w", deps.DownstreamURL, deps.DownstreamCmd, constants.ErrGatewayDownstreamMutuallyExclusive)
	}

	if deps.AuditStore == nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrAuditStoreRequired)
	}

	if deps.EmbeddedSession == nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrEmbeddedSessionResolverRequired)
	}

	if deps.ScrubbingService == nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrScrubbingServiceRequired)
	}

	registryFactory := deps.FieldPathRegistryFactory
	if registryFactory == nil {
		registryFactory = NewFieldPathRegistry
	}
	fieldPathRegistry, err := registryFactory(deps.Logger)
	if err != nil {
		return nil, fmt.Errorf("gateway: initialize field path registry: %w", err)
	}

	nativeToolHandler, err := NewNativeToolHandler(deps.Logger)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
	}
	if deps.AuditReceiptQuery != nil {
		nativeToolHandler.SetAuditReceiptQuery(deps.AuditReceiptQuery)
	}

	var subprocessDownstream *SubprocessDownstream
	if deps.DownstreamCmd != "" {
		subprocessDownstream = NewSubprocessDownstream(deps.DownstreamCmd, deps.DownstreamArgs, deps.Logger)
	}

	g := &GatewayService{
		logger:                 deps.Logger,
		responder:              deps.Responder,
		suspendedStore:         deps.SuspendedStore,
		fieldPathRegistry:      fieldPathRegistry,
		auditStore:             deps.AuditStore,
		nativeToolHandler:      nativeToolHandler,
		scrubbingService:       deps.ScrubbingService,
		threatScanner:          deps.ThreatScanner,
		posture:                deps.Posture,
		a2aDownstreamURL:       deps.A2ADownstreamURL,
		publicBaseURL:          deps.PublicBaseURL,
		maxFailures:            5,
		cooldownDuration:       1 * time.Minute,
		maxPayloadBytes:        deps.MaxPayloadBytes,
		envProc:                deps.EnvProc,
		stateRootProvider:      deps.StateRootProvider,
		signingKey:             deps.SigningKey,
		keyID:                  deps.KeyID,
		downstreamURL:          deps.DownstreamURL,
		downstreamCmd:          deps.DownstreamCmd,
		downstreamArgs:         deps.DownstreamArgs,
		subprocessDownstream:   subprocessDownstream,
		dbService:              deps.DBService,
		sessionValidator:       deps.SessionValidator,
		auditLogger:            deps.AuditLogger,
		l2ConsensusDeliberator: deps.L2ConsensusDeliberator,
		embeddedSession:        deps.EmbeddedSession,
	}
	return g, nil
}

// runMaintenanceSweep performs a single maintenance sweep to audit and delete expired transactions.
//
// The sweep reads only the plaintext columns of the expired rows (hash, tool, session,
// expiry), never the encrypted envelope, so a locked vault or one undecryptable row
// cannot halt expiry.
//
// Each expiry is recorded with RecordEventOnce keyed by the transaction hash, and only
// then are the rows deleted. A delete that fails after the record therefore leaves the
// row for the next sweep, which finds the event already recorded and only retries the
// delete: a transaction never gets transaction.expired twice. A row that was claimed for
// execution (ResumeWithL3Proof) is never recorded as expired, because its execution was
// attempted; it is only deleted.
func (g *GatewayService) runMaintenanceSweep(ctx context.Context) error {
	if g.suspendedStore == nil {
		return fmt.Errorf("gateway: expire suspended transactions: %w", constants.ErrSuspendedStoreRequired)
	}

	expiredTxs, err := g.suspendedStore.GetExpiredSuspendedTransactions(ctx)
	if err != nil {
		return fmt.Errorf("gateway: list expired suspended transactions: %w", err)
	}

	// Audit each expired transaction to the Operator session it targeted, so the
	// agent reading that session chain can detect the expiry. Only transactions
	// whose expiry is on the chain are deleted; one whose event cannot be recorded
	// stays for the next sweep, and the failure is returned.
	settled := make([]*models.SuspendedTransaction, 0, len(expiredTxs))
	var auditErrs []error
	for _, tx := range expiredTxs {
		if tx.ClaimedAt != nil {
			g.logger.Warn("Deleting claimed suspended transaction without an expiry event: its execution was attempted",
				"tx_hash", tx.TransactionHash, "claimed_at", tx.ClaimedAt)
			settled = append(settled, tx)
			continue
		}
		event := &storage.Event{
			TransactionID:     tx.TransactionHash,
			OperatorSessionID: tx.OperatorSessionID,
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Notary.TransactionExpired,
			ContentText:       fmt.Sprintf("Transaction %s approval expired (tool: %s)", tx.TransactionHash, tx.ToolName),
		}
		if _, _, err := g.auditStore.RecordEventOnce(event); err != nil {
			auditErrs = append(auditErrs, fmt.Errorf("record expiry of transaction %s on session %s: %w", tx.TransactionHash, tx.OperatorSessionID, err))
			continue
		}
		settled = append(settled, tx)
	}

	deletedCount, err := g.suspendedStore.DeleteExpiredSuspendedTransactions(ctx, settled)
	if err != nil {
		auditErrs = append(auditErrs, fmt.Errorf("delete expired transactions: %w", err))
	}
	if len(auditErrs) > 0 {
		return fmt.Errorf("gateway: expire suspended transactions: %w", errors.Join(auditErrs...))
	}

	if deletedCount > 0 {
		g.logger.Info("Pruned expired suspended transactions", "count", deletedCount)
	}

	return nil
}

// RunMaintenance periodically prunes expired suspended transactions.
// It audits expired transactions before deletion by recording expiry events
// to the originating session's audit-vault chain, then deletes them.
func (g *GatewayService) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.runMaintenanceSweep(ctx); err != nil {
				g.logger.Error("Maintenance sweep failed", "error", err)
			}
		}
	}
}

// IsNativeTool checks if a tool name is a native tool compiled into the Operator.
func (g *GatewayService) IsNativeTool(name string) bool {
	return g.isNativeTool(name)
}

// isNativeTool checks if a tool name is a native tool compiled into the Operator.
func (g *GatewayService) isNativeTool(name string) bool {
	if g.nativeToolHandler == nil {
		return false
	}
	_, ok := g.nativeToolHandler.registry.Get(name)
	return ok
}

func (g *GatewayService) isCircuitOpen() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if !g.circuitOpen {
		return false
	}

	// Check if cooldown period has elapsed
	if time.Since(g.lastFailure) > g.cooldownDuration {
		return false // Half-open state implicitly (next request will either succeed or re-open)
	}

	return true
}

func (g *GatewayService) hasDownstream() bool {
	return g.downstreamURL != "" || g.subprocessDownstream != nil
}

func (g *GatewayService) downstreamTarget() string {
	if g.downstreamURL != "" {
		return g.downstreamURL
	}
	if g.subprocessDownstream != nil {
		return g.subprocessDownstream.Command()
	}
	return ""
}

// Close terminates any active downstream subprocess resources.
func (g *GatewayService) Close() error {
	if g.subprocessDownstream != nil {
		return g.subprocessDownstream.Close()
	}
	return nil
}

func (g *GatewayService) recordFailure() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.failureCount++
	g.lastFailure = time.Now()
	if g.failureCount >= g.maxFailures {
		if !g.circuitOpen {
			if g.logger != nil {
				g.logger.Warn("MCP downstream circuit breaker OPENED", "target", g.downstreamTarget(), "failures", g.failureCount)
			}
		}
		g.circuitOpen = true
	}
}

func (g *GatewayService) recordSuccess() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.circuitOpen {
		if g.logger != nil {
			g.logger.Info("MCP downstream circuit breaker CLOSED", "target", g.downstreamTarget())
		}
	}
	g.failureCount = 0
	g.circuitOpen = false
}

func (g *GatewayService) handleA2ARequest(w http.ResponseWriter, r *http.Request, method string, handler func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error)) {
	if r.Method != http.MethodPost {
		g.responder.ProtoJSONCanonical(w, http.StatusMethodNotAllowed,
			governance.NewIngressRefusal(fmt.Errorf("a2a request: %w", constants.ErrMethodNotAllowed)))
		return
	}

	if g.isCircuitOpen() {
		g.logger.Warn("MCP downstream circuit is open, rejecting request", "method", method, "url", g.downstreamURL)
		g.writeDispatchError(w, nil, g.rpcRefusal(-32603, fmt.Errorf("a2a request: %w", constants.ErrGatewayDownstreamUnavailable)))
		return
	}

	// P1-1: Enforce payload limits from config
	r.Body = http.MaxBytesReader(w, r.Body, g.maxPayloadBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			g.writeDispatchError(w, nil, g.rpcRefusal(-32600, fmt.Errorf("a2a request: read body: %w", constants.ErrPayloadExceedsLimit)))
			return
		}
		g.writeDispatchError(w, nil, g.rpcRefusal(-32603, fmt.Errorf("a2a request: read body: %w", constants.ErrRequestBodyRead)))
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		g.writeDispatchError(w, nil, g.rpcRefusal(constants.JSONRPCErrorCodeParseError, fmt.Errorf("a2a request: decode: %w", constants.ErrInvalidJSONBody)))
		return
	}

	// Validate JSON-RPC 2.0
	if req.JSONRPC != "2.0" {
		g.writeDispatchError(w, req.ID, g.rpcRefusal(-32600, fmt.Errorf("a2a request: %w", constants.ErrJSONRPCVersionInvalid)))
		return
	}

	if req.Method == "" {
		g.writeDispatchError(w, req.ID, g.rpcRefusal(-32600, fmt.Errorf("a2a request: %w", constants.ErrJSONRPCMethodRequired)))
		return
	}

	if req.Method != method {
		g.writeDispatchError(w, req.ID, g.rpcRefusal(-32601, fmt.Errorf("a2a request: %w", constants.ErrJSONRPCMethodNotFound)))
		return
	}

	result, dErr := g.wrapDispatch(handler(r.Context(), req.ID, req.Params))
	if dErr != nil {
		g.writeDispatchError(w, req.ID, dErr)
		return
	}

	if result != nil {
		g.responder.RPCResponse(w, req.ID, result)
	}
}

// callTool executes a governed MCP tools/call. It is called by the unified
// Streamable HTTP dispatcher (HandleMCP) via dispatchMCP.
// The *http.Request is used to extract identity headers and the client
// certificate fingerprint when a transaction is suspended for L3 approval.
func (g *GatewayService) callTool(ctx context.Context, r *http.Request, params json.RawMessage) (interface{}, error) {
	var callParams CallToolRequest
	if err := json.Unmarshal(params, &callParams); err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrGatewayInvalidToolArguments)
	}

	if callParams.Name == "" {
		return nil, constants.ErrGatewayToolNameRequired
	}

	argumentsJSON := "{}"
	if len(callParams.Arguments) > 0 {
		var probe interface{}
		if err := json.Unmarshal(callParams.Arguments, &probe); err != nil {
			return nil, constants.ErrGatewayInvalidToolArguments
		}
		argumentsJSON = string(callParams.Arguments)
	}

	executionID, investigationID, err := callIDs(callParams.ExecutionID, callParams.InvestigationID)
	if err != nil {
		return nil, err
	}
	mcpPayload := &operatorv1.McpCallRequested{
		ToolName:      callParams.Name,
		ArgumentsJson: argumentsJSON,
		ExecutionId:   executionID,
	}
	payloadBytes, err := proto.Marshal(mcpPayload)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	hash, envelopeBytes, stateRoot, err := g.processGatewayTransaction(ctx, processGatewayOptions{
		eventType:       constants.Event.Operator.Mcp.CallRequested,
		targetResource:  callParams.Name,
		payloadBytes:    payloadBytes,
		investigationID: investigationID,
	})
	if err != nil {
		return nil, err
	}

	if stateRoot != "" {
		ctx = context.WithValue(ctx, constants.ContextKeyStateMerkleRoot, stateRoot)
	}

	receipt, err := g.envProc.ProcessEnvelope(ctx, envelopeBytes)
	if err != nil {
		if errors.Is(err, governance.ErrL3ProofMissing) {
			suspension, err := g.suspendGovernedCall(ctx, r, hash, envelopeBytes, callParams.Name, callParams.Arguments)
			if err != nil {
				return nil, err
			}
			return CallToolResult{
				Content: []TextContent{
					{
						Type: "text",
						Text: suspension.Message,
					},
				},
				Suspension: suspension,
			}, nil
		}
		return nil, err
	}

	receiptJSON, err := encodeActionReceipt(receipt)
	if err != nil {
		return nil, err
	}
	return ReceiptCallToolResult(receipt, receiptJSON), nil
}

// ReceiptCallToolResult builds the tools/call result of a completed governed call:
// the receipt summary as text, the signed receipt bytes, and IsError unless the
// receipt reports COMPLETED. receiptJSON must be the canonical encoding of receipt.
func ReceiptCallToolResult(receipt *operatorv1.ActionReceipt, receiptJSON json.RawMessage) CallToolResult {
	mcpRes := CallToolResult{
		Content: []TextContent{
			{
				Type: "text",
				Text: receipt.ResultSummary,
			},
		},
		Receipt: receiptJSON,
	}
	if receipt.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		mcpRes.IsError = true
	}
	return mcpRes
}

// handleReadField processes the read_field tool with governed access controls
func (g *GatewayService) handleReadField(ctx context.Context, arguments json.RawMessage) (interface{}, error) {
	if g.fieldPathRegistry == nil {
		return nil, constants.ErrGatewayFieldPathRegistryNotInit
	}

	if g.dbService == nil {
		return nil, constants.ErrGatewayDatabaseServiceNotConfigured
	}

	var req FieldReadRequest
	if err := json.Unmarshal(arguments, &req); err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInvalidJSONBody)
	}

	// Validate required fields
	if req.Collection == "" {
		return nil, constants.ErrGatewayCollectionRequired
	}
	if req.DocumentID == "" {
		return nil, constants.ErrGatewayDocumentIDRequired
	}
	if req.FieldPath == "" {
		return nil, constants.ErrGatewayFieldPathRequired
	}
	if req.OperatorSessionID == "" {
		return nil, constants.ErrGatewayOperatorSessionIDRequired
	}

	// L1: Validate field path against schema registry
	if err := g.fieldPathRegistry.ValidateFieldPath(req.Collection, req.FieldPath); err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	// L3: Validate Operator session
	if g.sessionValidator != nil {
		valid, err := g.sessionValidator.ValidateSession(req.OperatorSessionID)
		if err != nil {
			return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
		}
		if !valid {
			return nil, constants.ErrGatewayOperatorSessionInvalid
		}
	}

	// Extract field value from database
	value, err := g.dbService.GetField(ctx, req.Collection, req.DocumentID, req.FieldPath)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	// L1: Scan field value for forbidden patterns
	if err := g.scanForForbiddenPatterns(value); err != nil {
		return nil, err
	}

	// Audit vault logging
	if g.auditLogger != nil {
		if err := g.auditLogger.LogFieldRead(req.OperatorSessionID, req.Collection, req.DocumentID, req.FieldPath, value); err != nil {
			g.logger.Warn("Failed to log field read to audit vault", "error", err, "collection", req.Collection)
		}
	}

	return CallToolResult{
		Content: []TextContent{
			{
				Type: "text",
				Text: value.String(),
			},
		},
	}, nil
}

// scanForForbiddenPatterns checks if a FieldValue contains security threats
// using the L1 doctrine threat detection system. Delegates to the ThreatScanner
// (governance.L1Doctrine) which uses regex-based word-boundary matching instead
// of the former hardcoded substring patterns.
func (g *GatewayService) scanForForbiddenPatterns(value FieldValue) error {
	if value.Null {
		return nil
	}

	if g.threatScanner == nil {
		return nil
	}

	valueStr := value.String()
	signals := g.threatScanner.AnalyzeCommand(valueStr)
	for _, sig := range signals {
		if sig.BlockRecommended {
			return fmt.Errorf("gateway: L1 hard gate: threat detected (%s): %s: %w", sig.Category, sig.Indicator, constants.ErrGatewayForbiddenPattern)
		}
	}

	return nil
}

// readResource executes a governed MCP resources/read. Shared by the unified
// dispatcher (HandleMCP).
func (g *GatewayService) readResource(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var readParams ReadResourceRequest
	if err := json.Unmarshal(params, &readParams); err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInvalidJSONBody)
	}

	if readParams.URI == "" {
		return nil, constants.ErrGatewayURIRequired
	}

	executionID, err := uuid.NewString()
	if err != nil {
		return nil, err
	}
	mcpPayload := &operatorv1.McpResourceReadRequested{
		Uri:         readParams.URI,
		ExecutionId: executionID,
	}
	payloadBytes, err := proto.Marshal(mcpPayload)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	_, envelopeBytes, stateRoot, err := g.processGatewayTransaction(ctx, processGatewayOptions{
		eventType:      constants.Event.Operator.Mcp.ResourceReadRequested,
		targetResource: readParams.URI,
		payloadBytes:   payloadBytes,
	})
	if err != nil {
		return nil, err
	}

	if stateRoot != "" {
		ctx = context.WithValue(ctx, constants.ContextKeyStateMerkleRoot, stateRoot)
	}

	receipt, err := g.envProc.ProcessEnvelope(ctx, envelopeBytes)
	if err != nil {
		return nil, err
	}

	mcpRes := ReadResourceResult{
		Contents: []TextContent{
			{
				Type: "text",
				Text: receipt.ResultSummary,
			},
		},
	}
	if receipt.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		mcpRes.Contents[0].Text = fmt.Sprintf("Error: %s", receipt.ResultSummary)
	}
	return mcpRes, nil
}

// getPrompt executes a governed MCP prompts/get. Shared by the unified
// dispatcher (HandleMCP).
func (g *GatewayService) getPrompt(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var getParams GetPromptRequest
	if err := json.Unmarshal(params, &getParams); err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInvalidJSONBody)
	}

	if getParams.Name == "" {
		return nil, constants.ErrGatewayNameRequired
	}

	executionID, err := uuid.NewString()
	if err != nil {
		return nil, err
	}
	mcpPayload := &operatorv1.McpPromptGetRequested{
		Name:        getParams.Name,
		ExecutionId: executionID,
	}
	payloadBytes, err := proto.Marshal(mcpPayload)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	_, envelopeBytes, stateRoot, err := g.processGatewayTransaction(ctx, processGatewayOptions{
		eventType:      constants.Event.Operator.Mcp.PromptGetRequested,
		targetResource: getParams.Name,
		payloadBytes:   payloadBytes,
	})
	if err != nil {
		return nil, err
	}

	if stateRoot != "" {
		ctx = context.WithValue(ctx, constants.ContextKeyStateMerkleRoot, stateRoot)
	}

	receipt, err := g.envProc.ProcessEnvelope(ctx, envelopeBytes)
	if err != nil {
		return nil, err
	}

	mcpRes := GetPromptResult{
		Description: receipt.ResultSummary,
		Messages: []PromptMessage{
			{
				Role: string(constants.UserRoleUser),
				Content: TextContent{
					Type: "text",
					Text: receipt.ResultSummary,
				},
			},
		},
	}
	if receipt.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		mcpRes.Description = fmt.Sprintf("Error: %s", receipt.ResultSummary)
	}
	return mcpRes, nil
}

// ExecuteResourceRead is the L5 egress for a verified MCP_RESOURCE_READ.
func (g *GatewayService) ExecuteResourceRead(ctx context.Context, uri string) (string, error) {
	if uri == "" {
		return "", constants.ErrGatewayURIRequired
	}
	raw, err := g.proxyMCPMethod(ctx, "resources/read", map[string]string{"uri": uri})
	if err != nil {
		return "", err
	}
	return mcpResultSummary(raw), nil
}

// ExecutePromptGet is the L5 egress for a verified MCP_PROMPT_GET.
func (g *GatewayService) ExecutePromptGet(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", constants.ErrGatewayNameRequired
	}
	raw, err := g.proxyMCPMethod(ctx, "prompts/get", map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	return mcpResultSummary(raw), nil
}

// ExecuteResourceList is the L5 egress for a verified MCP_RESOURCE_LIST.
func (g *GatewayService) ExecuteResourceList(ctx context.Context) (string, error) {
	res, err := g.listResourcesResult(ctx)
	if err != nil {
		return "", err
	}
	return marshalMCPCatalog(res)
}

// ExecutePromptList is the L5 egress for a verified MCP_PROMPT_LIST.
func (g *GatewayService) ExecutePromptList(ctx context.Context) (string, error) {
	res, err := g.listPromptsResult(ctx)
	if err != nil {
		return "", err
	}
	return marshalMCPCatalog(res)
}

func marshalMCPCatalog(res interface{}) (string, error) {
	if raw, ok := res.(json.RawMessage); ok {
		return mcpResultSummary(raw), nil
	}
	body, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
	}
	return mcpResultSummary(body), nil
}

func mcpResultSummary(raw json.RawMessage) string {
	summary := strings.TrimSpace(string(raw))
	if summary == "" || summary == "null" {
		return "completed"
	}
	if len(summary) > constants.ReceiptSummaryMaxBytes {
		return summary[:constants.ReceiptSummaryMaxBytes]
	}
	return summary
}

type processGatewayOptions struct {
	eventType       constants.EventType
	targetResource  string
	payloadBytes    []byte
	investigationID string
}

func (g *GatewayService) processGatewayTransaction(ctx context.Context, opts processGatewayOptions) (hash string, envelopeBytes []byte, stateRoot string, err error) {
	stateRoot = ""
	if g.stateRootProvider != nil {
		var err error
		stateRoot, err = g.stateRootProvider.GetCurrentStateRoot(ctx)
		if err != nil {
			g.logger.Warn("Failed to get current state root", "error", err)
		}
	}

	actionType, err := constants.ValidateGovernedRequest(opts.eventType)
	if err != nil {
		return "", nil, "", fmt.Errorf("gateway: %w", err)
	}

	now := time.Now().UTC()
	nonce, err := uuid.NewString()
	if err != nil {
		return "", nil, "", err
	}
	env := &commonv1.GovernanceEnvelope{
		Timestamp:       timestamppb.New(now),
		ExpiresAt:       timestamppb.New(now.Add(5 * time.Minute)),
		SourceComponent: commonv1.Component_COMPONENT_CLIENT,
		OperatorId:      string(constants.DocIDEmbeddedOperator),
		EventType:       string(opts.eventType),
		ActionType:      string(actionType),
		TargetResource:  opts.targetResource,
		Payload:         opts.payloadBytes,
		ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
		Nonce:           nonce,
		StateMerkleRoot: stateRoot,
		Posture:         string(g.posture),
		Governance:      &commonv1.GovernanceMetadata{},
	}
	if opts.investigationID != "" {
		env.InvestigationId = opts.investigationID
	}

	// Enrich from context if present
	if tenantID, ok := ctx.Value(constants.ContextKeyTenantID).(string); ok {
		env.TenantId = tenantID
	}
	if persona, ok := ctx.Value(constants.ContextKeyBindingPersona).(string); ok {
		env.BindingPersona = persona
	}
	// Bind both the app identity and the human requestor to the envelope.
	// For delegated credentials the auth middleware extracts both SANs from the cert
	// and places them in context — no trusted headers.
	if appID, ok := ctx.Value(constants.ContextKeyAppID).(string); ok && appID != "" {
		// App identity stays in acting_app_id. The app has no Operator session of
		// its own; the envelope targets the embedded Operator that executes it, so
		// its approval, expiry, and execution evidence is recorded on that
		// Operator's session chain. An unclaimed embedded Operator has no session
		// and the call fails closed.
		embeddedSessionID, err := g.embeddedSession.OperatorSessionID(ctx)
		if err != nil {
			return "", nil, "", fmt.Errorf("gateway: resolve embedded operator session for app %s: %w", appID, err)
		}
		env.ActingAppId = appID
		env.OperatorSessionId = embeddedSessionID
	} else {
		if opSessionID, ok := ctx.Value(constants.ContextKeyOperatorSessionID).(string); ok && opSessionID != "" {
			env.OperatorSessionId = opSessionID
		}
	}
	if userID, ok := ctx.Value(constants.ContextKeyUserID).(string); ok && userID != "" {
		env.RequestorUserId = userID
	}

	hash, err = govpkg.GenerateMessageID(env)
	if err != nil {
		return "", nil, "", fmt.Errorf("gateway: %w", constants.ErrInternal)
	}
	env.Id = hash
	env.TransactionHash = hash

	envelopeBytes, err = (protojson.MarshalOptions{Multiline: false}).Marshal(env)
	if err != nil {
		return "", nil, "", fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	// Under a posture that enforces L2 for this action (consensus and notary; bootstrap
	// actions are exempt via governance.L2Enforced), send the envelope to the Consensus
	// for L2 deliberation before dispatch. The Consensus collects signed votes from its
	// members and returns the envelope with L2 metadata populated. If the deliberator is
	// not configured, the envelope proceeds without L2 votes and will fail-closed at L4
	// verification.
	if governance.L2Enforced(actionType, g.posture) && g.l2ConsensusDeliberator != nil {
		deliberatedBytes, err := g.l2ConsensusDeliberator.Deliberate(ctx, envelopeBytes)
		if err != nil {
			g.logger.Error("L2 consensus deliberation failed", "tx_hash", hash, "error", err)
			return "", nil, "", fmt.Errorf("gateway: l2 consensus deliberation: %w", err)
		}
		envelopeBytes = deliberatedBytes
	}

	return hash, envelopeBytes, stateRoot, nil
}

// @Summary		A2A call
// @Description	Calls an A2A agent endpoint
// @Tags			a2a
// @Accept			json
// @Produce		json
// @Success		200	{object}	mcp.A2ASuccessResponse,mcp.GovernedSuspension
// @Router			/api/v1/a2a/call [post]
func (g *GatewayService) HandleA2aCall(w http.ResponseWriter, r *http.Request) {
	if g.envProc == nil {
		g.writeDispatchError(w, nil, g.rpcRefusal(-32603, fmt.Errorf("a2a call: %w", constants.ErrGatewayNotReady)))
		return
	}
	g.handleA2ARequest(w, r, "a2a/call", func(ctx context.Context, id interface{}, params json.RawMessage) (interface{}, error) {
		return g.a2aCall(ctx, r, params)
	})
}

// callIDs returns the caller's execution and investigation IDs, minting any
// that are absent, so every governed call is attributable in its receipt and
// approval reference.
func callIDs(executionID, investigationID string) (string, string, error) {
	if executionID == "" {
		id, err := uuid.NewString()
		if err != nil {
			return "", "", err
		}
		executionID = id
	}
	if investigationID == "" {
		id, err := uuid.NewString()
		if err != nil {
			return "", "", err
		}
		investigationID = id
	}
	return executionID, investigationID, nil
}

// a2aCall executes a governed A2A skill invocation. Shared by the A2A REST
// handler (HandleA2aCall) and the unified dispatcher (HandleMCP).
func (g *GatewayService) a2aCall(ctx context.Context, r *http.Request, params json.RawMessage) (interface{}, error) {
	var req A2ACallRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInvalidJSONBody)
	}

	if req.SkillName == "" {
		return nil, constants.ErrGatewaySkillNameRequired
	}

	payloadStr := "{}"
	if len(req.Payload) > 0 {
		payloadStr = string(req.Payload)
	}

	executionID, investigationID, err := callIDs(req.ExecutionID, "")
	if err != nil {
		return nil, err
	}
	a2aPayload := &operatorv1.A2ACallRequested{
		SkillName:   req.SkillName,
		PayloadJson: payloadStr,
		ExecutionId: executionID,
	}
	payloadBytes, err := proto.Marshal(a2aPayload)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	hash, envelopeBytes, stateRoot, err := g.processGatewayTransaction(ctx, processGatewayOptions{
		eventType:       constants.Event.Operator.A2a.CallRequested,
		targetResource:  req.SkillName,
		payloadBytes:    payloadBytes,
		investigationID: investigationID,
	})
	if err != nil {
		return nil, err
	}

	if stateRoot != "" {
		ctx = context.WithValue(ctx, constants.ContextKeyStateMerkleRoot, stateRoot)
	}

	receipt, err := g.envProc.ProcessEnvelope(ctx, envelopeBytes)
	if err != nil {
		if errors.Is(err, governance.ErrL3ProofMissing) {
			suspension, err := g.suspendGovernedCall(ctx, r, hash, envelopeBytes, req.SkillName, req.Payload)
			if err != nil {
				return nil, err
			}
			return suspension, nil
		}
		return nil, err
	}

	receiptJSON, err := encodeActionReceipt(receipt)
	if err != nil {
		return nil, err
	}
	return A2ASuccessResponse{
		ID:     hash,
		Result: receiptJSON,
	}, nil
}

// suspendGovernedCall stores a call that awaits L3 approval and returns the typed
// suspension shared by MCP tools/call and A2A a2a/call.
func (g *GatewayService) suspendGovernedCall(ctx context.Context, r *http.Request, hash string, envelopeBytes []byte, targetResource string, arguments json.RawMessage) (*GovernedSuspension, error) {
	userID, _ := r.Context().Value(constants.ContextKeyUserID).(string)
	operatorID, _ := r.Context().Value(constants.ContextKeyAppID).(string)
	certFingerprint := extractCertFingerprint(r)

	if err := g.StoreSuspendedTransaction(ctx, hash, envelopeBytes, targetResource, arguments, userID, operatorID, certFingerprint); err != nil {
		return nil, err
	}

	approvalURL := g.publicBaseURL + constants.APIPaths.ApprovePagePrefix + hash
	return &GovernedSuspension{
		ID:          hash,
		Status:      string(constants.GatewayResponseStatusSuspended),
		TxHash:      hash,
		ApprovalURL: approvalURL,
		Message:     approvalPausedMessage(g.posture, approvalURL),
	}, nil
}

// encodeActionReceipt renders the executing Operator's signed receipt as canonical
// protojson, the same bytes the direct and dispatch ingresses write. A missing
// receipt is an error: a governed success never returns without its receipt.
func encodeActionReceipt(receipt *operatorv1.ActionReceipt) (json.RawMessage, error) {
	if receipt == nil {
		return nil, fmt.Errorf("gateway: encode action receipt: %w", constants.ErrInternal)
	}
	receiptJSON, err := response.MarshalProtoJSONCanonical(receipt)
	if err != nil {
		return nil, fmt.Errorf("gateway: encode action receipt: %w", err)
	}
	return receiptJSON, nil
}

// mapGatewayError maps governance verification errors to granular JSON-RPC codes.
func (g *GatewayService) mapGatewayError(err error) int {
	if err == nil {
		return 0
	}

	switch {
	case errors.Is(err, governance.ErrInvalidEnvelope),
		errors.Is(err, governance.ErrTransactionIDMissing),
		errors.Is(err, governance.ErrPayloadMissing),
		errors.Is(err, governance.ErrUnknownActionType):
		return constants.ErrCodeInvalidEnvelope

	case errors.Is(err, governance.ErrPayloadDecodeFailed):
		return constants.ErrCodePayloadDecodeFailed

	case errors.Is(err, governance.ErrTransactionHashMissing),
		errors.Is(err, governance.ErrTransactionHashMismatch):
		return constants.ErrCodeHashMismatch

	case errors.Is(err, governance.ErrTransactionExpired):
		return constants.ErrCodeExpired

	case errors.Is(err, governance.ErrTransactionReplay):
		return constants.ErrCodeReplay

	case errors.Is(err, governance.ErrStateRootMissing),
		errors.Is(err, governance.ErrStateRootRequired),
		errors.Is(err, governance.ErrStateRootMismatch):
		return constants.ErrCodeStateMismatch

	case errors.Is(err, governance.ErrL1ValidationFailed):
		return constants.ErrCodeL1ValidationFailed

	case errors.Is(err, governance.ErrL2SignatureMissing),
		errors.Is(err, governance.ErrL2SignatureInvalid),
		errors.Is(err, governance.ErrL2ConsensusNotConfigured),
		errors.Is(err, governance.ErrL2ConsensusSetUnknown),
		errors.Is(err, governance.ErrL2QuorumNotMet),
		errors.Is(err, governance.ErrL2DuplicateSigner):
		return constants.ErrCodeL2SignatureInvalid

	case errors.Is(err, governance.ErrL3ProofInvalid),
		errors.Is(err, governance.ErrL3NotaryNotConfigured):
		return constants.ErrCodeL3ProofInvalid
	}

	// Map other Gateway errors back to JSON-RPC error
	return -32603
}

// extractCertFingerprint extracts the SHA-256 fingerprint of the client certificate from an HTTP request.
// Returns empty string if no valid certificate is present.
func extractCertFingerprint(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	cert := r.TLS.PeerCertificates[0]
	hash := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(hash[:])
}

func (g *GatewayService) storeSuspendedTransaction(ctx context.Context, tx *models.SuspendedTransaction) error {
	if g.suspendedStore == nil {
		return fmt.Errorf("gateway: store suspended transaction %s: %w", tx.TransactionHash, constants.ErrSuspendedStoreRequired)
	}
	if err := g.suspendedStore.StoreSuspendedTransaction(ctx, tx); err != nil {
		return fmt.Errorf("gateway: store suspended transaction %s: %w", tx.TransactionHash, err)
	}

	// Emit approval.requested to the targeted Operator session's audit chain.
	// This is the opening bookend for the approval lifecycle.
	event := &storage.Event{
		OperatorSessionID: tx.OperatorSessionID,
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.Notary.ApprovalRequested,
		ContentText:       fmt.Sprintf("Transaction %s approval requested (tool: %s, expires at: %s)", tx.TransactionHash, tx.ToolName, tx.ExpiresAt.Format(time.RFC3339)),
	}
	if _, err := g.auditStore.RecordEvent(event); err != nil {
		auditErr := fmt.Errorf("gateway: record approval requested for transaction %s on session %s: %w", tx.TransactionHash, tx.OperatorSessionID, err)
		if delErr := g.suspendedStore.DeleteSuspendedTransaction(ctx, tx.TransactionHash); delErr != nil {
			return errors.Join(auditErr, fmt.Errorf("gateway: roll back suspended transaction %s: %w", tx.TransactionHash, delErr))
		}
		return auditErr
	}
	return nil
}

// StoreSuspendedTransaction stores a transaction awaiting L3 approval and records
// the approval.requested event on the audit chain of the Operator session the
// envelope targets. The suspension fails closed: an envelope without an Operator
// session cannot be suspended, and if the approval.requested event cannot be
// recorded the stored transaction is removed again, so no approval is ever
// pending without its opening audit record.
func (g *GatewayService) StoreSuspendedTransaction(ctx context.Context, txHash string, envelope []byte, toolName string, toolArgs json.RawMessage, userID, operatorID string, certFingerprint string) error {
	if g.suspendedStore == nil {
		return fmt.Errorf("gateway: store suspended transaction %s: %w", txHash, constants.ErrSuspendedStoreRequired)
	}
	env := &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal(envelope, env); err != nil {
		return fmt.Errorf("gateway: store suspended transaction %s: decode envelope: %w", txHash, err)
	}
	cliSessionID, _ := ctx.Value(constants.ContextKeyCLISessionID).(string)
	now := time.Now().UTC()
	tx := &models.SuspendedTransaction{
		TransactionHash:         txHash,
		Envelope:                json.RawMessage(envelope),
		CreatedAt:               now,
		ExpiresAt:               now.Add(ApprovalRequestTTL),
		ToolName:                toolName,
		ToolArguments:           toolArgs,
		UserID:                  userID,
		OperatorID:              operatorID,
		OperatorSessionID:       env.GetOperatorSessionId(),
		SubmitterCLISessionID:   cliSessionID,
		ExpectedCertFingerprint: certFingerprint,
	}
	return g.storeSuspendedTransaction(ctx, tx)
}

// SuspendGovernanceEnvelope suspends a GovernanceEnvelope awaiting L3 human approval
// and returns the typed suspension metadata including the console approval URL.
func (g *GatewayService) SuspendGovernanceEnvelope(ctx context.Context, envelope *commonv1.GovernanceEnvelope) (models.PlatformEnrollmentL3SuspensionResponse, error) {
	if envelope == nil || strings.TrimSpace(envelope.GetTransactionHash()) == "" || strings.TrimSpace(envelope.GetOperatorSessionId()) == "" {
		return models.PlatformEnrollmentL3SuspensionResponse{}, fmt.Errorf("gateway: suspend governance envelope: %w", constants.ErrMissingRequiredField)
	}
	rawEnvelope, err := (protojson.MarshalOptions{Multiline: false}).Marshal(envelope)
	if err != nil {
		return models.PlatformEnrollmentL3SuspensionResponse{}, fmt.Errorf("gateway: marshal suspended governance envelope %s: %w", envelope.GetTransactionHash(), err)
	}
	userID := strings.TrimSpace(envelope.GetRequestorUserId())
	if userID == "" {
		userID, _ = ctx.Value(constants.ContextKeyUserID).(string)
	}
	cliSessionID := strings.TrimSpace(envelope.GetCliSessionId())
	if cliSessionID == "" {
		cliSessionID, _ = ctx.Value(constants.ContextKeyCLISessionID).(string)
	}
	toolName := strings.TrimSpace(envelope.GetTargetResource())
	if toolName == "" {
		toolName = strings.TrimSpace(envelope.GetActionType())
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ApprovalRequestTTL)
	txHash := envelope.GetTransactionHash()
	tx := &models.SuspendedTransaction{
		TransactionHash:       txHash,
		Envelope:              json.RawMessage(rawEnvelope),
		CreatedAt:             now,
		ExpiresAt:             expiresAt,
		ToolName:              toolName,
		ToolArguments:         json.RawMessage(envelope.GetPayload()),
		UserID:                userID,
		OperatorID:            strings.TrimSpace(envelope.GetOperatorId()),
		OperatorSessionID:     strings.TrimSpace(envelope.GetOperatorSessionId()),
		SubmitterCLISessionID: cliSessionID,
	}
	if err := g.storeSuspendedTransaction(ctx, tx); err != nil {
		return models.PlatformEnrollmentL3SuspensionResponse{}, err
	}
	approvalURL := g.publicBaseURL + constants.APIPaths.ApprovePagePrefix + txHash
	return models.PlatformEnrollmentL3SuspensionResponse{
		Status:      models.PlatformEnrollmentStatusL3Required,
		Posture:     g.posture,
		TxHash:      txHash,
		ApprovalURL: approvalURL,
		ExpiresAt:   expiresAt,
		Message:     g.posture.HumanApprovalReason(),
	}, nil
}

// GetSuspendedTransaction retrieves a suspended transaction by hash.
func (g *GatewayService) GetSuspendedTransaction(ctx context.Context, txHash string) (*models.SuspendedTransaction, bool, error) {
	if g.suspendedStore == nil {
		return nil, false, nil
	}
	return g.suspendedStore.GetSuspendedTransaction(ctx, txHash)
}

// DeleteSuspendedTransaction removes a suspended transaction after approval/rejection.
func (g *GatewayService) DeleteSuspendedTransaction(ctx context.Context, txHash string) {
	if g.suspendedStore == nil {
		return
	}
	if err := g.suspendedStore.DeleteSuspendedTransaction(ctx, txHash); err != nil {
		g.logger.Error("Failed to delete suspended transaction", "tx_hash", txHash, "error", err)
	}
}

// ResumeWithL3Proof re-submits a suspended transaction with an attached L3
// WebAuthn proof through the ztd Gateway. The proof is verified
// inside the Gateway's TransactionVerifier - this method does not perform
// independent passkey validation, it only re-wires the envelope and calls
// the same fail-closed entry point used for primary submission.
//
// The signed receipt returned by the Gateway is forwarded to the caller so
// the OOB approval UI can surface the downstream tool result to the user.
func (g *GatewayService) ResumeWithL3Proof(ctx context.Context, txHash, userID string, proof *commonv1.L3Proof) (*operatorv1.ActionReceipt, error) {
	if proof == nil {
		return nil, constants.ErrGatewayL3ProofRequired
	}
	tx, err := g.claimSuspended(ctx, txHash, userID)
	if err != nil {
		return nil, err
	}

	receipt, procErr := g.resubmitSuspended(ctx, tx, &commonv1.L3Metadata{Proof: proof})
	if procErr != nil {
		// Keep the suspension in place so the user can retry the proof
		// without re-issuing the upstream MCP call: release the claim.
		return receipt, g.releaseAfterFailedResume(ctx, txHash, procErr)
	}

	// Successful execution - remove from the suspension list. The transaction ran, so a
	// failed delete is returned with the receipt instead of being logged: the caller must
	// know the pending row is still listed. The row stays claimed, so the expiry sweep
	// deletes it without ever recording it as expired.
	if err := g.suspendedStore.DeleteSuspendedTransaction(ctx, txHash); err != nil {
		return receipt, fmt.Errorf("gateway: transaction %s executed but its suspended record was not removed: %w", txHash, err)
	}
	return receipt, nil
}

// DenySuspendedTransaction ends a suspended transaction the human declined. The
// envelope is re-submitted with the L3 deny marker, so the executing Operator's L4
// rejects it with L3_DENIED and its L5 signs the rejection receipt. The deny is
// therefore recorded on the executing runtime's own ledger, never only as a
// Gateway-side delete, and the reserved nonce means the transaction can never run.
//
// Any signed governance rejection ends the suspension and is returned: a deny that
// L4 rejected for another reason first (an expired envelope) still cannot execute.
// A failure that produced no signed rejection releases the claim, so the deny can be
// retried. A deny that executed is a platform defect and fails closed.
func (g *GatewayService) DenySuspendedTransaction(ctx context.Context, txHash, userID string) (*operatorv1.ActionReceipt, error) {
	tx, err := g.claimSuspended(ctx, txHash, userID)
	if err != nil {
		return nil, err
	}

	receipt, procErr := g.resubmitSuspended(ctx, tx, &commonv1.L3Metadata{Denied: true})
	if procErr == nil {
		return receipt, fmt.Errorf("gateway: deny transaction %s: %w", txHash, constants.ErrL3DenyNotRejected)
	}
	if !governance.IsGovernanceRejectionReceipt(receipt) {
		return nil, g.releaseAfterFailedResume(ctx, txHash, fmt.Errorf("gateway: deny transaction %s: %w", txHash, procErr))
	}
	if err := g.suspendedStore.DeleteSuspendedTransaction(ctx, txHash); err != nil {
		return receipt, fmt.Errorf("gateway: transaction %s denied but its suspended record was not removed: %w", txHash, err)
	}
	return receipt, nil
}

// claimSuspended loads a suspended transaction and claims it for one resume or
// deny. The claim keeps the row in place (L3 verification reads it) but guarantees
// that a second resume or deny cannot submit the same transaction, and that the
// expiry sweep never records transaction.expired for a transaction that reached L4,
// even if the later delete fails. Deleting before submission is not possible: the
// outbound L3 verifier reads the row during ProcessEnvelope.
func (g *GatewayService) claimSuspended(ctx context.Context, txHash, userID string) (*models.SuspendedTransaction, error) {
	if g.envProc == nil {
		return nil, constants.ErrGatewayNotReady
	}
	if userID == "" {
		return nil, constants.ErrGatewayUserIDRequired
	}
	if g.suspendedStore == nil {
		return nil, fmt.Errorf("gateway: resume suspended transaction %s: %w", txHash, constants.ErrSuspendedStoreRequired)
	}

	tx, ok, err := g.GetSuspendedTransaction(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("gateway: resume suspended transaction %s: %w", txHash, err)
	}
	if !ok {
		// The maintenance sweep now owns expiry event recording.
		// A resume cannot positively confirm the not-found reason
		// (expired vs never-existed vs already-decided), so it returns
		// ErrTransactionExpired without writing to the audit vault.
		return nil, fmt.Errorf("gateway: %w", constants.ErrTransactionExpired)
	}

	if err := g.suspendedStore.ClaimSuspendedTransaction(ctx, txHash); err != nil {
		if errors.Is(err, constants.ErrSuspendedTransactionNotFound) {
			return nil, fmt.Errorf("gateway: %w", constants.ErrTransactionExpired)
		}
		return nil, fmt.Errorf("gateway: claim suspended transaction %s: %w", txHash, err)
	}
	return tx, nil
}

// resubmitSuspended re-submits a claimed suspended envelope with l3 attached. The
// L3 metadata is outside the transaction hash, so the hashed fields are untouched.
func (g *GatewayService) resubmitSuspended(ctx context.Context, tx *models.SuspendedTransaction, l3 *commonv1.L3Metadata) (*operatorv1.ActionReceipt, error) {
	env := &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal([]byte(tx.Envelope), env); err != nil {
		return nil, fmt.Errorf("gateway: decode suspended envelope %s: %w", tx.TransactionHash, err)
	}
	if env.Governance == nil {
		env.Governance = &commonv1.GovernanceMetadata{}
	}
	env.Governance.L3 = l3

	resubmitted, err := (protojson.MarshalOptions{Multiline: false}).Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("gateway: encode resubmitted envelope %s: %w", tx.TransactionHash, err)
	}

	// The suspended envelope is Gateway-built, so it keeps its admission
	// snapshot (INV-GOV-WARD-04): writes while it awaited approval, including
	// the passkey sign-in and the L3 credential-use record, must not invalidate it.
	if env.StateMerkleRoot != "" {
		ctx = context.WithValue(ctx, constants.ContextKeyStateMerkleRoot, env.StateMerkleRoot)
	}
	return g.envProc.ProcessEnvelope(ctx, resubmitted)
}

// releaseAfterFailedResume clears the execution claim of a transaction that did not run so
// the approval can be retried. A failed release is joined to cause: the transaction then
// stays claimed and cannot be resumed again, and the caller sees both errors.
func (g *GatewayService) releaseAfterFailedResume(ctx context.Context, txHash string, cause error) error {
	if err := g.suspendedStore.ReleaseSuspendedTransaction(context.WithoutCancel(ctx), txHash); err != nil {
		return errors.Join(cause, fmt.Errorf("gateway: release suspended transaction %s: %w", txHash, err))
	}
	return cause
}

// DispatchToDownstream forwards a verified MCP tool call to the downstream MCP server.
// This implements the Actuator Egress phase for MCP protocol translation.
// Native tools are executed locally so every call — native or downstream — passes through
// the full L1-L5 governance pipeline and produces a signed receipt.
func (g *GatewayService) DispatchToDownstream(ctx context.Context, toolName string, toolArgs json.RawMessage, operatorSessionID string) (string, error) {
	// Handle read_field tool locally (JIT field resolution)
	if toolName == "read_field" {
		result, err := g.handleReadField(ctx, toolArgs)
		if err != nil {
			return "", err
		}
		// Extract text content for summary
		callResult, ok := result.(CallToolResult)
		if !ok {
			return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
		}
		var sb strings.Builder
		for _, c := range callResult.Content {
			if c.Type == "text" {
				sb.WriteString(c.Text)
				sb.WriteString("\n")
			}
		}
		summary := strings.TrimRight(sb.String(), "\n")
		if summary == "" {
			summary = "completed"
		}
		return g.scrubToolResult(ctx, summary)
	}

	if g.isNativeTool(toolName) && g.nativeToolHandler != nil {
		result, err := g.nativeToolHandler.HandleTool(ctx, toolName, toolArgs)
		if err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, c := range result.Content {
			if c.Type == "text" {
				sb.WriteString(c.Text)
				sb.WriteString("\n")
			}
		}
		summary := strings.TrimRight(sb.String(), "\n")
		if summary == "" {
			summary = "completed"
		}

		return g.scrubToolResult(ctx, summary)
	}

	if !g.hasDownstream() {
		return "", constants.ErrGatewayNoDownstreamConfigured
	}

	if g.isCircuitOpen() {
		return "", constants.ErrGatewayDownstreamUnavailable
	}

	// Construct MCP tools/call request with proper params envelope.
	// The MCP tools/call protocol requires Params: {"name": toolName, "arguments": toolArgs}.
	// Sending raw toolArgs as Params causes downstream servers to fail tool name lookup.
	mcpParams, err := json.Marshal(CallToolRequest{
		Name:      toolName,
		Arguments: toolArgs,
	})
	if err != nil {
		return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	var resultRaw json.RawMessage

	if g.subprocessDownstream != nil {
		raw, err := g.subprocessDownstream.Call(ctx, "tools/call", json.RawMessage(mcpParams))
		if err != nil {
			g.recordFailure()
			return "", fmt.Errorf("gateway: %w", constants.ErrGatewayDownstreamUnavailable)
		}
		g.recordSuccess()
		resultRaw = raw
	} else {
		mcpReq := &response.JSONRPCRequest{
			JSONRPC: "2.0",
			Method:  "tools/call",
			Params:  mcpParams,
			ID:      1,
		}

		reqBody, err := json.Marshal(mcpReq)
		if err != nil {
			return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
		}

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Post(g.downstreamURL, "application/json", strings.NewReader(string(reqBody)))
		if err != nil {
			g.recordFailure()
			return "", fmt.Errorf("gateway: %w", constants.ErrGatewayDownstreamUnavailable)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				g.logger.Error("Failed to close response body", "error", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode >= 500 {
				g.recordFailure()
			}
			return "", fmt.Errorf("gateway: %w", constants.ErrGatewayDownstreamHTTPError)
		}

		g.recordSuccess()

		// Parse MCP response
		var mcpResp JSONRPCResponse
		if err := json.NewDecoder(resp.Body).Decode(&mcpResp); err != nil {
			return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
		}

		if mcpResp.Error != nil {
			return "", fmt.Errorf("gateway: %w", constants.ErrGatewayMCPError)
		}

		resultRaw = mcpResp.Result
	}

	// Extract result from MCP response
	var callResult CallToolResult
	if err := json.Unmarshal(resultRaw, &callResult); err != nil {
		return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	// Concatenate text content for result summary
	var summary strings.Builder
	for _, content := range callResult.Content {
		if content.Type == "text" {
			summary.WriteString(content.Text)
			summary.WriteString("\n")
		}
	}

	resultSummary := summary.String()
	if resultSummary == "" {
		resultSummary = "completed"
	}

	// Scrub downstream MCP output before it leaves this layer. There is no unscrubbed
	// path: with no active scrubber the call fails closed and the output is dropped.
	resultSummary, err = g.scrubToolResult(ctx, resultSummary)
	if err != nil {
		return "", err
	}

	// Audit downstream MCP call execution. The call has already run, so a record failure
	// marks the result FAILED instead of returning an output with no audit record behind it.
	event := &storage.Event{
		OperatorSessionID: operatorSessionID,
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.Audit.McpCall,
		ContentText:       toolName,
		CommandRaw:        string(toolArgs),
		CommandStdout:     resultSummary,
	}
	if _, err := g.auditStore.RecordEvent(event); err != nil {
		return "", fmt.Errorf("gateway: record downstream MCP call %s on session %s: %w: %w", toolName, operatorSessionID, constants.ErrGatewayDownstreamAuditFailed, err)
	}

	return resultSummary, nil
}

// scrubToolResult is the egress scrub for every tool result DispatchToDownstream returns:
// read_field, native tools and downstream MCP output. It fails closed with
// constants.ErrEgressScrubberUnavailable when the scrubber is nil or disabled, so raw
// output never leaves this layer.
func (g *GatewayService) scrubToolResult(ctx context.Context, text string) (string, error) {
	if g.scrubbingService == nil || !g.scrubbingService.IsEnabled() {
		return "", fmt.Errorf("gateway: scrub tool result: %w", constants.ErrEgressScrubberUnavailable)
	}
	return g.scrubbingService.ScrubText(ctx, text), nil
}

// DispatchToA2ADownstream forwards a verified A2A call to the downstream A2A server.
func (g *GatewayService) DispatchToA2ADownstream(ctx context.Context, skillName string, payload json.RawMessage) (string, error) {
	if g.a2aDownstreamURL == "" {
		return "", constants.ErrGatewayNoA2ADownstreamConfigured
	}

	// A2A shares the MCP downstream circuit breaker state and cooldown.
	if g.isCircuitOpen() {
		return "", constants.ErrGatewayDownstreamUnavailable
	}

	// Construct A2A request
	a2aReq := A2ADownstreamRequest{
		SkillName: skillName,
		Payload:   payload,
	}

	reqBody, err := json.Marshal(a2aReq)
	if err != nil {
		return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(g.a2aDownstreamURL, "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		g.recordFailure()
		return "", fmt.Errorf("gateway: %w", constants.ErrGatewayDownstreamUnavailable)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			g.logger.Error("Failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			g.recordFailure()
		}
		return "", fmt.Errorf("gateway: %w", constants.ErrGatewayDownstreamHTTPError)
	}

	g.recordSuccess()

	// Parse A2A response
	var a2aResp struct {
		Result  string `json:"result"`
		Error   string `json:"error,omitempty"`
		Summary string `json:"summary,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&a2aResp); err != nil {
		return "", fmt.Errorf("gateway: %w", constants.ErrInternal)
	}

	if a2aResp.Error != "" {
		return "", fmt.Errorf("gateway: %w", constants.ErrGatewayA2AError)
	}

	if a2aResp.Summary != "" {
		return a2aResp.Summary, nil
	}

	if a2aResp.Result != "" {
		return a2aResp.Result, nil
	}

	return "completed", nil
}
