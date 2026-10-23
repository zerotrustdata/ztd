// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import "time"

// OperatorHeartbeatStaleAfter is the shortest window a remote Operator may go
// without a heartbeat before the Gateway reads its document as stale, and the
// window for an Operator that declared no heartbeat interval. Operators
// heartbeat every 30 seconds by default, so this tolerates one missed beat plus
// jitter.
const OperatorHeartbeatStaleAfter = 60 * time.Second

// OperatorDefaultHeartbeatInterval is how often an Operator sends heartbeats
// when it declares no interval of its own.
const OperatorDefaultHeartbeatInterval = 30 * time.Second

// OperatorHeartbeatMaxInterval is the longest heartbeat interval an Operator
// may declare at session start.
const OperatorHeartbeatMaxInterval = 5 * time.Minute

// OperatorHeartbeatStaleMultiplier is how many declared heartbeat intervals an
// Operator may stay silent before the Gateway reads it as stale.
const OperatorHeartbeatStaleMultiplier = 2

// OperatorStaleAfter returns how long an Operator that declared the given
// heartbeat interval may stay silent before it reads as stale: the declared
// interval times OperatorHeartbeatStaleMultiplier, never less than
// OperatorHeartbeatStaleAfter. A zero or negative interval (nothing declared)
// yields OperatorHeartbeatStaleAfter. The interval is clamped to
// OperatorHeartbeatMaxInterval so no Operator can defer staleness past
// OperatorHeartbeatStaleMultiplier times that maximum.
func OperatorStaleAfter(declaredInterval time.Duration) time.Duration {
	if declaredInterval > OperatorHeartbeatMaxInterval {
		declaredInterval = OperatorHeartbeatMaxInterval
	}
	return max(OperatorHeartbeatStaleAfter, declaredInterval*OperatorHeartbeatStaleMultiplier)
}

// ExecutionStatus is a typed string for execution status.
type ExecutionStatus string

const (
	ExecutionStatusCancelRequested ExecutionStatus = "cancel_requested"
	ExecutionStatusCancelled       ExecutionStatus = "cancelled"
	ExecutionStatusCompleted       ExecutionStatus = "completed"
	ExecutionStatusDenied          ExecutionStatus = "denied"
	ExecutionStatusExecuting       ExecutionStatus = "executing"
	ExecutionStatusFailed          ExecutionStatus = "failed"
	ExecutionStatusFeedback        ExecutionStatus = "feedback"
	ExecutionStatusPending         ExecutionStatus = "pending"
	ExecutionStatusTimeout         ExecutionStatus = "timeout"
)

// FileOperation is a typed string for file operations.
type FileOperation string

const (
	FileOperationCreate  FileOperation = "create"
	FileOperationDelete  FileOperation = "delete"
	FileOperationInsert  FileOperation = "insert"
	FileOperationRead    FileOperation = "read"
	FileOperationReplace FileOperation = "replace"
	FileOperationUpdate  FileOperation = "update"
	FileOperationWrite   FileOperation = "write"
)

// ConnectionState is a typed string for connection states.
type ConnectionState string

const (
	ConnectionStateClosed       ConnectionState = "closed"
	ConnectionStateConnected    ConnectionState = "connected"
	ConnectionStateConnecting   ConnectionState = "connecting"
	ConnectionStateDisconnected ConnectionState = "disconnected"
	ConnectionStateError        ConnectionState = "error"
	ConnectionStateReconnecting ConnectionState = "reconnecting"
)

// OperatorStatus is a typed string for Operator status.
type OperatorStatus string

const (
	OperatorStatusActive      OperatorStatus = "active"
	OperatorStatusAvailable   OperatorStatus = "available"
	OperatorStatusBound       OperatorStatus = "bound"
	OperatorStatusOffline     OperatorStatus = "offline"
	OperatorStatusStale       OperatorStatus = "stale"
	OperatorStatusStopped     OperatorStatus = "stopped"
	OperatorStatusTerminated  OperatorStatus = "terminated"
	OperatorStatusUnavailable OperatorStatus = "unavailable"
)

// OperatorType is a typed string for Operator type.
//   - embedded: the gateway's in-process operator substrate, claimed by the
//     first user's bootstrap act. Certless; its operator_session_id is a
//     binding record, not a remote enrollment lease.
//   - remote: an operator enrolled from a remote host via CSR or platform
//     enrollment.
type OperatorType string

