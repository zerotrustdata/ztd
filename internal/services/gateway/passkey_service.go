// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/zerotrustdata/ztd/internal/constants"
	gov "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/uuid"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	passkeyChallengeTTL = 5 * time.Minute
	challengeBytes      = 32
)

// MCPServiceProvider provides access to suspended transaction operations needed
// by the approval handlers. This interface is satisfied by *mcp.GatewayService.
type MCPServiceProvider interface {
	GetSuspendedTransaction(ctx context.Context, txHash string) (*models.SuspendedTransaction, bool, error)
	ResumeWithL3Proof(ctx context.Context, txHash, userID string, proof *commonv1.L3Proof) (*operatorv1.EventReceipt, error)
	DenySuspendedTransaction(ctx context.Context, txHash, userID string) (*operatorv1.EventReceipt, error)
}

// userStore defines the interface for user storage operations.
type userStore interface {
	GetUser(ctx context.Context, userID string) (*models.User, error)
	UpdateUser(ctx context.Context, userID string, user *models.User) error
	CreateUser(ctx context.Context) (*models.User, error)
	HasAnyUsers(ctx context.Context) (bool, error)
}

// webauthnCeremonyStore defines the interface for WebAuthn ceremony storage.
type webauthnCeremonyStore interface {
	StoreCeremony(ctx context.Context, userID string, session *webauthn.SessionData) error
	GetCeremony(ctx context.Context, userID string) (*webauthn.SessionData, error)
	DeleteCeremony(ctx context.Context, userID string) error
}

// webauthnClient defines the interface for WebAuthn operations.
type webauthnClient interface {
	BeginRegistration(user webauthn.User) (*protocol.CredentialCreation, *webauthn.SessionData, error)
	FinishRegistration(user webauthn.User, session webauthn.SessionData, r *http.Request) (*webauthn.Credential, error)
	BeginLogin(user webauthn.User) (*protocol.CredentialAssertion, *webauthn.SessionData, error)
	ValidateLogin(user webauthn.User, session webauthn.SessionData, parsedResponse *protocol.ParsedCredentialAssertionData) (*webauthn.Credential, error)
}

// PasskeyService handles L3 proof brokerage for passkey/WebAuthn operations.
// This moves the L3 authorization from client into ztdo as the sovereign authority.
type PasskeyService struct {
	userStore     userStore
	ceremonyStore webauthnCeremonyStore
	webauthn      webauthnClient
	logger        *slog.Logger
	rpID          string
	rpName        string
	rpOrigins     []string
}

// PasskeyConfig holds configuration for passkey operations.
type PasskeyConfig struct {
	RpID      string
	RpName    string
	RpOrigins []string
	HTTPPort  int
	HTTPSPort int
}

// encodeCredID encodes a WebAuthn credential ID (raw bytes) as a base64 URL string.
// This is the canonical encoding used across all passkey transports.
func encodeCredID(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}

// encodeChallenge encodes arbitrary challenge bytes (e.g. transaction hashes) as
// base64 URL. Semantically distinct from encodeCredID but uses the same encoding.
func encodeChallenge(challenge []byte) string {
	return base64.RawURLEncoding.EncodeToString(challenge)
}

// dbUserStore implements userStore using DocumentStoreService.
type dbUserStore struct {
	db *DocumentStoreService
}

func (s *dbUserStore) GetUser(ctx context.Context, userID string) (*models.User, error) {
	doc, err := s.db.DocGet(ctx, marshaler.CollectionName(constants.CollectionUsers), userID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, nil
	}

	var user models.User
	if err := doc.DecodeData(&user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}
	user.ID = doc.ID
	return &user, nil
}

func (s *dbUserStore) UpdateUser(ctx context.Context, userID string, user *models.User) error {
	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}
	_, err = s.db.DocUpdate(ctx, marshaler.CollectionName(constants.CollectionUsers), userID, data)
	return err
}

func (s *dbUserStore) CreateUser(ctx context.Context) (*models.User, error) {
	userID, err := uuid.NewString()
	if err != nil {
		return nil, err
	}
	webAuthnUserID, err := uuid.NewString()
	if err != nil {
		return nil, err
	}

	u := &models.User{
		ID:                 userID,
		PasskeyCredentials: []models.PasskeyCredential{},
		Provider:           string(constants.AuthProviderPasskey),
		Status:             constants.UserStatusActive,
		WebAuthnUserID:     webAuthnUserID,
	}

	data, err := json.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user: %w", err)
	}
	if err := s.db.DocSet(ctx, marshaler.CollectionName(constants.CollectionUsers), userID, data); err != nil {
		return nil, fmt.Errorf("failed to store user: %w", err)
	}
	return u, nil
}

