# Protocol Documentation
<a name="top"></a>

## Table of Contents

- [ztd/compliance/v1/compliance.proto](#ztd_compliance_v1_compliance-proto)
    - [AssessmentApplicabilitySelection](#ztd-compliance-v1-AssessmentApplicabilitySelection)
    - [AssessmentCoverage](#ztd-compliance-v1-AssessmentCoverage)
    - [AssessmentDiagnostic](#ztd-compliance-v1-AssessmentDiagnostic)
    - [AssessmentPopulationSelection](#ztd-compliance-v1-AssessmentPopulationSelection)
    - [AssessmentScope](#ztd-compliance-v1-AssessmentScope)
    - [AssessmentSourceAdmission](#ztd-compliance-v1-AssessmentSourceAdmission)
    - [AssessmentSubjectSelection](#ztd-compliance-v1-AssessmentSubjectSelection)
    - [BundleArtifact](#ztd-compliance-v1-BundleArtifact)
    - [ChecksumEntry](#ztd-compliance-v1-ChecksumEntry)
    - [ComplianceAnalysis](#ztd-compliance-v1-ComplianceAnalysis)
    - [ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference)
    - [ComplianceEvidenceTrustPolicy](#ztd-compliance-v1-ComplianceEvidenceTrustPolicy)
    - [ComplianceEvidenceTrustedKey](#ztd-compliance-v1-ComplianceEvidenceTrustedKey)
    - [ComplianceFinding](#ztd-compliance-v1-ComplianceFinding)
    - [ComplianceGap](#ztd-compliance-v1-ComplianceGap)
    - [ComplianceRemediation](#ztd-compliance-v1-ComplianceRemediation)
    - [ComplianceReportBundle](#ztd-compliance-v1-ComplianceReportBundle)
    - [ComplianceReportManifest](#ztd-compliance-v1-ComplianceReportManifest)
    - [ComplianceReportSigningKeyMetadata](#ztd-compliance-v1-ComplianceReportSigningKeyMetadata)
    - [ComplianceReportTrustPolicy](#ztd-compliance-v1-ComplianceReportTrustPolicy)
    - [ComplianceReportTrustedKey](#ztd-compliance-v1-ComplianceReportTrustedKey)
    - [ComplianceVerificationReport](#ztd-compliance-v1-ComplianceVerificationReport)
    - [ComponentInventoryEntry](#ztd-compliance-v1-ComponentInventoryEntry)
    - [ControlAssertionAssessment](#ztd-compliance-v1-ControlAssertionAssessment)
    - [ControlAssertionCatalog](#ztd-compliance-v1-ControlAssertionCatalog)
    - [ControlAssertionDefinition](#ztd-compliance-v1-ControlAssertionDefinition)
    - [ControlCrosswalk](#ztd-compliance-v1-ControlCrosswalk)
    - [ControlCrosswalkCatalog](#ztd-compliance-v1-ControlCrosswalkCatalog)
    - [ControlSection](#ztd-compliance-v1-ControlSection)
    - [EvidenceEncryptionMetadata](#ztd-compliance-v1-EvidenceEncryptionMetadata)
    - [EvidenceLink](#ztd-compliance-v1-EvidenceLink)
    - [EvidenceWindowCompleteness](#ztd-compliance-v1-EvidenceWindowCompleteness)
    - [FrameworkCatalog](#ztd-compliance-v1-FrameworkCatalog)
    - [FrameworkControlAssessment](#ztd-compliance-v1-FrameworkControlAssessment)
    - [FrameworkControlDefinition](#ztd-compliance-v1-FrameworkControlDefinition)
    - [FrameworkControlReference](#ztd-compliance-v1-FrameworkControlReference)
    - [FrameworkDefinition](#ztd-compliance-v1-FrameworkDefinition)
    - [FrameworkProfile](#ztd-compliance-v1-FrameworkProfile)
    - [NamedDigest](#ztd-compliance-v1-NamedDigest)
    - [OSCALValidationFailure](#ztd-compliance-v1-OSCALValidationFailure)
    - [OSCALValidationResult](#ztd-compliance-v1-OSCALValidationResult)
    - [OSCALValidatorIdentity](#ztd-compliance-v1-OSCALValidatorIdentity)
    - [RenderedFormatEntry](#ztd-compliance-v1-RenderedFormatEntry)
    - [ReportSignature](#ztd-compliance-v1-ReportSignature)
    - [UnavailableAssessmentContext](#ztd-compliance-v1-UnavailableAssessmentContext)
    - [VerificationCheckResult](#ztd-compliance-v1-VerificationCheckResult)
    - [VerificationFailure](#ztd-compliance-v1-VerificationFailure)
    - [VersionedReference](#ztd-compliance-v1-VersionedReference)
  
    - [AssessmentContextKind](#ztd-compliance-v1-AssessmentContextKind)
    - [AssessmentWitnessPolicy](#ztd-compliance-v1-AssessmentWitnessPolicy)
    - [VerificationCheckStatus](#ztd-compliance-v1-VerificationCheckStatus)
  
- [Scalar Value Types](#scalar-value-types)



<a name="ztd_compliance_v1_compliance-proto"></a>
<p align="right"><a href="#top">Top</a></p>

## ztd/compliance/v1/compliance.proto



<a name="ztd-compliance-v1-AssessmentApplicabilitySelection"></a>

### AssessmentApplicabilitySelection



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| components | [string](#string) | repeated |  |
| action_classes | [string](#string) | repeated |  |
| arms | [string](#string) | repeated |  |






<a name="ztd-compliance-v1-AssessmentCoverage"></a>

### AssessmentCoverage



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| selected_subject_count | [int32](#int32) |  |  |
| assessed_subject_count | [int32](#int32) |  |  |
| failed_subject_count | [int32](#int32) |  |  |
| unavailable_subject_count | [int32](#int32) |  |  |
| unavailable_subjects | [AssessmentSubjectSelection](#ztd-compliance-v1-AssessmentSubjectSelection) | repeated |  |






<a name="ztd-compliance-v1-AssessmentDiagnostic"></a>

### AssessmentDiagnostic



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| code | [string](#string) |  |  |
| severity | [string](#string) |  |  |
| source_admission_id | [string](#string) |  |  |
| subject | [AssessmentSubjectSelection](#ztd-compliance-v1-AssessmentSubjectSelection) |  |  |
| message | [string](#string) |  |  |






<a name="ztd-compliance-v1-AssessmentPopulationSelection"></a>

### AssessmentPopulationSelection



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| subjects | [AssessmentSubjectSelection](#ztd-compliance-v1-AssessmentSubjectSelection) | repeated |  |






<a name="ztd-compliance-v1-AssessmentScope"></a>

### AssessmentScope



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| scope_id | [string](#string) |  |  |
| organization_id | [string](#string) |  |  |
| deployment_id | [string](#string) |  |  |
| product_version | [string](#string) |  |  |
| build_identity | [string](#string) |  |  |
| source_revision | [string](#string) |  |  |
| image_digests | [NamedDigest](#ztd-compliance-v1-NamedDigest) | repeated |  |
| component_inventory | [ComponentInventoryEntry](#ztd-compliance-v1-ComponentInventoryEntry) | repeated |  |
| network_topology_hash | [string](#string) |  |  |
| configuration_hashes | [NamedDigest](#ztd-compliance-v1-NamedDigest) | repeated |  |
| doctrine_bundle_hashes | [NamedDigest](#ztd-compliance-v1-NamedDigest) | repeated |  |
| consensus_policy_hashes | [NamedDigest](#ztd-compliance-v1-NamedDigest) | repeated |  |
| trust_anchor_ids | [string](#string) | repeated |  |
| cryptographic_mode | [string](#string) |  |  |
| assessment_window_start | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| assessment_window_end | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| excluded_components | [string](#string) | repeated |  |
| customer_responsibilities | [string](#string) | repeated |  |
| active_posture | [string](#string) |  |  |
| source_admissions | [AssessmentSourceAdmission](#ztd-compliance-v1-AssessmentSourceAdmission) | repeated |  |
| applicability | [AssessmentApplicabilitySelection](#ztd-compliance-v1-AssessmentApplicabilitySelection) |  |  |
| selected_population | [AssessmentPopulationSelection](#ztd-compliance-v1-AssessmentPopulationSelection) |  |  |
| assessment_as_of | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| unavailable_context | [UnavailableAssessmentContext](#ztd-compliance-v1-UnavailableAssessmentContext) | repeated |  |






<a name="ztd-compliance-v1-AssessmentSourceAdmission"></a>

### AssessmentSourceAdmission



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| admission_id | [string](#string) |  |  |
| source_kind | [string](#string) |  |  |
| source_version | [string](#string) |  |  |
| source_scope_id | [string](#string) |  |  |
| owner_runtime_boundary | [string](#string) |  |  |
| acquisition_boundary | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| snapshot_id | [string](#string) |  |  |
| artifact_ids | [string](#string) | repeated |  |
| verifier_ref | [VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| disclosure_classification | [string](#string) |  |  |
| provider_observation_policy | [AssessmentWitnessPolicy](#ztd-compliance-v1-AssessmentWitnessPolicy) |  |  |
| model_provenance_policy | [AssessmentWitnessPolicy](#ztd-compliance-v1-AssessmentWitnessPolicy) |  |  |






<a name="ztd-compliance-v1-AssessmentSubjectSelection"></a>

### AssessmentSubjectSelection



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| source_admission_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| attempt_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| transaction_id | [string](#string) |  |  |






<a name="ztd-compliance-v1-BundleArtifact"></a>

### BundleArtifact



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| bundle_path | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |
| media_type | [string](#string) |  |  |
| profile | [string](#string) |  |  |
| byte_length | [int64](#int64) |  |  |
| encryption | [EvidenceEncryptionMetadata](#ztd-compliance-v1-EvidenceEncryptionMetadata) |  |  |






<a name="ztd-compliance-v1-ChecksumEntry"></a>

### ChecksumEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| bundle_path | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |






<a name="ztd-compliance-v1-ComplianceAnalysis"></a>

### ComplianceAnalysis



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| analysis_id | [string](#string) |  |  |
| analysis_schema_version | [string](#string) |  |  |
| scope_ref | [string](#string) |  |  |
| generated_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| generator_identity | [string](#string) |  |  |
| generator_version | [string](#string) |  |  |
| evidence_window_completeness | [EvidenceWindowCompleteness](#ztd-compliance-v1-EvidenceWindowCompleteness) |  |  |
| assertion_assessments | [ControlAssertionAssessment](#ztd-compliance-v1-ControlAssertionAssessment) | repeated |  |
| framework_assessments | [FrameworkControlAssessment](#ztd-compliance-v1-FrameworkControlAssessment) | repeated |  |
| gaps | [ComplianceGap](#ztd-compliance-v1-ComplianceGap) | repeated |  |
| evidence_links | [EvidenceLink](#ztd-compliance-v1-EvidenceLink) | repeated |  |
| limitations | [string](#string) | repeated |  |
| findings | [ComplianceFinding](#ztd-compliance-v1-ComplianceFinding) | repeated |  |
| remediation | [ComplianceRemediation](#ztd-compliance-v1-ComplianceRemediation) | repeated |  |
| sections | [ControlSection](#ztd-compliance-v1-ControlSection) | repeated |  |
| evidence_graph_failures | [string](#string) | repeated |  |
| evidence_graph_valid | [bool](#bool) |  |  |
| evidence_resources | [ComplianceEvidenceReference](#ztd-compliance-v1-ComplianceEvidenceReference) | repeated |  |
| assessment_scope_sha256 | [string](#string) |  |  |
| diagnostics | [AssessmentDiagnostic](#ztd-compliance-v1-AssessmentDiagnostic) | repeated |  |






<a name="ztd-compliance-v1-ComplianceEvidenceReference"></a>

### ComplianceEvidenceReference



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| artifact_id | [string](#string) |  |  |
| artifact_type | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |
| media_type | [string](#string) |  |  |
| schema_ref | [string](#string) |  |  |
| producer_identity | [string](#string) |  |  |
| produced_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| scope_id | [string](#string) |  |  |
| run_id | [string](#string) |  |  |
| attempt_id | [string](#string) |  |  |
| scenario_id | [string](#string) |  |  |
| transaction_id | [string](#string) |  |  |
| verification_status | [string](#string) |  |  |
| verifier_id | [string](#string) |  |  |
| verifier_version | [string](#string) |  |  |
| verified_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| bundle_path | [string](#string) |  |  |
| encryption | [EvidenceEncryptionMetadata](#ztd-compliance-v1-EvidenceEncryptionMetadata) |  |  |
| source_admission_id | [string](#string) |  |  |






<a name="ztd-compliance-v1-ComplianceEvidenceTrustPolicy"></a>

### ComplianceEvidenceTrustPolicy



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| policy_id | [string](#string) |  |  |
| policy_version | [string](#string) |  |  |
| trusted_keys | [ComplianceEvidenceTrustedKey](#ztd-compliance-v1-ComplianceEvidenceTrustedKey) | repeated |  |






<a name="ztd-compliance-v1-ComplianceEvidenceTrustedKey"></a>

### ComplianceEvidenceTrustedKey



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| key_id | [string](#string) |  |  |
| public_key | [string](#string) |  |  |
| public_key_sha256 | [string](#string) |  |  |
| assessment_id | [string](#string) |  |  |
| assessor_identity | [string](#string) |  |  |
| assessed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| valid_from | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| valid_until | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| allowed_scope_refs | [string](#string) | repeated |  |
| revoked_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |






<a name="ztd-compliance-v1-ComplianceFinding"></a>

### ComplianceFinding



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| finding_id | [string](#string) |  |  |
| subject_ref | [string](#string) |  |  |
| severity | [string](#string) |  |  |
| description | [string](#string) |  |  |
| related_assertion_ref | [string](#string) |  |  |
| related_control_id | [string](#string) |  |  |






<a name="ztd-compliance-v1-ComplianceGap"></a>

### ComplianceGap



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| gap_id | [string](#string) |  |  |
| assertion_ref | [string](#string) |  |  |
| framework_ref | [string](#string) |  |  |
| control_id | [string](#string) |  |  |
| gap_type | [string](#string) |  |  |
| description | [string](#string) |  |  |
| responsibility | [string](#string) |  |  |






<a name="ztd-compliance-v1-ComplianceRemediation"></a>

### ComplianceRemediation



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| remediation_id | [string](#string) |  |  |
| finding_ref | [string](#string) |  |  |
| action | [string](#string) |  |  |
| priority | [string](#string) |  |  |
| owner | [string](#string) |  |  |






<a name="ztd-compliance-v1-ComplianceReportBundle"></a>

### ComplianceReportBundle



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| manifest | [ComplianceReportManifest](#ztd-compliance-v1-ComplianceReportManifest) |  |  |
| artifacts | [BundleArtifact](#ztd-compliance-v1-BundleArtifact) | repeated |  |
| analysis | [ComplianceAnalysis](#ztd-compliance-v1-ComplianceAnalysis) |  |  |
| profiles | [FrameworkProfile](#ztd-compliance-v1-FrameworkProfile) | repeated |  |
| rendered_formats | [RenderedFormatEntry](#ztd-compliance-v1-RenderedFormatEntry) | repeated |  |
| checksum_root | [string](#string) |  |  |
| checksum_root_signature | [ReportSignature](#ztd-compliance-v1-ReportSignature) |  |  |






<a name="ztd-compliance-v1-ComplianceReportManifest"></a>

### ComplianceReportManifest



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| report_id | [string](#string) |  |  |
| report_schema_version | [string](#string) |  |  |
| generated_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| generator_identity | [string](#string) |  |  |
| generator_version | [string](#string) |  |  |
| scope_ref | [string](#string) |  |  |
| framework_refs | [VersionedReference](#ztd-compliance-v1-VersionedReference) | repeated |  |
| assertion_catalog_ref | [string](#string) |  |  |
| crosswalk_refs | [string](#string) | repeated |  |
| assessment_refs | [string](#string) | repeated |  |
| evidence_index_ref | [string](#string) |  |  |
| checksum_root | [string](#string) |  |  |
| signature | [ReportSignature](#ztd-compliance-v1-ReportSignature) |  |  |
| bundle_profile | [string](#string) |  |  |
| manifest_sha256 | [string](#string) |  |  |






<a name="ztd-compliance-v1-ComplianceReportSigningKeyMetadata"></a>

### ComplianceReportSigningKeyMetadata



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| key_id | [string](#string) |  |  |
| algorithm | [string](#string) |  |  |
| purpose | [string](#string) |  |  |
| public_key_sha256 | [string](#string) |  |  |
| created_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| expires_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |






<a name="ztd-compliance-v1-ComplianceReportTrustPolicy"></a>

### ComplianceReportTrustPolicy



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| policy_id | [string](#string) |  |  |
| policy_version | [string](#string) |  |  |
| trusted_keys | [ComplianceReportTrustedKey](#ztd-compliance-v1-ComplianceReportTrustedKey) | repeated |  |






<a name="ztd-compliance-v1-ComplianceReportTrustedKey"></a>

### ComplianceReportTrustedKey



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| metadata | [ComplianceReportSigningKeyMetadata](#ztd-compliance-v1-ComplianceReportSigningKeyMetadata) |  |  |
| public_key | [string](#string) |  |  |
| assessment_id | [string](#string) |  |  |
| assessor_identity | [string](#string) |  |  |
| assessed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| allowed_scope_refs | [string](#string) | repeated |  |
| revoked_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |






<a name="ztd-compliance-v1-ComplianceVerificationReport"></a>

### ComplianceVerificationReport



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| report_id | [string](#string) |  |  |
| valid | [bool](#bool) |  |  |
| verified_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| verifier_id | [string](#string) |  |  |
| verifier_version | [string](#string) |  |  |
| failures | [VerificationFailure](#ztd-compliance-v1-VerificationFailure) | repeated |  |
| reproduced_checksum_root | [string](#string) |  |  |
| checks | [VerificationCheckResult](#ztd-compliance-v1-VerificationCheckResult) | repeated |  |






<a name="ztd-compliance-v1-ComponentInventoryEntry"></a>

### ComponentInventoryEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| component_id | [string](#string) |  |  |
| component_type | [string](#string) |  |  |
| version | [string](#string) |  |  |
| digest | [string](#string) |  |  |






<a name="ztd-compliance-v1-ControlAssertionAssessment"></a>

### ControlAssertionAssessment



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assessment_id | [string](#string) |  |  |
| scope_id | [string](#string) |  |  |
| assertion_ref | [VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| status | [string](#string) |  |  |
| evidence_level | [string](#string) |  |  |
| evaluated_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| verifier_ref | [VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| evidence_refs | [string](#string) | repeated |  |
| metric_refs | [string](#string) | repeated |  |
| freshness_status | [string](#string) |  |  |
| failure_reason | [string](#string) |  |  |
| limitations | [string](#string) | repeated |  |
| coverage | [AssessmentCoverage](#ztd-compliance-v1-AssessmentCoverage) |  |  |
| diagnostics | [AssessmentDiagnostic](#ztd-compliance-v1-AssessmentDiagnostic) | repeated |  |






<a name="ztd-compliance-v1-ControlAssertionCatalog"></a>

### ControlAssertionCatalog



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| catalog_id | [string](#string) |  |  |
| catalog_version | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |
| assertions | [ControlAssertionDefinition](#ztd-compliance-v1-ControlAssertionDefinition) | repeated |  |






<a name="ztd-compliance-v1-ControlAssertionDefinition"></a>

### ControlAssertionDefinition



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assertion_id | [string](#string) |  |  |
| assertion_version | [string](#string) |  |  |
| title | [string](#string) |  |  |
| statement | [string](#string) |  |  |
| category | [string](#string) |  |  |
| component_scope | [string](#string) | repeated |  |
| responsibility | [string](#string) |  |  |
| applicable_action_classes | [string](#string) | repeated |  |
| applicable_arms | [string](#string) | repeated |  |
| required_evidence_types | [string](#string) | repeated |  |
| required_grader_refs | [VersionedReference](#ztd-compliance-v1-VersionedReference) | repeated |  |
| required_verifier_refs | [VersionedReference](#ztd-compliance-v1-VersionedReference) | repeated |  |
| minimum_evidence_level | [string](#string) |  |  |
| validation_cycle | [string](#string) |  |  |
| missing_evidence_policy | [string](#string) |  |  |
| passing_rule | [string](#string) |  |  |
| exclusions | [string](#string) | repeated |  |






<a name="ztd-compliance-v1-ControlCrosswalk"></a>

### ControlCrosswalk



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| crosswalk_id | [string](#string) |  |  |
| crosswalk_version | [string](#string) |  |  |
| framework_ref | [VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| control_id | [string](#string) |  |  |
| assertion_refs | [VersionedReference](#ztd-compliance-v1-VersionedReference) | repeated |  |
| mapping_type | [string](#string) |  |  |
| rationale | [string](#string) |  |  |
| applicability_conditions | [string](#string) | repeated |  |
| responsibility | [string](#string) |  |  |
| required_evidence_level | [string](#string) |  |  |
| reviewed_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| reviewer_identity | [string](#string) |  |  |






<a name="ztd-compliance-v1-ControlCrosswalkCatalog"></a>

### ControlCrosswalkCatalog



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| catalog_id | [string](#string) |  |  |
| catalog_version | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |
| mappings | [ControlCrosswalk](#ztd-compliance-v1-ControlCrosswalk) | repeated |  |






<a name="ztd-compliance-v1-ControlSection"></a>

### ControlSection



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| section_id | [string](#string) |  |  |
| title | [string](#string) |  |  |
| responsibility | [string](#string) |  |  |
| status_filter | [string](#string) |  |  |
| control_assessment_refs | [string](#string) | repeated |  |
| description | [string](#string) |  |  |
| control_refs | [FrameworkControlReference](#ztd-compliance-v1-FrameworkControlReference) | repeated |  |






<a name="ztd-compliance-v1-EvidenceEncryptionMetadata"></a>

### EvidenceEncryptionMetadata



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| algorithm | [string](#string) |  |  |
| key_id | [string](#string) |  |  |
| authorization_scope | [string](#string) |  |  |
| plaintext_sha256 | [string](#string) |  |  |
| authenticated_metadata_sha256 | [string](#string) |  |  |






<a name="ztd-compliance-v1-EvidenceLink"></a>

### EvidenceLink



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| source_ref | [string](#string) |  |  |
| target_ref | [string](#string) |  |  |
| link_type | [string](#string) |  |  |






<a name="ztd-compliance-v1-EvidenceWindowCompleteness"></a>

### EvidenceWindowCompleteness



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| scope_id | [string](#string) |  |  |
| expected_evidence_count | [int32](#int32) |  |  |
| actual_evidence_count | [int32](#int32) |  |  |
| missing_evidence_refs | [string](#string) | repeated |  |
| stale_evidence_refs | [string](#string) | repeated |  |
| completeness_status | [string](#string) |  |  |
| window_start_ref | [string](#string) |  |  |
| window_end_ref | [string](#string) |  |  |






<a name="ztd-compliance-v1-FrameworkCatalog"></a>

### FrameworkCatalog



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| catalog_id | [string](#string) |  |  |
| catalog_version | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |
| frameworks | [FrameworkDefinition](#ztd-compliance-v1-FrameworkDefinition) | repeated |  |






<a name="ztd-compliance-v1-FrameworkControlAssessment"></a>

### FrameworkControlAssessment



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| assessment_id | [string](#string) |  |  |
| scope_id | [string](#string) |  |  |
| framework_ref | [VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| control_id | [string](#string) |  |  |
| status | [string](#string) |  |  |
| responsibility | [string](#string) |  |  |
| mapping_refs | [string](#string) | repeated |  |
| assertion_assessment_refs | [string](#string) | repeated |  |
| customer_attestation_refs | [string](#string) | repeated |  |
| evidence_level | [string](#string) |  |  |
| findings | [string](#string) | repeated |  |
| limitations | [string](#string) | repeated |  |
| remediation | [string](#string) | repeated |  |






<a name="ztd-compliance-v1-FrameworkControlDefinition"></a>

### FrameworkControlDefinition



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| control_id | [string](#string) |  |  |
| title | [string](#string) |  |  |
| description | [string](#string) |  |  |
| applicability_rules | [string](#string) | repeated |  |
| responsibility | [string](#string) |  |  |
| source_reference | [string](#string) |  |  |
| support_status | [string](#string) |  |  |
| support_rationale | [string](#string) |  |  |






<a name="ztd-compliance-v1-FrameworkControlReference"></a>

### FrameworkControlReference



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| framework_ref | [VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| control_id | [string](#string) |  |  |






<a name="ztd-compliance-v1-FrameworkDefinition"></a>

### FrameworkDefinition



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| framework_id | [string](#string) |  |  |
| framework_version | [string](#string) |  |  |
| title | [string](#string) |  |  |
| publisher | [string](#string) |  |  |
| source | [string](#string) |  |  |
| catalog_sha256 | [string](#string) |  |  |
| effective_date | [string](#string) |  |  |
| controls | [FrameworkControlDefinition](#ztd-compliance-v1-FrameworkControlDefinition) | repeated |  |






<a name="ztd-compliance-v1-FrameworkProfile"></a>

### FrameworkProfile



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| profile_id | [string](#string) |  |  |
| framework_ref | [VersionedReference](#ztd-compliance-v1-VersionedReference) |  |  |
| profile_version | [string](#string) |  |  |
| generated_at | [google.protobuf.Timestamp](#google-protobuf-Timestamp) |  |  |
| analysis_ref | [string](#string) |  |  |
| control_assessments | [FrameworkControlAssessment](#ztd-compliance-v1-FrameworkControlAssessment) | repeated |  |
| limitations | [string](#string) | repeated |  |






<a name="ztd-compliance-v1-NamedDigest"></a>

### NamedDigest



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| name | [string](#string) |  |  |
| sha256 | [string](#string) |  |  |






<a name="ztd-compliance-v1-OSCALValidationFailure"></a>

### OSCALValidationFailure



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| code | [string](#string) |  |  |
| message | [string](#string) |  |  |
| instance_ptr | [string](#string) |  |  |
| schema_ptr | [string](#string) |  |  |
| keyword | [string](#string) |  |  |






<a name="ztd-compliance-v1-OSCALValidationResult"></a>

### OSCALValidationResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| validator | [OSCALValidatorIdentity](#ztd-compliance-v1-OSCALValidatorIdentity) |  |  |
| valid | [bool](#bool) |  |  |
| structural_failures | [OSCALValidationFailure](#ztd-compliance-v1-OSCALValidationFailure) | repeated |  |
| semantic_failures | [OSCALValidationFailure](#ztd-compliance-v1-OSCALValidationFailure) | repeated |  |






<a name="ztd-compliance-v1-OSCALValidatorIdentity"></a>

### OSCALValidatorIdentity



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| validator_id | [string](#string) |  |  |
| validator_version | [string](#string) |  |  |
| schema_version | [string](#string) |  |  |
| schema_digest | [string](#string) |  |  |






<a name="ztd-compliance-v1-RenderedFormatEntry"></a>

### RenderedFormatEntry



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| format | [string](#string) |  |  |
| media_type | [string](#string) |  |  |
| bundle_path | [string](#string) |  |  |






<a name="ztd-compliance-v1-ReportSignature"></a>

### ReportSignature



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| key_id | [string](#string) |  |  |
| algorithm | [string](#string) |  |  |
| signed_sha256 | [string](#string) |  |  |
| signature | [string](#string) |  |  |






<a name="ztd-compliance-v1-UnavailableAssessmentContext"></a>

### UnavailableAssessmentContext



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| kind | [AssessmentContextKind](#ztd-compliance-v1-AssessmentContextKind) |  |  |
| reason | [string](#string) |  |  |






<a name="ztd-compliance-v1-VerificationCheckResult"></a>

### VerificationCheckResult



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| check_id | [string](#string) |  |  |
| status | [VerificationCheckStatus](#ztd-compliance-v1-VerificationCheckStatus) |  |  |
| evidence_refs | [string](#string) | repeated |  |
| verifier_id | [string](#string) |  |  |
| verifier_version | [string](#string) |  |  |
| failures | [VerificationFailure](#ztd-compliance-v1-VerificationFailure) | repeated |  |






<a name="ztd-compliance-v1-VerificationFailure"></a>

### VerificationFailure



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| code | [string](#string) |  |  |
| subject_ref | [string](#string) |  |  |
| reason | [string](#string) |  |  |






<a name="ztd-compliance-v1-VersionedReference"></a>

### VersionedReference



| Field | Type | Label | Description |
| ----- | ---- | ----- | ----------- |
| id | [string](#string) |  |  |
| version | [string](#string) |  |  |





 


<a name="ztd-compliance-v1-AssessmentContextKind"></a>

### AssessmentContextKind


| Name | Number | Description |
| ---- | ------ | ----------- |
| ASSESSMENT_CONTEXT_KIND_UNSPECIFIED | 0 |  |
| ASSESSMENT_CONTEXT_KIND_BUILD_IDENTITY | 1 |  |
| ASSESSMENT_CONTEXT_KIND_SOURCE_REVISION | 2 |  |
| ASSESSMENT_CONTEXT_KIND_COMPONENT_INVENTORY | 3 |  |
| ASSESSMENT_CONTEXT_KIND_NETWORK_TOPOLOGY | 4 |  |
| ASSESSMENT_CONTEXT_KIND_CONFIGURATION | 5 |  |
| ASSESSMENT_CONTEXT_KIND_DOCTRINE_BUNDLES | 6 |  |
| ASSESSMENT_CONTEXT_KIND_TRUST_ANCHORS | 7 |  |



<a name="ztd-compliance-v1-AssessmentWitnessPolicy"></a>

### AssessmentWitnessPolicy


| Name | Number | Description |
| ---- | ------ | ----------- |
| ASSESSMENT_WITNESS_POLICY_UNSPECIFIED | 0 |  |
| ASSESSMENT_WITNESS_POLICY_INTERIM | 1 |  |
| ASSESSMENT_WITNESS_POLICY_STRICT | 2 |  |



<a name="ztd-compliance-v1-VerificationCheckStatus"></a>

### VerificationCheckStatus


| Name | Number | Description |
| ---- | ------ | ----------- |
| VERIFICATION_CHECK_STATUS_UNSPECIFIED | 0 |  |
| VERIFICATION_CHECK_STATUS_PASSED | 1 |  |
| VERIFICATION_CHECK_STATUS_FAILED | 2 |  |


 

 

 



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