const (
	OperatorTypeEmbedded OperatorType = "embedded"
	OperatorTypeRemote   OperatorType = "remote"
)

// OperatorCapability is a typed string for one governed workload an Operator
// serves. An Operator carries a set of capabilities independent of its
// OperatorType; multiple Operators on the same host are differentiated by their
// capabilities, local directory, launching account, and port.
type OperatorCapability string

const (
	OperatorCapabilityData       OperatorCapability = "data"
	OperatorCapabilityInference  OperatorCapability = "inference"
	OperatorCapabilityProvenance OperatorCapability = "provenance"
	OperatorCapabilityObserver   OperatorCapability = "observer"
)

// UserStatus is a typed string for user status.
type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
)

// AuthProvider is a typed string for auth provider.
type AuthProvider string

const (
	AuthProviderJWT     AuthProvider = "jwt"
	AuthProviderLocal   AuthProvider = "local"
	AuthProviderPasskey AuthProvider = "passkey"
)

// ApprovalType is a typed string for approval type.
type ApprovalType string

const (
	ApprovalTypeAgentContinue ApprovalType = "agent.continue"
	ApprovalTypeCommand       ApprovalType = "command"
	ApprovalTypeFileEdit      ApprovalType = "file.edit"
	ApprovalTypeIntent        ApprovalType = "intent"
)

// SessionType is a typed string for session type.
type SessionType string

const (
	SessionTypeCLI      SessionType = "cli"
	SessionTypeOperator SessionType = "operator"
	SessionTypeWeb      SessionType = "web"
)

// EmbeddedOperatorBootstrapSessionID is the audit session the Gateway starts for its embedded
// Operator at startup. It is the embedded Operator's own session until the first user claims it
// and mints its operator session, so execution before the claim, such as platform enrollment
// requested the moment the first user exists, is still recorded under a session the Gateway owns.
const EmbeddedOperatorBootstrapSessionID = "embedded-operator-bootstrap"

// StreamStatus is a typed string for stream status.
type StreamStatus string

const (
	StreamStatusCancelled StreamStatus = "cancelled"
	StreamStatusCompleted StreamStatus = "completed"
	StreamStatusExited    StreamStatus = "exited"
	StreamStatusFailed    StreamStatus = "failed"
	StreamStatusSummary   StreamStatus = "summary"
)

// CommandExitStatus is a typed string for command exit status.
type CommandExitStatus string

const (
	CommandExitStatusError         CommandExitStatus = "error"
	CommandExitStatusFailure       CommandExitStatus = "failure"
	CommandExitStatusInterrupted   CommandExitStatus = "interrupted"
	CommandExitStatusInvalidExit   CommandExitStatus = "invalid_exit"
	CommandExitStatusKilled        CommandExitStatus = "killed"
	CommandExitStatusMisuse        CommandExitStatus = "misuse"
	CommandExitStatusNotExecutable CommandExitStatus = "not_executable"
	CommandExitStatusNotFound      CommandExitStatus = "not_found"
	CommandExitStatusSuccess       CommandExitStatus = "success"
	CommandExitStatusTerminated    CommandExitStatus = "terminated"
	CommandExitStatusSignal1       CommandExitStatus = "signal_1"  // SIGHUP
	CommandExitStatusSignal2       CommandExitStatus = "signal_2"  // SIGINT
	CommandExitStatusSignal3       CommandExitStatus = "signal_3"  // SIGQUIT
	CommandExitStatusSignal6       CommandExitStatus = "signal_6"  // SIGABRT
	CommandExitStatusSignal9       CommandExitStatus = "signal_9"  // SIGKILL
	CommandExitStatusSignal11      CommandExitStatus = "signal_11" // SIGSEGV
	CommandExitStatusSignal13      CommandExitStatus = "signal_13" // SIGPIPE
	CommandExitStatusSignal15      CommandExitStatus = "signal_15" // SIGTERM
)

// ToolScope is a typed string for tool scope.
type ToolScope string

const (
	ToolScopeOperatorGated ToolScope = "operator_gated"
	ToolScopeUniversal     ToolScope = "universal"
)

// ToolCallStatus is a typed string for tool call status.
type ToolCallStatus string

