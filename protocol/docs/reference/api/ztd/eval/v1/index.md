# Protocol Documentation
<a name="top"></a>

## Table of Contents

- [ztd/eval/v1/eval.proto](#ztd_eval_v1_eval-proto)
    - [CampaignComplianceSourceArtifact](#ztd-eval-v1-CampaignComplianceSourceArtifact)
    - [CampaignComplianceSourceInventory](#ztd-eval-v1-CampaignComplianceSourceInventory)
    - [DecomposedScoreRecord](#ztd-eval-v1-DecomposedScoreRecord)
    - [DeterministicGrade](#ztd-eval-v1-DeterministicGrade)
    - [EscalationRecord](#ztd-eval-v1-EscalationRecord)
    - [EvaluationAssertion](#ztd-eval-v1-EvaluationAssertion)
    - [EvaluationAssignment](#ztd-eval-v1-EvaluationAssignment)
    - [EvaluationAssignmentResult](#ztd-eval-v1-EvaluationAssignmentResult)
    - [EvaluationAttempt](#ztd-eval-v1-EvaluationAttempt)
    - [EvaluationCampaignSpec](#ztd-eval-v1-EvaluationCampaignSpec)
    - [EvaluationDeploymentIdentity](#ztd-eval-v1-EvaluationDeploymentIdentity)
    - [EvaluationMetric](#ztd-eval-v1-EvaluationMetric)
    - [EvaluationObservation](#ztd-eval-v1-EvaluationObservation)
    - [EvaluationReport](#ztd-eval-v1-EvaluationReport)
    - [EvaluationRun](#ztd-eval-v1-EvaluationRun)
    - [EvaluationRuntimeBoundary](#ztd-eval-v1-EvaluationRuntimeBoundary)
    - [EvaluationScenarioCatalog](#ztd-eval-v1-EvaluationScenarioCatalog)
    - [EvaluationScenarioDefinition](#ztd-eval-v1-EvaluationScenarioDefinition)
    - [EvaluationTargetState](#ztd-eval-v1-EvaluationTargetState)
    - [EvaluationValue](#ztd-eval-v1-EvaluationValue)
    - [EvaluationVerdict](#ztd-eval-v1-EvaluationVerdict)
    - [EvaluationVerificationReport](#ztd-eval-v1-EvaluationVerificationReport)
    - [EvaluationVerifiedPopulation](#ztd-eval-v1-EvaluationVerifiedPopulation)
    - [EvaluationVerifiedPopulationEntry](#ztd-eval-v1-EvaluationVerifiedPopulationEntry)
    - [GovernedActionBinding](#ztd-eval-v1-GovernedActionBinding)
    - [GraderModelCallRecord](#ztd-eval-v1-GraderModelCallRecord)
    - [HandoffRecord](#ztd-eval-v1-HandoffRecord)
    - [HeterogeneousAssignmentTarget](#ztd-eval-v1-HeterogeneousAssignmentTarget)
    - [HeterogeneousStackDefinition](#ztd-eval-v1-HeterogeneousStackDefinition)
    - [HomogeneousAssignmentTarget](#ztd-eval-v1-HomogeneousAssignmentTarget)
    - [ModelCampaignBinding](#ztd-eval-v1-ModelCampaignBinding)
    - [ModelCapabilityObservation](#ztd-eval-v1-ModelCapabilityObservation)
    - [ModelInferenceRecord](#ztd-eval-v1-ModelInferenceRecord)
    - [ModelProvenanceAttestationWindow](#ztd-eval-v1-ModelProvenanceAttestationWindow)
    - [ModelProvenanceObservationCommand](#ztd-eval-v1-ModelProvenanceObservationCommand)
    - [ModelProvenanceObservationCompleted](#ztd-eval-v1-ModelProvenanceObservationCompleted)
    - [ModelVariant](#ztd-eval-v1-ModelVariant)
    - [ModelWeightAttestation](#ztd-eval-v1-ModelWeightAttestation)
    - [PolicyDecisionRecord](#ztd-eval-v1-PolicyDecisionRecord)
    - [PromptHint](#ztd-eval-v1-PromptHint)
    - [PromptHintArgument](#ztd-eval-v1-PromptHintArgument)
    - [ProviderBoundaryHardwareSample](#ztd-eval-v1-ProviderBoundaryHardwareSample)
    - [ProviderBoundaryObservationCommand](#ztd-eval-v1-ProviderBoundaryObservationCommand)
    - [ProviderBoundaryObservationCompleted](#ztd-eval-v1-ProviderBoundaryObservationCompleted)
    - [ProviderBoundaryObservationWindow](#ztd-eval-v1-ProviderBoundaryObservationWindow)
    - [PublicAssignmentActivitySummary](#ztd-eval-v1-PublicAssignmentActivitySummary)
    - [PublicAssignmentLifecycleRecord](#ztd-eval-v1-PublicAssignmentLifecycleRecord)
    - [PublicAssignmentResultProjection](#ztd-eval-v1-PublicAssignmentResultProjection)
    - [PublicCampaignIdentity](#ztd-eval-v1-PublicCampaignIdentity)
    - [PublicEvidenceBinding](#ztd-eval-v1-PublicEvidenceBinding)
    - [PublicGovernedActionActivity](#ztd-eval-v1-PublicGovernedActionActivity)
    - [PublicGovernedActionActivityRecord](#ztd-eval-v1-PublicGovernedActionActivityRecord)
    - [PublicLiveEvent](#ztd-eval-v1-PublicLiveEvent)
    - [PublicLiveEvent.MetricDeltaEntry](#ztd-eval-v1-PublicLiveEvent-MetricDeltaEntry)
    - [PublicLiveMetricValue](#ztd-eval-v1-PublicLiveMetricValue)
    - [PublicModelActivity](#ztd-eval-v1-PublicModelActivity)
    - [PublicModelActivityRecord](#ztd-eval-v1-PublicModelActivityRecord)
    - [PublicModelCallSummary](#ztd-eval-v1-PublicModelCallSummary)
    - [PublicModelVariantIdentity](#ztd-eval-v1-PublicModelVariantIdentity)
    - [PublicPolicyDecisionActivity](#ztd-eval-v1-PublicPolicyDecisionActivity)
    - [PublicPolicyDecisionActivityRecord](#ztd-eval-v1-PublicPolicyDecisionActivityRecord)
    - [PublicScenarioCriterion](#ztd-eval-v1-PublicScenarioCriterion)
    - [PublicScenarioSummary](#ztd-eval-v1-PublicScenarioSummary)
    - [PublicSemanticGradeSummary](#ztd-eval-v1-PublicSemanticGradeSummary)
    - [PublicToolCallActivity](#ztd-eval-v1-PublicToolCallActivity)
    - [PublicToolCallActivityRecord](#ztd-eval-v1-PublicToolCallActivityRecord)
    - [PublicToolDecisionActivity](#ztd-eval-v1-PublicToolDecisionActivity)
    - [PublicToolDecisionActivityRecord](#ztd-eval-v1-PublicToolDecisionActivityRecord)
    - [PublicToolScoreDimensionRequirement](#ztd-eval-v1-PublicToolScoreDimensionRequirement)
    - [PublicVerificationMetadata](#ztd-eval-v1-PublicVerificationMetadata)
    - [RecoveryRecord](#ztd-eval-v1-RecoveryRecord)
    - [RoleAssignment](#ztd-eval-v1-RoleAssignment)
    - [SemanticGrade](#ztd-eval-v1-SemanticGrade)
    - [ToolCallRecord](#ztd-eval-v1-ToolCallRecord)
    - [ToolDecisionRecord](#ztd-eval-v1-ToolDecisionRecord)
  
    - [EvaluationAssignmentLifecycleStatus](#ztd-eval-v1-EvaluationAssignmentLifecycleStatus)
    - [EvaluationAttemptStatus](#ztd-eval-v1-EvaluationAttemptStatus)
    - [EvaluationCallClassification](#ztd-eval-v1-EvaluationCallClassification)
    - [EvaluationComparator](#ztd-eval-v1-EvaluationComparator)
    - [EvaluationEvidenceAuthority](#ztd-eval-v1-EvaluationEvidenceAuthority)
    - [EvaluationGovernancePosture](#ztd-eval-v1-EvaluationGovernancePosture)
    - [EvaluationGradingMethod](#ztd-eval-v1-EvaluationGradingMethod)
    - [EvaluationHintArgumentSource](#ztd-eval-v1-EvaluationHintArgumentSource)
    - [EvaluationLane](#ztd-eval-v1-EvaluationLane)
    - [EvaluationLoadState](#ztd-eval-v1-EvaluationLoadState)
    - [EvaluationMetricDirection](#ztd-eval-v1-EvaluationMetricDirection)
    - [EvaluationMetricUnit](#ztd-eval-v1-EvaluationMetricUnit)
    - [EvaluationMissingDataPolicy](#ztd-eval-v1-EvaluationMissingDataPolicy)
    - [EvaluationObservationSource](#ztd-eval-v1-EvaluationObservationSource)
    - [EvaluationPolicyDecisionOutcome](#ztd-eval-v1-EvaluationPolicyDecisionOutcome)
    - [EvaluationRuntimeComponent](#ztd-eval-v1-EvaluationRuntimeComponent)
    - [EvaluationScenarioCategory](#ztd-eval-v1-EvaluationScenarioCategory)
    - [EvaluationTrajectoryOutcome](#ztd-eval-v1-EvaluationTrajectoryOutcome)
    - [EvaluationTrajectoryPolicy](#ztd-eval-v1-EvaluationTrajectoryPolicy)
    - [EvaluationUsageAvailability](#ztd-eval-v1-EvaluationUsageAvailability)
    - [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus)
    - [EvaluationWitnessPolicy](#ztd-eval-v1-EvaluationWitnessPolicy)
    - [GradeBasis](#ztd-eval-v1-GradeBasis)
    - [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole)
    - [ModelCapabilityKind](#ztd-eval-v1-ModelCapabilityKind)
    - [ModelManifestVerificationStatus](#ztd-eval-v1-ModelManifestVerificationStatus)
    - [ModelProvenanceObservationAttemptStatus](#ztd-eval-v1-ModelProvenanceObservationAttemptStatus)
    - [ModelProvenanceObservationPhase](#ztd-eval-v1-ModelProvenanceObservationPhase)
    - [ProviderBoundaryObservationAttemptStatus](#ztd-eval-v1-ProviderBoundaryObservationAttemptStatus)
    - [ProviderBoundaryObservationPhase](#ztd-eval-v1-ProviderBoundaryObservationPhase)
    - [ProviderHardwareMetricAvailability](#ztd-eval-v1-ProviderHardwareMetricAvailability)
    - [PublicActivityAvailability](#ztd-eval-v1-PublicActivityAvailability)
    - [PublicEvidenceSource](#ztd-eval-v1-PublicEvidenceSource)
    - [PublicFinishState](#ztd-eval-v1-PublicFinishState)
    - [PublicGradeExplanationCode](#ztd-eval-v1-PublicGradeExplanationCode)
    - [PublicReceiptStatus](#ztd-eval-v1-PublicReceiptStatus)
    - [PublicReleaseBasis](#ztd-eval-v1-PublicReleaseBasis)
    - [PublicToolScoreDimension](#ztd-eval-v1-PublicToolScoreDimension)
    - [PublicUnavailableReason](#ztd-eval-v1-PublicUnavailableReason)
    - [PublicVerificationProvenance](#ztd-eval-v1-PublicVerificationProvenance)
  
- [Scalar Value Types](#scalar-value-types)



<a name="ztd_eval_v1_eval-proto"></a>
<p align="right"><a href="#top">Top</a></p>

## ztd/eval/v1/eval.proto



<a name="ztd-eval-v1-CampaignComplianceSourceArtifact"></a>

### CampaignComplianceSourceArtifact



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| runtime_path | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |
| media_type | [string](#string) |  |  |






<a name="ztd-eval-v1-CampaignComplianceSourceInventory"></a>

### CampaignComplianceSourceInventory



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| admission_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| provider_observation_policy | [EvaluationWitnessPolicy](#ztd-eval-v1-EvaluationWitnessPolicy) |  |  |
| model_provenance_policy | [EvaluationWitnessPolicy](#ztd-eval-v1-EvaluationWitnessPolicy) |  |  |
| artifacts | [CampaignComplianceSourceArtifact](#ztd-eval-v1-CampaignComplianceSourceArtifact) | repeated |  |






<a name="ztd-eval-v1-DecomposedScoreRecord"></a>

### DecomposedScoreRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| score_id | [string](#string) |  |  |
| dimension | [string](#string) |  |  |
| value | [double](#double) |  |  |
| unit | [EvaluationMetricUnit](#ztd-eval-v1-EvaluationMetricUnit) |  |  |
| direction | [EvaluationMetricDirection](#ztd-eval-v1-EvaluationMetricDirection) |  |  |
| missing_data_policy | [EvaluationMissingDataPolicy](#ztd-eval-v1-EvaluationMissingDataPolicy) |  |  |






<a name="ztd-eval-v1-DeterministicGrade"></a>

### DeterministicGrade



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| grade_id | [string](#string) |  |  |
| criterion_id | [string](#string) |  |  |
| status | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| score | [double](#double) |  |  |
| detail | [string](#string) |  |  |
| basis | [GradeBasis](#ztd-eval-v1-GradeBasis) |  |  |






<a name="ztd-eval-v1-EscalationRecord"></a>

### EscalationRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| escalation_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| from_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| to_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| justified | [bool](#bool) |  |  |
| reason_code | [string](#string) |  |  |






<a name="ztd-eval-v1-EvaluationAssertion"></a>

### EvaluationAssertion



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assertion_id | [string](#string) |  |  |
| assertion_version | [string](#string) |  |  |
| comparator | [EvaluationComparator](#ztd-eval-v1-EvaluationComparator) |  |  |
| expected | [EvaluationValue](#ztd-eval-v1-EvaluationValue) |  |  |
| required_observation_types | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) | repeated |  |
| required_authorities | [EvaluationEvidenceAuthority](#ztd-eval-v1-EvaluationEvidenceAuthority) | repeated |  |






<a name="ztd-eval-v1-EvaluationAssignment"></a>

### EvaluationAssignment
EvaluationAssignment is one scheduled model-role or system-lane cell.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| deterministic_identity | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| scenario_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| scenario_id | [string](#string) |  |  |
| lane | [EvaluationLane](#ztd-eval-v1-EvaluationLane) |  |  |
| lifecycle_status | [EvaluationAssignmentLifecycleStatus](#ztd-eval-v1-EvaluationAssignmentLifecycleStatus) |  |  |
| repetition | [uint32](#uint32) |  |  |
| queued_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| started_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| completed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| homogeneous | [HomogeneousAssignmentTarget](#ztd-eval-v1-HomogeneousAssignmentTarget) |  |  |
| heterogeneous | [HeterogeneousAssignmentTarget](#ztd-eval-v1-HeterogeneousAssignmentTarget) |  |  |






<a name="ztd-eval-v1-EvaluationAssignmentResult"></a>

### EvaluationAssignmentResult
EvaluationAssignmentResult is the private terminal result for one assignment.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| lane | [EvaluationLane](#ztd-eval-v1-EvaluationLane) |  |  |
| lifecycle_status | [EvaluationAssignmentLifecycleStatus](#ztd-eval-v1-EvaluationAssignmentLifecycleStatus) |  |  |
| result_digest | [string](#string) |  |  |
| model_inferences | [ModelInferenceRecord](#ztd-eval-v1-ModelInferenceRecord) | repeated |  |
| tool_decisions | [ToolDecisionRecord](#ztd-eval-v1-ToolDecisionRecord) | repeated |  |
| tool_calls | [ToolCallRecord](#ztd-eval-v1-ToolCallRecord) | repeated |  |
| escalations | [EscalationRecord](#ztd-eval-v1-EscalationRecord) | repeated |  |
| handoffs | [HandoffRecord](#ztd-eval-v1-HandoffRecord) | repeated |  |
| recoveries | [RecoveryRecord](#ztd-eval-v1-RecoveryRecord) | repeated |  |
| governed_actions | [GovernedActionBinding](#ztd-eval-v1-GovernedActionBinding) | repeated |  |
| deterministic_grades | [DeterministicGrade](#ztd-eval-v1-DeterministicGrade) | repeated |  |
| semantic_grades | [SemanticGrade](#ztd-eval-v1-SemanticGrade) | repeated |  |
| decomposed_scores | [DecomposedScoreRecord](#ztd-eval-v1-DecomposedScoreRecord) | repeated |  |
| grader_calls | [GraderModelCallRecord](#ztd-eval-v1-GraderModelCallRecord) | repeated |  |
| evidence_refs | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) | repeated |  |
| completed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| policy_decisions | [PolicyDecisionRecord](#ztd-eval-v1-PolicyDecisionRecord) | repeated |  |
| tool_decisions_captured | [bool](#bool) |  |  |
| tool_calls_captured | [bool](#bool) |  |  |
| governed_actions_captured | [bool](#bool) |  |  |
| policy_decisions_captured | [bool](#bool) |  |  |
| scored_inference_span_nanos | [uint64](#uint64) | optional |  |
| trajectory_outcome | [EvaluationTrajectoryOutcome](#ztd-eval-v1-EvaluationTrajectoryOutcome) |  |  |
| guided_retry_count | [uint32](#uint32) |  |  |
| failure_reason | [string](#string) |  | Private one-sentence failure reason (may quote the hint values and the start of the model output). Empty when the scenario passed. |
| public_failure_reason | [string](#string) |  | Public-safe failure reason built only from public fields. |






<a name="ztd-eval-v1-EvaluationAttempt"></a>

### EvaluationAttempt



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| attempt_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| scenario_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| status | [EvaluationAttemptStatus](#ztd-eval-v1-EvaluationAttemptStatus) |  |  |
| started_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| completed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| transaction_id | [string](#string) |  |  |
| execution_id | [string](#string) |  |  |
| observation_refs | [string](#string) | repeated |  |
| assertion_refs | [string](#string) | repeated |  |
| verdict_refs | [string](#string) | repeated |  |
| failure_detail | [string](#string) |  |  |






<a name="ztd-eval-v1-EvaluationCampaignSpec"></a>

### EvaluationCampaignSpec
EvaluationCampaignSpec is the frozen private campaign definition.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| catalog_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| catalog_digest | [string](#string) |  |  |
| model_registry | [ModelVariant](#ztd-eval-v1-ModelVariant) | repeated |  |
| model_registry_digest | [string](#string) |  |  |
| campaign_digest | [string](#string) |  |  |
| governance_posture | [EvaluationGovernancePosture](#ztd-eval-v1-EvaluationGovernancePosture) |  |  |
| scenario_count | [uint32](#uint32) |  |  |
| repetition_count | [uint32](#uint32) |  |  |
| platform_release | [string](#string) |  | platform_release is the ztd build version that froze the campaign. It is part of campaign_digest, so a campaign cannot be re-labeled. Empty only on campaigns frozen before v2.3.0; their release is an operator-asserted tag held outside the digest. |
| source_revision | [string](#string) |  | source_revision is the source commit of the build that froze the campaign. |






<a name="ztd-eval-v1-EvaluationDeploymentIdentity"></a>

### EvaluationDeploymentIdentity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| deployment_id | [string](#string) |  |  |
| topology_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| runtime_boundaries | [EvaluationRuntimeBoundary](#ztd-eval-v1-EvaluationRuntimeBoundary) | repeated |  |
| controlled_target | [string](#string) |  |  |
| independent_observer | [string](#string) |  |  |






<a name="ztd-eval-v1-EvaluationMetric"></a>

### EvaluationMetric



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| metric_id | [string](#string) |  |  |
| metric_version | [string](#string) |  |  |
| numerator | [int64](#int64) |  |  |
| denominator | [int64](#int64) |  |  |
| value | [double](#double) |  |  |
| unit | [EvaluationMetricUnit](#ztd-eval-v1-EvaluationMetricUnit) |  |  |
| direction | [EvaluationMetricDirection](#ztd-eval-v1-EvaluationMetricDirection) |  |  |
| eligible_population_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| missing_data_policy | [EvaluationMissingDataPolicy](#ztd-eval-v1-EvaluationMissingDataPolicy) |  |  |
| source_verdict_refs | [string](#string) | repeated |  |
| evidence_refs | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) | repeated |  |






<a name="ztd-eval-v1-EvaluationObservation"></a>

### EvaluationObservation



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| observation_id | [string](#string) |  |  |
| observation_type | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| source | [EvaluationObservationSource](#ztd-eval-v1-EvaluationObservationSource) |  |  |
| observed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| run_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| attempt_id | [string](#string) |  |  |
| authority | [EvaluationEvidenceAuthority](#ztd-eval-v1-EvaluationEvidenceAuthority) |  |  |
| value | [EvaluationValue](#ztd-eval-v1-EvaluationValue) |  |  |
| evidence_refs | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) | repeated |  |






<a name="ztd-eval-v1-EvaluationReport"></a>

### EvaluationReport



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| run | [EvaluationRun](#ztd-eval-v1-EvaluationRun) |  |  |
| attempts | [EvaluationAttempt](#ztd-eval-v1-EvaluationAttempt) | repeated |  |
| observations | [EvaluationObservation](#ztd-eval-v1-EvaluationObservation) | repeated |  |
| assertions | [EvaluationAssertion](#ztd-eval-v1-EvaluationAssertion) | repeated |  |
| verdicts | [EvaluationVerdict](#ztd-eval-v1-EvaluationVerdict) | repeated |  |
| metrics | [EvaluationMetric](#ztd-eval-v1-EvaluationMetric) | repeated |  |
| evidence_refs | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) | repeated |  |
| summary_status | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| required_verdict_count | [uint32](#uint32) |  |  |
| passed_verdict_count | [uint32](#uint32) |  |  |
| summary | [string](#string) |  |  |
| assignment_results | [EvaluationAssignmentResult](#ztd-eval-v1-EvaluationAssignmentResult) | repeated |  |
| campaign_verification_report | [EvaluationVerificationReport](#ztd-eval-v1-EvaluationVerificationReport) |  |  |






<a name="ztd-eval-v1-EvaluationRun"></a>

### EvaluationRun



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| suite_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| deployment | [EvaluationDeploymentIdentity](#ztd-eval-v1-EvaluationDeploymentIdentity) |  |  |
| active_posture | [EvaluationGovernancePosture](#ztd-eval-v1-EvaluationGovernancePosture) |  |  |
| lane | [EvaluationLane](#ztd-eval-v1-EvaluationLane) |  |  |
| target_operator_id | [string](#string) |  |  |
| target_operator_session_id | [string](#string) |  |  |
| started_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| completed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| attempt_refs | [string](#string) | repeated |  |
| final_verification_report_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| campaign_binding | [ModelCampaignBinding](#ztd-eval-v1-ModelCampaignBinding) |  |  |






<a name="ztd-eval-v1-EvaluationRuntimeBoundary"></a>

### EvaluationRuntimeBoundary



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| component | [EvaluationRuntimeComponent](#ztd-eval-v1-EvaluationRuntimeComponent) |  |  |
| process_identity | [string](#string) |  |  |
| runtime_namespace | [string](#string) |  |  |
| mounted_filesystems | [string](#string) | repeated |  |
| persistent_store | [string](#string) |  |  |
| endpoint | [string](#string) |  |  |
| authenticated_identity | [string](#string) |  |  |
| execution_owner_operator_id | [string](#string) |  |  |






<a name="ztd-eval-v1-EvaluationScenarioCatalog"></a>

### EvaluationScenarioCatalog
EvaluationScenarioCatalog is the immutable 25-scenario private catalog.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| catalog_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| catalog_digest | [string](#string) |  |  |
| scenarios | [EvaluationScenarioDefinition](#ztd-eval-v1-EvaluationScenarioDefinition) | repeated |  |






<a name="ztd-eval-v1-EvaluationScenarioDefinition"></a>

### EvaluationScenarioDefinition
EvaluationScenarioDefinition describes one frozen typed scenario.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| scenario_id | [string](#string) |  |  |
| scenario_version | [string](#string) |  |  |
| category | [EvaluationScenarioCategory](#ztd-eval-v1-EvaluationScenarioCategory) |  |  |
| public_description | [string](#string) |  |  |
| grading_method | [EvaluationGradingMethod](#ztd-eval-v1-EvaluationGradingMethod) |  |  |
| allowed_tools | [string](#string) | repeated |  |
| expected_tools | [string](#string) | repeated |  |
| forbidden_tools | [string](#string) | repeated |  |
| input_fixture_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| required_concepts | [string](#string) | repeated |  |
| gold_criteria_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| public_criteria | [PublicScenarioCriterion](#ztd-eval-v1-PublicScenarioCriterion) | repeated |  |
| public_tool_score_dimensions | [PublicToolScoreDimensionRequirement](#ztd-eval-v1-PublicToolScoreDimensionRequirement) | repeated |  |
| eligible_roles | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) | repeated | eligible_roles lists the model roles that perform this task in the external client. The homogeneous scheduler assigns the scenario only to these roles; the frozen catalog digest binds the set. |
| trajectory_policy | [EvaluationTrajectoryPolicy](#ztd-eval-v1-EvaluationTrajectoryPolicy) |  |  |
| prompt_hint | [PromptHint](#ztd-eval-v1-PromptHint) |  |  |






<a name="ztd-eval-v1-EvaluationTargetState"></a>

### EvaluationTargetState



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| attempt_id | [string](#string) |  |  |
| target_resource | [string](#string) |  |  |
| observed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| present | [bool](#bool) |  |  |
| content | [bytes](#bytes) |  |  |






<a name="ztd-eval-v1-EvaluationValue"></a>

### EvaluationValue



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| boolean_value | [bool](#bool) |  |  |
| integer_value | [int64](#int64) |  |  |
| string_value | [string](#string) |  |  |
| artifact_reference | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |






<a name="ztd-eval-v1-EvaluationVerdict"></a>

### EvaluationVerdict



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| verdict_id | [string](#string) |  |  |
| assertion_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| status | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| observed_refs | [string](#string) | repeated |  |
| evidence_refs | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) | repeated |  |
| grader_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| evaluated_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| failure_reason | [string](#string) |  |  |






<a name="ztd-eval-v1-EvaluationVerificationReport"></a>

### EvaluationVerificationReport
EvaluationVerificationReport independently verifies one run or assignment.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| report_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| status | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| failure_count | [uint32](#uint32) |  |  |
| failure_reasons | [string](#string) | repeated |  |
| report_digest_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| verified_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| verifier_release_version | [string](#string) |  |  |
| verifier_contract_version | [string](#string) |  |  |
| verified_population_digest | [string](#string) |  |  |
| verified_assignment_count | [uint32](#uint32) |  |  |
| expected_assignment_count | [uint32](#uint32) |  |  |
| campaign_digest | [string](#string) |  |  |
| catalog_digest | [string](#string) |  |  |
| model_registry_digest | [string](#string) |  |  |
| provider_observation_policy | [EvaluationWitnessPolicy](#ztd-eval-v1-EvaluationWitnessPolicy) |  |  |
| model_provenance_policy | [EvaluationWitnessPolicy](#ztd-eval-v1-EvaluationWitnessPolicy) |  |  |






<a name="ztd-eval-v1-EvaluationVerifiedPopulation"></a>

### EvaluationVerifiedPopulation



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |
| campaign_digest | [string](#string) |  |  |
| catalog_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| catalog_digest | [string](#string) |  |  |
| model_registry_digest | [string](#string) |  |  |
| lane | [EvaluationLane](#ztd-eval-v1-EvaluationLane) |  |  |
| expected_assignment_count | [uint32](#uint32) |  |  |
| entries | [EvaluationVerifiedPopulationEntry](#ztd-eval-v1-EvaluationVerifiedPopulationEntry) | repeated |  |






<a name="ztd-eval-v1-EvaluationVerifiedPopulationEntry"></a>

### EvaluationVerifiedPopulationEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assignment_id | [string](#string) |  |  |
| deterministic_identity | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| scenario_version | [string](#string) |  |  |
| repetition | [uint32](#uint32) |  |  |
| lifecycle_status | [EvaluationAssignmentLifecycleStatus](#ztd-eval-v1-EvaluationAssignmentLifecycleStatus) |  |  |
| result_digest | [string](#string) |  |  |
| trace_digest | [string](#string) |  |  |
| variant_id | [string](#string) |  |  |
| designated_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| stack_id | [string](#string) |  |  |






<a name="ztd-eval-v1-GovernedActionBinding"></a>

### GovernedActionBinding



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| binding_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| transaction_id | [string](#string) |  |  |
| operator_id | [string](#string) |  |  |
| operator_session_id | [string](#string) |  |  |
| receipt_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| persistence_attestation_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| policy_decision | [string](#string) |  |  |
| effect_observation_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |






<a name="ztd-eval-v1-GraderModelCallRecord"></a>

### GraderModelCallRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| grader_call_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| judge_variant_id | [string](#string) |  |  |
| inference_record_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |






<a name="ztd-eval-v1-HandoffRecord"></a>

### HandoffRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| handoff_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| from_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| to_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| reason_code | [string](#string) |  |  |






<a name="ztd-eval-v1-HeterogeneousAssignmentTarget"></a>

### HeterogeneousAssignmentTarget



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| stack | [HeterogeneousStackDefinition](#ztd-eval-v1-HeterogeneousStackDefinition) |  |  |






<a name="ztd-eval-v1-HeterogeneousStackDefinition"></a>

### HeterogeneousStackDefinition
HeterogeneousStackDefinition binds Primary, Assistant, and Lite slots.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| stack_id | [string](#string) |  |  |
| stack_digest | [string](#string) |  |  |
| primary_slot | [RoleAssignment](#ztd-eval-v1-RoleAssignment) |  |  |
| assistant_slot | [RoleAssignment](#ztd-eval-v1-RoleAssignment) |  |  |
| lite_slot | [RoleAssignment](#ztd-eval-v1-RoleAssignment) |  |  |






<a name="ztd-eval-v1-HomogeneousAssignmentTarget"></a>

### HomogeneousAssignmentTarget



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| candidate_variant | [ModelVariant](#ztd-eval-v1-ModelVariant) |  |  |
| designated_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |






<a name="ztd-eval-v1-ModelCampaignBinding"></a>

### ModelCampaignBinding
ModelCampaignBinding pins immutable campaign identity on a run.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| campaign_id | [string](#string) |  |  |
| campaign_digest | [string](#string) |  |  |
| catalog_ref | [ztd.compliance.v1.VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| catalog_digest | [string](#string) |  |  |
| model_registry_digest | [string](#string) |  |  |
| inference_operator_session_id | [string](#string) |  |  |
| data_operator_session_id | [string](#string) |  |  |






<a name="ztd-eval-v1-ModelCapabilityObservation"></a>

### ModelCapabilityObservation
ModelCapabilityObservation records a descriptive probe result. It never
authorizes assignment exclusion.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| capability | [ModelCapabilityKind](#ztd-eval-v1-ModelCapabilityKind) |  |  |
| outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| observation_detail | [string](#string) |  |  |






<a name="ztd-eval-v1-ModelInferenceRecord"></a>

### ModelInferenceRecord
ModelInferenceRecord captures one governed scored inference call.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| inference_record_id | [string](#string) |  |  |
| provider_attempt_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| evaluation_attempt_id | [string](#string) |  |  |
| model_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| agent_persona | [string](#string) |  |  |
| call_site | [string](#string) |  |  |
| model_variant | [ModelVariant](#ztd-eval-v1-ModelVariant) |  |  |
| temperature | [float](#float) |  |  |
| top_p | [float](#float) | optional |  |
| top_k | [int32](#int32) | optional |  |
| seed | [int32](#int32) | optional |  |
| max_output_tokens | [uint32](#uint32) |  |  |
| input_hash | [string](#string) |  |  |
| output_hash | [string](#string) |  |  |
| usage_availability | [EvaluationUsageAvailability](#ztd-eval-v1-EvaluationUsageAvailability) |  |  |
| prompt_tokens | [uint32](#uint32) |  |  |
| completion_tokens | [uint32](#uint32) |  |  |
| thinking_tokens | [uint32](#uint32) | optional |  |
| cache_tokens | [uint32](#uint32) | optional |  |
| request_started_at_unix_nanos | [uint64](#uint64) |  |  |
| first_token_at_unix_nanos | [uint64](#uint64) |  |  |
| generation_duration_nanos | [uint64](#uint64) |  |  |
| total_duration_nanos | [uint64](#uint64) |  |  |
| load_duration_nanos | [uint64](#uint64) |  |  |
| load_state | [EvaluationLoadState](#ztd-eval-v1-EvaluationLoadState) |  |  |
| retry_count | [uint32](#uint32) | optional |  |
| finish_reason | [string](#string) |  |  |
| privacy_attested | [bool](#bool) |  |  |
| governed_receipt_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| result_digest | [string](#string) |  |  |
| provider_boundary_observation_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| tools_declared | [string](#string) | repeated | Tool names as sent to the provider for this call, captured at the provider boundary. tools_declared_reported distinguishes &#34;none declared&#34; from &#34;not reported&#34;. |
| tools_declared_reported | [bool](#bool) |  |  |
| classification | [EvaluationCallClassification](#ztd-eval-v1-EvaluationCallClassification) |  | Which chain the call belongs to, stated by the producer at the call site. Aggregators select scored calls by this field, never by persona or span. |






<a name="ztd-eval-v1-ModelProvenanceAttestationWindow"></a>

### ModelProvenanceAttestationWindow
ModelProvenanceAttestationWindow binds cryptographic model-weight evidence
to one governed inference provider attempt.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| provider_attempt_id | [string](#string) |  |  |
| provenance_operator_id | [string](#string) |  |  |
| served_model_tag | [string](#string) |  |  |
| expected_model_digest | [string](#string) |  |  |
| observed_model_digest | [string](#string) |  |  |
| manifest_digest | [string](#string) |  |  |
| manifest_verification_status | [ModelManifestVerificationStatus](#ztd-eval-v1-ModelManifestVerificationStatus) |  |  |
| weight_attestations | [ModelWeightAttestation](#ztd-eval-v1-ModelWeightAttestation) | repeated |  |
| attempt_started_at_unix_ms | [int64](#int64) |  |  |
| attempt_completed_at_unix_ms | [int64](#int64) |  |  |
| attested_at_unix_ms | [int64](#int64) |  |  |
| digest_match | [bool](#bool) |  |  |
| attestation_digest | [string](#string) |  |  |






<a name="ztd-eval-v1-ModelProvenanceObservationCommand"></a>

### ModelProvenanceObservationCommand
ModelProvenanceObservationCommand is the pubsub cmd-channel payload the
campaign Gateway sends to the remote Provenance Operator at the model
storage site.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| provider_attempt_id | [string](#string) |  |  |
| inference_transaction_id | [string](#string) |  |  |
| phase | [ModelProvenanceObservationPhase](#ztd-eval-v1-ModelProvenanceObservationPhase) |  |  |
| attempt_started_at_unix_ms | [int64](#int64) |  |  |
| attempt_completed_at_unix_ms | [int64](#int64) |  |  |
| attempt_status | [ModelProvenanceObservationAttemptStatus](#ztd-eval-v1-ModelProvenanceObservationAttemptStatus) |  |  |
| retry_count | [uint32](#uint32) |  |  |
| served_model_tag | [string](#string) |  |  |
| expected_model_digest | [string](#string) |  |  |
| model_registry_digest | [string](#string) |  |  |
| campaign_id | [string](#string) |  |  |






<a name="ztd-eval-v1-ModelProvenanceObservationCompleted"></a>

### ModelProvenanceObservationCompleted
ModelProvenanceObservationCompleted is the pubsub results-channel payload
published by the remote Provenance Operator after FINALIZE attestation.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| window | [ModelProvenanceAttestationWindow](#ztd-eval-v1-ModelProvenanceAttestationWindow) |  |  |






<a name="ztd-eval-v1-ModelVariant"></a>

### ModelVariant
ModelVariant is one frozen provider-backed model identity in a campaign.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| variant_id | [string](#string) |  |  |
| provider_class | [string](#string) |  |  |
| served_model_tag | [string](#string) |  |  |
| model_digest | [string](#string) |  |  |
| model_family | [string](#string) |  |  |
| parameter_count | [uint64](#uint64) |  |  |
| quantization | [string](#string) |  |  |
| context_limit | [uint32](#uint32) |  |  |
| capability_observations | [ModelCapabilityObservation](#ztd-eval-v1-ModelCapabilityObservation) | repeated |  |






<a name="ztd-eval-v1-ModelWeightAttestation"></a>

### ModelWeightAttestation
ModelWeightAttestation is one content-addressed model weight blob attested
by the storage-side Provenance Operator at the model file site.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| blob_digest | [string](#string) |  |  |
| size_bytes | [uint64](#uint64) |  |  |
| media_type | [string](#string) |  |  |






<a name="ztd-eval-v1-PolicyDecisionRecord"></a>

### PolicyDecisionRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| decision_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| tool_name | [string](#string) |  |  |
| outcome | [EvaluationPolicyDecisionOutcome](#ztd-eval-v1-EvaluationPolicyDecisionOutcome) |  |  |
| detail | [string](#string) |  |  |






<a name="ztd-eval-v1-PromptHint"></a>

### PromptHint
PromptHint records which tools a scenario prompt hints at and where each
required argument comes from, so a failure can say the opportunity was real.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| hinted_tools | [string](#string) | repeated |  |
| arguments | [PromptHintArgument](#ztd-eval-v1-PromptHintArgument) | repeated |  |






<a name="ztd-eval-v1-PromptHintArgument"></a>

### PromptHintArgument
PromptHintArgument names one required argument of a hinted tool and where
its value is derivable from. Values stay in the private input fixture.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| argument_name | [string](#string) |  |  |
| source | [EvaluationHintArgumentSource](#ztd-eval-v1-EvaluationHintArgumentSource) |  |  |
| tool_name | [string](#string) |  |  |






<a name="ztd-eval-v1-ProviderBoundaryHardwareSample"></a>

### ProviderBoundaryHardwareSample
ProviderBoundaryHardwareSample is one timestamped read-only hardware
observation from the external provider execution boundary.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| observed_at_unix_nanos | [uint64](#uint64) |  |  |
| device_pseudonym | [string](#string) |  |  |
| vram_bytes_availability | [ProviderHardwareMetricAvailability](#ztd-eval-v1-ProviderHardwareMetricAvailability) |  |  |
| vram_used_bytes | [uint64](#uint64) |  |  |
| vram_total_bytes | [uint64](#uint64) |  |  |
| gpu_utilization_availability | [ProviderHardwareMetricAvailability](#ztd-eval-v1-ProviderHardwareMetricAvailability) |  |  |
| gpu_utilization_percent | [float](#float) |  |  |
| temperature_availability | [ProviderHardwareMetricAvailability](#ztd-eval-v1-ProviderHardwareMetricAvailability) |  |  |
| temperature_celsius | [float](#float) |  |  |
| power_availability | [ProviderHardwareMetricAvailability](#ztd-eval-v1-ProviderHardwareMetricAvailability) |  |  |
| power_watts | [float](#float) |  |  |
| clock_availability | [ProviderHardwareMetricAvailability](#ztd-eval-v1-ProviderHardwareMetricAvailability) |  |  |
| clock_mhz | [uint32](#uint32) |  |  |
| host_ram_availability | [ProviderHardwareMetricAvailability](#ztd-eval-v1-ProviderHardwareMetricAvailability) |  |  |
| host_ram_used_bytes | [uint64](#uint64) |  |  |
| host_ram_total_bytes | [uint64](#uint64) |  |  |






<a name="ztd-eval-v1-ProviderBoundaryObservationCommand"></a>

### ProviderBoundaryObservationCommand
ProviderBoundaryObservationCommand is the pubsub cmd-channel payload the
campaign Gateway sends to the remote provider-boundary observer operator.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| provider_attempt_id | [string](#string) |  |  |
| inference_transaction_id | [string](#string) |  |  |
| phase | [ProviderBoundaryObservationPhase](#ztd-eval-v1-ProviderBoundaryObservationPhase) |  |  |
| attempt_started_at_unix_ms | [int64](#int64) |  |  |
| attempt_completed_at_unix_ms | [int64](#int64) |  |  |
| attempt_status | [ProviderBoundaryObservationAttemptStatus](#ztd-eval-v1-ProviderBoundaryObservationAttemptStatus) |  |  |
| retry_count | [uint32](#uint32) |  |  |






<a name="ztd-eval-v1-ProviderBoundaryObservationCompleted"></a>

### ProviderBoundaryObservationCompleted
ProviderBoundaryObservationCompleted is the pubsub results-channel payload
published by the remote observer operator after FINALIZE sampling completes.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| window | [ProviderBoundaryObservationWindow](#ztd-eval-v1-ProviderBoundaryObservationWindow) |  |  |






<a name="ztd-eval-v1-ProviderBoundaryObservationWindow"></a>

### ProviderBoundaryObservationWindow
ProviderBoundaryObservationWindow binds provider-boundary hardware samples
to one governed inference provider attempt.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| provider_attempt_id | [string](#string) |  |  |
| observer_id | [string](#string) |  |  |
| observer_clock_source | [string](#string) |  |  |
| window_started_at_unix_nanos | [uint64](#uint64) |  |  |
| window_completed_at_unix_nanos | [uint64](#uint64) |  |  |
| attempt_started_at_unix_ms | [int64](#int64) |  |  |
| attempt_completed_at_unix_ms | [int64](#int64) |  |  |
| clock_skew_nanos | [int64](#int64) |  |  |
| samples | [ProviderBoundaryHardwareSample](#ztd-eval-v1-ProviderBoundaryHardwareSample) | repeated |  |
| observation_digest | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicAssignmentActivitySummary"></a>

### PublicAssignmentActivitySummary



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| model_activity | [PublicModelActivity](#ztd-eval-v1-PublicModelActivity) |  |  |
| tool_decisions | [PublicToolDecisionActivity](#ztd-eval-v1-PublicToolDecisionActivity) |  |  |
| tool_calls | [PublicToolCallActivity](#ztd-eval-v1-PublicToolCallActivity) |  |  |
| policy_decisions | [PublicPolicyDecisionActivity](#ztd-eval-v1-PublicPolicyDecisionActivity) |  |  |
| governed_actions | [PublicGovernedActionActivity](#ztd-eval-v1-PublicGovernedActionActivity) |  |  |






<a name="ztd-eval-v1-PublicAssignmentLifecycleRecord"></a>

### PublicAssignmentLifecycleRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assignment_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| scenario_category | [EvaluationScenarioCategory](#ztd-eval-v1-EvaluationScenarioCategory) |  |  |
| lane | [EvaluationLane](#ztd-eval-v1-EvaluationLane) |  |  |
| designated_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| variant_id | [string](#string) |  |  |
| stack_id | [string](#string) |  |  |
| lifecycle_status | [EvaluationAssignmentLifecycleStatus](#ztd-eval-v1-EvaluationAssignmentLifecycleStatus) |  |  |
| repetition | [uint32](#uint32) |  |  |
| observed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| release | [string](#string) |  |  |
| release_basis | [PublicReleaseBasis](#ztd-eval-v1-PublicReleaseBasis) |  |  |
| source_revision | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicAssignmentResultProjection"></a>

### PublicAssignmentResultProjection



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assignment_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| scenario_category | [EvaluationScenarioCategory](#ztd-eval-v1-EvaluationScenarioCategory) |  |  |
| lane | [EvaluationLane](#ztd-eval-v1-EvaluationLane) |  |  |
| designated_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| variant_id | [string](#string) |  |  |
| lifecycle_status | [EvaluationAssignmentLifecycleStatus](#ztd-eval-v1-EvaluationAssignmentLifecycleStatus) |  |  |
| summary_status | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| decomposed_scores | [DecomposedScoreRecord](#ztd-eval-v1-DecomposedScoreRecord) | repeated |  |
| result_digest | [string](#string) |  |  |
| verification_status | [string](#string) |  |  |
| unavailable_metric_reasons | [string](#string) | repeated |  |
| completed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| scenario_summary | [PublicScenarioSummary](#ztd-eval-v1-PublicScenarioSummary) |  |  |
| semantic_grade_summaries | [PublicSemanticGradeSummary](#ztd-eval-v1-PublicSemanticGradeSummary) | repeated |  |
| activity_summary | [PublicAssignmentActivitySummary](#ztd-eval-v1-PublicAssignmentActivitySummary) |  |  |
| evidence_bindings | [PublicEvidenceBinding](#ztd-eval-v1-PublicEvidenceBinding) | repeated |  |
| verification_metadata | [PublicVerificationMetadata](#ztd-eval-v1-PublicVerificationMetadata) |  |  |
| trajectory_outcome | [EvaluationTrajectoryOutcome](#ztd-eval-v1-EvaluationTrajectoryOutcome) |  |  |
| guided_retry_count | [uint32](#uint32) |  |  |
| failure_reason | [string](#string) |  |  |
| tools_declared | [string](#string) | repeated |  |
| release | [string](#string) |  |  |
| release_basis | [PublicReleaseBasis](#ztd-eval-v1-PublicReleaseBasis) |  |  |
| source_revision | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicCampaignIdentity"></a>

### PublicCampaignIdentity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| campaign_id | [string](#string) |  |  |
| campaign_digest | [string](#string) |  |  |
| catalog_id | [string](#string) |  |  |
| catalog_version | [string](#string) |  |  |
| catalog_digest | [string](#string) |  |  |
| model_registry_digest | [string](#string) |  |  |
| lane | [EvaluationLane](#ztd-eval-v1-EvaluationLane) |  |  |
| release | [string](#string) |  |  |
| release_basis | [PublicReleaseBasis](#ztd-eval-v1-PublicReleaseBasis) |  |  |
| source_revision | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicEvidenceBinding"></a>

### PublicEvidenceBinding



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| sha256 | [string](#string) |  |  |
| schema_ref | [string](#string) |  |  |
| kind | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicGovernedActionActivity"></a>

### PublicGovernedActionActivity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| availability | [PublicActivityAvailability](#ztd-eval-v1-PublicActivityAvailability) |  |  |
| unavailable_reason | [PublicUnavailableReason](#ztd-eval-v1-PublicUnavailableReason) |  |  |
| records | [PublicGovernedActionActivityRecord](#ztd-eval-v1-PublicGovernedActionActivityRecord) | repeated |  |






<a name="ztd-eval-v1-PublicGovernedActionActivityRecord"></a>

### PublicGovernedActionActivityRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| action_label | [string](#string) |  |  |
| reported_policy_outcome | [EvaluationPolicyDecisionOutcome](#ztd-eval-v1-EvaluationPolicyDecisionOutcome) |  |  |
| receipt_status | [PublicReceiptStatus](#ztd-eval-v1-PublicReceiptStatus) |  |  |
| evidence_source | [PublicEvidenceSource](#ztd-eval-v1-PublicEvidenceSource) |  |  |






<a name="ztd-eval-v1-PublicLiveEvent"></a>

### PublicLiveEvent
One disclosure-safe Explorer live event. kind, quality_state,
lifecycle_status, and role carry the Explorer view contract&#39;s closed string
vocabularies, which publicdisclosure.ValidatePublicFeedRecord enforces.
completed and total are presence-tracked: zero progress is a value, and a
producer that leaves either unset is rejected at validation.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| schema_version | [string](#string) |  |  |
| kind | [string](#string) |  |  |
| dataset_id | [string](#string) |  |  |
| quality_state | [string](#string) |  |  |
| observed_at | [string](#string) |  |  |
| source_revision_label | [string](#string) |  |  |
| release | [string](#string) |  |  |
| release_basis | [PublicReleaseBasis](#ztd-eval-v1-PublicReleaseBasis) |  |  |
| source_revision | [string](#string) |  |  |
| event_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| variant_id | [string](#string) |  |  |
| role | [string](#string) |  |  |
| lifecycle_status | [string](#string) |  |  |
| completed | [uint32](#uint32) | optional |  |
| total | [uint32](#uint32) | optional |  |
| stage_label | [string](#string) |  |  |
| task_id | [string](#string) |  |  |
| metric_delta | [PublicLiveEvent.MetricDeltaEntry](#ztd-eval-v1-PublicLiveEvent-MetricDeltaEntry) | repeated |  |






<a name="ztd-eval-v1-PublicLiveEvent-MetricDeltaEntry"></a>

### PublicLiveEvent.MetricDeltaEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| key | [string](#string) |  |  |
| value | [PublicLiveMetricValue](#ztd-eval-v1-PublicLiveMetricValue) |  |  |






<a name="ztd-eval-v1-PublicLiveMetricValue"></a>

### PublicLiveMetricValue
One scalar in a live event&#39;s metric_delta. value is presence-tracked so a
measured zero (a failed assignment&#39;s binary pass) is emitted, never dropped;
exactly one of value and unavailable_reason is set.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| value | [double](#double) | optional |  |
| unavailable_reason | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicModelActivity"></a>

### PublicModelActivity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| availability | [PublicActivityAvailability](#ztd-eval-v1-PublicActivityAvailability) |  |  |
| unavailable_reason | [PublicUnavailableReason](#ztd-eval-v1-PublicUnavailableReason) |  |  |
| records | [PublicModelActivityRecord](#ztd-eval-v1-PublicModelActivityRecord) | repeated |  |






<a name="ztd-eval-v1-PublicModelActivityRecord"></a>

### PublicModelActivityRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| model_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| agent_persona | [string](#string) |  |  |
| variant_id | [string](#string) |  |  |
| usage_availability | [EvaluationUsageAvailability](#ztd-eval-v1-EvaluationUsageAvailability) |  |  |
| input_tokens | [uint64](#uint64) |  |  |
| output_tokens | [uint64](#uint64) |  |  |
| thinking_tokens | [uint64](#uint64) | optional |  |
| cache_tokens | [uint64](#uint64) | optional |  |
| total_duration_nanos | [uint64](#uint64) |  |  |
| generation_duration_nanos | [uint64](#uint64) |  |  |
| retry_count | [uint32](#uint32) | optional |  |
| finish_state | [PublicFinishState](#ztd-eval-v1-PublicFinishState) |  |  |
| load_state | [EvaluationLoadState](#ztd-eval-v1-EvaluationLoadState) |  |  |






<a name="ztd-eval-v1-PublicModelCallSummary"></a>

### PublicModelCallSummary



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assignment_id | [string](#string) |  |  |
| inference_record_id | [string](#string) |  |  |
| model_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| agent_persona | [string](#string) |  |  |
| variant_id | [string](#string) |  |  |
| usage_availability | [EvaluationUsageAvailability](#ztd-eval-v1-EvaluationUsageAvailability) |  |  |
| prompt_tokens | [uint32](#uint32) |  |  |
| completion_tokens | [uint32](#uint32) |  |  |
| first_token_at_unix_nanos | [uint64](#uint64) |  |  |
| generation_duration_nanos | [uint64](#uint64) |  |  |
| load_state | [EvaluationLoadState](#ztd-eval-v1-EvaluationLoadState) |  |  |
| finish_reason | [string](#string) |  |  |
| input_hash | [string](#string) |  |  |
| output_hash | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicModelVariantIdentity"></a>

### PublicModelVariantIdentity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| variant_id | [string](#string) |  |  |
| served_model_tag | [string](#string) |  |  |
| model_digest | [string](#string) |  |  |
| model_family | [string](#string) |  |  |
| quantization | [string](#string) |  |  |






<a name="ztd-eval-v1-PublicPolicyDecisionActivity"></a>

### PublicPolicyDecisionActivity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| availability | [PublicActivityAvailability](#ztd-eval-v1-PublicActivityAvailability) |  |  |
| unavailable_reason | [PublicUnavailableReason](#ztd-eval-v1-PublicUnavailableReason) |  |  |
| records | [PublicPolicyDecisionActivityRecord](#ztd-eval-v1-PublicPolicyDecisionActivityRecord) | repeated |  |






<a name="ztd-eval-v1-PublicPolicyDecisionActivityRecord"></a>

### PublicPolicyDecisionActivityRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| tool_label | [string](#string) |  |  |
| outcome | [EvaluationPolicyDecisionOutcome](#ztd-eval-v1-EvaluationPolicyDecisionOutcome) |  |  |
| evidence_source | [PublicEvidenceSource](#ztd-eval-v1-PublicEvidenceSource) |  |  |






<a name="ztd-eval-v1-PublicScenarioCriterion"></a>

### PublicScenarioCriterion



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| criterion_id | [string](#string) |  |  |
| public_label | [string](#string) |  |  |
| public_description | [string](#string) |  |  |
| grading_method | [EvaluationGradingMethod](#ztd-eval-v1-EvaluationGradingMethod) |  |  |
| required | [bool](#bool) |  |  |






<a name="ztd-eval-v1-PublicScenarioSummary"></a>

### PublicScenarioSummary



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| scenario_id | [string](#string) |  |  |
| scenario_version | [string](#string) |  |  |
| category | [EvaluationScenarioCategory](#ztd-eval-v1-EvaluationScenarioCategory) |  |  |
| public_description | [string](#string) |  |  |
| grading_method | [EvaluationGradingMethod](#ztd-eval-v1-EvaluationGradingMethod) |  |  |
| allowed_tools | [string](#string) | repeated |  |
| expected_tools | [string](#string) | repeated |  |
| forbidden_tools | [string](#string) | repeated |  |
| criteria | [PublicScenarioCriterion](#ztd-eval-v1-PublicScenarioCriterion) | repeated |  |
| tool_score_dimensions | [PublicToolScoreDimensionRequirement](#ztd-eval-v1-PublicToolScoreDimensionRequirement) | repeated |  |
| trajectory_policy | [EvaluationTrajectoryPolicy](#ztd-eval-v1-EvaluationTrajectoryPolicy) |  |  |
| prompt_hint | [PromptHint](#ztd-eval-v1-PromptHint) |  |  |






<a name="ztd-eval-v1-PublicSemanticGradeSummary"></a>

### PublicSemanticGradeSummary



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| criterion_id | [string](#string) |  |  |
| status | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| grading_method | [EvaluationGradingMethod](#ztd-eval-v1-EvaluationGradingMethod) |  |  |
| judge_variant_id | [string](#string) |  |  |
| explanation_code | [PublicGradeExplanationCode](#ztd-eval-v1-PublicGradeExplanationCode) |  |  |






<a name="ztd-eval-v1-PublicToolCallActivity"></a>

### PublicToolCallActivity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| availability | [PublicActivityAvailability](#ztd-eval-v1-PublicActivityAvailability) |  |  |
| unavailable_reason | [PublicUnavailableReason](#ztd-eval-v1-PublicUnavailableReason) |  |  |
| records | [PublicToolCallActivityRecord](#ztd-eval-v1-PublicToolCallActivityRecord) | repeated |  |






<a name="ztd-eval-v1-PublicToolCallActivityRecord"></a>

### PublicToolCallActivityRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| tool_label | [string](#string) |  |  |
| execution_outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| semantic_outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| evidence_source | [PublicEvidenceSource](#ztd-eval-v1-PublicEvidenceSource) |  |  |
| loop_turn | [uint32](#uint32) |  |  |
| error_type | [string](#string) |  |  |
| guidance_shown | [bool](#bool) |  |  |






<a name="ztd-eval-v1-PublicToolDecisionActivity"></a>

### PublicToolDecisionActivity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| availability | [PublicActivityAvailability](#ztd-eval-v1-PublicActivityAvailability) |  |  |
| unavailable_reason | [PublicUnavailableReason](#ztd-eval-v1-PublicUnavailableReason) |  |  |
| records | [PublicToolDecisionActivityRecord](#ztd-eval-v1-PublicToolDecisionActivityRecord) | repeated |  |






<a name="ztd-eval-v1-PublicToolDecisionActivityRecord"></a>

### PublicToolDecisionActivityRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| tool_label | [string](#string) |  |  |
| recognized | [bool](#bool) |  |  |
| selected | [bool](#bool) |  |  |
| permission_compliant | [bool](#bool) |  |  |
| unnecessary | [bool](#bool) |  |  |
| outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| evidence_source | [PublicEvidenceSource](#ztd-eval-v1-PublicEvidenceSource) |  |  |






<a name="ztd-eval-v1-PublicToolScoreDimensionRequirement"></a>

### PublicToolScoreDimensionRequirement



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| dimension | [PublicToolScoreDimension](#ztd-eval-v1-PublicToolScoreDimension) |  |  |
| required | [bool](#bool) |  |  |






<a name="ztd-eval-v1-PublicVerificationMetadata"></a>

### PublicVerificationMetadata



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| provenance | [PublicVerificationProvenance](#ztd-eval-v1-PublicVerificationProvenance) |  |  |
| verifier_state | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| verifier_release_version | [string](#string) |  |  |
| verifier_contract_version | [string](#string) |  |  |
| report_digest | [string](#string) |  |  |
| population_digest | [string](#string) |  |  |






<a name="ztd-eval-v1-RecoveryRecord"></a>

### RecoveryRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| recovery_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| recovery_kind | [string](#string) |  |  |
| outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| detail | [string](#string) |  |  |






<a name="ztd-eval-v1-RoleAssignment"></a>

### RoleAssignment
RoleAssignment binds one designated responsibility to one frozen variant.


| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| designated_role | [ModelCampaignRole](#ztd-eval-v1-ModelCampaignRole) |  |  |
| variant_id | [string](#string) |  |  |






<a name="ztd-eval-v1-SemanticGrade"></a>

### SemanticGrade



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| grade_id | [string](#string) |  |  |
| criterion_id | [string](#string) |  |  |
| status | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| judge_variant_id | [string](#string) |  |  |
| grader_call_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| detail | [string](#string) |  |  |






<a name="ztd-eval-v1-ToolCallRecord"></a>

### ToolCallRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| call_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| tool_name | [string](#string) |  |  |
| arguments_hash | [string](#string) |  |  |
| schema_outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| semantic_outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |
| governed_binding_ref | [ztd.compliance.v1.ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) |  |  |
| loop_turn | [uint32](#uint32) |  | Tool-loop turn whose model response issued the call (1-based; 0 = not reported). The model-visible guidance text stays in the private trace. |
| error_type | [string](#string) |  |  |
| guidance_shown | [bool](#bool) |  |  |






<a name="ztd-eval-v1-ToolDecisionRecord"></a>

### ToolDecisionRecord



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| decision_id | [string](#string) |  |  |
| assignment_id | [string](#string) |  |  |
| tool_name | [string](#string) |  |  |
| recognized | [bool](#bool) |  |  |
| selected | [bool](#bool) |  |  |
| permission_compliant | [bool](#bool) |  |  |
| unnecessary | [bool](#bool) |  |  |
| outcome | [EvaluationVerdictStatus](#ztd-eval-v1-EvaluationVerdictStatus) |  |  |





 


<a name="ztd-eval-v1-EvaluationAssignmentLifecycleStatus"></a>

### EvaluationAssignmentLifecycleStatus


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_UNSPECIFIED | 0 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED | 1 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING | 2 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED | 3 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED | 4 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PARTIAL | 5 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_ESCALATED | 6 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED | 7 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED | 8 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED | 9 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_POLICY_REJECTED | 10 |  |
| EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_UNAVAILABLE | 11 |  |



<a name="ztd-eval-v1-EvaluationAttemptStatus"></a>

### EvaluationAttemptStatus


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_ATTEMPT_STATUS_UNSPECIFIED | 0 |  |
| EVALUATION_ATTEMPT_STATUS_COMPLETED | 1 |  |
| EVALUATION_ATTEMPT_STATUS_REJECTED | 2 |  |
| EVALUATION_ATTEMPT_STATUS_FAILED | 3 |  |
| EVALUATION_ATTEMPT_STATUS_UNAVAILABLE | 4 |  |
| EVALUATION_ATTEMPT_STATUS_UNSUPPORTED | 5 |  |
| EVALUATION_ATTEMPT_STATUS_INVALID_EVIDENCE | 6 |  |



<a name="ztd-eval-v1-EvaluationCallClassification"></a>

### EvaluationCallClassification
EvaluationCallClassification names the chain a model call belongs to.
SCORED_CHAIN calls answer the scored turn and are the only calls summed into
scored aggregates. POST_TURN calls (for example the memory write) run after
the turn. GRADER calls judge the turn and are recorded in grader_calls, never
in model_inferences.

| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_CALL_CLASSIFICATION_UNSPECIFIED | 0 |  |
| EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN | 1 |  |
| EVALUATION_CALL_CLASSIFICATION_POST_TURN | 2 |  |
| EVALUATION_CALL_CLASSIFICATION_GRADER | 3 |  |



<a name="ztd-eval-v1-EvaluationComparator"></a>

### EvaluationComparator


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_COMPARATOR_UNSPECIFIED | 0 |  |
| EVALUATION_COMPARATOR_EQUAL | 1 |  |
| EVALUATION_COMPARATOR_NOT_EQUAL | 2 |  |
| EVALUATION_COMPARATOR_GREATER_THAN | 3 |  |
| EVALUATION_COMPARATOR_GREATER_THAN_OR_EQUAL | 4 |  |
| EVALUATION_COMPARATOR_LESS_THAN | 5 |  |
| EVALUATION_COMPARATOR_LESS_THAN_OR_EQUAL | 6 |  |



<a name="ztd-eval-v1-EvaluationEvidenceAuthority"></a>

### EvaluationEvidenceAuthority


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_EVIDENCE_AUTHORITY_UNSPECIFIED | 0 |  |
| EVALUATION_EVIDENCE_AUTHORITY_INDEPENDENT_TARGET_STATE | 1 |  |
| EVALUATION_EVIDENCE_AUTHORITY_OPERATOR_AUTHORITATIVE | 2 |  |
| EVALUATION_EVIDENCE_AUTHORITY_GATEWAY_COORDINATION | 3 |  |
| EVALUATION_EVIDENCE_AUTHORITY_HARNESS_EXCHANGE | 4 |  |



<a name="ztd-eval-v1-EvaluationGovernancePosture"></a>

### EvaluationGovernancePosture


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED | 0 |  |
| EVALUATION_GOVERNANCE_POSTURE_DOCTRINE | 1 |  |
| EVALUATION_GOVERNANCE_POSTURE_CONSENSUS | 2 |  |
| EVALUATION_GOVERNANCE_POSTURE_RATIFY | 3 |  |
| EVALUATION_GOVERNANCE_POSTURE_NOTARY | 4 |  |



<a name="ztd-eval-v1-EvaluationGradingMethod"></a>

### EvaluationGradingMethod


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_GRADING_METHOD_UNSPECIFIED | 0 |  |
| EVALUATION_GRADING_METHOD_DETERMINISTIC | 1 |  |
| EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE | 2 |  |



<a name="ztd-eval-v1-EvaluationHintArgumentSource"></a>

### EvaluationHintArgumentSource
EvaluationHintArgumentSource names where a hinted tool argument&#39;s value is
derivable from, so catalog lint can prove a prompt is answerable.

| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_HINT_ARGUMENT_SOURCE_UNSPECIFIED | 0 |  |
| EVALUATION_HINT_ARGUMENT_SOURCE_PROMPT | 1 | The literal value appears in the rendered prompt. |
| EVALUATION_HINT_ARGUMENT_SOURCE_SEED | 2 | The value appears in the scenario&#39;s investigation seed. |
| EVALUATION_HINT_ARGUMENT_SOURCE_WORKSPACE | 3 | The value is the attempt-scoped fixture workspace named in the prompt. |
| EVALUATION_HINT_ARGUMENT_SOURCE_OPERATOR_CONTEXT | 4 | The value comes from the bound-operator context supplied by the external client. |
| EVALUATION_HINT_ARGUMENT_SOURCE_MODEL_AUTHORED | 5 | Free-text rationale the model authors (for example a justification). |



<a name="ztd-eval-v1-EvaluationLane"></a>

### EvaluationLane


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_LANE_UNSPECIFIED | 0 |  |
| EVALUATION_LANE_PLATFORM | 1 |  |
| EVALUATION_LANE_MODEL_ROLE | 2 |  |
| EVALUATION_LANE_SYSTEM | 3 |  |



<a name="ztd-eval-v1-EvaluationLoadState"></a>

### EvaluationLoadState


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_LOAD_STATE_UNSPECIFIED | 0 |  |
| EVALUATION_LOAD_STATE_COLD | 1 |  |
| EVALUATION_LOAD_STATE_WARM | 2 |  |
| EVALUATION_LOAD_STATE_UNAVAILABLE | 3 |  |



<a name="ztd-eval-v1-EvaluationMetricDirection"></a>

### EvaluationMetricDirection


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_METRIC_DIRECTION_UNSPECIFIED | 0 |  |
| EVALUATION_METRIC_DIRECTION_HIGHER_IS_BETTER | 1 |  |
| EVALUATION_METRIC_DIRECTION_LOWER_IS_BETTER | 2 |  |
| EVALUATION_METRIC_DIRECTION_TARGET_IS_BETTER | 3 |  |



<a name="ztd-eval-v1-EvaluationMetricUnit"></a>

### EvaluationMetricUnit


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_METRIC_UNIT_UNSPECIFIED | 0 |  |
| EVALUATION_METRIC_UNIT_COUNT | 1 |  |
| EVALUATION_METRIC_UNIT_RATIO | 2 |  |



<a name="ztd-eval-v1-EvaluationMissingDataPolicy"></a>

### EvaluationMissingDataPolicy


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_MISSING_DATA_POLICY_UNSPECIFIED | 0 |  |
| EVALUATION_MISSING_DATA_POLICY_FAIL | 1 |  |
| EVALUATION_MISSING_DATA_POLICY_EXCLUDE | 2 |  |
| EVALUATION_MISSING_DATA_POLICY_UNAVAILABLE | 3 |  |



<a name="ztd-eval-v1-EvaluationObservationSource"></a>

### EvaluationObservationSource


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_OBSERVATION_SOURCE_UNSPECIFIED | 0 |  |
| EVALUATION_OBSERVATION_SOURCE_TARGET_OBSERVER | 1 |  |
| EVALUATION_OBSERVATION_SOURCE_OPERATOR_RECEIPT | 2 |  |
| EVALUATION_OBSERVATION_SOURCE_GATEWAY_ADMISSION | 3 |  |
| EVALUATION_OBSERVATION_SOURCE_HARNESS_EXCHANGE | 4 |  |



<a name="ztd-eval-v1-EvaluationPolicyDecisionOutcome"></a>

### EvaluationPolicyDecisionOutcome


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_POLICY_DECISION_OUTCOME_UNSPECIFIED | 0 |  |
| EVALUATION_POLICY_DECISION_OUTCOME_ALLOW | 1 |  |
| EVALUATION_POLICY_DECISION_OUTCOME_DENY | 2 |  |
| EVALUATION_POLICY_DECISION_OUTCOME_REFUSED | 3 |  |



<a name="ztd-eval-v1-EvaluationRuntimeComponent"></a>

### EvaluationRuntimeComponent


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_RUNTIME_COMPONENT_UNSPECIFIED | 0 |  |
| EVALUATION_RUNTIME_COMPONENT_EVALUATOR | 1 |  |
| EVALUATION_RUNTIME_COMPONENT_GATEWAY | 2 |  |
| EVALUATION_RUNTIME_COMPONENT_OPERATOR | 3 |  |
| EVALUATION_RUNTIME_COMPONENT_CONTROLLED_TARGET | 4 |  |



<a name="ztd-eval-v1-EvaluationScenarioCategory"></a>

### EvaluationScenarioCategory


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_SCENARIO_CATEGORY_UNSPECIFIED | 0 |  |
| EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE | 1 |  |
| EVALUATION_SCENARIO_CATEGORY_TOOL_SELECTION | 2 |  |
| EVALUATION_SCENARIO_CATEGORY_TOOL_ARGUMENT | 3 |  |
| EVALUATION_SCENARIO_CATEGORY_TECHNICAL_ANALYSIS | 4 |  |
| EVALUATION_SCENARIO_CATEGORY_ROUTING_DELEGATION | 5 |  |
| EVALUATION_SCENARIO_CATEGORY_VERIFICATION | 6 |  |
| EVALUATION_SCENARIO_CATEGORY_SECURITY_POLICY | 7 |  |
| EVALUATION_SCENARIO_CATEGORY_RECOVERY | 8 |  |
| EVALUATION_SCENARIO_CATEGORY_FINAL_RESPONSE | 9 |  |



<a name="ztd-eval-v1-EvaluationTrajectoryOutcome"></a>

### EvaluationTrajectoryOutcome
EvaluationTrajectoryOutcome is the closed vocabulary for what the model did
across its guided loop.

| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_TRAJECTORY_OUTCOME_UNSPECIFIED | 0 |  |
| EVALUATION_TRAJECTORY_OUTCOME_DIRECT | 1 |  |
| EVALUATION_TRAJECTORY_OUTCOME_RECOVERED | 2 |  |
| EVALUATION_TRAJECTORY_OUTCOME_YIELDED_TO_DENIAL | 3 |  |
| EVALUATION_TRAJECTORY_OUTCOME_NO_TOOL_CALL | 4 |  |
| EVALUATION_TRAJECTORY_OUTCOME_WRONG_TOOL | 5 |  |
| EVALUATION_TRAJECTORY_OUTCOME_WRONG_ARGUMENTS | 6 |  |
| EVALUATION_TRAJECTORY_OUTCOME_IGNORED_GUIDANCE | 7 |  |
| EVALUATION_TRAJECTORY_OUTCOME_ABANDONED_AFTER_ERROR | 8 |  |
| EVALUATION_TRAJECTORY_OUTCOME_CIRCUMVENTED_DENIAL | 9 |  |
| EVALUATION_TRAJECTORY_OUTCOME_LOOP_EXHAUSTED | 10 |  |
| EVALUATION_TRAJECTORY_OUTCOME_PROVIDER_REJECTED_TOOL_DECLARATION | 11 |  |



<a name="ztd-eval-v1-EvaluationTrajectoryPolicy"></a>

### EvaluationTrajectoryPolicy
EvaluationTrajectoryPolicy decides how a scenario grades the ordered tool
trajectory the model produced in its guided loop.

| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_TRAJECTORY_POLICY_UNSPECIFIED | 0 |  |
| EVALUATION_TRAJECTORY_POLICY_ANSWER | 1 | No tool is expected; graded on the final output. |
| EVALUATION_TRAJECTORY_POLICY_FIRST_CHOICE | 2 | The first tool choice is the test: the expected tool must be called and no forbidden tool may be called. |
| EVALUATION_TRAJECTORY_POLICY_GUIDED | 3 | Graded on the final outcome after a legitimate guided trajectory; a correction that follows returned guidance passes as RECOVERED. |
| EVALUATION_TRAJECTORY_POLICY_GOVERNED | 4 | No unauthorized effect plus an acceptable response to governance. |



<a name="ztd-eval-v1-EvaluationUsageAvailability"></a>

### EvaluationUsageAvailability


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_USAGE_AVAILABILITY_UNSPECIFIED | 0 |  |
| EVALUATION_USAGE_AVAILABILITY_REPORTED | 1 |  |
| EVALUATION_USAGE_AVAILABILITY_UNAVAILABLE | 2 |  |



<a name="ztd-eval-v1-EvaluationVerdictStatus"></a>

### EvaluationVerdictStatus


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_VERDICT_STATUS_UNSPECIFIED | 0 |  |
| EVALUATION_VERDICT_STATUS_PASS | 1 |  |
| EVALUATION_VERDICT_STATUS_FAIL | 2 |  |
| EVALUATION_VERDICT_STATUS_UNAVAILABLE | 3 |  |
| EVALUATION_VERDICT_STATUS_UNSUPPORTED | 4 |  |
| EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE | 5 |  |



<a name="ztd-eval-v1-EvaluationWitnessPolicy"></a>

### EvaluationWitnessPolicy


| Name | Number | Description |
| ---- | ------ | ----------- |
| EVALUATION_WITNESS_POLICY_UNSPECIFIED | 0 |  |
| EVALUATION_WITNESS_POLICY_INTERIM | 1 |  |
| EVALUATION_WITNESS_POLICY_STRICT | 2 |  |



<a name="ztd-eval-v1-GradeBasis"></a>

### GradeBasis
GradeBasis states what a deterministic grade measures, so each independent
observation counts once in the verdict and the pass rate. OBSERVATION is a
fact read from the trace or workspace and is the only counted basis. DERIVED
is a boolean function of other grades in the same result. STRUCTURAL is a
harness precondition, not a model result. UNSPECIFIED is never valid.

| Name | Number | Description |
| ---- | ------ | ----------- |
| GRADE_BASIS_UNSPECIFIED | 0 |  |
| GRADE_BASIS_OBSERVATION | 1 |  |
| GRADE_BASIS_DERIVED | 2 |  |
| GRADE_BASIS_STRUCTURAL | 3 |  |



<a name="ztd-eval-v1-ModelCampaignRole"></a>

### ModelCampaignRole


| Name | Number | Description |
| ---- | ------ | ----------- |
| MODEL_CAMPAIGN_ROLE_UNSPECIFIED | 0 |  |
| MODEL_CAMPAIGN_ROLE_PRIMARY | 1 |  |
| MODEL_CAMPAIGN_ROLE_ASSISTANT | 2 |  |
| MODEL_CAMPAIGN_ROLE_LITE | 3 |  |



<a name="ztd-eval-v1-ModelCapabilityKind"></a>

### ModelCapabilityKind


| Name | Number | Description |
| ---- | ------ | ----------- |
| MODEL_CAPABILITY_KIND_UNSPECIFIED | 0 |  |
| MODEL_CAPABILITY_KIND_COMPLETION | 1 |  |
| MODEL_CAPABILITY_KIND_TOOL_CALLING | 2 |  |
| MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT | 3 |  |
| MODEL_CAPABILITY_KIND_THINKING | 4 |  |
| MODEL_CAPABILITY_KIND_CONTEXT_LIMIT | 5 |  |



<a name="ztd-eval-v1-ModelManifestVerificationStatus"></a>

### ModelManifestVerificationStatus
ModelManifestVerificationStatus reports signed-manifest verification outcome.

| Name | Number | Description |
| ---- | ------ | ----------- |
| MODEL_MANIFEST_VERIFICATION_STATUS_UNSPECIFIED | 0 |  |
| MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED | 1 |  |
| MODEL_MANIFEST_VERIFICATION_STATUS_VERIFIED | 2 |  |
| MODEL_MANIFEST_VERIFICATION_STATUS_FAILED | 3 |  |
| MODEL_MANIFEST_VERIFICATION_STATUS_UNAVAILABLE | 4 |  |



<a name="ztd-eval-v1-ModelProvenanceObservationAttemptStatus"></a>

### ModelProvenanceObservationAttemptStatus
ModelProvenanceObservationAttemptStatus mirrors the terminal provider-attempt
disposition carried in FINALIZE commands without importing operator.proto.

| Name | Number | Description |
| ---- | ------ | ----------- |
| MODEL_PROVENANCE_OBSERVATION_ATTEMPT_STATUS_UNSPECIFIED | 0 |  |
| MODEL_PROVENANCE_OBSERVATION_ATTEMPT_STATUS_IN_PROGRESS | 1 |  |
| MODEL_PROVENANCE_OBSERVATION_ATTEMPT_STATUS_COMPLETED | 2 |  |
| MODEL_PROVENANCE_OBSERVATION_ATTEMPT_STATUS_FAILED | 3 |  |



<a name="ztd-eval-v1-ModelProvenanceObservationPhase"></a>

### ModelProvenanceObservationPhase
ModelProvenanceObservationPhase identifies one lifecycle transition for a
governed provider attempt on the remote Provenance Operator.

| Name | Number | Description |
| ---- | ------ | ----------- |
| MODEL_PROVENANCE_OBSERVATION_PHASE_UNSPECIFIED | 0 |  |
| MODEL_PROVENANCE_OBSERVATION_PHASE_BEGIN | 1 |  |
| MODEL_PROVENANCE_OBSERVATION_PHASE_FINALIZE | 2 |  |



<a name="ztd-eval-v1-ProviderBoundaryObservationAttemptStatus"></a>

### ProviderBoundaryObservationAttemptStatus
ProviderBoundaryObservationAttemptStatus mirrors the terminal provider-attempt
disposition carried in FINALIZE commands without importing operator.proto.

| Name | Number | Description |
| ---- | ------ | ----------- |
| PROVIDER_BOUNDARY_OBSERVATION_ATTEMPT_STATUS_UNSPECIFIED | 0 |  |
| PROVIDER_BOUNDARY_OBSERVATION_ATTEMPT_STATUS_IN_PROGRESS | 1 |  |
| PROVIDER_BOUNDARY_OBSERVATION_ATTEMPT_STATUS_COMPLETED | 2 |  |
| PROVIDER_BOUNDARY_OBSERVATION_ATTEMPT_STATUS_FAILED | 3 |  |



<a name="ztd-eval-v1-ProviderBoundaryObservationPhase"></a>

### ProviderBoundaryObservationPhase
ProviderBoundaryObservationPhase identifies one lifecycle transition for a
governed provider attempt on the remote observer operator.

| Name | Number | Description |
| ---- | ------ | ----------- |
| PROVIDER_BOUNDARY_OBSERVATION_PHASE_UNSPECIFIED | 0 |  |
| PROVIDER_BOUNDARY_OBSERVATION_PHASE_BEGIN | 1 |  |
| PROVIDER_BOUNDARY_OBSERVATION_PHASE_FINALIZE | 2 |  |



<a name="ztd-eval-v1-ProviderHardwareMetricAvailability"></a>

### ProviderHardwareMetricAvailability


| Name | Number | Description |
| ---- | ------ | ----------- |
| PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNSPECIFIED | 0 |  |
| PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED | 1 |  |
| PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE | 2 |  |



<a name="ztd-eval-v1-PublicActivityAvailability"></a>

### PublicActivityAvailability


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_ACTIVITY_AVAILABILITY_UNSPECIFIED | 0 |  |
| PUBLIC_ACTIVITY_AVAILABILITY_OBSERVED | 1 |  |
| PUBLIC_ACTIVITY_AVAILABILITY_UNAVAILABLE | 2 |  |
| PUBLIC_ACTIVITY_AVAILABILITY_NOT_APPLICABLE | 3 |  |



<a name="ztd-eval-v1-PublicEvidenceSource"></a>

### PublicEvidenceSource


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_EVIDENCE_SOURCE_UNSPECIFIED | 0 |  |
| PUBLIC_EVIDENCE_SOURCE_APPLICATION_REPORTED | 1 |  |
| PUBLIC_EVIDENCE_SOURCE_BOUND_PUBLIC_PROOF | 2 |  |



<a name="ztd-eval-v1-PublicFinishState"></a>

### PublicFinishState


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_FINISH_STATE_UNSPECIFIED | 0 |  |
| PUBLIC_FINISH_STATE_STOP | 1 |  |
| PUBLIC_FINISH_STATE_LENGTH | 2 |  |
| PUBLIC_FINISH_STATE_TOOL_CALL | 3 |  |
| PUBLIC_FINISH_STATE_ERROR | 4 |  |
| PUBLIC_FINISH_STATE_UNAVAILABLE | 5 |  |



<a name="ztd-eval-v1-PublicGradeExplanationCode"></a>

### PublicGradeExplanationCode


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_GRADE_EXPLANATION_CODE_UNSPECIFIED | 0 |  |
| PUBLIC_GRADE_EXPLANATION_CODE_CRITERION_PASSED | 1 |  |
| PUBLIC_GRADE_EXPLANATION_CODE_CRITERION_FAILED | 2 |  |
| PUBLIC_GRADE_EXPLANATION_CODE_EVIDENCE_UNAVAILABLE | 3 |  |
| PUBLIC_GRADE_EXPLANATION_CODE_UNSUPPORTED | 4 |  |
| PUBLIC_GRADE_EXPLANATION_CODE_INVALID_EVIDENCE | 5 |  |
| PUBLIC_GRADE_EXPLANATION_CODE_GRADER_UNAVAILABLE | 6 |  |



<a name="ztd-eval-v1-PublicReceiptStatus"></a>

### PublicReceiptStatus


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_RECEIPT_STATUS_UNSPECIFIED | 0 |  |
| PUBLIC_RECEIPT_STATUS_UNAVAILABLE | 1 |  |
| PUBLIC_RECEIPT_STATUS_REPORTED | 2 |  |



<a name="ztd-eval-v1-PublicReleaseBasis"></a>

### PublicReleaseBasis
The release measured by a campaign is independent of verifier provenance.

| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_RELEASE_BASIS_UNSPECIFIED | 0 |  |
| PUBLIC_RELEASE_BASIS_RECORDED | 1 | Bound into campaign_digest at creation. |
| PUBLIC_RELEASE_BASIS_ASSERTED | 2 | Operator tag outside campaign_digest. |
| PUBLIC_RELEASE_BASIS_UNKNOWN | 3 | Historical campaign without a release tag. |



<a name="ztd-eval-v1-PublicToolScoreDimension"></a>

### PublicToolScoreDimension


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_TOOL_SCORE_DIMENSION_UNSPECIFIED | 0 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_TOOL_RECOGNITION | 1 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_TOOL_SELECTION | 2 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_ARGUMENT_SCHEMA | 3 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_ARGUMENT_SEMANTICS | 4 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_PERMISSION_COMPLIANCE | 5 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_RESULT_INTERPRETATION | 6 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_FOLLOW_UP_DECISION | 7 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_UNNECESSARY_TOOL_CALLS | 8 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_LOOPING | 9 |  |
| PUBLIC_TOOL_SCORE_DIMENSION_RECOVERY | 10 |  |



<a name="ztd-eval-v1-PublicUnavailableReason"></a>

### PublicUnavailableReason


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_UNAVAILABLE_REASON_UNSPECIFIED | 0 |  |
| PUBLIC_UNAVAILABLE_REASON_HISTORICAL_NOT_CAPTURED | 1 |  |
| PUBLIC_UNAVAILABLE_REASON_SOURCE_NOT_CAPTURED | 2 |  |
| PUBLIC_UNAVAILABLE_REASON_SOURCE_UNAVAILABLE | 3 |  |
| PUBLIC_UNAVAILABLE_REASON_SCENARIO_NOT_APPLICABLE | 4 |  |
| PUBLIC_UNAVAILABLE_REASON_INCOMPLETE_CONTRIBUTOR_EVIDENCE | 5 |  |
| PUBLIC_UNAVAILABLE_REASON_NO_SCORED_CALLS | 6 |  |



<a name="ztd-eval-v1-PublicVerificationProvenance"></a>

### PublicVerificationProvenance


| Name | Number | Description |
| ---- | ------ | ----------- |
| PUBLIC_VERIFICATION_PROVENANCE_UNSPECIFIED | 0 |  |
| PUBLIC_VERIFICATION_PROVENANCE_BOUND | 1 |  |
| PUBLIC_VERIFICATION_PROVENANCE_LEGACY_UNBOUND | 2 |  |


 

 

 



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

