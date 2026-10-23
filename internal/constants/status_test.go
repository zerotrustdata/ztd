// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statusEntry struct {
	GoConst string `json:"_go_const"`
	Value   string `json:"value"`
}

// loadStatusJSON loads protocol/constants/status.json for cross-checking Go constants.
func loadStatusJSON(t *testing.T) map[string]map[string]statusEntry {
	t.Helper()
	data, err := os.ReadFile("../../protocol/constants/status.json")
	require.NoError(t, err)
	var raw struct {
		Status map[string]map[string]statusEntry `json:"status"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	return raw.Status
}

func TestComponentStatusConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst ComponentStatus
		value   string
	}{
		{ComponentStatusActive, "active"},
		{ComponentStatusError, "error"},
		{ComponentStatusInactive, "inactive"},
		{ComponentStatusMaintenance, "maintenance"},
		{ComponentStatusDegraded, "degraded"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestWorkflowTypeConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst WorkflowType
		value   string
	}{
		{WorkflowTypeZtdBound, "ztd.bound"},
		{WorkflowTypeZtdCloudBound, "ztd.cloud.bound"},
		{WorkflowTypeZtdNotBound, "ztd.not.bound"},
		{WorkflowTypeTriage, "triage"},
		{WorkflowTypeInvestigation, "investigation"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestVersionStabilityExtendedConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "unstable", string(VersionStabilityUnstable))
	assert.Equal(t, "deprecated", string(VersionStabilityDeprecated))
}

func TestAITaskIdConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst AITaskId
		value   string
	}{
		{AITaskIDAgentContinue, "ai.agent.continue"},
		{AITaskIDChat, "ai.chat"},
		{AITaskIDCommand, "ai.command"},
		{AITaskIDDirectCommand, "ai.direct.command"},
		{AITaskIDFileEdit, "ai.file.edit"},
		{AITaskIDFsList, "ai.fs.list"},
		{AITaskIDPortCheck, "ai.port.check"},
		{AITaskIDIntentGrant, "ai.intent.grant"},
		{AITaskIDIntentRevoke, "ai.intent.revoke"},
		{AITaskIDRecursiveGrep, "ai.recursive_grep"},
		{AITaskIDRestoreFile, "ai.restore.file"},
		{AITaskIDFetchFileDiff, "ai.fetch.file.diff"},
		{AITaskIDFetchFileHistory, "ai.fetch.file.history"},
		{AITaskIDFetchHistory, "ai.fetch.history"},
		{AITaskIDFetchLogs, "ai.fetch.logs"},
		{AITaskIDFsRead, "ai.fs.read"},
		{AITaskIdChat, "ai.chat"},
		{AITaskIdCase, "ai.case"},
		{AITaskIdMemory, "ai.memory"},
		{AITaskIdCommand, "ai.command"},
		{AITaskIdCommandExecution, "ai.command.execution"},
		{AITaskIdDirectCommand, "ai.direct.command"},
		{AITaskIdIntentGrant, "ai.intent.grant"},
		{AITaskIdIntentRevoke, "ai.intent.revoke"},
		{AITaskIdFileEdit, "ai.file.edit"},
		{AITaskIdFileOperation, "ai.file.operation"},
		{AITaskIdFsList, "ai.fs.list"},
		{AITaskIdRecursiveGrep, "ai.recursive.grep"},
		{AITaskIdPortCheck, "ai.port.check"},
		{AITaskIdAgentContinue, "ai.agent.continue"},
		{AITaskIdInvestigationQuery, "ai.investigation.query"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestConsensusMemberConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst ConsensusMember
		value   string
	}{
		{ConsensusMemberAxiom, "axiom"},
		{ConsensusMemberConcord, "concord"},
		{ConsensusMemberVariance, "variance"},
		{ConsensusMemberPragma, "pragma"},
		{ConsensusMemberNemesis, "nemesis"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestConsensusAuditModeConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "unanimous", string(ConsensusAuditModeUnanimous))
	assert.Equal(t, "majority", string(ConsensusAuditModeMajority))
	assert.Equal(t, "tied", string(ConsensusAuditModeTied))
}

func TestConsensusAuditStatusConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ok", string(ConsensusAuditStatusOk))
	assert.Equal(t, "revised", string(ConsensusAuditStatusRevised))
	assert.Equal(t, "swap", string(ConsensusAuditStatusSwap))
}

func TestAuditorReasonConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst AuditorReason
		value   string
	}{
		{AuditorReasonOk, "ok"},
		{AuditorReasonRevised, "revised"},
		{AuditorReasonRevisedFromDissent, "revised_from_dissent"},
		{AuditorReasonSwappedToDissenter, "swapped_to_dissenter"},
		{AuditorReasonWhitelistViolation, "whitelist_violation"},
		{AuditorReasonNoValidRevision, "no_valid_revision"},
		{AuditorReasonAuditorError, "auditor_error"},
		{AuditorReasonEmptyResponse, "empty_response"},
		{AuditorReasonContextOverflow, "context_overflow"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestTieBreakReasonConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "shortest", string(TieBreakReasonShortest))
	assert.Equal(t, "excluded_nemesis", string(TieBreakReasonExcludedNemesis))
}

func TestReasoningAgentConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "sage", string(ReasoningAgentSage))
	assert.Equal(t, "dash", string(ReasoningAgentDash))
}

func TestErrorCodeConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst ErrorCode
		value   string
	}{
		{ErrorCodeGenericError, "ZTD-1000"},
		{ErrorCodeUnexpectedError, "ZTD-1001"},
		{ErrorCodeNotImplemented, "ZTD-1002"},
		{ErrorCodeConfigError, "ZTD-1100"},
		{ErrorCodeMissingEnvVar, "ZTD-1101"},
		{ErrorCodeInvalidSettings, "ZTD-1102"},
		{ErrorCodeServiceInitError, "ZTD-1103"},
		{ErrorCodeAuthError, "ZTD-1200"},
		{ErrorCodeTokenExpired, "ZTD-1201"},
		{ErrorCodeInvalidToken, "ZTD-1202"},
		{ErrorCodeInsufficientPermissions, "ZTD-1203"},
		{ErrorCodeDBConnectionError, "ZTD-1300"},
		{ErrorCodeDBQueryError, "ZTD-1301"},
		{ErrorCodeDBDocumentNotFound, "ZTD-1302"},
		{ErrorCodeDBWriteError, "ZTD-1303"},
		{ErrorCodeDBTransactionError, "ZTD-1304"},
		{ErrorCodePubSubConnectionError, "ZTD-1400"},
		{ErrorCodePubSubPublishError, "ZTD-1401"},
		{ErrorCodePubSubSubscribeError, "ZTD-1402"},
		{ErrorCodePubSubTopicError, "ZTD-1403"},
		{ErrorCodeStorageConnectionError, "ZTD-1500"},
		{ErrorCodeStorageReadError, "ZTD-1501"},
		{ErrorCodeStorageWriteError, "ZTD-1502"},
		{ErrorCodeStorageDeleteError, "ZTD-1503"},
		{ErrorCodeAPIConnectionError, "ZTD-1600"},
		{ErrorCodeAPITimeoutError, "ZTD-1601"},
		{ErrorCodeAPIResponseError, "ZTD-1602"},
		{ErrorCodeAPIRequestError, "ZTD-1603"},
		{ErrorCodeAPIRateLimitError, "ZTD-1604"},
		{ErrorCodeGenericNotFound, "ZTD-1605"},
		{ErrorCodeExternalServiceError, "ZTD-1607"},
		{ErrorCodeValidationError, "ZTD-1700"},
		{ErrorCodeMissingRequiredField, "ZTD-1701"},
		{ErrorCodeInvalidFieldFormat, "ZTD-1702"},
		{ErrorCodeInvalidFieldValue, "ZTD-1703"},
		{ErrorCodeInvalidFieldType, "ZTD-1704"},
		{ErrorCodeSchemaValidationFailed, "ZTD-1705"},
		{ErrorCodeSchemaNotFound, "ZTD-1706"},
		{ErrorCodeInvalidInput, "ZTD-1707"},
		{ErrorCodeBusinessLogicError, "ZTD-1800"},
		{ErrorCodeWorkflowError, "ZTD-1801"},
		{ErrorCodeStateTransitionError, "ZTD-1802"},
		{ErrorCodeResourceConflict, "ZTD-1803"},
		{ErrorCodeTaskCreationFailed, "ZTD-1804"},
		{ErrorCodeOperationFailed, "ZTD-1805"},
		{ErrorCodeGovernanceRejected, "ZTD-1806"},
		{ErrorCodeModelCapabilityUnsupported, "ZTD-1807"},
		{ErrorCodeServiceUnavailableError, "ZTD-1900"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestErrorCategoryConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst ErrorCategory
		value   string
	}{
		{ErrorCategoryValidation, "validation"},
		{ErrorCategoryBusinessLogic, "business_logic"},
		{ErrorCategoryConfiguration, "configuration"},
		{ErrorCategoryAuth, "auth"},
		{ErrorCategoryPermission, "permission"},
		{ErrorCategoryResourceNotFound, "resource_not_found"},
		{ErrorCategoryConflict, "conflict"},
		{ErrorCategoryRateLimit, "rate_limit"},
		{ErrorCategoryServiceUnavailable, "service_unavailable"},
		{ErrorCategoryExternalService, "external_service"},
		{ErrorCategoryTimeout, "timeout"},
		{ErrorCategoryDatabase, "database"},
		{ErrorCategoryNetwork, "network"},
		{ErrorCategoryPubSub, "pubsub"},
		{ErrorCategoryStorage, "storage"},
		{ErrorCategoryInternal, "internal"},
		{ErrorCategoryDependency, "dependency"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestErrorSeverityConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst ErrorSeverity
		value   string
	}{
		{ErrorSeverityLow, "low"},
		{ErrorSeverityMedium, "medium"},
		{ErrorSeverityHigh, "high"},
		{ErrorSeverityCritical, "critical"},
		{ErrorSeverityInfo, "info"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestStatusJSONValidity(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../protocol/constants/status.json")
	require.NoError(t, err)
	assert.True(t, json.Valid(data), "status.json is valid JSON")
}

func TestStatusJSONGoConstPresence(t *testing.T) {
	t.Parallel()
	statusJSON := loadStatusJSON(t)
	for catName, entries := range statusJSON {
		for key, meta := range entries {
			if key == "" {
				continue
			}
			assert.NotEmpty(t, meta.GoConst,
				"category %s key %s missing _go_const", catName, key)
			assert.NotEmpty(t, meta.Value,
				"category %s key %s missing value", catName, key)
		}
	}
}

func TestStatusJSONComponentStatusMatches(t *testing.T) {
	t.Parallel()
	statusJSON := loadStatusJSON(t)
	cat, ok := statusJSON["component_status"]
	require.True(t, ok, "component_status category exists")
	expected := map[string]string{
		"active":      "active",
		"error":       "error",
		"inactive":    "inactive",
		"maintenance": "maintenance",
		"degraded":    "degraded",
	}
	for key, exp := range expected {
		entry, exists := cat[key]
		require.True(t, exists, "component_status key %s exists in JSON", key)
		assert.Equal(t, exp, entry.Value)
	}
}

func TestStatusJSONOperatorTypeMatches(t *testing.T) {
	t.Parallel()
	statusJSON := loadStatusJSON(t)
	cat, ok := statusJSON["operator_type"]
	require.True(t, ok, "operator_type category exists")
	require.Len(t, cat, 2, "operator_type vocabulary is exactly embedded and remote")
	assert.Equal(t, string(OperatorTypeEmbedded), cat["embedded"].Value)
	assert.Equal(t, string(OperatorTypeRemote), cat["remote"].Value)
	_, hasSystem := cat["system"]
	assert.False(t, hasSystem, "removed 'system' operator type must not reappear")
	_, hasCloud := cat["cloud"]
	assert.False(t, hasCloud, "removed 'cloud' operator type must not reappear")
	_, hasCloudSubtype := statusJSON["cloud_subtype"]
	assert.False(t, hasCloudSubtype, "removed 'cloud_subtype' category must not reappear")
}

func TestStatusJSONConsensusMemberMatches(t *testing.T) {
	t.Parallel()
	statusJSON := loadStatusJSON(t)
	cat, ok := statusJSON["consensus_member"]
	require.True(t, ok, "consensus_member category exists")
	expected := map[string]string{
		"axiom":    "axiom",
		"concord":  "concord",
		"variance": "variance",
		"pragma":   "pragma",
		"nemesis":  "nemesis",
	}
	for key, exp := range expected {
		entry, exists := cat[key]
		require.True(t, exists, "consensus_member key %s exists in JSON", key)
		assert.Equal(t, exp, entry.Value)
	}
}

func TestInfrastructureStatusConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst InfrastructureStatus
		value   string
	}{
		{InfrastructureStatusCritical, "critical"},
		{InfrastructureStatusDegraded, "degraded"},
		{InfrastructureStatusHealthy, "healthy"},
		{InfrastructureStatusStable, "stable"},
		{InfrastructureStatusUnknown, "unknown"},
		{InfrastructureStatusOperational, "operational"},
		{InfrastructureStatusDown, "down"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestAuthMethodConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst AuthMethod
		value   string
	}{
		{AuthMethodKvPubSub, "kv_pubsub"},
		{AuthMethodSession, "session"},
		{AuthMethodProxy, "proxy"},
		{AuthMethodOperatorSession, "operator_session"},
		{AuthMethodTest, "test"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestToolCallStatusConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		goConst ToolCallStatus
		value   string
	}{
		{ToolCallStatusCompleted, "completed"},
		{ToolCallStatusFailed, "failed"},
		{ToolCallStatusStarted, "started"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestStatusJSONOperatorCapabilitiesMatchesCentralVocabulary(t *testing.T) {
	t.Parallel()
	category := loadStatusJSON(t)["operator_capability"]
	capabilities := OperatorCapabilities{OperatorCapabilityData, OperatorCapabilityInference, OperatorCapabilityProvenance, OperatorCapabilityObserver}
	require.Len(t, category, len(capabilities))
	for _, capability := range capabilities {
		require.Equal(t, string(capability), category[string(capability)].Value)
	}
}

func TestOperatorStaleAfter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{"nothing declared", 0, OperatorHeartbeatStaleAfter},
		{"negative treated as undeclared", -time.Second, OperatorHeartbeatStaleAfter},
		{"default interval keeps the floor", OperatorDefaultHeartbeatInterval, OperatorHeartbeatStaleAfter},
		{"just under the floor multiple", 29 * time.Second, OperatorHeartbeatStaleAfter},
		{"two missed beats", 90 * time.Second, 180 * time.Second},
		{"maximum interval", OperatorHeartbeatMaxInterval, 2 * OperatorHeartbeatMaxInterval},
		{"clamped past the maximum", time.Hour, 2 * OperatorHeartbeatMaxInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, OperatorStaleAfter(tt.interval))
		})
	}
}