const (
	ToolCallStatusCompleted ToolCallStatus = "completed"
	ToolCallStatusFailed    ToolCallStatus = "failed"
	ToolCallStatusStarted   ToolCallStatus = "started"
)

// Platform is a typed string for platform.
type Platform string

const (
	PlatformDarwin  Platform = "darwin"
	PlatformLinux   Platform = "linux"
	PlatformWindows Platform = "windows"
)

// ComponentName is a typed string for component name.
type ComponentName string

const (
	ComponentNameClient      ComponentName = "client"
	ComponentNameZTDO        ComponentName = "ztdo"
	ComponentNameZTDOGateway ComponentName = "ztdo-gateway"
)

// SystemHealth is a typed string for system health.
type SystemHealth string

const (
	SystemHealthDegraded  SystemHealth = "degraded"
	SystemHealthHealthy   SystemHealth = "healthy"
	SystemHealthUnhealthy SystemHealth = "unhealthy"
	SystemHealthUnknown   SystemHealth = "unknown"

	BuildMetadataUnavailable = "unavailable"
)

// NetworkProtocol is a typed string for network protocol.
type NetworkProtocol string

const (
	NetworkProtocolTCP NetworkProtocol = "tcp"
	NetworkProtocolUDP NetworkProtocol = "udp"
)

// Environment is a typed string for environment.
type Environment string

const (
	EnvironmentDev        Environment = "dev"
	EnvironmentProduction Environment = "production"
	EnvironmentTest       Environment = "test"
)

// VersionStability is a typed string for version stability.
type VersionStability string

const (
	VersionStabilityBeta       VersionStability = "beta"
	VersionStabilityDev        VersionStability = "dev"
	VersionStabilityStable     VersionStability = "stable"
	VersionStabilityUnstable   VersionStability = "unstable"
	VersionStabilityDeprecated VersionStability = "deprecated"
)

// UserRole is a typed string for user role.
type UserRole string

const (
	UserRoleAdmin    UserRole = "admin"
	UserRoleOperator UserRole = "operator"
	UserRoleOwner    UserRole = "owner"
	UserRoleUser     UserRole = "user"
)

// CAType is a typed string for certificate authority type.
type CAType string

const (
	CATypeRoot        CAType = "root"
	CATypeHub         CAType = "hub"
	CATypeOperator    CAType = "operator"
	CATypeGatewayPeer CAType = "gateway-peer"
)

// ServiceName is a typed string for service names.
type ServiceName string

const (
	ServiceNameOperatorGateway ServiceName = "operator-gateway"
)

// GatewayMode is a typed string for gateway mode.
type GatewayMode string

const (
	GatewayModeGateway  GatewayMode = "gateway"
	GatewayModeStatusOK GatewayMode = "ok"
)

// ThinkingPhase is a typed string for an AI thinking phase.
type ThinkingPhase string

const (
	ThinkingPhaseEnd    ThinkingPhase = "end"
	ThinkingPhaseStart  ThinkingPhase = "start"
	ThinkingPhaseUpdate ThinkingPhase = "update"
)

// HistoryEventType is a typed string for history event type.
type HistoryEventType string

const (
	HistoryEventTypeAuthenticated        HistoryEventType = "authenticated"
	HistoryEventTypeBound                HistoryEventType = "bound"
	HistoryEventTypeClaimed              HistoryEventType = "claimed"
	HistoryEventTypeCreated              HistoryEventType = "created"
	HistoryEventTypeCreatedFromRefresh   HistoryEventType = "created.from.refresh"
	HistoryEventTypeDeactivated          HistoryEventType = "deactivated"
	HistoryEventTypeHeartbeatReceived    HistoryEventType = "heartbeat.received"
	HistoryEventTypeReconnected          HistoryEventType = "reconnected"
	HistoryEventTypeRegistered           HistoryEventType = "registered"
	HistoryEventTypeReset                HistoryEventType = "reset"
	HistoryEventTypeShutdownRequested    HistoryEventType = "shutdown.requested"
	HistoryEventTypeSlotConsumed         HistoryEventType = "slot.consumed"
	HistoryEventTypeSlotCreated          HistoryEventType = "slot.created"
	HistoryEventTypeSlotReleased         HistoryEventType = "slot.released"
	HistoryEventTypeStatusChanged        HistoryEventType = "status.changed"
	HistoryEventTypeStopped              HistoryEventType = "stopped"
	HistoryEventTypeTerminated           HistoryEventType = "terminated"
	HistoryEventTypeTerminatedForRefresh HistoryEventType = "terminated.for.refresh"
	HistoryEventTypeUnbound              HistoryEventType = "unbound"
)