func (s *dbUserStore) HasAnyUsers(ctx context.Context) (bool, error) {
	docs, err := s.db.DocQuery(ctx, marshaler.CollectionName(constants.CollectionUsers), []models.DocFilter{}, "", 1)
	if err != nil {
		return false, err
	}
	return len(docs) > 0, nil
}

// dbWebAuthnCeremonyStore implements webauthnCeremonyStore using DocumentStoreService.
type dbWebAuthnCeremonyStore struct {
	db *DocumentStoreService
}

func (s *dbWebAuthnCeremonyStore) StoreCeremony(ctx context.Context, userID string, session *webauthn.SessionData) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return s.db.DocSet(ctx, marshaler.CollectionName(constants.CollectionPasskeyChallenges), userID, data)
}

func (s *dbWebAuthnCeremonyStore) GetCeremony(ctx context.Context, userID string) (*webauthn.SessionData, error) {
	doc, err := s.db.DocGet(ctx, marshaler.CollectionName(constants.CollectionPasskeyChallenges), userID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, constants.ErrExpired
	}

	var session webauthn.SessionData
	if err := doc.DecodeData(&session); err != nil {
		return nil, err
	}

	return &session, nil
}

func (s *dbWebAuthnCeremonyStore) DeleteCeremony(ctx context.Context, userID string) error {
	err := s.db.DocDelete(ctx, marshaler.CollectionName(constants.CollectionPasskeyChallenges), userID)
	return err
}

// realWebauthnClient implements webauthnClient using the actual webauthn library.
type realWebauthnClient struct {
	w *webauthn.WebAuthn
}

func (c *realWebauthnClient) BeginRegistration(user webauthn.User) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	return c.w.BeginRegistration(user)
}

func (c *realWebauthnClient) FinishRegistration(user webauthn.User, session webauthn.SessionData, r *http.Request) (*webauthn.Credential, error) {
	return c.w.FinishRegistration(user, session, r)
}

func (c *realWebauthnClient) BeginLogin(user webauthn.User) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	return c.w.BeginLogin(user)
}

func (c *realWebauthnClient) ValidateLogin(user webauthn.User, session webauthn.SessionData, parsedResponse *protocol.ParsedCredentialAssertionData) (*webauthn.Credential, error) {
	return c.w.ValidateLogin(user, session, parsedResponse)
}

// buildRPOrigins constructs the WebAuthn RPOrigins list from PasskeyConfig.
// It is extracted as a pure function so tests can verify the origins list
// without constructing a full PasskeyService or depending on webauthn internals.
func buildRPOrigins(cfg *PasskeyConfig) []string {
	httpPort := cfg.HTTPPort
	if httpPort == 0 {
		httpPort = constants.Ports.OperatorHttp
	}
	httpsPort := cfg.HTTPSPort
	if httpsPort == 0 {
		httpsPort = constants.Ports.OperatorHttps
	}

	var rpOrigins []string
	if cfg.RpID == "localhost" || cfg.RpID == "127.0.0.1" {
		rpOrigins = []string{cfg.RpID,
			"http://localhost", fmt.Sprintf("http://localhost:%d", httpPort),
			"http://127.0.0.1", fmt.Sprintf("http://127.0.0.1:%d", httpPort),
			"https://localhost", fmt.Sprintf("https://localhost:%d", httpsPort),
			"https://127.0.0.1", fmt.Sprintf("https://127.0.0.1:%d", httpsPort),
		}
	} else {
		rpOrigins = []string{"https://" + cfg.RpID}
	}

	rpOrigins = append(rpOrigins, cfg.RpOrigins...)
	return rpOrigins
}

