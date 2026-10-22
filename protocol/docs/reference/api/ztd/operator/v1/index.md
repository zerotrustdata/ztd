# Protocol Documentation
<a name="top"></a>

## Table of Contents

- [ztd/operator/v1/operator.proto](#ztd_operator_v1_operator-proto)
    - [A2aCallRequested](#ztd-operator-v1-A2aCallRequested)
    - [ActionReceipt](#ztd-operator-v1-ActionReceipt)
    - [AssertionResponse](#ztd-operator-v1-AssertionResponse)
    - [AttestationResponse](#ztd-operator-v1-AttestationResponse)
    - [AuditEvent](#ztd-operator-v1-AuditEvent)
    - [AuditFileMutation](#ztd-operator-v1-AuditFileMutation)
    - [AuditMsgRequested](#ztd-operator-v1-AuditMsgRequested)
    - [AuditWebSession](#ztd-operator-v1-AuditWebSession)
    - [BindOperatorsRequested](#ztd-operator-v1-BindOperatorsRequested)
    - [BindOperatorsResult](#ztd-operator-v1-BindOperatorsResult)
    - [CheckPortRequested](#ztd-operator-v1-CheckPortRequested)
    - [CommandCancelRequested](#ztd-operator-v1-CommandCancelRequested)
    - [CommandRequested](#ztd-operator-v1-CommandRequested)
    - [CommandRequested.EnvironmentEntry](#ztd-operator-v1-CommandRequested-EnvironmentEntry)
    - [CommandResult](#ztd-operator-v1-CommandResult)
    - [CommitmentAttestation](#ztd-operator-v1-CommitmentAttestation)
    - [CreateDeviceLinkRequested](#ztd-operator-v1-CreateDeviceLinkRequested)
    - [DeleteDeviceLinkRequested](#ztd-operator-v1-DeleteDeviceLinkRequested)
    - [DeterministicStageEvidence](#ztd-operator-v1-DeterministicStageEvidence)
    - [DeviceLink](#ztd-operator-v1-DeviceLink)
    - [DeviceLinkResult](#ztd-operator-v1-DeviceLinkResult)
    - [DirectCommandAuditRequested](#ztd-operator-v1-DirectCommandAuditRequested)
    - [DirectCommandResultAuditRequested](#ztd-operator-v1-DirectCommandResultAuditRequested)
    - [DiskDetails](#ztd-operator-v1-DiskDetails)
    - [DocumentDeleteRequested](#ztd-operator-v1-DocumentDeleteRequested)
    - [DocumentUpdateRequested](#ztd-operator-v1-DocumentUpdateRequested)
    - [EnvironmentDetails](#ztd-operator-v1-EnvironmentDetails)
    - [EvalAnswerRequested](#ztd-operator-v1-EvalAnswerRequested)
    - [ExecutionStatusUpdate](#ztd-operator-v1-ExecutionStatusUpdate)
    - [FetchFileDiffRequested](#ztd-operator-v1-FetchFileDiffRequested)
    - [FetchFileDiffResult](#ztd-operator-v1-FetchFileDiffResult)
    - [FetchFileHistoryRequested](#ztd-operator-v1-FetchFileHistoryRequested)
    - [FetchFileHistoryResult](#ztd-operator-v1-FetchFileHistoryResult)
    - [FetchHistoryRequested](#ztd-operator-v1-FetchHistoryRequested)
    - [FetchHistoryResult](#ztd-operator-v1-FetchHistoryResult)
    - [FetchLogsRequested](#ztd-operator-v1-FetchLogsRequested)
    - [FetchLogsResult](#ztd-operator-v1-FetchLogsResult)
    - [FileDiffEntry](#ztd-operator-v1-FileDiffEntry)
    - [FileEditRequested](#ztd-operator-v1-FileEditRequested)
    - [FileEditResult](#ztd-operator-v1-FileEditResult)
    - [FileHistoryEntry](#ztd-operator-v1-FileHistoryEntry)
    - [FingerprintDetails](#ztd-operator-v1-FingerprintDetails)
    - [FsEntry](#ztd-operator-v1-FsEntry)
    - [FsGrepMatch](#ztd-operator-v1-FsGrepMatch)
    - [FsGrepRequested](#ztd-operator-v1-FsGrepRequested)
    - [FsGrepResult](#ztd-operator-v1-FsGrepResult)
    - [FsListRequested](#ztd-operator-v1-FsListRequested)
    - [FsListResult](#ztd-operator-v1-FsListResult)
    - [FsReadRequested](#ztd-operator-v1-FsReadRequested)
    - [FsReadResult](#ztd-operator-v1-FsReadResult)
    - [GetRevocationBundleRequested](#ztd-operator-v1-GetRevocationBundleRequested)
    - [GetRevocationBundleResult](#ztd-operator-v1-GetRevocationBundleResult)
    - [GovernanceRejection](#ztd-operator-v1-GovernanceRejection)
    - [GovernedDispatchResult](#ztd-operator-v1-GovernedDispatchResult)
    - [HeartbeatRequested](#ztd-operator-v1-HeartbeatRequested)
    - [HeartbeatResult](#ztd-operator-v1-HeartbeatResult)
    - [InferenceChoiceAnswer](#ztd-operator-v1-InferenceChoiceAnswer)
    - [InferenceChoiceQuestion](#ztd-operator-v1-InferenceChoiceQuestion)
    - [InferenceCompletion](#ztd-operator-v1-InferenceCompletion)
    - [InferenceDecisionAnswer](#ztd-operator-v1-InferenceDecisionAnswer)
    - [InferenceDecisionCriterion](#ztd-operator-v1-InferenceDecisionCriterion)
    - [InferenceDecisionProbability](#ztd-operator-v1-InferenceDecisionProbability)
    - [InferenceDecisionQuestion](#ztd-operator-v1-InferenceDecisionQuestion)
    - [InferenceDecisionRequest](#ztd-operator-v1-InferenceDecisionRequest)
    - [InferenceDecisionResult](#ztd-operator-v1-InferenceDecisionResult)
    - [InferenceDispatchFailure](#ztd-operator-v1-InferenceDispatchFailure)
    - [InferenceDispatchRequest](#ztd-operator-v1-InferenceDispatchRequest)
    - [InferenceDispatchResponse](#ztd-operator-v1-InferenceDispatchResponse)
    - [InferenceDispatchStreamFailure](#ztd-operator-v1-InferenceDispatchStreamFailure)
    - [InferenceDispatchStreamFrame](#ztd-operator-v1-InferenceDispatchStreamFrame)
    - [InferenceHostedEgressAttestation](#ztd-operator-v1-InferenceHostedEgressAttestation)
    - [InferenceMessage](#ztd-operator-v1-InferenceMessage)
    - [InferenceMessagePart](#ztd-operator-v1-InferenceMessagePart)
    - [InferenceModelVariant](#ztd-operator-v1-InferenceModelVariant)
    - [InferenceNoulQuestion](#ztd-operator-v1-InferenceNoulQuestion)
    - [InferenceProgressEvent](#ztd-operator-v1-InferenceProgressEvent)
    - [InferenceProviderAttemptRecord](#ztd-operator-v1-InferenceProviderAttemptRecord)
    - [InferenceRequested](#ztd-operator-v1-InferenceRequested)
    - [InferenceResponseFormat](#ztd-operator-v1-InferenceResponseFormat)
    - [InferenceResponsePart](#ztd-operator-v1-InferenceResponsePart)
    - [InferenceResult](#ztd-operator-v1-InferenceResult)
    - [InferenceScoreAnswer](#ztd-operator-v1-InferenceScoreAnswer)
    - [InferenceScoreQuestion](#ztd-operator-v1-InferenceScoreQuestion)
    - [InferenceThinkingControl](#ztd-operator-v1-InferenceThinkingControl)
    - [InferenceToolCall](#ztd-operator-v1-InferenceToolCall)
    - [InferenceToolChoice](#ztd-operator-v1-InferenceToolChoice)
    - [InferenceToolDeclaration](#ztd-operator-v1-InferenceToolDeclaration)
    - [InferenceToolResult](#ztd-operator-v1-InferenceToolResult)
    - [IngressRefusal](#ztd-operator-v1-IngressRefusal)
    - [ListDeviceLinksRequested](#ztd-operator-v1-ListDeviceLinksRequested)
    - [ListDeviceLinksResult](#ztd-operator-v1-ListDeviceLinksResult)
    - [ListOperatorSlotsRequested](#ztd-operator-v1-ListOperatorSlotsRequested)
    - [ListOperatorSlotsResult](#ztd-operator-v1-ListOperatorSlotsResult)
    - [ListPasskeyCredentialsRequested](#ztd-operator-v1-ListPasskeyCredentialsRequested)
    - [ListPasskeyCredentialsResult](#ztd-operator-v1-ListPasskeyCredentialsResult)
    - [McpCallRequested](#ztd-operator-v1-McpCallRequested)
    - [McpPromptGetRequested](#ztd-operator-v1-McpPromptGetRequested)
    - [McpPromptListRequested](#ztd-operator-v1-McpPromptListRequested)
    - [McpResourceListRequested](#ztd-operator-v1-McpResourceListRequested)
    - [McpResourceReadRequested](#ztd-operator-v1-McpResourceReadRequested)
    - [MemoryDetails](#ztd-operator-v1-MemoryDetails)
    - [NetworkInfo](#ztd-operator-v1-NetworkInfo)
    - [NetworkInterface](#ztd-operator-v1-NetworkInterface)
    - [OSDetails](#ztd-operator-v1-OSDetails)
    - [OllamaModelInventoryRequested](#ztd-operator-v1-OllamaModelInventoryRequested)
    - [OllamaModelInventoryResult](#ztd-operator-v1-OllamaModelInventoryResult)
    - [OllamaModelResidencyModel](#ztd-operator-v1-OllamaModelResidencyModel)
    - [OllamaModelResidencyRequested](#ztd-operator-v1-OllamaModelResidencyRequested)
    - [OllamaModelResidencyResult](#ztd-operator-v1-OllamaModelResidencyResult)
    - [OperatorDocument](#ztd-operator-v1-OperatorDocument)
    - [OperatorRuntimeConfig](#ztd-operator-v1-OperatorRuntimeConfig)
    - [PasskeyAuthChallengeRequested](#ztd-operator-v1-PasskeyAuthChallengeRequested)
    - [PasskeyAuthChallengeResult](#ztd-operator-v1-PasskeyAuthChallengeResult)
    - [PasskeyAuthVerifyRequested](#ztd-operator-v1-PasskeyAuthVerifyRequested)
    - [PasskeyAuthVerifyResult](#ztd-operator-v1-PasskeyAuthVerifyResult)
    - [PasskeyCredential](#ztd-operator-v1-PasskeyCredential)
    - [PasskeyRegisterChallengeRequested](#ztd-operator-v1-PasskeyRegisterChallengeRequested)
    - [PasskeyRegisterChallengeResult](#ztd-operator-v1-PasskeyRegisterChallengeResult)
    - [PasskeyRegisterChallengeResult.AuthenticatorSelection](#ztd-operator-v1-PasskeyRegisterChallengeResult-AuthenticatorSelection)
    - [PasskeyRegisterChallengeResult.PublicKeyCredentialParameters](#ztd-operator-v1-PasskeyRegisterChallengeResult-PublicKeyCredentialParameters)
    - [PasskeyRegisterChallengeResult.RelyingParty](#ztd-operator-v1-PasskeyRegisterChallengeResult-RelyingParty)
    - [PasskeyRegisterChallengeResult.UserInfo](#ztd-operator-v1-PasskeyRegisterChallengeResult-UserInfo)
    - [PasskeyRegisterVerifyRequested](#ztd-operator-v1-PasskeyRegisterVerifyRequested)
    - [PasskeyRegisterVerifyResult](#ztd-operator-v1-PasskeyRegisterVerifyResult)
    - [PerformanceMetrics](#ztd-operator-v1-PerformanceMetrics)
    - [PortCheckEntry](#ztd-operator-v1-PortCheckEntry)
    - [PortCheckResult](#ztd-operator-v1-PortCheckResult)
    - [ProviderModelInventoryEntry](#ztd-operator-v1-ProviderModelInventoryEntry)
    - [ReceiptPersistenceAttestation](#ztd-operator-v1-ReceiptPersistenceAttestation)
    - [RestoreFileRequested](#ztd-operator-v1-RestoreFileRequested)
    - [RestoreFileResult](#ztd-operator-v1-RestoreFileResult)
    - [RevokeCertificateRequested](#ztd-operator-v1-RevokeCertificateRequested)
    - [RevokeCertificateResult](#ztd-operator-v1-RevokeCertificateResult)
    - [RevokePasskeyCredentialRequested](#ztd-operator-v1-RevokePasskeyCredentialRequested)
    - [RevokePasskeyCredentialResult](#ztd-operator-v1-RevokePasskeyCredentialResult)
    - [RunShellCommandArguments](#ztd-operator-v1-RunShellCommandArguments)
    - [SetTargetContextRequested](#ztd-operator-v1-SetTargetContextRequested)
    - [SetTargetContextResult](#ztd-operator-v1-SetTargetContextResult)
    - [ShutdownRequested](#ztd-operator-v1-ShutdownRequested)
    - [SignCertificateRequested](#ztd-operator-v1-SignCertificateRequested)
    - [SignCertificateResult](#ztd-operator-v1-SignCertificateResult)
    - [SystemIdentity](#ztd-operator-v1-SystemIdentity)
    - [TerminateOperatorRequested](#ztd-operator-v1-TerminateOperatorRequested)
    - [TerminateOperatorResult](#ztd-operator-v1-TerminateOperatorResult)
    - [UnbindOperatorsRequested](#ztd-operator-v1-UnbindOperatorsRequested)
    - [UnbindOperatorsResult](#ztd-operator-v1-UnbindOperatorsResult)
    - [UptimeInfo](#ztd-operator-v1-UptimeInfo)
    - [UserDetails](#ztd-operator-v1-UserDetails)
    - [VersionInfo](#ztd-operator-v1-VersionInfo)
  
    - [DeterministicStageKind](#ztd-operator-v1-DeterministicStageKind)
    - [DeterministicStageOutcome](#ztd-operator-v1-DeterministicStageOutcome)
    - [ExecutionStatus](#ztd-operator-v1-ExecutionStatus)
    - [GovernanceRejectionCode](#ztd-operator-v1-GovernanceRejectionCode)
    - [HeartbeatType](#ztd-operator-v1-HeartbeatType)
    - [InferenceDispatchFailureCode](#ztd-operator-v1-InferenceDispatchFailureCode)
    - [InferenceLoadState](#ztd-operator-v1-InferenceLoadState)
    - [InferenceMessageRole](#ztd-operator-v1-InferenceMessageRole)
    - [InferenceProviderAttemptStatus](#ztd-operator-v1-InferenceProviderAttemptStatus)
    - [InferenceRetryClassification](#ztd-operator-v1-InferenceRetryClassification)
    - [InferenceTimingSource](#ztd-operator-v1-InferenceTimingSource)
    - [InferenceToolChoiceMode](#ztd-operator-v1-InferenceToolChoiceMode)
    - [IngressRefusalCode](#ztd-operator-v1-IngressRefusalCode)
    - [L2Status](#ztd-operator-v1-L2Status)
    - [L3Status](#ztd-operator-v1-L3Status)
    - [ModelRole](#ztd-operator-v1-ModelRole)
    - [ReceiptFailureCode](#ztd-operator-v1-ReceiptFailureCode)
  
    - [OperatorService](#ztd-operator-v1-OperatorService)
  
- [Scalar Value Types](#scalar-value-types)



<a name="ztd_operator_v1_operator-proto"></a>
<p align="right"><a href="#top">Top</a></p>

## ztd/operator/v1/operator.proto



<a name="ztd-operator-v1-A2aCallRequested"></a>

### A2aCallRequested
Payload for ztd.v1.operator.a2a.call.requested
Mirrors McpCallRequested for the Agent-to-Agent JSON protocol.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| skill_name | [string](#string) |  | Target agent skill / capability name. |
| payload_json | [string](#string) |  | JSON-encoded A2A task payload. |
| execution_id | [string](#string) |  |  |






<a name="ztd-operator-v1-ActionReceipt"></a>

### ActionReceipt
ActionReceipt is the signed proof of an executing, completed, or failed mutation. L5 emits and persists it before execution, then updates and attests it after execution.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| transaction_id | [string](#string) |  | Original GovernanceEnvelope ID |
| transaction_hash | [string](#string) |  | Original GovernanceEnvelope hash |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  | Final execution status |
| result_summary | [string](#string) |  | Concise summary of the result (e.g. &#34;exit code 0&#34;, &#34;file written&#34;) |
| state_root_before | [string](#string) |  | State root before execution |
| state_root_after | [string](#string) |  | State root after execution |
| executed_at_unix_ms | [int64](#int64) |  | Timestamp when execution finished |
| signer_key_id | [string](#string) |  | ID of the Warden&#39;s signing key |
| signature | [string](#string) |  | ED25519 signature over the canonical receipt fields and deterministic stage evidence hash. |
| l2_status | [L2Status](#ztd-operator-v1-L2Status) |  | Status of L2 (Consensus) signature verification. Distinguishes between &#34;not required&#34; vs &#34;required but failed&#34; for compliance. |
| l3_status | [L3Status](#ztd-operator-v1-L3Status) |  | Status of L3 (Notary/Human) proof verification. Distinguishes between &#34;not required&#34; vs &#34;required but failed&#34; for compliance. |
| deterministic_stage_evidence | [DeterministicStageEvidence](#ztd-operator-v1-DeterministicStageEvidence) | repeated |  |
| final_persistence_attestation | [ReceiptPersistenceAttestation](#ztd-operator-v1-ReceiptPersistenceAttestation) |  |  |
| failure_code | [ReceiptFailureCode](#ztd-operator-v1-ReceiptFailureCode) |  | Typed classification of a FAILED receipt. UNSPECIFIED on non-failed receipts. Bound into the signature via canonicalization. |
| event_type | [string](#string) |  | The semantic request event that produced this receipt. |
| action_type | [string](#string) |  | The registry-derived governance class for the request event. |
| rejection_code | [GovernanceRejectionCode](#ztd-operator-v1-GovernanceRejectionCode) |  | The verification check that rejected the transaction. Set exactly when failure_code is GOVERNANCE_REJECTED; UNSPECIFIED otherwise. Bound into the signature via canonicalization. |






<a name="ztd-operator-v1-AssertionResponse"></a>

### AssertionResponse



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| raw_id | [string](#string) |  |  |
| client_data_json | [string](#string) |  |  |
| authenticator_data | [string](#string) |  |  |
| signature | [string](#string) |  |  |
| user_handle | [string](#string) |  |  |






<a name="ztd-operator-v1-AttestationResponse"></a>

### AttestationResponse



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| raw_id | [string](#string) |  |  |
| client_data_json | [string](#string) |  |  |
| attestation_object | [string](#string) |  |  |
| transports | [string](#string) | repeated |  |






<a name="ztd-operator-v1-AuditEvent"></a>

### AuditEvent



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [int64](#int64) |  |  |
| operator_session_id | [string](#string) |  |  |
| timestamp | [string](#string) |  |  |
| type | [string](#string) |  |  |
| content_text | [string](#string) |  |  |
| command_raw | [string](#string) |  |  |
| command_exit_code | [int32](#int32) |  |  |
| command_stdout | [string](#string) |  |  |
| command_stderr | [string](#string) |  |  |
| execution_duration_ms | [int64](#int64) |  |  |
| stored_locally | [bool](#bool) |  |  |
| file_mutations | [AuditFileMutation](#ztd-operator-v1-AuditFileMutation) | repeated |  |






<a name="ztd-operator-v1-AuditFileMutation"></a>

### AuditFileMutation



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [int64](#int64) |  |  |
| filepath | [string](#string) |  |  |
| operation | [string](#string) |  |  |
| ledger_hash_before | [string](#string) |  |  |
| ledger_hash_after | [string](#string) |  |  |
| diff_stat | [string](#string) |  |  |






<a name="ztd-operator-v1-AuditMsgRequested"></a>

### AuditMsgRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| content | [string](#string) |  |  |






<a name="ztd-operator-v1-AuditWebSession"></a>

### AuditWebSession



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| title | [string](#string) |  |  |
| created_at | [string](#string) |  |  |
| user_identity | [string](#string) |  |  |






<a name="ztd-operator-v1-BindOperatorsRequested"></a>

### BindOperatorsRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| operator_ids | [string](#string) | repeated |  |
| user_id | [string](#string) |  |  |
| web_session_id | [string](#string) |  | The client/web session ID |






<a name="ztd-operator-v1-BindOperatorsResult"></a>

### BindOperatorsResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| bound_count | [int32](#int32) |  |  |
| failed_count | [int32](#int32) |  |  |
| bound_operator_ids | [string](#string) | repeated |  |
| failed_operator_ids | [string](#string) | repeated |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-CheckPortRequested"></a>

### CheckPortRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| port | [int32](#int32) |  |  |
| host | [string](#string) |  |  |
| protocol | [string](#string) |  |  |






<a name="ztd-operator-v1-CommandCancelRequested"></a>

### CommandCancelRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |






<a name="ztd-operator-v1-CommandRequested"></a>

### CommandRequested
Payload for ztd.v1.operator.command.requested


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| command | [string](#string) |  | Shell command string to execute |
| execution_id | [string](#string) |  | Unique execution identifier (correlation key) |
| justification | [string](#string) |  | Justification for running this command (High-level Sage request) |
| timeout_seconds | [int32](#int32) |  | Execution timeout override in seconds |
| intent | [string](#string) |  | Intent: The high-level goal (The &#34;Why&#34;) |
| environment | [CommandRequested.EnvironmentEntry](#ztd-operator-v1-CommandRequested-EnvironmentEntry) | repeated | Environment variables |
| working_directory | [string](#string) |  | Working directory override |






<a name="ztd-operator-v1-CommandRequested-EnvironmentEntry"></a>

### CommandRequested.EnvironmentEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| key | [string](#string) |  |  |
| value | [string](#string) |  |  |






<a name="ztd-operator-v1-CommandResult"></a>

### CommandResult
Result payload for ztd.v1.operator.command.completed or failed


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| stdout | [string](#string) |  |  |
| error | [string](#string) |  |  |
| stderr | [string](#string) |  |  |
| return_code | [int32](#int32) |  |  |
| execution_time_seconds | [float](#float) |  |  |
| start_time_unix_ms | [int64](#int64) |  | Timestamp when execution started |
| end_time_unix_ms | [int64](#int64) |  | Timestamp when execution ended |






<a name="ztd-operator-v1-CommitmentAttestation"></a>

### CommitmentAttestation
CommitmentAttestation is the Auditor&#39;s signed record that a verified
transaction is consistent with the prior ledger of commitments and is
authorized to be executed.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| transaction_id | [string](#string) |  | TransactionID and TransactionHash identify the envelope being committed (these mirror UAPEnvelope.Id / TransactionHash). |
| transaction_hash | [string](#string) |  |  |
| prior_commitment_hash | [string](#string) |  | PriorCommitmentHash is the Hash of the immediately-preceding CommitmentAttestation in the ledger, or GenesisPriorHash for the first commitment. This is what makes the ledger a chain. |
| state_root_at_commit | [string](#string) |  | StateRootAtCommit is the Gateway&#39;s state root at the moment the Auditor signed this attestation. It is checked against the envelope.StateMerkleRoot the L2 consensus signed over. |
| l2_signature_digest | [string](#string) |  | L2SignatureDigest, WardenIntentSignatureDigest and HumanSignatureDigest bind each authority&#39;s signatures without duplicating them. The digest input removes empty signature strings, sorts the remaining UTF-8 strings by byte value, prefixes the list with its unsigned 32-bit big-endian count, and appends each string as an unsigned 32-bit big-endian byte length followed by its bytes. The digest is the lowercase hexadecimal SHA-256 of that input, or the empty string when no signatures remain. |
| warden_intent_signature_digest | [string](#string) |  |  |
| human_signature_digest | [string](#string) |  |  |
| action_type | [string](#string) |  | ActionType and TargetResource are carried for ledger readability without re-fetching the original envelope. |
| target_resource | [string](#string) |  |  |
| committed_at_unix_ms | [int64](#int64) |  | CommittedAtUnixMs is the Auditor&#39;s local timestamp at signing. |
| auditor_key_id | [string](#string) |  | AuditorKeyID identifies which Auditor key signed this attestation. |
| signature | [string](#string) |  | ED25519 signature over the canonical serialization of fields 1-11. String fields are UTF-8 encoded in field-number order as an unsigned 32-bit big-endian byte length followed by the bytes. CommittedAtUnixMs is encoded as a signed 64-bit two&#39;s-complement big-endian integer without a length prefix. |
| hash | [string](#string) |  | Lowercase hexadecimal SHA-256 of the canonical serialization signed by Signature. It is what the next attestation chains to. |






<a name="ztd-operator-v1-CreateDeviceLinkRequested"></a>

### CreateDeviceLinkRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |
| organization_id | [string](#string) |  |  |
| operator_id | [string](#string) |  |  |
| web_session_id | [string](#string) |  |  |
| name | [string](#string) |  |  |
| max_uses | [int32](#int32) |  |  |
| ttl_seconds | [int32](#int32) |  |  |






<a name="ztd-operator-v1-DeleteDeviceLinkRequested"></a>

### DeleteDeviceLinkRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| token | [string](#string) |  |  |
| user_id | [string](#string) |  |  |






<a name="ztd-operator-v1-DeterministicStageEvidence"></a>

### DeterministicStageEvidence



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| stage_id | [string](#string) |  |  |
| kind | [DeterministicStageKind](#ztd-operator-v1-DeterministicStageKind) |  |  |
| monotonic_start_ns | [int64](#int64) |  |  |
| monotonic_end_ns | [int64](#int64) |  |  |
| clock_domain | [string](#string) |  |  |
| timing_source | [string](#string) |  |  |
| outcome | [DeterministicStageOutcome](#ztd-operator-v1-DeterministicStageOutcome) |  |  |
| transaction_id | [string](#string) |  |  |
| transaction_hash | [string](#string) |  |  |
| action_type | [string](#string) |  |  |
| operator_id | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| requestor_user_id | [string](#string) |  |  |
| acting_app_id | [string](#string) |  |  |
| case_id | [string](#string) |  |  |
| investigation_id | [string](#string) |  |  |
| task_id | [string](#string) |  |  |
| state_root_before | [string](#string) |  |  |
| state_root_after | [string](#string) |  |  |
| signer_key_id | [string](#string) |  |  |
| receipt_signature_digest | [string](#string) |  |  |
| commitment_hash | [string](#string) |  |  |
| prior_commitment_hash | [string](#string) |  |  |
| l2_signature_digest | [string](#string) |  |  |
| l3_signature_digest | [string](#string) |  |  |
| audit_record_id | [string](#string) |  |  |
| parent_stage_id | [string](#string) |  |  |
| doctrine_bundle_hash | [string](#string) |  |  |
| doctrine_bundle_version | [string](#string) |  |  |






<a name="ztd-operator-v1-DeviceLink"></a>

### DeviceLink



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| token | [string](#string) |  |  |
| user_id | [string](#string) |  |  |
| organization_id | [string](#string) |  |  |
| operator_id | [string](#string) |  |  |
| web_session_id | [string](#string) |  |  |
| name | [string](#string) |  |  |
| max_uses | [int32](#int32) |  |  |
| uses | [int32](#int32) |  |  |
| status | [string](#string) |  |  |
| created_at_unix_ms | [int64](#int64) |  |  |
| expires_at_unix_ms | [int64](#int64) |  |  |






<a name="ztd-operator-v1-DeviceLinkResult"></a>

### DeviceLinkResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| link | [DeviceLink](#ztd-operator-v1-DeviceLink) |  |  |
| operator_command | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-DirectCommandAuditRequested"></a>

### DirectCommandAuditRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| command | [string](#string) |  |  |
| execution_id | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| type | [string](#string) |  |  |






<a name="ztd-operator-v1-DirectCommandResultAuditRequested"></a>

### DirectCommandResultAuditRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| command | [string](#string) |  |  |
| execution_id | [string](#string) |  |  |
| output | [string](#string) |  |  |
| stderr | [string](#string) |  |  |
| exit_code | [int32](#int32) |  |  |
| execution_time_seconds | [float](#float) |  |  |






<a name="ztd-operator-v1-DiskDetails"></a>

### DiskDetails



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| total_gb | [double](#double) |  |  |
| used_gb | [double](#double) |  |  |
| free_gb | [double](#double) |  |  |
| percent | [double](#double) |  |  |






<a name="ztd-operator-v1-DocumentDeleteRequested"></a>

### DocumentDeleteRequested
DocumentDeleteRequested is the typed payload for governed document
deletion operations (cases, investigations). App workloads submit
these through the governance envelope pipeline for L1-L5 verification
before the gateway removes them via DocDelete.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| collection | [string](#string) |  | Target collection name. |
| document_id | [string](#string) |  | Document ID to delete. |






<a name="ztd-operator-v1-DocumentUpdateRequested"></a>

### DocumentUpdateRequested
DocumentUpdateRequested is the typed payload for governed document
create/update operations (cases, investigations, memories, tasks).
App workloads submit these through the governance envelope pipeline
for L1-L5 verification before the gateway persists them via DocSet.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| collection | [string](#string) |  | Target collection name (e.g., &#34;cases&#34;, &#34;investigations&#34;, &#34;memories&#34;). |
| document_id | [string](#string) |  | Document ID to create or update. |
| updates | [google.protobuf.Struct](#google-protobuf-Struct) |  | Field updates as a structured payload. The gateway converts this to JSON for document store persistence. |
| merge | [bool](#bool) |  | If true, use PATCH (merge); if false, use PUT (replace). |






<a name="ztd-operator-v1-EnvironmentDetails"></a>

### EnvironmentDetails



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| pwd | [string](#string) |  |  |
| lang | [string](#string) |  |  |
| timezone | [string](#string) |  |  |
| term | [string](#string) |  |  |
| is_container | [bool](#bool) |  |  |
| container_runtime | [string](#string) |  |  |
| container_signals | [string](#string) | repeated |  |
| init_system | [string](#string) |  |  |






<a name="ztd-operator-v1-EvalAnswerRequested"></a>

### EvalAnswerRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| prompt_id | [string](#string) |  | Unique identifier for this prompt/task in the benchmark |
| benchmark | [string](#string) |  | Benchmark name (e.g., &#34;ifeval&#34;, &#34;simpleqa&#34;, &#34;gpqa&#34;) |
| answer | [string](#string) |  | The model&#39;s answer text (opaque to the Gateway) |
| model | [string](#string) |  | Model identifier (provider:model format, e.g., &#34;openai:gpt-4&#34;) |






<a name="ztd-operator-v1-ExecutionStatusUpdate"></a>

### ExecutionStatusUpdate



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| command | [string](#string) |  |  |
| process_alive | [bool](#bool) |  |  |
| elapsed_seconds | [float](#float) |  |  |
| new_output | [string](#string) |  |  |
| new_stderr | [string](#string) |  |  |
| message | [string](#string) |  |  |






<a name="ztd-operator-v1-FetchFileDiffRequested"></a>

### FetchFileDiffRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| diff_id | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| file_path | [string](#string) |  |  |
| limit | [int32](#int32) |  |  |






<a name="ztd-operator-v1-FetchFileDiffResult"></a>

### FetchFileDiffResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| execution_id | [string](#string) |  |  |
| diffs | [FileDiffEntry](#ztd-operator-v1-FileDiffEntry) | repeated |  |
| diff | [FileDiffEntry](#ztd-operator-v1-FileDiffEntry) |  |  |
| total | [int32](#int32) |  |  |
| operator_session_id | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-FetchFileHistoryRequested"></a>

### FetchFileHistoryRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| file_path | [string](#string) |  |  |
| limit | [int32](#int32) |  |  |
| operator_session_id | [string](#string) |  |  |






<a name="ztd-operator-v1-FetchFileHistoryResult"></a>

### FetchFileHistoryResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| execution_id | [string](#string) |  |  |
| file_path | [string](#string) |  |  |
| history | [FileHistoryEntry](#ztd-operator-v1-FileHistoryEntry) | repeated |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-FetchHistoryRequested"></a>

### FetchHistoryRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| limit | [int32](#int32) |  |  |
| offset | [int32](#int32) |  |  |
| include_commands | [bool](#bool) |  |  |
| include_file_mutations | [bool](#bool) |  |  |






<a name="ztd-operator-v1-FetchHistoryResult"></a>

### FetchHistoryResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| execution_id | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| web_session | [AuditWebSession](#ztd-operator-v1-AuditWebSession) |  |  |
| events | [AuditEvent](#ztd-operator-v1-AuditEvent) | repeated |  |
| total | [int32](#int32) |  |  |
| limit | [int32](#int32) |  |  |
| offset | [int32](#int32) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-FetchLogsRequested"></a>

### FetchLogsRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |






<a name="ztd-operator-v1-FetchLogsResult"></a>

### FetchLogsResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| command | [string](#string) |  |  |
| return_code | [int32](#int32) |  |  |
| duration_ms | [int64](#int64) |  |  |
| stdout | [string](#string) |  |  |
| stderr | [string](#string) |  |  |
| stdout_size | [int32](#int32) |  |  |
| stderr_size | [int32](#int32) |  |  |
| timestamp | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-FileDiffEntry"></a>

### FileDiffEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| timestamp | [string](#string) |  |  |
| file_path | [string](#string) |  |  |
| operation | [string](#string) |  |  |
| ledger_hash_before | [string](#string) |  |  |
| ledger_hash_after | [string](#string) |  |  |
| diff_stat | [string](#string) |  |  |
| diff_content | [string](#string) |  |  |
| diff_size | [int32](#int32) |  |  |
| operator_session_id | [string](#string) |  |  |






<a name="ztd-operator-v1-FileEditRequested"></a>

### FileEditRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| file_path | [string](#string) |  |  |
| operation | [string](#string) |  |  |
| execution_id | [string](#string) |  |  |
| justification | [string](#string) |  |  |
| content | [string](#string) |  |  |
| old_content | [string](#string) |  |  |
| new_content | [string](#string) |  |  |
| insert_content | [string](#string) |  |  |
| insert_position | [int32](#int32) |  |  |
| start_line | [int32](#int32) |  |  |
| end_line | [int32](#int32) |  |  |
| patch_content | [string](#string) |  |  |
| create_backup | [bool](#bool) |  |  |
| create_if_missing | [bool](#bool) |  |  |






<a name="ztd-operator-v1-FileEditResult"></a>

### FileEditResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| file_path | [string](#string) |  |  |
| operation | [string](#string) |  |  |
| duration_seconds | [float](#float) |  |  |
| bytes_written | [int64](#int64) |  |  |
| lines_changed | [int32](#int32) |  |  |
| backup_path | [string](#string) |  |  |
| error_message | [string](#string) |  |  |
| error_type | [string](#string) |  |  |
| content | [string](#string) |  |  |
| stdout_size | [int32](#int32) |  |  |
| stderr_size | [int32](#int32) |  |  |






<a name="ztd-operator-v1-FileHistoryEntry"></a>

### FileHistoryEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| commit_hash | [string](#string) |  |  |
| timestamp | [string](#string) |  |  |
| message | [string](#string) |  |  |






<a name="ztd-operator-v1-FingerprintDetails"></a>

### FingerprintDetails



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| os | [string](#string) |  |  |
| architecture | [string](#string) |  |  |
| cpu_count | [int32](#int32) |  |  |
| machine_id | [string](#string) |  |  |






<a name="ztd-operator-v1-FsEntry"></a>

### FsEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| is_dir | [bool](#bool) |  |  |
| size | [int64](#int64) |  |  |
| mode | [int32](#int32) |  |  |
| mod_time | [int64](#int64) |  |  |
| path | [string](#string) |  |  |






<a name="ztd-operator-v1-FsGrepMatch"></a>

### FsGrepMatch



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| path | [string](#string) |  |  |
| line_number | [int32](#int32) |  |  |
| content | [string](#string) |  |  |
| before | [string](#string) | repeated |  |
| after | [string](#string) | repeated |  |






<a name="ztd-operator-v1-FsGrepRequested"></a>

### FsGrepRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| path | [string](#string) |  |  |
| execution_id | [string](#string) |  |  |
| pattern | [string](#string) |  |  |
| includes | [string](#string) | repeated |  |
| max_matches | [int32](#int32) |  |  |






<a name="ztd-operator-v1-FsGrepResult"></a>

### FsGrepResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| path | [string](#string) |  |  |
| matches | [FsGrepMatch](#ztd-operator-v1-FsGrepMatch) | repeated |  |
| total_matches | [int32](#int32) |  |  |
| truncated | [bool](#bool) |  |  |
| duration_seconds | [float](#float) |  |  |
| error_message | [string](#string) |  |  |
| error_type | [string](#string) |  |  |






<a name="ztd-operator-v1-FsListRequested"></a>

### FsListRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| path | [string](#string) |  |  |
| execution_id | [string](#string) |  |  |
| max_depth | [int32](#int32) |  |  |
| max_entries | [int32](#int32) |  |  |






<a name="ztd-operator-v1-FsListResult"></a>

### FsListResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| path | [string](#string) |  |  |
| entries | [FsEntry](#ztd-operator-v1-FsEntry) | repeated |  |
| truncated | [bool](#bool) |  |  |
| total_count | [int32](#int32) |  |  |
| duration_seconds | [float](#float) |  |  |
| error_message | [string](#string) |  |  |
| error_type | [string](#string) |  |  |






<a name="ztd-operator-v1-FsReadRequested"></a>

### FsReadRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| path | [string](#string) |  |  |
| execution_id | [string](#string) |  |  |
| max_size | [int32](#int32) |  |  |






<a name="ztd-operator-v1-FsReadResult"></a>

### FsReadResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| path | [string](#string) |  |  |
| content | [string](#string) |  |  |
| size_bytes | [int64](#int64) |  |  |
| truncated | [bool](#bool) |  |  |
| duration_seconds | [float](#float) |  |  |
| error_message | [string](#string) |  |  |
| error_type | [string](#string) |  |  |






<a name="ztd-operator-v1-GetRevocationBundleRequested"></a>

### GetRevocationBundleRequested







<a name="ztd-operator-v1-GetRevocationBundleResult"></a>

### GetRevocationBundleResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| bundle_json | [string](#string) |  |  |
| signature | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-GovernanceRejection"></a>

### GovernanceRejection
GovernanceRejection is the client-facing body every governed ingress
(direct envelope submission, governed dispatch, MCP, A2A) returns when the
executing Operator rejected a transaction at L4. receipt is the Operator&#39;s
signed FAILED receipt with failure_code GOVERNANCE_REJECTED; its
transaction_id and rejection_code are the authoritative identity and
attribution of the rejection. error is the governance sentinel text, for
display only.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| error | [string](#string) |  |  |
| receipt | [ActionReceipt](#ztd-operator-v1-ActionReceipt) |  |  |






<a name="ztd-operator-v1-GovernedDispatchResult"></a>

### GovernedDispatchResult
GovernedDispatchResult is the HTTP 200 body of POST /api/v1/operators/commands.
receipt is the executing Operator&#39;s signed final ActionReceipt. Failures are
GovernanceRejection (signed L4 rejection) or IngressRefusal (pre-envelope).


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| transaction_id | [string](#string) |  |  |
| event_type | [string](#string) |  |  |
| action_type | [string](#string) |  |  |
| result_payload | [bytes](#bytes) |  | result_payload is the proto-marshaled terminal operator result for event_type (for example FsReadResult), the same bytes the dispatch response carries today. |
| inference_result | [InferenceResult](#ztd-operator-v1-InferenceResult) |  |  |
| receipt | [ActionReceipt](#ztd-operator-v1-ActionReceipt) |  |  |






<a name="ztd-operator-v1-HeartbeatRequested"></a>

### HeartbeatRequested
Empty message - just the event type matters






<a name="ztd-operator-v1-HeartbeatResult"></a>

### HeartbeatResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| operator_id | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| timestamp | [string](#string) |  |  |
| status | [string](#string) |  |  |
| event_type | [string](#string) |  |  |
| source_component | [string](#string) |  |  |
| case_id | [string](#string) |  |  |
| investigation_id | [string](#string) |  |  |
| system_identity | [SystemIdentity](#ztd-operator-v1-SystemIdentity) |  |  |
| network_info | [NetworkInfo](#ztd-operator-v1-NetworkInfo) |  |  |
| version_info | [VersionInfo](#ztd-operator-v1-VersionInfo) |  |  |
| uptime_info | [UptimeInfo](#ztd-operator-v1-UptimeInfo) |  |  |
| performance_metrics | [PerformanceMetrics](#ztd-operator-v1-PerformanceMetrics) |  |  |
| os_details | [OSDetails](#ztd-operator-v1-OSDetails) |  |  |
| user_details | [UserDetails](#ztd-operator-v1-UserDetails) |  |  |
| disk_details | [DiskDetails](#ztd-operator-v1-DiskDetails) |  |  |
| memory_details | [MemoryDetails](#ztd-operator-v1-MemoryDetails) |  |  |
| environment | [EnvironmentDetails](#ztd-operator-v1-EnvironmentDetails) |  |  |
| fingerprint_details | [FingerprintDetails](#ztd-operator-v1-FingerprintDetails) |  |  |
| system_fingerprint | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceChoiceAnswer"></a>

### InferenceChoiceAnswer



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| selection | [string](#string) |  |  |
| probabilities | [InferenceDecisionProbability](#ztd-operator-v1-InferenceDecisionProbability) | repeated |  |
| confidence | [double](#double) |  |  |






<a name="ztd-operator-v1-InferenceChoiceQuestion"></a>

### InferenceChoiceQuestion



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| criteria | [InferenceDecisionCriterion](#ztd-operator-v1-InferenceDecisionCriterion) | repeated |  |






<a name="ztd-operator-v1-InferenceCompletion"></a>

### InferenceCompletion
InferenceCompletion is the protocol-owned completion publication for a
governed inference request. The Inference Node publishes it to the
results channel only after the L5 Actuator has finalized and signed the
ActionReceipt, so the complete result and its verified receipt are a
single correlated outcome. A failed execution carries the FAILED receipt
and no result, which terminates the Gateway&#39;s dispatch wait immediately
instead of at the dispatch deadline.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| receipt | [ActionReceipt](#ztd-operator-v1-ActionReceipt) |  | The signed final ActionReceipt for the inference transaction. |
| result | [InferenceResult](#ztd-operator-v1-InferenceResult) |  | The complete inference result. Absent when the receipt status is not EXECUTION_STATUS_COMPLETED. |






<a name="ztd-operator-v1-InferenceDecisionAnswer"></a>

### InferenceDecisionAnswer



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| noul | [double](#double) |  |  |
| choice | [InferenceChoiceAnswer](#ztd-operator-v1-InferenceChoiceAnswer) |  |  |
| score | [InferenceScoreAnswer](#ztd-operator-v1-InferenceScoreAnswer) |  |  |






<a name="ztd-operator-v1-InferenceDecisionCriterion"></a>

### InferenceDecisionCriterion



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| label | [string](#string) |  |  |
| description | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceDecisionProbability"></a>

### InferenceDecisionProbability



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| label | [string](#string) |  |  |
| probability | [double](#double) |  |  |






<a name="ztd-operator-v1-InferenceDecisionQuestion"></a>

### InferenceDecisionQuestion



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| instructions | [string](#string) |  |  |
| noul | [InferenceNoulQuestion](#ztd-operator-v1-InferenceNoulQuestion) |  |  |
| choice | [InferenceChoiceQuestion](#ztd-operator-v1-InferenceChoiceQuestion) |  |  |
| score | [InferenceScoreQuestion](#ztd-operator-v1-InferenceScoreQuestion) |  |  |






<a name="ztd-operator-v1-InferenceDecisionRequest"></a>

### InferenceDecisionRequest
Payload for ztd.v1.operator.inference.requested. The handler receives an ordered typed conversation, validates and scrubs every data-bearing part, and sends only the normalized request across the provider boundary.
Decision inference uses named, typed questions; no generated text or tools.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| state | [string](#string) |  |  |
| questions | [InferenceDecisionQuestion](#ztd-operator-v1-InferenceDecisionQuestion) | repeated |  |
| allow_hosted | [bool](#bool) |  | Explicit consent to hosted egress, committed into the governed envelope. |






<a name="ztd-operator-v1-InferenceDecisionResult"></a>

### InferenceDecisionResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| answers | [InferenceDecisionAnswer](#ztd-operator-v1-InferenceDecisionAnswer) | repeated |  |






<a name="ztd-operator-v1-InferenceDispatchFailure"></a>

### InferenceDispatchFailure
InferenceDispatchFailure is the protocol-owned error body for a non-2xx
POST /api/v1/inference/dispatch. Error is a public-safe centralized
sentinel; code classifies it, and is UNSPECIFIED for request faults the
handler rejects before dispatch (method, identity, body size). Receipt is
the executing Operator&#39;s verified signed FAILED receipt when the dispatch
reached it: a governance rejection, or a failed execution whose commitment
the Operator&#39;s ledger holds. It is absent for every failure that ended
before the Operator signed an outcome. Observation_receipts are the
observers&#39; verified signed BEGIN and FINALIZE receipts the failed attempt
produced, in dispatch order, so a trace bracket can claim their
commitments as it does for InferenceDispatchResponse.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| error | [string](#string) |  |  |
| code | [InferenceDispatchFailureCode](#ztd-operator-v1-InferenceDispatchFailureCode) |  |  |
| receipt | [ActionReceipt](#ztd-operator-v1-ActionReceipt) |  |  |
| observation_receipts | [ActionReceipt](#ztd-operator-v1-ActionReceipt) | repeated |  |






<a name="ztd-operator-v1-InferenceDispatchRequest"></a>

### InferenceDispatchRequest
InferenceDispatchRequest is the protocol-owned request body for
POST /api/v1/inference/dispatch, the mTLS-protected platform-internal
endpoint app workloads call to dispatch a governed
inference request to the Inference Node.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| role | [ModelRole](#ztd-operator-v1-ModelRole) |  | Chat-tier role for this request (primary, assistant, lite). |
| model | [string](#string) |  | Requested provider model. A campaign Inference Operator accepts it only when the exact model and digest occur in the frozen registry committed by model_registry_digest; standard inference uses the configured role model. |
| temperature | [float](#float) |  | Generation temperature override (0 = use backend default). |
| max_tokens | [int32](#int32) |  | Maximum tokens to generate (0 = use backend default). |
| keep_alive | [string](#string) |  | Ollama keep-alive duration override (empty = use config default). |
| target_operator_session_id | [string](#string) |  | Explicit Inference Node target. When empty, the gateway requires exactly one enrolled inference-capable operator for the requestor; zero or multiple matches are rejected. |
| acting_app_id | [string](#string) |  | Application that initiated the inference request. |
| case_id | [string](#string) |  | Application context propagated from the chat turn. |
| investigation_id | [string](#string) |  |  |
| task_id | [string](#string) |  |  |
| web_session_id | [string](#string) |  |  |
| cli_session_id | [string](#string) |  |  |
| messages | [InferenceMessage](#ztd-operator-v1-InferenceMessage) | repeated | Ordered conversation turns sent to the Inference Node. The node validates and re-scrubs every data-bearing part before crossing the provider boundary. |
| tools | [InferenceToolDeclaration](#ztd-operator-v1-InferenceToolDeclaration) | repeated | Ordered callable function declarations available to the model. |
| top_p | [float](#float) | optional |  |
| top_k | [int32](#int32) | optional |  |
| stop_sequences | [string](#string) | repeated |  |
| response_format | [InferenceResponseFormat](#ztd-operator-v1-InferenceResponseFormat) |  |  |
| request_schema_version | [string](#string) |  |  |
| tool_choice | [InferenceToolChoice](#ztd-operator-v1-InferenceToolChoice) |  |  |
| parallel_tool_calls | [bool](#bool) | optional |  |
| thinking | [InferenceThinkingControl](#ztd-operator-v1-InferenceThinkingControl) |  |  |
| context_limit | [int32](#int32) | optional |  |
| provider_attempt_id | [string](#string) |  |  |
| model_digest | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| evaluation_attempt_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| model_registry | [InferenceModelVariant](#ztd-operator-v1-InferenceModelVariant) | repeated |  |
| model_registry_digest | [string](#string) |  |  |
| stream | [bool](#bool) |  | When true, the Gateway forwards live InferenceProgressEvent telemetry to the caller while waiting for the authoritative terminal completion. |
| retry_count | [uint32](#uint32) |  | Zero-based retry ordinal for this provider attempt. |
| seed | [int32](#int32) | optional | Optional deterministic generation seed forwarded to the provider when supported. Absent means use the provider default. |
| decision | [InferenceDecisionRequest](#ztd-operator-v1-InferenceDecisionRequest) |  | Exclusive with messages, tools, streaming and generation controls. |






<a name="ztd-operator-v1-InferenceDispatchResponse"></a>

### InferenceDispatchResponse
InferenceDispatchResponse is the protocol-owned success response for
POST /api/v1/inference/dispatch. The Gateway returns it only after
verifying the final receipt signature, persistence attestation,
transaction identity, and result digest equality. Failure responses use
the gateway&#39;s standard typed error envelope with a public-safe code.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| transaction_id | [string](#string) |  | Transaction ID correlating the dispatch with the audit chain. |
| result | [InferenceResult](#ztd-operator-v1-InferenceResult) |  | The complete inference result whose result_digest is bound into the signed receipt. |
| receipt | [ActionReceipt](#ztd-operator-v1-ActionReceipt) |  | The verified final signed ActionReceipt for the transaction. |
| observation_receipts | [ActionReceipt](#ztd-operator-v1-ActionReceipt) | repeated | The observers&#39; verified final signed receipts for this attempt&#39;s provider-boundary and model provenance BEGIN and FINALIZE commands, in dispatch order. Each was signed before the response was returned, so a trace bracket closing after the dispatch can claim its commitment. |






<a name="ztd-operator-v1-InferenceDispatchStreamFailure"></a>

### InferenceDispatchStreamFailure
InferenceDispatchStreamFailure is the protocol-owned terminal failure
frame for a streaming dispatch that already started NDJSON delivery.
Reason is a public-safe centralized sentinel; it never carries raw
prompts, model output, or receipt bodies. Code classifies it.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| reason | [string](#string) |  |  |
| code | [InferenceDispatchFailureCode](#ztd-operator-v1-InferenceDispatchFailureCode) |  |  |






<a name="ztd-operator-v1-InferenceDispatchStreamFrame"></a>

### InferenceDispatchStreamFrame
InferenceDispatchStreamFrame is one NDJSON line in a streaming dispatch
response. Progress frames are delivery telemetry only. The completion
frame carries the verified terminal result and signed receipt. A failure
frame is the explicit terminal outcome when cancellation, disconnect,
backpressure, or event-to-terminal hash reconciliation prevents a
verified completion after the HTTP response has started.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| progress | [InferenceProgressEvent](#ztd-operator-v1-InferenceProgressEvent) |  |  |
| completion | [InferenceDispatchResponse](#ztd-operator-v1-InferenceDispatchResponse) |  |  |
| failure | [InferenceDispatchStreamFailure](#ztd-operator-v1-InferenceDispatchStreamFailure) |  |  |






<a name="ztd-operator-v1-InferenceHostedEgressAttestation"></a>

### InferenceHostedEgressAttestation
Included in result_digest and therefore bound by the Operator&#39;s signed L5 receipt.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| provider_origin | [string](#string) |  |  |
| model_version | [string](#string) |  |  |
| provenance | [string](#string) |  |  |
| normalized_request_hash | [string](#string) |  |  |
| explicit_consent | [bool](#bool) |  |  |






<a name="ztd-operator-v1-InferenceMessage"></a>

### InferenceMessage
InferenceMessage preserves one ordered conversation turn without prompt flattening.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| role | [InferenceMessageRole](#ztd-operator-v1-InferenceMessageRole) |  |  |
| parts | [InferenceMessagePart](#ztd-operator-v1-InferenceMessagePart) | repeated |  |






<a name="ztd-operator-v1-InferenceMessagePart"></a>

### InferenceMessagePart
InferenceMessagePart carries exactly one typed part of a conversation turn.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| text | [string](#string) |  |  |
| tool_call | [InferenceToolCall](#ztd-operator-v1-InferenceToolCall) |  |  |
| tool_result | [InferenceToolResult](#ztd-operator-v1-InferenceToolResult) |  |  |






<a name="ztd-operator-v1-InferenceModelVariant"></a>

### InferenceModelVariant



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| model | [string](#string) |  |  |
| digest | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceNoulQuestion"></a>

### InferenceNoulQuestion



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| true_label | [string](#string) |  |  |
| false_label | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceProgressEvent"></a>

### InferenceProgressEvent
InferenceProgressEvent carries bounded incremental provider output for
live cross-boundary delivery telemetry. It is not authoritative; the
signed InferenceCompletion remains the sole terminal outcome.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| provider_attempt_id | [string](#string) |  | Stable provider-attempt identity copied from the governed request. |
| sequence | [uint32](#uint32) |  | Monotonic sequence number for this attempt, starting at 1. |
| parts | [InferenceResponsePart](#ztd-operator-v1-InferenceResponsePart) | repeated | Ordered response parts emitted by this provider stream event. |
| time_to_first_token_ns | [int64](#int64) | optional | Measured time from immediately before the provider request to the first observable thinking, text, or tool-call event. Present only on the first progress event that carries observable output for the attempt. |
| served_model | [string](#string) |  | Provider-served model tag when known from the stream event. |






<a name="ztd-operator-v1-InferenceProviderAttemptRecord"></a>

### InferenceProviderAttemptRecord
InferenceProviderAttemptRecord is the durable operator-local record for one
governed provider attempt. It is written before provider contact and
finalized only after terminal receipt/result evidence is available.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| provider_attempt_id | [string](#string) |  |  |
| transaction_id | [string](#string) |  |  |
| retry_count | [uint32](#uint32) |  |  |
| retry_classification | [InferenceRetryClassification](#ztd-operator-v1-InferenceRetryClassification) |  |  |
| status | [InferenceProviderAttemptStatus](#ztd-operator-v1-InferenceProviderAttemptStatus) |  |  |
| started_at_unix_ms | [int64](#int64) |  |  |
| completed_at_unix_ms | [int64](#int64) |  |  |
| result_digest | [string](#string) |  |  |
| failure_summary | [string](#string) |  | failure_summary is a fixed failure category (never provider or Go error text, which can echo prompt material); empty unless status is FAILED. |
| raw_response_digest | [string](#string) |  | raw_response_digest is the hex SHA-256 of the provider byte stream of a 200 response. Only the digest and size are kept; the stream itself is never persisted in the attempt store. |
| raw_response_size_bytes | [uint64](#uint64) |  |  |






<a name="ztd-operator-v1-InferenceRequested"></a>

### InferenceRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| role | [ModelRole](#ztd-operator-v1-ModelRole) |  | Chat-tier role for this request (primary, assistant, lite). |
| model | [string](#string) |  | Ollama model name (e.g., &#34;gemma3:4b&#34;). Encodes the role; Ollama routes by model name in the API call. |
| temperature | [float](#float) |  | Generation temperature override (0 = use backend default). |
| max_tokens | [int32](#int32) |  | Maximum tokens to generate (0 = use backend default). |
| keep_alive | [string](#string) |  | Ollama keep-alive duration override (empty = use config default). |
| messages | [InferenceMessage](#ztd-operator-v1-InferenceMessage) | repeated | Ordered conversation turns sent to the backend. |
| tools | [InferenceToolDeclaration](#ztd-operator-v1-InferenceToolDeclaration) | repeated | Ordered callable function declarations available to the model. |
| top_p | [float](#float) | optional |  |
| top_k | [int32](#int32) | optional |  |
| stop_sequences | [string](#string) | repeated |  |
| response_format | [InferenceResponseFormat](#ztd-operator-v1-InferenceResponseFormat) |  |  |
| request_schema_version | [string](#string) |  |  |
| tool_choice | [InferenceToolChoice](#ztd-operator-v1-InferenceToolChoice) |  |  |
| parallel_tool_calls | [bool](#bool) | optional |  |
| thinking | [InferenceThinkingControl](#ztd-operator-v1-InferenceThinkingControl) |  |  |
| context_limit | [int32](#int32) | optional |  |
| provider_attempt_id | [string](#string) |  |  |
| model_digest | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| evaluation_attempt_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| model_registry | [InferenceModelVariant](#ztd-operator-v1-InferenceModelVariant) | repeated |  |
| model_registry_digest | [string](#string) |  |  |
| stream | [bool](#bool) |  | When true, the Inference Node publishes bounded InferenceProgressEvent telemetry on the results channel while the provider stream is active. Progress events are delivery telemetry only; the signed InferenceCompletion remains the sole authoritative terminal outcome. |
| retry_count | [uint32](#uint32) |  | Zero-based retry ordinal for this provider attempt. Each retry uses a distinct provider_attempt_id and is durably retained. |
| seed | [int32](#int32) | optional | Optional deterministic generation seed forwarded to the provider when supported. Absent means use the provider default. |
| decision | [InferenceDecisionRequest](#ztd-operator-v1-InferenceDecisionRequest) |  | Exclusive with messages, tools, streaming and generation controls. |






<a name="ztd-operator-v1-InferenceResponseFormat"></a>

### InferenceResponseFormat



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| media_type | [string](#string) |  |  |
| json_schema | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceResponsePart"></a>

### InferenceResponsePart
InferenceResponsePart carries exactly one typed model response part.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| text | [string](#string) |  |  |
| tool_call | [InferenceToolCall](#ztd-operator-v1-InferenceToolCall) |  |  |
| thinking | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceResult"></a>

### InferenceResult
InferenceResult carries ordered response parts, usage metadata, and the finish reason returned by the backend. The governed envelope carries this for the receipt and audit chain.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| prompt_tokens | [int32](#int32) |  | Number of prompt tokens consumed. |
| completion_tokens | [int32](#int32) |  | Number of completion tokens generated. |
| total_tokens | [int32](#int32) |  | Total tokens (prompt &#43; completion). |
| finish_reason | [string](#string) |  | Finish reason (e.g., &#34;stop&#34;, &#34;length&#34;). |
| model | [string](#string) |  | Model that produced the result (echoed from the request or resolved by the backend). |
| result_digest | [string](#string) |  | Canonical digest of the complete result: lowercase hex SHA-256 over the deterministic protobuf serialization of this message with result_digest cleared. The Inference Node computes it at execution time; the signed ActionReceipt&#39;s result_summary binds it, and the User Gateway verifies digest equality before returning success to the caller. |
| parts | [InferenceResponsePart](#ztd-operator-v1-InferenceResponsePart) | repeated | Ordered model response parts covered by result_digest. |
| usage_reported | [bool](#bool) |  |  |
| thinking_tokens | [int32](#int32) | optional |  |
| cache_tokens | [int32](#int32) | optional |  |
| load_duration_ns | [int64](#int64) | optional |  |
| prompt_eval_duration_ns | [int64](#int64) | optional |  |
| generation_duration_ns | [int64](#int64) | optional |  |
| total_duration_ns | [int64](#int64) | optional |  |
| time_to_first_token_ns | [int64](#int64) | optional |  |
| timing_source | [InferenceTimingSource](#ztd-operator-v1-InferenceTimingSource) |  |  |
| provider_attempt_id | [string](#string) |  |  |
| requested_model | [string](#string) |  |  |
| requested_model_digest | [string](#string) |  |  |
| served_model_digest | [string](#string) |  |  |
| normalized_request_hash | [string](#string) |  |  |
| output_hash | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| evaluation_attempt_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| model_registry_digest | [string](#string) |  |  |
| retry_count | [uint32](#uint32) |  |  |
| load_state | [InferenceLoadState](#ztd-operator-v1-InferenceLoadState) |  |  |
| retry_classification | [InferenceRetryClassification](#ztd-operator-v1-InferenceRetryClassification) |  |  |
| decision | [InferenceDecisionResult](#ztd-operator-v1-InferenceDecisionResult) |  | Exclusive with response parts. Covered by result_digest. |
| hosted_egress | [InferenceHostedEgressAttestation](#ztd-operator-v1-InferenceHostedEgressAttestation) |  |  |






<a name="ztd-operator-v1-InferenceScoreAnswer"></a>

### InferenceScoreAnswer



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| score | [double](#double) |  |  |
| probabilities | [InferenceDecisionProbability](#ztd-operator-v1-InferenceDecisionProbability) | repeated |  |
| confidence | [double](#double) |  |  |






<a name="ztd-operator-v1-InferenceScoreQuestion"></a>

### InferenceScoreQuestion



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| criteria | [string](#string) | repeated |  |






<a name="ztd-operator-v1-InferenceThinkingControl"></a>

### InferenceThinkingControl



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| enabled | [bool](#bool) |  |  |
| level | [string](#string) |  |  |
| include_thoughts | [bool](#bool) |  |  |






<a name="ztd-operator-v1-InferenceToolCall"></a>

### InferenceToolCall
InferenceToolCall carries a model-requested function invocation. arguments_json is a canonical JSON object.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| call_id | [string](#string) |  |  |
| name | [string](#string) |  |  |
| arguments_json | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceToolChoice"></a>

### InferenceToolChoice



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| mode | [InferenceToolChoiceMode](#ztd-operator-v1-InferenceToolChoiceMode) |  |  |
| allowed_tool_names | [string](#string) | repeated |  |






<a name="ztd-operator-v1-InferenceToolDeclaration"></a>

### InferenceToolDeclaration
InferenceToolDeclaration defines one callable function. json_schema is a canonical strict JSON Schema object.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| description | [string](#string) |  |  |
| json_schema | [string](#string) |  |  |






<a name="ztd-operator-v1-InferenceToolResult"></a>

### InferenceToolResult
InferenceToolResult carries a tool continuation value. result_json is a canonical JSON value.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| call_id | [string](#string) |  |  |
| name | [string](#string) |  |  |
| result_json | [string](#string) |  |  |






<a name="ztd-operator-v1-IngressRefusal"></a>

### IngressRefusal
IngressRefusal is the body of every pre-envelope refusal: the HTTP body on the
direct and dispatch ingresses, and JSON-RPC error.data on MCP and A2A.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| code | [IngressRefusalCode](#ztd-operator-v1-IngressRefusalCode) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-ListDeviceLinksRequested"></a>

### ListDeviceLinksRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |






<a name="ztd-operator-v1-ListDeviceLinksResult"></a>

### ListDeviceLinksResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| links | [DeviceLink](#ztd-operator-v1-DeviceLink) | repeated |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-ListOperatorSlotsRequested"></a>

### ListOperatorSlotsRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |






<a name="ztd-operator-v1-ListOperatorSlotsResult"></a>

### ListOperatorSlotsResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| operators | [OperatorDocument](#ztd-operator-v1-OperatorDocument) | repeated |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-ListPasskeyCredentialsRequested"></a>

### ListPasskeyCredentialsRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |






<a name="ztd-operator-v1-ListPasskeyCredentialsResult"></a>

### ListPasskeyCredentialsResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| error | [string](#string) |  |  |
| credentials | [PasskeyCredential](#ztd-operator-v1-PasskeyCredential) | repeated |  |






<a name="ztd-operator-v1-McpCallRequested"></a>

### McpCallRequested
Payload for ztd.v1.operator.mcp.call.requested
Carries the downstream MCP tool name and its JSON-RPC arguments verbatim so
the Gateway can verify L1 forbidden patterns against the tool name before
the Warden dispatches the verified call to the configured downstream MCP
server.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| tool_name | [string](#string) |  | The MCP tool name (e.g., &#34;fs_read&#34;, &#34;drop_table&#34;). Forbidden patterns here block the interlock sequence at L1 before any signature or state check. |
| arguments_json | [string](#string) |  | JSON-encoded arguments object exactly as the MCP client supplied it. Stored as a string (not Struct) so the canonical hash is computed over the bytes the client signed, with no normalization ambiguity. |
| execution_id | [string](#string) |  | Optional client-supplied invocation id surfaced back in the result. |






<a name="ztd-operator-v1-McpPromptGetRequested"></a>

### McpPromptGetRequested
Payload for ztd.v1.operator.mcp.prompts.get.requested
Gets a specific prompt template from the downstream MCP server.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  | The prompt name/template identifier. |
| execution_id | [string](#string) |  | Optional client-supplied invocation id surfaced back in the result. |






<a name="ztd-operator-v1-McpPromptListRequested"></a>

### McpPromptListRequested
Payload for ztd.v1.operator.mcp.prompts.list.requested
Lists available prompts from the downstream MCP server.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  | Optional client-supplied invocation id surfaced back in the result. |






<a name="ztd-operator-v1-McpResourceListRequested"></a>

### McpResourceListRequested
Payload for ztd.v1.operator.mcp.resources.list.requested
Lists available resources from the downstream MCP server.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  | Optional client-supplied invocation id surfaced back in the result. |






<a name="ztd-operator-v1-McpResourceReadRequested"></a>

### McpResourceReadRequested
Payload for ztd.v1.operator.mcp.resources.read.requested
Reads a specific resource from the downstream MCP server.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| uri | [string](#string) |  | The resource URI to read (e.g., &#34;file:///path/to/file&#34;, &#34;memory://var&#34;). |
| execution_id | [string](#string) |  | Optional client-supplied invocation id surfaced back in the result. |






<a name="ztd-operator-v1-MemoryDetails"></a>

### MemoryDetails



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| total_mb | [int64](#int64) |  |  |
| available_mb | [int64](#int64) |  |  |
| used_mb | [int64](#int64) |  |  |
| percent | [double](#double) |  |  |






<a name="ztd-operator-v1-NetworkInfo"></a>

### NetworkInfo



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| public_ip | [string](#string) |  |  |
| internal_ip | [string](#string) |  |  |
| interfaces | [string](#string) | repeated |  |
| connectivity_status | [NetworkInterface](#ztd-operator-v1-NetworkInterface) | repeated |  |






<a name="ztd-operator-v1-NetworkInterface"></a>

### NetworkInterface



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| ip | [string](#string) |  |  |
| mtu | [int32](#int32) |  |  |






<a name="ztd-operator-v1-OSDetails"></a>

### OSDetails



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| kernel | [string](#string) |  |  |
| distro | [string](#string) |  |  |
| version | [string](#string) |  |  |






<a name="ztd-operator-v1-OllamaModelInventoryRequested"></a>

### OllamaModelInventoryRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |






<a name="ztd-operator-v1-OllamaModelInventoryResult"></a>

### OllamaModelInventoryResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| entries | [ProviderModelInventoryEntry](#ztd-operator-v1-ProviderModelInventoryEntry) | repeated |  |
| error_message | [string](#string) |  |  |
| error_type | [string](#string) |  |  |






<a name="ztd-operator-v1-OllamaModelResidencyModel"></a>

### OllamaModelResidencyModel



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |






<a name="ztd-operator-v1-OllamaModelResidencyRequested"></a>

### OllamaModelResidencyRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |






<a name="ztd-operator-v1-OllamaModelResidencyResult"></a>

### OllamaModelResidencyResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| models | [OllamaModelResidencyModel](#ztd-operator-v1-OllamaModelResidencyModel) | repeated |  |
| error_message | [string](#string) |  |  |
| error_type | [string](#string) |  |  |






<a name="ztd-operator-v1-OperatorDocument"></a>

### OperatorDocument
OperatorDocument is the canonical Operator record. The Gateway owns and
persists it; every other component reads it through this contract.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| user_id | [string](#string) |  |  |
| organization_id | [string](#string) |  |  |
| component | [string](#string) |  |  |
| name | [string](#string) |  |  |
| status | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| bound_web_session_id | [string](#string) |  |  |
| operator_cert | [string](#string) |  |  |
| operator_cert_serial | [string](#string) |  |  |
| slot_number | [int32](#int32) |  |  |
| is_slot | [bool](#bool) |  |  |
| claimed | [bool](#bool) |  |  |
| operator_type | [string](#string) |  |  |
| system_fingerprint | [string](#string) |  |  |
| created_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| updated_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| started_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| claimed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| last_heartbeat_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| operator_capabilities | [string](#string) | repeated |  |
| local_dir | [string](#string) |  |  |
| account | [string](#string) |  |  |
| port | [int32](#int32) |  |  |
| stop_reason | [string](#string) |  |  |
| latest_heartbeat_snapshot | [HeartbeatResult](#ztd-operator-v1-HeartbeatResult) |  |  |
| current_hostname | [string](#string) |  |  |
| runtime_config | [OperatorRuntimeConfig](#ztd-operator-v1-OperatorRuntimeConfig) |  |  |
| consumed_by_operator_id | [string](#string) |  |  |
| operator_cert_chain | [string](#string) |  | Chain of operator certificates for zero-trust verification. |
| termination_reason | [string](#string) |  | Reason for operator termination (e.g., &#34;user_requested&#34;, &#34;max_duration_exceeded&#34;). |
| stale_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  | Instant after which an active remote Operator that stays silent reads as stale: its last sign of life plus the stale window for its declared heartbeat interval. The Gateway derives it on every read and never stores it; it is unset for an Operator that cannot read as stale. |
| actuator_public_key | [string](#string) |  | actuator_public_key is the hex Ed25519 public key of this Operator&#39;s L5 Actuator, bound once at enrollment over the authenticated bootstrap channel. Receipts executed by this Operator verify only against this key. |
| actuator_key_id | [string](#string) |  | actuator_key_id equals actuator_public_key (key_id == hex(pub)). |






<a name="ztd-operator-v1-OperatorRuntimeConfig"></a>

### OperatorRuntimeConfig
OperatorRuntimeConfig captures the CLI flags and env var overrides active
when the Operator started. Sent to the Gateway at bootstrap and stored in
OperatorDocument.runtime_config.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| cloud_mode | [bool](#bool) |  |  |
| cloud_provider | [string](#string) |  |  |
| log_level | [string](#string) |  |  |
| http_port | [int32](#int32) |  |  |
| capabilities | [string](#string) | repeated | Enabled capabilities (data, inference, provenance, observer). Deployment shape is OperatorDocument.operator_type, never a capability. |
| local_dir | [string](#string) |  |  |
| account | [string](#string) |  |  |
| inference_ollama_endpoint | [string](#string) |  | Approved remote Ollama provider URL (--inference-ollama-endpoint). |
| provenance_operator_model_storage_root | [string](#string) |  | Root directory containing content-addressed model weight blobs. |
| platform | [string](#string) |  | Operator host GOOS recorded at startup (for example &#34;linux&#34; or &#34;windows&#34;). |
| heartbeat_interval_ms | [uint32](#uint32) |  | How often this Operator sends heartbeats, declared at session start. The Gateway derives the Operator&#39;s stale deadline from it. Zero means the Operator did not declare one and the Gateway default window applies. |
| inference_keep_alive | [string](#string) |  | Ollama keep-alive duration (--inference-keep-alive). |
| provenance_operator_id | [string](#string) |  | Stable provenance attestor pseudonym (--provenance-operator-id). |
| provider_boundary_observer_id | [string](#string) |  | Stable provider-boundary observer pseudonym (--provider-boundary-observer-id). |
| inference_decision_backend | [string](#string) |  | Supported decision transport(s); model inventory is reported by provider inventory. |
| inference_allow_hosted_decision | [bool](#bool) |  |  |
| inference_decision_models | [string](#string) | repeated |  |
| inference_decision_only | [bool](#bool) |  |  |






<a name="ztd-operator-v1-PasskeyAuthChallengeRequested"></a>

### PasskeyAuthChallengeRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |






<a name="ztd-operator-v1-PasskeyAuthChallengeResult"></a>

### PasskeyAuthChallengeResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| error | [string](#string) |  |  |
| needs_setup | [bool](#bool) |  |  |
| challenge | [string](#string) |  | WebAuthn PublicKeyCredentialRequestOptions fields |
| timeout | [int64](#int64) |  |  |
| rp_id | [string](#string) |  |  |
| allow_credentials | [string](#string) | repeated |  |
| user_verification | [string](#string) |  |  |






<a name="ztd-operator-v1-PasskeyAuthVerifyRequested"></a>

### PasskeyAuthVerifyRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |
| assertion_response | [AssertionResponse](#ztd-operator-v1-AssertionResponse) |  |  |






<a name="ztd-operator-v1-PasskeyAuthVerifyResult"></a>

### PasskeyAuthVerifyResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| error | [string](#string) |  |  |
| user_id | [string](#string) |  |  |
| web_session_id | [string](#string) |  |  |
| session_expires_at_unix_ms | [int64](#int64) |  |  |






<a name="ztd-operator-v1-PasskeyCredential"></a>

### PasskeyCredential
A stored passkey credential for a user


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| public_key | [string](#string) |  | base64url-encoded public key |
| counter | [int64](#int64) |  |  |
| transports | [string](#string) | repeated |  |
| created_at_unix_ms | [int64](#int64) |  |  |
| last_used_at_unix_ms | [int64](#int64) |  |  |






<a name="ztd-operator-v1-PasskeyRegisterChallengeRequested"></a>

### PasskeyRegisterChallengeRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |
| user_name | [string](#string) |  |  |






<a name="ztd-operator-v1-PasskeyRegisterChallengeResult"></a>

### PasskeyRegisterChallengeResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| error | [string](#string) |  |  |
| challenge | [string](#string) |  | WebAuthn PublicKeyCredentialCreationOptions fields |
| rp | [PasskeyRegisterChallengeResult.RelyingParty](#ztd-operator-v1-PasskeyRegisterChallengeResult-RelyingParty) |  |  |
| user | [PasskeyRegisterChallengeResult.UserInfo](#ztd-operator-v1-PasskeyRegisterChallengeResult-UserInfo) |  |  |
| pub_key_cred_params | [PasskeyRegisterChallengeResult.PublicKeyCredentialParameters](#ztd-operator-v1-PasskeyRegisterChallengeResult-PublicKeyCredentialParameters) | repeated |  |
| timeout | [int64](#int64) |  |  |
| attestation | [string](#string) |  |  |
| authenticator_selection | [PasskeyRegisterChallengeResult.AuthenticatorSelection](#ztd-operator-v1-PasskeyRegisterChallengeResult-AuthenticatorSelection) |  |  |
| exclude_credentials | [string](#string) | repeated |  |






<a name="ztd-operator-v1-PasskeyRegisterChallengeResult-AuthenticatorSelection"></a>

### PasskeyRegisterChallengeResult.AuthenticatorSelection



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| resident_key | [string](#string) |  |  |
| user_verification | [string](#string) |  |  |






<a name="ztd-operator-v1-PasskeyRegisterChallengeResult-PublicKeyCredentialParameters"></a>

### PasskeyRegisterChallengeResult.PublicKeyCredentialParameters



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| type | [string](#string) |  |  |
| alg | [int32](#int32) |  |  |






<a name="ztd-operator-v1-PasskeyRegisterChallengeResult-RelyingParty"></a>

### PasskeyRegisterChallengeResult.RelyingParty



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| id | [string](#string) |  |  |






<a name="ztd-operator-v1-PasskeyRegisterChallengeResult-UserInfo"></a>

### PasskeyRegisterChallengeResult.UserInfo



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| name | [string](#string) |  |  |
| display_name | [string](#string) |  |  |






<a name="ztd-operator-v1-PasskeyRegisterVerifyRequested"></a>

### PasskeyRegisterVerifyRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |
| attestation_response | [AttestationResponse](#ztd-operator-v1-AttestationResponse) |  |  |






<a name="ztd-operator-v1-PasskeyRegisterVerifyResult"></a>

### PasskeyRegisterVerifyResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| error | [string](#string) |  |  |
| credential | [PasskeyCredential](#ztd-operator-v1-PasskeyCredential) |  |  |






<a name="ztd-operator-v1-PerformanceMetrics"></a>

### PerformanceMetrics



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| cpu_percent | [double](#double) |  |  |
| memory_percent | [double](#double) |  |  |
| disk_percent | [double](#double) |  |  |
| network_latency | [double](#double) |  |  |
| memory_used_mb | [int32](#int32) |  |  |
| memory_total_mb | [int32](#int32) |  |  |
| disk_used_gb | [double](#double) |  |  |
| disk_total_gb | [double](#double) |  |  |






<a name="ztd-operator-v1-PortCheckEntry"></a>

### PortCheckEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| host | [string](#string) |  |  |
| port | [int32](#int32) |  |  |
| open | [bool](#bool) |  |  |
| latency_ms | [float](#float) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-PortCheckResult"></a>

### PortCheckResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| status | [ExecutionStatus](#ztd-operator-v1-ExecutionStatus) |  |  |
| results | [PortCheckEntry](#ztd-operator-v1-PortCheckEntry) | repeated |  |
| error_message | [string](#string) |  |  |
| error_type | [string](#string) |  |  |






<a name="ztd-operator-v1-ProviderModelInventoryEntry"></a>

### ProviderModelInventoryEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| provider_class | [string](#string) |  |  |
| served_model_tag | [string](#string) |  |  |
| model_digest | [string](#string) |  |  |
| model_family | [string](#string) |  |  |
| parameter_size | [string](#string) |  |  |
| parameter_count | [uint64](#uint64) |  |  |
| quantization | [string](#string) |  |  |
| format | [string](#string) |  |  |
| context_limit | [uint32](#uint32) |  |  |
| advertised_capabilities | [string](#string) | repeated |  |






<a name="ztd-operator-v1-ReceiptPersistenceAttestation"></a>

### ReceiptPersistenceAttestation
ReceiptPersistenceAttestation is the signed proof that a receipt signature was durably associated with an audit record at a specific timestamp.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| transaction_id | [string](#string) |  |  |
| receipt_signature_digest | [string](#string) |  |  |
| persisted_at_unix_ms | [int64](#int64) |  |  |
| audit_record_id | [string](#string) |  |  |
| signer_key_id | [string](#string) |  |  |
| signature | [string](#string) |  |  |






<a name="ztd-operator-v1-RestoreFileRequested"></a>

### RestoreFileRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| execution_id | [string](#string) |  |  |
| file_path | [string](#string) |  |  |
| commit_hash | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |






<a name="ztd-operator-v1-RestoreFileResult"></a>

### RestoreFileResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| execution_id | [string](#string) |  |  |
| file_path | [string](#string) |  |  |
| commit_hash | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-RevokeCertificateRequested"></a>

### RevokeCertificateRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| serial | [string](#string) |  |  |
| reason | [string](#string) |  |  |






<a name="ztd-operator-v1-RevokeCertificateResult"></a>

### RevokeCertificateResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-RevokePasskeyCredentialRequested"></a>

### RevokePasskeyCredentialRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| user_id | [string](#string) |  |  |
| credential_id | [string](#string) |  |  |






<a name="ztd-operator-v1-RevokePasskeyCredentialResult"></a>

### RevokePasskeyCredentialResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| error | [string](#string) |  |  |
| found | [bool](#bool) |  |  |
| remaining | [int32](#int32) |  |  |






<a name="ztd-operator-v1-RunShellCommandArguments"></a>

### RunShellCommandArguments
RunShellCommandArguments is the shared typed argument contract for the
run_shell_command MCP tool and Tribunal-authored commands.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| command | [string](#string) |  |  |
| args | [string](#string) | repeated |  |
| timeout | [int32](#int32) |  |  |
| working_dir | [string](#string) |  |  |






<a name="ztd-operator-v1-SetTargetContextRequested"></a>

### SetTargetContextRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| operator_id | [string](#string) |  |  |
| user_id | [string](#string) |  |  |
| web_session_id | [string](#string) |  |  |






<a name="ztd-operator-v1-SetTargetContextResult"></a>

### SetTargetContextResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| operator_id | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-ShutdownRequested"></a>

### ShutdownRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| reason | [string](#string) |  |  |






<a name="ztd-operator-v1-SignCertificateRequested"></a>

### SignCertificateRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| public_key_pem | [string](#string) |  |  |
| common_name | [string](#string) |  |  |
| organizational_unit | [string](#string) |  |  |
| validity_days | [int32](#int32) |  |  |






<a name="ztd-operator-v1-SignCertificateResult"></a>

### SignCertificateResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| certificate_pem | [string](#string) |  |  |
| serial | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-SystemIdentity"></a>

### SystemIdentity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| hostname | [string](#string) |  |  |
| os | [string](#string) |  |  |
| architecture | [string](#string) |  |  |
| pwd | [string](#string) |  |  |
| current_user | [string](#string) |  |  |
| cpu_count | [int32](#int32) |  |  |
| memory_mb | [int32](#int32) |  |  |






<a name="ztd-operator-v1-TerminateOperatorRequested"></a>

### TerminateOperatorRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| operator_id | [string](#string) |  |  |
| user_id | [string](#string) |  |  |
| reason | [string](#string) |  |  |






<a name="ztd-operator-v1-TerminateOperatorResult"></a>

### TerminateOperatorResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| message | [string](#string) |  |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-UnbindOperatorsRequested"></a>

### UnbindOperatorsRequested



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| operator_ids | [string](#string) | repeated |  |
| user_id | [string](#string) |  |  |
| web_session_id | [string](#string) |  |  |






<a name="ztd-operator-v1-UnbindOperatorsResult"></a>

### UnbindOperatorsResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| success | [bool](#bool) |  |  |
| unbound_count | [int32](#int32) |  |  |
| failed_count | [int32](#int32) |  |  |
| unbound_operator_ids | [string](#string) | repeated |  |
| failed_operator_ids | [string](#string) | repeated |  |
| error | [string](#string) |  |  |






<a name="ztd-operator-v1-UptimeInfo"></a>

### UptimeInfo



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| uptime | [string](#string) |  |  |
| uptime_seconds | [int64](#int64) |  |  |






<a name="ztd-operator-v1-UserDetails"></a>

### UserDetails



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| username | [string](#string) |  |  |
| uid | [int32](#int32) |  |  |
| gid | [int32](#int32) |  |  |
| home | [string](#string) |  |  |
| name | [string](#string) |  |  |
| shell | [string](#string) |  |  |






<a name="ztd-operator-v1-VersionInfo"></a>

### VersionInfo



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| operator_version | [string](#string) |  |  |
| status | [string](#string) |  |  |





 


<a name="ztd-operator-v1-DeterministicStageKind"></a>

### DeterministicStageKind


| Name | Number | Description |
| ---- | ------ | ----------- |
| DETERMINISTIC_STAGE_KIND_UNSPECIFIED | 0 |  |
| DETERMINISTIC_STAGE_KIND_L1_DOCTRINE | 1 |  |
| DETERMINISTIC_STAGE_KIND_PROTOCOL_L2 | 2 |  |
| DETERMINISTIC_STAGE_KIND_L3_NOTARY | 3 |  |
| DETERMINISTIC_STAGE_KIND_L4_VERIFICATION | 4 |  |
| DETERMINISTIC_STAGE_KIND_RECEIPT_PERSISTENCE | 5 |  |
| DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND | 6 |  |
| DETERMINISTIC_STAGE_KIND_L5_EXECUTION | 7 |  |



<a name="ztd-operator-v1-DeterministicStageOutcome"></a>

### DeterministicStageOutcome


| Name | Number | Description |
| ---- | ------ | ----------- |
| DETERMINISTIC_STAGE_OUTCOME_UNSPECIFIED | 0 |  |
| DETERMINISTIC_STAGE_OUTCOME_VERIFIED | 1 |  |
| DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED | 2 |  |
| DETERMINISTIC_STAGE_OUTCOME_COMPLETED | 3 |  |
| DETERMINISTIC_STAGE_OUTCOME_FAILED | 4 |  |



<a name="ztd-operator-v1-ExecutionStatus"></a>

### ExecutionStatus
Execution status enum for command and operation results

| Name | Number | Description |
| ---- | ------ | ----------- |
| EXECUTION_STATUS_UNSPECIFIED | 0 |  |
| EXECUTION_STATUS_EXECUTING | 1 |  |
| EXECUTION_STATUS_COMPLETED | 2 |  |
| EXECUTION_STATUS_FAILED | 3 |  |
| EXECUTION_STATUS_CANCELLED | 4 |  |
| EXECUTION_STATUS_TIMEOUT | 5 |  |



<a name="ztd-operator-v1-GovernanceRejectionCode"></a>

### GovernanceRejectionCode
GovernanceRejectionCode names the verification check that rejected a
transaction before execution. The L5 actuator stamps it on every
GOVERNANCE_REJECTED receipt, so a verifier can attribute a rejection to a
layer from signed evidence alone, without parsing result_summary. It is
bound into the receipt signature.

| Name | Number | Description |
| ---- | ------ | ----------- |
| GOVERNANCE_REJECTION_CODE_UNSPECIFIED | 0 | The receipt is not a governance rejection. |
| GOVERNANCE_REJECTION_CODE_UNCLASSIFIED | 1 | The rejection error is not mapped to a check. A verifier treats it as a defect. |
| GOVERNANCE_REJECTION_CODE_RUNTIME_VAULT_LOCKED | 2 | The runtime vault is locked or absent, so nothing may execute. |
| GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED | 3 | A verifier dependency (doctrine, replay store, signer store, consensus, notary, state root provider) is absent. |
| GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED | 4 | The envelope is structurally incomplete or its typed payload does not decode. |
| GOVERNANCE_REJECTION_CODE_PROTOCOL_VERSION_UNSUPPORTED | 5 |  |
| GOVERNANCE_REJECTION_CODE_EVENT_ACTION_MISMATCH | 6 |  |
| GOVERNANCE_REJECTION_CODE_EXPIRED | 7 |  |
| GOVERNANCE_REJECTION_CODE_REPLAY | 8 | The nonce was already reserved or is in flight. |
| GOVERNANCE_REJECTION_CODE_DOCUMENT_COLLECTION_NOT_GOVERNED | 9 |  |
| GOVERNANCE_REJECTION_CODE_TARGET_OPERATOR_MISMATCH | 10 |  |
| GOVERNANCE_REJECTION_CODE_TARGET_SESSION_MISMATCH | 11 |  |
| GOVERNANCE_REJECTION_CODE_L1_DOCTRINE | 12 | L1 doctrine found a forbidden pattern in the typed payload. |
| GOVERNANCE_REJECTION_CODE_TRANSACTION_HASH_MISMATCH | 13 |  |
| GOVERNANCE_REJECTION_CODE_TRANSACTION_ID_MISMATCH | 14 |  |
| GOVERNANCE_REJECTION_CODE_STATE_ROOT_MISMATCH | 15 |  |
| GOVERNANCE_REJECTION_CODE_POSTURE_MISSING | 16 |  |
| GOVERNANCE_REJECTION_CODE_L2_PROOF_MISSING | 17 |  |
| GOVERNANCE_REJECTION_CODE_L2_SIGNATURE_INVALID | 18 |  |
| GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET | 19 |  |
| GOVERNANCE_REJECTION_CODE_L2_DUPLICATE_SIGNER | 20 |  |
| GOVERNANCE_REJECTION_CODE_L3_PROOF_MISSING | 21 |  |
| GOVERNANCE_REJECTION_CODE_L3_PROOF_INVALID | 22 |  |
| GOVERNANCE_REJECTION_CODE_L2_CONSENSUS_SET_UNKNOWN | 23 | The L2 proof names a consensus set that is unknown or disabled. |
| GOVERNANCE_REJECTION_CODE_L3_DENIED | 24 | The human denied the suspended transaction at the L3 approval. |
| GOVERNANCE_REJECTION_CODE_MCP_TARGET_MISMATCH | 25 | An MCP_CALL&#39;s target_resource is not its payload&#39;s tool name. |



<a name="ztd-operator-v1-HeartbeatType"></a>

### HeartbeatType
Heartbeat type enum

| Name | Number | Description |
| ---- | ------ | ----------- |
| HEARTBEAT_TYPE_UNSPECIFIED | 0 |  |
| HEARTBEAT_TYPE_AUTOMATIC | 1 |  |
| HEARTBEAT_TYPE_MANUAL | 2 |  |



<a name="ztd-operator-v1-InferenceDispatchFailureCode"></a>

### InferenceDispatchFailureCode
InferenceDispatchFailureCode is the typed classification of a failed
POST /api/v1/inference/dispatch. Each code names one centralized public
sentinel, so a caller classifies the failure (a provider that rejects tool
declarations, a backend timeout, a governance rejection) without parsing
the error text.

| Name | Number | Description |
| ---- | ------ | ----------- |
| INFERENCE_DISPATCH_FAILURE_CODE_UNSPECIFIED | 0 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_INTERNAL | 1 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_OUTCOME_UNKNOWN | 2 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_RESULT_TIMEOUT | 3 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_OPERATOR_NOT_FOUND | 4 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_OPERATOR_AMBIGUOUS | 5 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_OPERATOR_NOT_CAPABLE | 6 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_ROLE_INVALID | 7 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_MESSAGES_REQUIRED | 8 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED | 9 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_MESSAGE_INVALID | 10 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_JSON_INVALID | 11 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_JSON_NON_CANONICAL | 12 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_TOOL_SCHEMA_INVALID | 13 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_GENERATION_OPTIONS_INVALID | 14 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_CAPABILITY_UNSUPPORTED | 15 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_TOOLS_UNSUPPORTED | 16 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_CONTEXT_OVERFLOW | 17 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_MODEL_REF_INVALID | 18 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_MODEL_OVERRIDE_DENIED | 19 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_MODEL_REGISTRY_INVALID | 20 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_CAMPAIGN_BINDING_INVALID | 21 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED | 22 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_NO_DELIVERY | 23 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_RECEIPT_FAILED | 24 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_RECEIPT_VERIFY | 25 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_RESULT_DIGEST_MISMATCH | 26 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_RESULT_DIGEST | 27 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_IDENTITY_MISMATCH | 28 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_MODEL_DIGEST_MISMATCH | 29 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_EVIDENCE_HASH_INVALID | 30 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_RESULT_DECODE | 31 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_COMPLETION_NO_RECEIPT | 32 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_COMPLETION_NO_RESULT | 33 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_UNAVAILABLE | 34 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_NOT_REGISTERED | 35 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_TIMEOUT | 36 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_GENERATE_FAILED | 37 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_MODEL_NOT_FOUND | 38 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_PROVIDER_RESPONSE_INVALID | 39 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_CANCELED | 40 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_CALLER_DISCONNECTED | 41 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_PROGRESS_BACKPRESSURE | 42 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_PROGRESS_HASH_MISMATCH | 43 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_DECISION_REQUEST_INVALID | 44 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_DECISION_RESPONSE_INVALID | 45 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_DECISION_UNSUPPORTED | 46 |  |
| INFERENCE_DISPATCH_FAILURE_CODE_HOSTED_NOT_ALLOWED | 47 |  |



<a name="ztd-operator-v1-InferenceLoadState"></a>

### InferenceLoadState


| Name | Number | Description |
| ---- | ------ | ----------- |
| INFERENCE_LOAD_STATE_UNSPECIFIED | 0 |  |
| INFERENCE_LOAD_STATE_COLD | 1 |  |
| INFERENCE_LOAD_STATE_WARM | 2 |  |
| INFERENCE_LOAD_STATE_UNAVAILABLE | 3 |  |



<a name="ztd-operator-v1-InferenceMessageRole"></a>

### InferenceMessageRole
InferenceMessageRole identifies the semantic role of one ordered conversation turn.

| Name | Number | Description |
| ---- | ------ | ----------- |
| INFERENCE_MESSAGE_ROLE_UNSPECIFIED | 0 |  |
| INFERENCE_MESSAGE_ROLE_SYSTEM | 1 |  |
| INFERENCE_MESSAGE_ROLE_USER | 2 |  |
| INFERENCE_MESSAGE_ROLE_ASSISTANT | 3 |  |
| INFERENCE_MESSAGE_ROLE_TOOL | 4 |  |



<a name="ztd-operator-v1-InferenceProviderAttemptStatus"></a>

### InferenceProviderAttemptStatus


| Name | Number | Description |
| ---- | ------ | ----------- |
| INFERENCE_PROVIDER_ATTEMPT_STATUS_UNSPECIFIED | 0 |  |
| INFERENCE_PROVIDER_ATTEMPT_STATUS_IN_PROGRESS | 1 |  |
| INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED | 2 |  |
| INFERENCE_PROVIDER_ATTEMPT_STATUS_FAILED | 3 |  |



<a name="ztd-operator-v1-InferenceRetryClassification"></a>

### InferenceRetryClassification


| Name | Number | Description |
| ---- | ------ | ----------- |
| INFERENCE_RETRY_CLASSIFICATION_UNSPECIFIED | 0 |  |
| INFERENCE_RETRY_CLASSIFICATION_NONE | 1 |  |
| INFERENCE_RETRY_CLASSIFICATION_INFRASTRUCTURE_PRE_RESULT | 2 |  |



<a name="ztd-operator-v1-InferenceTimingSource"></a>

### InferenceTimingSource


| Name | Number | Description |
| ---- | ------ | ----------- |
| INFERENCE_TIMING_SOURCE_UNSPECIFIED | 0 |  |
| INFERENCE_TIMING_SOURCE_PROVIDER | 1 |  |



<a name="ztd-operator-v1-InferenceToolChoiceMode"></a>

### InferenceToolChoiceMode


| Name | Number | Description |
| ---- | ------ | ----------- |
| INFERENCE_TOOL_CHOICE_MODE_UNSPECIFIED | 0 |  |
| INFERENCE_TOOL_CHOICE_MODE_AUTO | 1 |  |
| INFERENCE_TOOL_CHOICE_MODE_NONE | 2 |  |
| INFERENCE_TOOL_CHOICE_MODE_REQUIRED | 3 |  |



<a name="ztd-operator-v1-IngressRefusalCode"></a>

### IngressRefusalCode
IngressRefusalCode classifies a refusal that happens before a GovernanceEnvelope
exists. It is the pre-envelope counterpart of GovernanceRejectionCode. A refusal
never carries a receipt, because nothing was executed or rejected by L4.

| Name | Number | Description |
| ---- | ------ | ----------- |
| INGRESS_REFUSAL_CODE_UNSPECIFIED | 0 |  |
| INGRESS_REFUSAL_CODE_UNCLASSIFIED | 1 |  |
| INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED | 2 |  |
| INGRESS_REFUSAL_CODE_PAYLOAD_TOO_LARGE | 3 |  |
| INGRESS_REFUSAL_CODE_MALFORMED_REQUEST | 4 |  |
| INGRESS_REFUSAL_CODE_MISSING_FIELD | 5 |  |
| INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE | 6 |  |
| INGRESS_REFUSAL_CODE_EVENT_ACTION_MISMATCH | 7 |  |
| INGRESS_REFUSAL_CODE_PAYLOAD_DECODE_FAILED | 8 |  |
| INGRESS_REFUSAL_CODE_MTLS_REQUIRED | 9 |  |
| INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED | 10 |  |
| INGRESS_REFUSAL_CODE_CALLER_SUPPLIED_POSTURE | 11 |  |
| INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND | 12 |  |
| INGRESS_REFUSAL_CODE_WITNESS_NOT_CAPABLE | 13 |  |
| INGRESS_REFUSAL_CODE_L1_DOCTRINE | 14 |  |
| INGRESS_REFUSAL_CODE_L3_PROOF_UNMINTABLE | 15 |  |
| INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE | 16 |  |
| INGRESS_REFUSAL_CODE_GATEWAY_MISCONFIGURED | 17 |  |
| INGRESS_REFUSAL_CODE_EMBEDDED_OPERATOR_UNCLAIMED | 18 |  |
| INGRESS_REFUSAL_CODE_L2_DELIBERATION_FAILED | 19 |  |
| INGRESS_REFUSAL_CODE_STATE_ROOT_UNAVAILABLE | 20 |  |



<a name="ztd-operator-v1-L2Status"></a>

### L2Status
L2 validation status enum for Consensus (L2Consensus) signature verification
Distinguishes between &#34;not required&#34; vs &#34;required but failed&#34; for compliance

| Name | Number | Description |
| ---- | ------ | ----------- |
| L2_STATUS_UNSPECIFIED | 0 |  |
| L2_STATUS_NOT_REQUIRED | 1 | L2 signature not required by posture (doctrine/ratify) |
| L2_STATUS_REQUIRED_VALID | 2 | L2 signature required and valid |
| L2_STATUS_REQUIRED_FAILED | 3 | L2 signature required but missing or invalid |



<a name="ztd-operator-v1-L3Status"></a>

### L3Status
L3 validation status enum for Notary (L3Notary) proof verification
Distinguishes between &#34;not required&#34; vs &#34;required but failed&#34; for compliance

| Name | Number | Description |
| ---- | ------ | ----------- |
| L3_STATUS_UNSPECIFIED | 0 |  |
| L3_STATUS_NOT_REQUIRED | 1 | L3 proof not required by posture (doctrine/consensus) |
| L3_STATUS_REQUIRED_VALID | 2 | L3 proof required and valid |
| L3_STATUS_REQUIRED_FAILED | 3 | L3 proof required but missing or invalid |



<a name="ztd-operator-v1-ModelRole"></a>

### ModelRole
ModelRole identifies the chat-tier role for a governed inference request.

| Name | Number | Description |
| ---- | ------ | ----------- |
| MODEL_ROLE_UNSPECIFIED | 0 |  |
| MODEL_ROLE_PRIMARY | 1 |  |
| MODEL_ROLE_ASSISTANT | 2 |  |
| MODEL_ROLE_LITE | 3 |  |



<a name="ztd-operator-v1-ReceiptFailureCode"></a>

### ReceiptFailureCode
ReceiptFailureCode is the typed classification of a FAILED ActionReceipt.
The L5 actuator stamps it at finalization so verifiers can distinguish
governance rejections, client faults, and provider failures without
string-matching the result_summary. It is bound into the receipt signature.

| Name | Number | Description |
| ---- | ------ | ----------- |
| RECEIPT_FAILURE_CODE_UNSPECIFIED | 0 |  |
| RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED | 1 | The transaction failed governance verification before execution (L1 doctrine, L2 consensus, L3 notary, identity, replay, expiry). |
| RECEIPT_FAILURE_CODE_MODEL_OVERRIDE_DENIED | 2 | The request model did not equal the configured role model. |
| RECEIPT_FAILURE_CODE_ROLE_INVALID | 3 | The request carried an unspecified or unknown model role. |
| RECEIPT_FAILURE_CODE_MODEL_REF_INVALID | 4 | The request carried a malformed model reference. |
| RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE | 5 | The inference backend was unreachable or not registered. |
| RECEIPT_FAILURE_CODE_BACKEND_TIMEOUT | 6 | The inference backend request timed out. |
| RECEIPT_FAILURE_CODE_GENERATE_FAILED | 7 | The inference backend failed generation. |
| RECEIPT_FAILURE_CODE_EXECUTION_FAILED | 8 | Any other execution failure. |
| RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND | 9 | The requested or configured model is absent from the provider&#39;s model store. |
| RECEIPT_FAILURE_CODE_PROVIDER_RESPONSE_INVALID | 10 | The provider returned a malformed or oversized response. |
| RECEIPT_FAILURE_CODE_GENERATION_OPTIONS_INVALID | 11 |  |
| RECEIPT_FAILURE_CODE_CAPABILITY_UNSUPPORTED | 12 |  |
| RECEIPT_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED | 13 |  |
| RECEIPT_FAILURE_CODE_IDENTITY_MISMATCH | 14 |  |
| RECEIPT_FAILURE_CODE_MODEL_DIGEST_MISMATCH | 15 |  |
| RECEIPT_FAILURE_CODE_EVIDENCE_HASH_INVALID | 16 |  |
| RECEIPT_FAILURE_CODE_MODEL_REGISTRY_INVALID | 17 |  |
| RECEIPT_FAILURE_CODE_CAMPAIGN_BINDING_INVALID | 18 |  |
| RECEIPT_FAILURE_CODE_TOOLS_UNSUPPORTED | 19 |  |
| RECEIPT_FAILURE_CODE_CONTEXT_OVERFLOW | 20 | The prompt filled the provider&#39;s context window, so the provider truncated it or left no room to generate. |
| RECEIPT_FAILURE_CODE_REHYDRATION_FAILED | 21 | A UEI placeholder in the payload could not be rehydrated (unresolved token or colliding object keys), so the payload was withheld from dispatch. |
| RECEIPT_FAILURE_CODE_DECISION_REQUEST_INVALID | 22 |  |
| RECEIPT_FAILURE_CODE_DECISION_RESPONSE_INVALID | 23 |  |
| RECEIPT_FAILURE_CODE_DECISION_UNSUPPORTED | 24 |  |
| RECEIPT_FAILURE_CODE_HOSTED_NOT_ALLOWED | 25 |  |


 

 


<a name="ztd-operator-v1-OperatorService"></a>

### OperatorService


| Method Name | Request Type | Response Type | Description |
| ----------- | ------------ | ------------- | ------------|
| ExecuteCommand | [CommandRequested](#ztd-operator-v1-CommandRequested) | [CommandResult](#ztd-operator-v1-CommandResult) | Execute a shell command |
| CancelCommand | [CommandCancelRequested](#ztd-operator-v1-CommandCancelRequested) | [CommandResult](#ztd-operator-v1-CommandResult) | Cancel a running command |
| EditFile | [FileEditRequested](#ztd-operator-v1-FileEditRequested) | [CommandResult](#ztd-operator-v1-CommandResult) | Edit a file |
| ListFileSystem | [FsListRequested](#ztd-operator-v1-FsListRequested) | [CommandResult](#ztd-operator-v1-CommandResult) | List directory contents |
| ReadFileSystem | [FsReadRequested](#ztd-operator-v1-FsReadRequested) | [CommandResult](#ztd-operator-v1-CommandResult) | Read file contents |

 



## Scalar Value Types

| .proto Type | Notes | C++ | Java | Python | Go | C# | PHP | Ruby |
| ----------- | ----- | --- | ---- | ------ | -- | -- | --- | ---- |
| <a name="double" /> double |  | double | double | float | float64 | double | float | Float |
| <a name="float" /> float |  | float | float | float | float32 | float | float | Float |
| <a name="int32" /> int32 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint32 instead. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="int64" /> int64 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint64 instead. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="uint32" /> uint32 | Uses variable-length encoding. | uint32 | int | int/long | uint32 | uint | integer | Bignum or Fixnum (as required) |
| <a name="uint64" /> uint64 | Uses variable-length encoding. | uint64 | long | int/long | uint64 | ulong | integer/string | Bignum or Fixnum (as required) |
| <a name="sint32" /> sint32 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int32s. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="sint64" /> sint64 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int64s. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="fixed32" /> fixed32 | Always four bytes. More efficient than uint32 if values are often greater than 2^28. | uint32 | int | int | uint32 | uint | integer | Bignum or Fixnum (as required) |
| <a name="fixed64" /> fixed64 | Always eight bytes. More efficient than uint64 if values are often greater than 2^56. | uint64 | long | int/long | uint64 | ulong | integer/string | Bignum |
| <a name="sfixed32" /> sfixed32 | Always four bytes. | int32 | int | int | int32 | int | integer | Bignum or Fixnum (as required) |
| <a name="sfixed64" /> sfixed64 | Always eight bytes. | int64 | long | int/long | int64 | long | integer/string | Bignum |
| <a name="bool" /> bool |  | bool | boolean | boolean | bool | bool | boolean | TrueClass/FalseClass |
| <a name="string" /> string | A string must always contain UTF-8 encoded or 7-bit ASCII text. | string | String | str/unicode | string | string | string | String (UTF-8) |
| <a name="bytes" /> bytes | May contain any arbitrary sequence of bytes. | string | ByteString | str | []byte | ByteString | string | String (ASCII-8BIT) |