// HeartbeatType is a typed string for heartbeat type.
type HeartbeatType string

const (
	HeartbeatTypeAutomatic HeartbeatType = "automatic"
	HeartbeatTypeBootstrap HeartbeatType = "bootstrap"
	HeartbeatTypeRequested HeartbeatType = "requested"
)

// AuthAuditResult is a typed string for auth audit result.
type AuthAuditResult string

const (
	AuthAuditResultFailure AuthAuditResult = "failure"
	AuthAuditResultSuccess AuthAuditResult = "success"
)

// ToolDisplayCategory is a typed string for tool display category.
type ToolDisplayCategory string

const (
	ToolDisplayCategoryExecution ToolDisplayCategory = "execution"
	ToolDisplayCategoryFile      ToolDisplayCategory = "file"
	ToolDisplayCategoryGeneral   ToolDisplayCategory = "general"
	ToolDisplayCategoryNetwork   ToolDisplayCategory = "network"
	ToolDisplayCategorySearch    ToolDisplayCategory = "search"
)

// SessionKeyPrefix is a typed string for session key prefix.
type SessionKeyPrefix string

const (
	SessionKeyPrefixCLI      SessionKeyPrefix = "cli_session"
	SessionKeyPrefixOperator SessionKeyPrefix = "operator_session"
	SessionKeyPrefixWeb      SessionKeyPrefix = "web_session"
)

// SuspendedTxStatus is a typed string for the lifecycle state of a stored
// suspended transaction. A decided transaction leaves the store, so these
// describe only rows still awaiting or undergoing execution.
type SuspendedTxStatus string

const (
	// SuspendedTxStatusPending is an unclaimed, unexpired transaction awaiting L3.
	SuspendedTxStatusPending SuspendedTxStatus = "pending"
	// SuspendedTxStatusClaimed is an approved transaction whose execution is in flight.
	SuspendedTxStatusClaimed SuspendedTxStatus = "claimed"
	// SuspendedTxStatusExpired is an unclaimed transaction past its expiry.
	SuspendedTxStatusExpired SuspendedTxStatus = "expired"
)

// GatewayResponseStatus is a typed string for the status field returned in
// gateway suspension responses (A2A suspension response, OOB suspension
// queries). It is the wire value clients compare against to detect that a
// mutation has been paused awaiting L3 notary approval.
type GatewayResponseStatus string

const (
	GatewayResponseStatusSuspended GatewayResponseStatus = "suspended"
)

// MCPApprovalPausedPrefix is the leading text of the directive returned to the
// calling agent when an MCP tool call is paused awaiting human passkey
// authorization under ratify or notary posture. The full message is built by
// mcp.approvalPausedMessage; tests assert the response content starts with
// this prefix rather than matching the entire dynamic string.
const MCPApprovalPausedPrefix = "Execution paused"

// HistoryActor is a typed string for history actor.
type HistoryActor string

const (
	HistoryActorNone   HistoryActor = ""
	HistoryActorZTDO   HistoryActor = "ztdo"
	HistoryActorSystem HistoryActor = "system"
	HistoryActorUser   HistoryActor = "user"
)

// AISource is a typed string for AI source.
type AISource string

const (
	AISourceTerminalAnchored AISource = "ai.terminal.anchored"
	AISourceTerminalDirect   AISource = "ai.terminal.direct"
	AISourceToolCall         AISource = "ai.tool.call"
)

// ComponentStatus is a typed string for component status.
type ComponentStatus string

const (
	ComponentStatusActive      ComponentStatus = "active"
	ComponentStatusError       ComponentStatus = "error"
	ComponentStatusInactive    ComponentStatus = "inactive"
	ComponentStatusMaintenance ComponentStatus = "maintenance"
	ComponentStatusDegraded    ComponentStatus = "degraded"
)

// WorkflowType is a typed string for workflow type.
type WorkflowType string