// NewPasskeyService creates a new PasskeyService with the given configuration.
func NewPasskeyService(docStore *DocumentStoreService, logger *slog.Logger, cfg *PasskeyConfig) (*PasskeyService, error) {
	rpName := cfg.RpName
	if rpName == "" {
		rpName = "ztd"
	}

	rpOrigins := buildRPOrigins(cfg)

	// User verification (PIN or biometric) is required for every ceremony:
	// presence alone would let anyone holding an unlocked authenticator log in
	// or approve L3 actions.
	w, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RpID,
		RPDisplayName: rpName,
		RPOrigins:     rpOrigins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize webauthn: %w", err)
	}

	return &PasskeyService{
		userStore:     &dbUserStore{db: docStore},
		ceremonyStore: &dbWebAuthnCeremonyStore{db: docStore},
		webauthn:      &realWebauthnClient{w: w},
		logger:        logger,
		rpID:          cfg.RpID,
		rpName:        rpName,
		rpOrigins:     append([]string(nil), rpOrigins...),
	}, nil
}

// embeddedOperatorBinder binds the gateway's claimed embedded operator to a
// freshly created web session. Satisfied by *RegistrationService.
type embeddedOperatorBinder interface {
	BindEmbeddedOperatorToWebSession(ctx context.Context, userID, webSessionID string) (bool, error)
}

// embeddedOperatorClaimer claims the gateway's embedded operator for a user
// and persists the operator session the claim mints. Satisfied by
// *embedded.Service.
type embeddedOperatorClaimer interface {
	ClaimEmbeddedOperator(ctx context.Context, userID string) (operatorID, operatorSessionID string, err error)
}

// PasskeyHandler handles HTTP endpoints for passkey registration, authentication,
// credential management, and OOB approval flows. It wraps a PasskeyService for
// domain logic and delegates business orchestration (MCP, suspended transactions,
// SSE, WebSocket) to PasskeyOrchestrator. HTTP-layer concerns only.
type PasskeyHandler struct {
	*PasskeyService
	webSessionSvc      *WebSessionService
	enrollmentTokenSvc *EnrollmentTokenService
	operatorBinder     embeddedOperatorBinder
	operatorClaimer    embeddedOperatorClaimer
	responder          *response.Writer
	maxPayload         int64
	orchestrator       *PasskeyOrchestrator
	crossOrigin        bool
	posture            constants.Posture
}

// PasskeyHandlerDeps groups all dependencies for NewPasskeyHandler.
type PasskeyHandlerDeps struct {
	Service            *PasskeyService
	WebSessionSvc      *WebSessionService
	EnrollmentTokenSvc *EnrollmentTokenService
	OperatorBinder     embeddedOperatorBinder
	OperatorClaimer    embeddedOperatorClaimer
	Responder          *response.Writer
	MaxPayload         int64
	Orchestrator       *PasskeyOrchestrator
	CrossOrigin        bool
	// Posture is the Gateway governance posture; postures that require L3
	// require grounded sessions for direct approval responses.
	Posture constants.Posture
}

// NewPasskeyHandler creates a new PasskeyHandler with all dependencies wired.
func NewPasskeyHandler(deps PasskeyHandlerDeps) *PasskeyHandler {
	return &PasskeyHandler{
		PasskeyService:     deps.Service,
		webSessionSvc:      deps.WebSessionSvc,
		enrollmentTokenSvc: deps.EnrollmentTokenSvc,
		operatorBinder:     deps.OperatorBinder,
		operatorClaimer:    deps.OperatorClaimer,
		responder:          deps.Responder,
		maxPayload:         deps.MaxPayload,
		orchestrator:       deps.Orchestrator,
		crossOrigin:        deps.CrossOrigin,
		posture:            deps.Posture,
	}
}

// MaxPayloadBytes returns the configured maximum request payload size in
// bytes. A zero value causes http.MaxBytesReader to reject every body on the
// first read, breaking all body-reading passkey endpoints (register/auth
// challenge and verify). Production wiring must set this to a non-zero value
// (typically cfg.Gateway.MaxPayloadBytes).
func (h *PasskeyHandler) MaxPayloadBytes() int64 {
	return h.maxPayload
}

// GenerateRegistrationChallenge creates a registration challenge for a user.
func (s *PasskeyService) GenerateRegistrationChallenge(ctx context.Context, userID, userName string) (*protocol.CredentialCreation, error) {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, constants.ErrUserNotFound
	}

	options, session, err := s.webauthn.BeginRegistration(user)
	if err != nil {
		return nil, fmt.Errorf("failed to begin registration: %w", err)
	}

	// Store session data
	if err := s.storeWebAuthnCeremony(ctx, userID, session); err != nil {
		return nil, err
	}

	return options, nil
}

