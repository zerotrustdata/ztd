# Protocol Documentation
<a name="top"></a>

## Table of Contents

- [ztd/tribunal/v1/tribunal.proto](#ztd_tribunal_v1_tribunal-proto)
    - [TribunalIntentRequest](#ztd-tribunal-v1-TribunalIntentRequest)
    - [TribunalIntentResult](#ztd-tribunal-v1-TribunalIntentResult)
    - [TribunalMemberCall](#ztd-tribunal-v1-TribunalMemberCall)
  
    - [TribunalOutcome](#ztd-tribunal-v1-TribunalOutcome)
    - [TribunalStage](#ztd-tribunal-v1-TribunalStage)
  
- [Scalar Value Types](#scalar-value-types)



<a name="ztd_tribunal_v1_tribunal-proto"></a>
<p align="right"><a href="#top">Top</a></p>

## ztd/tribunal/v1/tribunal.proto



<a name="ztd-tribunal-v1-TribunalIntentRequest"></a>

### TribunalIntentRequest
TribunalIntentRequest is the body of POST /api/v1/operators/intents. The
Gateway&#39;s Tribunal reviews typed run_shell_command arguments through governed
INFERENCE dispatches, then dispatches the approved bytes to the target
Operator as an ordinary governed MCP_CALL. It never supplies or replaces
an L2 quorum proof or an L3 approval.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| request | [string](#string) |  | Canonical JSON proposed run_shell_command arguments. Required. |
| guidelines | [string](#string) |  | Orchestration constraints that each member must follow. |
| working_directory | [string](#string) |  | Execution context shown to the panel. Empty os and shell use the Tribunal defaults. |
| os | [string](#string) |  |  |
| shell | [string](#string) |  |  |
| target_operator_session_id | [string](#string) |  | Command dispatch target and scope, as on POST /api/v1/operators/commands. |
| case_id | [string](#string) |  |  |
| investigation_id | [string](#string) |  |  |
| task_id | [string](#string) |  |  |
| cli_session_id | [string](#string) |  |  |
| execution_id | [string](#string) |  | execution_id is carried by the dispatched McpCallRequested. The Gateway merges timeout_seconds into the reviewed RunShellCommandArguments. |
| timeout_seconds | [int32](#int32) |  |  |
| member_inference | [ztd.operator.v1.InferenceDispatchRequest](#ztd-operator-v1-InferenceDispatchRequest) |  | member_inference is the template for every member call. The Gateway sets messages, provider_attempt_id and the role&#39;s response_format schema per call; the template must leave messages, tools, provider_attempt_id, acting_app_id, stream and response_format empty. |
| provider_attempt_prefix | [string](#string) |  | provider_attempt_prefix names the member calls: call n (from 1, in deliberation order) uses provider_attempt_id &#34;&lt;prefix&gt;-&lt;n&gt;&#34;. Required and unique per intent, so the caller can bind each result to its request. |
| tier | [ztd.eval.v1.ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  | tier is the campaign role recorded on each persona step. |
| step_start_index | [uint32](#uint32) |  | step_start_index numbers the persona steps from step_start_index &#43; 1. |






<a name="ztd-tribunal-v1-TribunalIntentResult"></a>

### TribunalIntentResult
TribunalIntentResult is the HTTP 200 body of POST /api/v1/operators/intents.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| outcome | [TribunalOutcome](#ztd-tribunal-v1-TribunalOutcome) |  |  |
| arguments | [ztd.operator.v1.RunShellCommandArguments](#ztd-operator-v1-RunShellCommandArguments) |  | arguments are the approved typed run_shell_command arguments. |
| arguments_json | [string](#string) |  | arguments_json are the exact canonical bytes dispatched to the Operator. |
| consensus_inferences | [ztd.operator.v1.ConsensusVoteInference](#ztd-operator-v1-ConsensusVoteInference) | repeated | All command admission rounds, including revised or failed contextual reviews. |
| steps | [ztd.eval.v1.AgentStep](#ztd-eval-v1-AgentStep) | repeated | steps are the persona steps of the deliberation, in order. |
| member_calls | [TribunalMemberCall](#ztd-tribunal-v1-TribunalMemberCall) | repeated | member_calls are the governed member inferences, in order. |
| risk_rationale | [string](#string) |  | Marshal&#39;s rationale and feedback for a BLOCKED outcome. |
| risk_feedback | [string](#string) |  |  |
| audit_failure | [string](#string) |  | audit_failure explains an AUDIT_FAILED outcome. |
| dispatched | [ztd.operator.v1.GovernedDispatchResult](#ztd-operator-v1-GovernedDispatchResult) |  |  |
| rejection | [ztd.operator.v1.GovernanceRejection](#ztd-operator-v1-GovernanceRejection) |  |  |
| refusal | [ztd.operator.v1.IngressRefusal](#ztd-operator-v1-IngressRefusal) |  |  |






<a name="ztd-tribunal-v1-TribunalMemberCall"></a>

### TribunalMemberCall
TribunalMemberCall is one governed INFERENCE dispatch made by the Tribunal.
Every call the Inference Operator executed carries its signed receipt.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| stage | [TribunalStage](#ztd-tribunal-v1-TribunalStage) |  |  |
| persona | [string](#string) |  |  |
| round | [uint32](#uint32) |  |  |
| provider_attempt_id | [string](#string) |  |  |
| transaction_id | [string](#string) |  |  |
| result | [ztd.operator.v1.InferenceResult](#ztd-operator-v1-InferenceResult) |  | result is the complete verified InferenceResult of a completed call. |
| receipt | [ztd.operator.v1.EventReceipt](#ztd-operator-v1-EventReceipt) |  | receipt is the Inference Operator&#39;s signed final EventReceipt. |
| failure | [ztd.operator.v1.InferenceDispatchFailure](#ztd-operator-v1-InferenceDispatchFailure) |  | failure is set on a call that did not complete. |
| observation_receipts | [ztd.operator.v1.EventReceipt](#ztd-operator-v1-EventReceipt) | repeated | observation_receipts are the observers&#39; signed final receipts for the call&#39;s BEGIN and FINALIZE commands (InferenceDispatchResponse field 4). |





 


<a name="ztd-tribunal-v1-TribunalOutcome"></a>

### TribunalOutcome
TribunalOutcome is how one deliberation ended.

| Name | Number | Description |
| ---- | ------ | ----------- |
| TRIBUNAL_OUTCOME_UNSPECIFIED | 0 |  |
| TRIBUNAL_OUTCOME_APPROVED | 1 | The Auditor approved a command and the Gateway dispatched it; the command outcome is set. |
| TRIBUNAL_OUTCOME_NO_CONSENSUS | 2 | Neither voting round produced a winner. |
| TRIBUNAL_OUTCOME_BLOCKED | 3 | Marshal blocked the winning command. |
| TRIBUNAL_OUTCOME_AUDIT_FAILED | 4 | The Auditor&#39;s review did not produce a usable command. |
| TRIBUNAL_OUTCOME_INFERENCE_FAILED | 5 | A member inference failed; the last member call carries the failure. |



<a name="ztd-tribunal-v1-TribunalStage"></a>

### TribunalStage
TribunalStage is the deliberation stage of one member call.

| Name | Number | Description |
| ---- | ------ | ----------- |
| TRIBUNAL_STAGE_UNSPECIFIED | 0 |  |
| TRIBUNAL_STAGE_GENERATE | 1 |  |
| TRIBUNAL_STAGE_MARSHAL | 2 |  |
| TRIBUNAL_STAGE_AUDIT | 3 |  |


 

 

 



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