const (
	WorkflowTypeZtdBound      WorkflowType = "ztd.bound"
	WorkflowTypeZtdCloudBound WorkflowType = "ztd.cloud.bound"
	WorkflowTypeZtdNotBound   WorkflowType = "ztd.not.bound"
	WorkflowTypeTriage        WorkflowType = "triage"
	WorkflowTypeInvestigation WorkflowType = "investigation"
)

// AITaskId is a typed string for AI task identifiers.
type AITaskId string

const (
	AITaskIDAgentContinue      AITaskId = "ai.agent.continue"
	AITaskIDChat               AITaskId = "ai.chat"
	AITaskIDCommand            AITaskId = "ai.command"
	AITaskIDDirectCommand      AITaskId = "ai.direct.command"
	AITaskIDFetchFileDiff      AITaskId = "ai.fetch.file.diff"
	AITaskIDFetchFileHistory   AITaskId = "ai.fetch.file.history"
	AITaskIDFetchHistory       AITaskId = "ai.fetch.history"
	AITaskIDFetchLogs          AITaskId = "ai.fetch.logs"
	AITaskIDFileEdit           AITaskId = "ai.file.edit"
	AITaskIDFsList             AITaskId = "ai.fs.list"
	AITaskIDFsRead             AITaskId = "ai.fs.read"
	AITaskIDIntentGrant        AITaskId = "ai.intent.grant"
	AITaskIDIntentRevoke       AITaskId = "ai.intent.revoke"
	AITaskIDPortCheck          AITaskId = "ai.port.check"
	AITaskIDRecursiveGrep      AITaskId = "ai.recursive_grep"
	AITaskIDRestoreFile        AITaskId = "ai.restore.file"
	AITaskIdChat               AITaskId = "ai.chat"
	AITaskIdCase               AITaskId = "ai.case"
	AITaskIdMemory             AITaskId = "ai.memory"
	AITaskIdCommand            AITaskId = "ai.command"
	AITaskIdCommandExecution   AITaskId = "ai.command.execution"
	AITaskIdDirectCommand      AITaskId = "ai.direct.command"
	AITaskIdIntentGrant        AITaskId = "ai.intent.grant"
	AITaskIdIntentRevoke       AITaskId = "ai.intent.revoke"
	AITaskIdFileEdit           AITaskId = "ai.file.edit"
	AITaskIdFileOperation      AITaskId = "ai.file.operation"
	AITaskIdFsList             AITaskId = "ai.fs.list"
	AITaskIdRecursiveGrep      AITaskId = "ai.recursive.grep"
	AITaskIdPortCheck          AITaskId = "ai.port.check"
	AITaskIdAgentContinue      AITaskId = "ai.agent.continue"
	AITaskIdInvestigationQuery AITaskId = "ai.investigation.query"
)

// ConsensusMember is a typed string for consensus member identifiers.
type ConsensusMember string

const (
	ConsensusMemberAxiom    ConsensusMember = "axiom"
	ConsensusMemberConcord  ConsensusMember = "concord"
	ConsensusMemberVariance ConsensusMember = "variance"
	ConsensusMemberPragma   ConsensusMember = "pragma"
	ConsensusMemberNemesis  ConsensusMember = "nemesis"
)

// ConsensusAuditMode is a typed string for consensus audit mode.
type ConsensusAuditMode string

const (
	ConsensusAuditModeUnanimous ConsensusAuditMode = "unanimous"
	ConsensusAuditModeMajority  ConsensusAuditMode = "majority"
	ConsensusAuditModeTied      ConsensusAuditMode = "tied"
)

// ConsensusAuditStatus is a typed string for consensus audit status.
type ConsensusAuditStatus string

const (
	ConsensusAuditStatusOk      ConsensusAuditStatus = "ok"
	ConsensusAuditStatusRevised ConsensusAuditStatus = "revised"
	ConsensusAuditStatusSwap    ConsensusAuditStatus = "swap"
)

// AuditorReason is a typed string for auditor reason.
type AuditorReason string

const (
	AuditorReasonOk                 AuditorReason = "ok"
	AuditorReasonRevised            AuditorReason = "revised"
	AuditorReasonRevisedFromDissent AuditorReason = "revised_from_dissent"
	AuditorReasonSwappedToDissenter AuditorReason = "swapped_to_dissenter"
	AuditorReasonWhitelistViolation AuditorReason = "whitelist_violation"
	AuditorReasonNoValidRevision    AuditorReason = "no_valid_revision"
	AuditorReasonAuditorError       AuditorReason = "auditor_error"
	AuditorReasonEmptyResponse      AuditorReason = "empty_response"
	AuditorReasonContextOverflow    AuditorReason = "context_overflow"
)