// VerifyRegistration verifies a registration response.
// It accepts the flat WebAuthn attestation response.
func (s *PasskeyService) VerifyRegistration(ctx context.Context, userID string, att *models.WebAuthnAttestationResponse) (*models.PasskeyCredential, error) {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, constants.ErrUserNotFound
	}

	session, err := s.getWebAuthnCeremony(ctx, userID)
	if err != nil {
		return nil, err
	}

	if att == nil {
		return nil, fmt.Errorf("failed to parse attestation response: %w", constants.ErrMissingRequiredField)
	}

	// The flat WebAuthnAttestationResponse must be re-serialized into the
	// nested CredentialCreationResponse format expected by go-webauthn:
	//   {"id":"...","type":"public-key","rawId":"...","response":{"clientDataJSON":"...","attestationObject":"...","transports":[...]}}
	creationResponse := struct {
		ID       string                             `json:"id"`
		Type     string                             `json:"type"`
		RawID    string                             `json:"rawId"`
		Response models.WebAuthnAttestationResponse `json:"response"`
	}{
		ID:       att.ID,
		Type:     "public-key",
		RawID:    att.RawID,
		Response: *att,
	}

	nestedJSON, err := json.Marshal(creationResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal creation response: %w", err)
	}

	r, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(nestedJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	r.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)

	credential, err := s.webauthn.FinishRegistration(user, *session, r)
	if err != nil {
		return nil, fmt.Errorf("failed to finish registration: %w", err)
	}

	newCred := models.PasskeyCredential{
		ID:              credential.ID,
		PublicKey:       credential.PublicKey,
		AttestationType: credential.AttestationType,
		Transport:       credential.Transport,
		Authenticator: models.Authenticator{
			AAGUID:       credential.Authenticator.AAGUID,
			SignCount:    credential.Authenticator.SignCount,
			CloneWarning: credential.Authenticator.CloneWarning,
		},
		CreatedAtUnixMs: time.Now().UnixMilli(),
	}

	if err := s.addCredential(ctx, userID, newCred); err != nil {
		return nil, err
	}

	if delErr := s.deleteWebAuthnCeremony(ctx, userID); delErr != nil {
		s.logger.Warn("Failed to delete WebAuthn session after registration", "error", delErr, "userID", userID)
	}

	return &newCred, nil
}

// GenerateAuthenticationChallenge creates an authentication challenge.
func (s *PasskeyService) GenerateAuthenticationChallenge(ctx context.Context, userID string) (*protocol.CredentialAssertion, error) {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, constants.ErrUserNotFound
	}

	if len(user.PasskeyCredentials) == 0 {
		return nil, constants.ErrNoPasskeysRegistered
	}

	options, session, err := s.webauthn.BeginLogin(user)
	if err != nil {
		return nil, fmt.Errorf("failed to begin login: %w", err)
	}

	if err := s.storeWebAuthnCeremony(ctx, userID, session); err != nil {
		return nil, err
	}

	return options, nil
}

// GenerateApprovalChallenge creates a WebAuthn assertion challenge bound to a transaction hash.
// This is used for Out-of-Band (OOB) approval of suspended transactions.
func (s *PasskeyService) GenerateApprovalChallenge(ctx context.Context, userID, transactionHash string) (*protocol.CredentialAssertion, error) {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, constants.ErrUserNotFound
	}

	// We don't use BeginLogin here because we want to force the challenge to be the transaction hash.
	// We build the assertion options manually.
	var allowedCredentials []protocol.CredentialDescriptor
	for _, cred := range user.PasskeyCredentials {
		allowedCredentials = append(allowedCredentials, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: cred.ID,
		})
	}

	options := &protocol.CredentialAssertion{
		Response: protocol.PublicKeyCredentialRequestOptions{
			Challenge:          protocol.URLEncodedBase64(transactionHash),
			Timeout:            60000,
			RelyingPartyID:     s.rpID,
			AllowedCredentials: allowedCredentials,
			UserVerification:   protocol.VerificationRequired,
		},
	}

	return options, nil
}

// VerifyAuthentication verifies a sign-in assertion against the user's
// pending authentication challenge.
func (s *PasskeyService) VerifyAuthentication(ctx context.Context, userID string, assertion *models.WebAuthnAssertionResponse) (*models.PasskeyCredential, error) {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, constants.ErrUserNotFound
	}

	session, err := s.getWebAuthnCeremony(ctx, userID)
	if err != nil {
		return nil, err
	}

	parsedResponse, err := gov.ParsePasskeyAssertion(assertion)
	if err != nil {
		return nil, err
	}

	credential, err := s.webauthn.ValidateLogin(user, *session, parsedResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to finish login: %w", err)
	}

	storedCred, err := s.recordCredentialUse(ctx, userID, user, credential)
	if err != nil {
		return nil, err
	}

	if delErr := s.deleteWebAuthnCeremony(ctx, userID); delErr != nil {
		s.logger.Warn("Failed to delete WebAuthn session after authentication", "error", delErr, "userID", userID)
	}

	return storedCred, nil
}

// listCredentials returns all passkey credentials for a user.
func (s *PasskeyService) listCredentials(ctx context.Context, userID string) ([]models.PasskeyCredential, error) {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, nil
	}
	return user.PasskeyCredentials, nil
}

// revokeCredential removes a passkey credential from a user. It refuses to
// remove the user's last passkey: with none left, L3 approvals and passkey
// login become impossible, and re-registration would fall back to weaker
// bootstrap paths. Register a replacement first.
func (s *PasskeyService) revokeCredential(ctx context.Context, userID, credentialID string) (found bool, remaining int, err error) {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return false, 0, err
	}
	if user == nil {
		return false, 0, nil
	}

	var newCreds []models.PasskeyCredential
	found = false
	for _, c := range user.PasskeyCredentials {
		if encodeCredID(c.ID) != credentialID {
			newCreds = append(newCreds, c)
		} else {
			found = true
		}
	}

	if !found {
		return false, len(user.PasskeyCredentials), nil
	}
	if len(newCreds) == 0 {
		return true, len(user.PasskeyCredentials), constants.ErrLastPasskeyRevoke
	}

	if err := s.setCredentials(ctx, userID, newCreds); err != nil {
		s.logger.Error("Failed to revoke credential", "error", err, "userID", userID)
		return false, 0, err
	}

	credPrefix := credentialID
	if len(credentialID) > 12 {
		credPrefix = credentialID[:12]
	}
	s.logger.Info("Credential revoked", "userID", userID, "credentialID", credPrefix)
	return true, len(newCreds), nil
}

// VerifyPasskeyProof verifies a WebAuthn assertion against a registered passkey.
// The challenge is the transaction_hash. This is the passkey-domain primitive
// composed by the governance gateway L3 notary (via governance.PasskeyVerifier);
// it intentionally does not implement governance.L3Notary so the passkey domain
// is not coupled to the governance interface shape.
// The cliSessionID parameter is ignored for web sessions (WebAuthn) but is
// accepted so the composing notary can forward it without a separate adapter.
func (s *PasskeyService) VerifyPasskeyProof(ctx context.Context, userID, transactionHash, cliSessionID string, proof *commonv1.L3Proof) (bool, error) {
	if userID == "" {
		return false, constants.ErrUserIDRequired
	}
	if transactionHash == "" {
		return false, constants.ErrCLIL3TransactionHashRequired
	}
	if proof == nil {
		return false, constants.ErrGatewayL3ProofRequired
	}
	if proof.CredentialId == "" {
		return false, constants.ErrMissingRequiredField
	}
	if proof.ClientDataJson == "" {
		return false, constants.ErrMissingRequiredField
	}
	if proof.AuthenticatorData == "" {
		return false, constants.ErrMissingRequiredField
	}
	if proof.Signature == "" {
		return false, constants.ErrMissingRequiredField
	}

	user, err := s.getUser(ctx, userID)
	if err != nil {
		return false, err
	}
	if user == nil {
		return false, constants.ErrUserNotFound
	}
	if len(user.PasskeyCredentials) == 0 {
		return false, constants.ErrNoPasskeysRegistered
	}

	allowedCredentialIDs := make([][]byte, 0, len(user.PasskeyCredentials))
	for _, credential := range user.PasskeyCredentials {
		allowedCredentialIDs = append(allowedCredentialIDs, credential.ID)
	}

	session := webauthn.SessionData{
		Challenge:            encodeChallenge([]byte(transactionHash)),
		RelyingPartyID:       s.rpID,
		UserID:               user.WebAuthnID(),
		AllowedCredentialIDs: allowedCredentialIDs,
		Expires:              time.Now().Add(passkeyChallengeTTL),
		UserVerification:     protocol.VerificationRequired,
	}

	parsedResponse, err := gov.ParsePasskeyAssertion(&models.WebAuthnAssertionResponse{
		ID:                proof.CredentialId,
		RawID:             proof.CredentialId,
		ClientDataJSON:    proof.ClientDataJson,
		AuthenticatorData: proof.AuthenticatorData,
		Signature:         proof.Signature,
	})
	if err != nil {
		return false, err
	}

	credential, err := s.webauthn.ValidateLogin(user, session, parsedResponse)
	if err != nil {
		return false, fmt.Errorf("L3 WebAuthn verification failed: %w", err)
	}
	if _, err := s.recordCredentialUse(ctx, userID, user, credential); err != nil {
		return false, fmt.Errorf("L3 WebAuthn verification failed: %w", err)
	}

	return true, nil
}