// TieBreakReason is a typed string for tie-break reason.
type TieBreakReason string

const (
	TieBreakReasonShortest        TieBreakReason = "shortest"
	TieBreakReasonExcludedNemesis TieBreakReason = "excluded_nemesis"
)

// ReasoningAgent is a typed string for reasoning agent identifiers.
type ReasoningAgent string

const (
	ReasoningAgentSage ReasoningAgent = "sage"
	ReasoningAgentDash ReasoningAgent = "dash"
)

// ErrorCode is a typed string for ztd error codes.
type ErrorCode string

const (
	ErrorCodeGenericError               ErrorCode = "ZTD-1000"
	ErrorCodeUnexpectedError            ErrorCode = "ZTD-1001"
	ErrorCodeNotImplemented             ErrorCode = "ZTD-1002"
	ErrorCodeConfigError                ErrorCode = "ZTD-1100"
	ErrorCodeMissingEnvVar              ErrorCode = "ZTD-1101"
	ErrorCodeInvalidSettings            ErrorCode = "ZTD-1102"
	ErrorCodeServiceInitError           ErrorCode = "ZTD-1103"
	ErrorCodeAuthError                  ErrorCode = "ZTD-1200"
	ErrorCodeTokenExpired               ErrorCode = "ZTD-1201"
	ErrorCodeInvalidToken               ErrorCode = "ZTD-1202"
	ErrorCodeInsufficientPermissions    ErrorCode = "ZTD-1203"
	ErrorCodeDBConnectionError          ErrorCode = "ZTD-1300"
	ErrorCodeDBQueryError               ErrorCode = "ZTD-1301"
	ErrorCodeDBDocumentNotFound         ErrorCode = "ZTD-1302"
	ErrorCodeDBWriteError               ErrorCode = "ZTD-1303"
	ErrorCodeDBTransactionError         ErrorCode = "ZTD-1304"
	ErrorCodePubSubConnectionError      ErrorCode = "ZTD-1400"
	ErrorCodePubSubPublishError         ErrorCode = "ZTD-1401"
	ErrorCodePubSubSubscribeError       ErrorCode = "ZTD-1402"
	ErrorCodePubSubTopicError           ErrorCode = "ZTD-1403"
	ErrorCodeStorageConnectionError     ErrorCode = "ZTD-1500"
	ErrorCodeStorageReadError           ErrorCode = "ZTD-1501"
	ErrorCodeStorageWriteError          ErrorCode = "ZTD-1502"
	ErrorCodeStorageDeleteError         ErrorCode = "ZTD-1503"
	ErrorCodeAPIConnectionError         ErrorCode = "ZTD-1600"
	ErrorCodeAPITimeoutError            ErrorCode = "ZTD-1601"
	ErrorCodeAPIResponseError           ErrorCode = "ZTD-1602"
	ErrorCodeAPIRequestError            ErrorCode = "ZTD-1603"
	ErrorCodeAPIRateLimitError          ErrorCode = "ZTD-1604"
	ErrorCodeGenericNotFound            ErrorCode = "ZTD-1605"
	ErrorCodeExternalServiceError       ErrorCode = "ZTD-1607"
	ErrorCodeValidationError            ErrorCode = "ZTD-1700"
	ErrorCodeMissingRequiredField       ErrorCode = "ZTD-1701"
	ErrorCodeInvalidFieldFormat         ErrorCode = "ZTD-1702"
	ErrorCodeInvalidFieldValue          ErrorCode = "ZTD-1703"
	ErrorCodeInvalidFieldType           ErrorCode = "ZTD-1704"
	ErrorCodeSchemaValidationFailed     ErrorCode = "ZTD-1705"
	ErrorCodeSchemaNotFound             ErrorCode = "ZTD-1706"
	ErrorCodeInvalidInput               ErrorCode = "ZTD-1707"
	ErrorCodeBusinessLogicError         ErrorCode = "ZTD-1800"
	ErrorCodeWorkflowError              ErrorCode = "ZTD-1801"
	ErrorCodeStateTransitionError       ErrorCode = "ZTD-1802"
	ErrorCodeResourceConflict           ErrorCode = "ZTD-1803"
	ErrorCodeTaskCreationFailed         ErrorCode = "ZTD-1804"
	ErrorCodeOperationFailed            ErrorCode = "ZTD-1805"
	ErrorCodeGovernanceRejected         ErrorCode = "ZTD-1806"
	ErrorCodeModelCapabilityUnsupported ErrorCode = "ZTD-1807"
	ErrorCodeServiceUnavailableError    ErrorCode = "ZTD-1900"
)

// ErrorCategory is a typed string for error categories.
type ErrorCategory string

const (
	ErrorCategoryValidation         ErrorCategory = "validation"
	ErrorCategoryBusinessLogic      ErrorCategory = "business_logic"
	ErrorCategoryConfiguration      ErrorCategory = "configuration"
	ErrorCategoryAuth               ErrorCategory = "auth"
	ErrorCategoryPermission         ErrorCategory = "permission"
	ErrorCategoryResourceNotFound   ErrorCategory = "resource_not_found"
	ErrorCategoryConflict           ErrorCategory = "conflict"
	ErrorCategoryRateLimit          ErrorCategory = "rate_limit"
	ErrorCategoryServiceUnavailable ErrorCategory = "service_unavailable"
	ErrorCategoryExternalService    ErrorCategory = "external_service"
	ErrorCategoryTimeout            ErrorCategory = "timeout"
	ErrorCategoryDatabase           ErrorCategory = "database"
	ErrorCategoryNetwork            ErrorCategory = "network"
	ErrorCategoryPubSub             ErrorCategory = "pubsub"
	ErrorCategoryStorage            ErrorCategory = "storage"
	ErrorCategoryInternal           ErrorCategory = "internal"
	ErrorCategoryDependency         ErrorCategory = "dependency"
)

// ErrorSeverity is a typed string for error severity levels.
type ErrorSeverity string

const (
	ErrorSeverityLow      ErrorSeverity = "low"
	ErrorSeverityMedium   ErrorSeverity = "medium"
	ErrorSeverityHigh     ErrorSeverity = "high"
	ErrorSeverityCritical ErrorSeverity = "critical"
	ErrorSeverityInfo     ErrorSeverity = "info"
)

// InfrastructureStatus is a typed string for infrastructure status.
type InfrastructureStatus string

const (
	InfrastructureStatusCritical    InfrastructureStatus = "critical"
	InfrastructureStatusDegraded    InfrastructureStatus = "degraded"
	InfrastructureStatusHealthy     InfrastructureStatus = "healthy"
	InfrastructureStatusStable      InfrastructureStatus = "stable"
	InfrastructureStatusUnknown     InfrastructureStatus = "unknown"
	InfrastructureStatusOperational InfrastructureStatus = "operational"
	InfrastructureStatusDown        InfrastructureStatus = "down"
)

// AuthMethod is a typed string for authentication method.
type AuthMethod string

const (
	AuthMethodKvPubSub        AuthMethod = "kv_pubsub"
	AuthMethodSession         AuthMethod = "session"
	AuthMethodProxy           AuthMethod = "proxy"
	AuthMethodOperatorSession AuthMethod = "operator_session"
	AuthMethodTest            AuthMethod = "test"
)

// InvestigationStatus is a typed string for investigation status.
type InvestigationStatus string

const (
	InvestigationStatusClosed    InvestigationStatus = "Closed"
	InvestigationStatusEscalated InvestigationStatus = "Escalated"
	InvestigationStatusOpen      InvestigationStatus = "Open"
	InvestigationStatusResolved  InvestigationStatus = "Resolved"
)

// CaseStatus is a typed string for case status.
type CaseStatus string

const (
	CaseStatusClosed             CaseStatus = "Closed"
	CaseStatusEscalated          CaseStatus = "Escalated"
	CaseStatusHumanReview        CaseStatus = "HumanReview"
	CaseStatusInProgress         CaseStatus = "InProgress"
	CaseStatusNew                CaseStatus = "New"
	CaseStatusResolved           CaseStatus = "Resolved"
	CaseStatusTriage             CaseStatus = "Triage"
	CaseStatusWaitingForCustomer CaseStatus = "WaitingForCustomer"
)