// recordCredentialUse rejects an assertion whose signature counter did not
// advance (a possible cloned authenticator) and otherwise persists the new
// counter and last-used time, so a replayed or cloned assertion cannot pass
// again with the same counter.
func (s *PasskeyService) recordCredentialUse(ctx context.Context, userID string, user *models.User, credential *webauthn.Credential) (*models.PasskeyCredential, error) {
	if credential == nil {
		return nil, fmt.Errorf("passkey: %w: validated credential", constants.ErrMissingRequiredField)
	}
	if credential.Authenticator.CloneWarning {
		s.logger.Warn("Passkey assertion rejected: signature counter did not advance", "userID", userID, "stored_sign_count", storedSignCount(user, credential.ID))
		return nil, constants.ErrPasskeyCloneDetected
	}

	var storedCred *models.PasskeyCredential
	for i := range user.PasskeyCredentials {
		if bytes.Equal(user.PasskeyCredentials[i].ID, credential.ID) {
			user.PasskeyCredentials[i].Authenticator.SignCount = credential.Authenticator.SignCount
			user.PasskeyCredentials[i].LastUsedAtUnixMs = time.Now().UnixMilli()
			storedCred = &user.PasskeyCredentials[i]
			break
		}
	}
	if storedCred == nil {
		return nil, fmt.Errorf("passkey: %w: credential not registered to user", constants.ErrNotFound)
	}

	if err := s.updateUser(ctx, userID, user); err != nil {
		return nil, err
	}
	return storedCred, nil
}

func storedSignCount(user *models.User, credentialID []byte) uint32 {
	for _, c := range user.PasskeyCredentials {
		if bytes.Equal(c.ID, credentialID) {
			return c.Authenticator.SignCount
		}
	}
	return 0
}

func (s *PasskeyService) getUser(ctx context.Context, userID string) (*models.User, error) {
	return s.userStore.GetUser(ctx, userID)
}

func (s *PasskeyService) addCredential(ctx context.Context, userID string, cred models.PasskeyCredential) error {
	if err := cred.Validate(); err != nil {
		return err
	}
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return err
	}
	if user == nil {
		return constants.ErrUserNotFound
	}

	user.PasskeyCredentials = append(user.PasskeyCredentials, cred)

	return s.updateUser(ctx, userID, user)
}

func (s *PasskeyService) setCredentials(ctx context.Context, userID string, creds []models.PasskeyCredential) error {
	user, err := s.getUser(ctx, userID)
	if err != nil {
		return err
	}
	if user == nil {
		return constants.ErrUserNotFound
	}

	user.PasskeyCredentials = creds
	return s.updateUser(ctx, userID, user)
}

func (s *PasskeyService) updateUser(ctx context.Context, userID string, user *models.User) error {
	return s.userStore.UpdateUser(ctx, userID, user)
}

func (s *PasskeyService) storeWebAuthnCeremony(ctx context.Context, userID string, session *webauthn.SessionData) error {
	return s.ceremonyStore.StoreCeremony(ctx, userID, session)
}

func (s *PasskeyService) getWebAuthnCeremony(ctx context.Context, userID string) (*webauthn.SessionData, error) {
	return s.ceremonyStore.GetCeremony(ctx, userID)
}

func (s *PasskeyService) deleteWebAuthnCeremony(ctx context.Context, userID string) error {
	return s.ceremonyStore.DeleteCeremony(ctx, userID)
}
