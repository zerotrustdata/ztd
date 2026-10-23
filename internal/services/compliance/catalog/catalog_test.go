// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package catalog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/compliance/catalog"
	complianceconstants "github.com/zerotrustdata/ztd/protocol/constants/compliance"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

const validSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validAssertion() *compliancev1.ControlAssertionDefinition {
	return &compliancev1.ControlAssertionDefinition{
		AssertionId:             "ZTD-GOV-BLOCK-001",
		AssertionVersion:        "1.0.0",
		Title:                   "Governed block outcome",
		Statement:               "A prohibited governed action is rejected at the expected layer.",
		Category:                "governance",
		ComponentScope:          []string{"gateway", "operator"},
		Responsibility:          "platform",
		ApplicableActionClasses: []string{"FILE_DELETE"},
		ApplicableArms:          []string{"governed"},
		RequiredEvidenceTypes:   []string{"action_receipt", "state_observation"},
		RequiredGraderRefs:      []*compliancev1.VersionedReference{{Id: "policy_outcome", Version: "1.0.0"}},
		RequiredVerifierRefs:    []*compliancev1.VersionedReference{{Id: "receipt_integrity", Version: "1.0.0"}},
		MinimumEvidenceLevel:    "L3",
		ValidationCycle:         "7d",
		MissingEvidencePolicy:   "unverifiable",
		PassingRule:             "all_required",
	}
}

func validAssertionCatalog() *compliancev1.ControlAssertionCatalog {
	return &compliancev1.ControlAssertionCatalog{
		CatalogId:      "ztd-control-assertions",
		CatalogVersion: "1.0.0",
		Sha256:         validSHA256,
		Assertions:     []*compliancev1.ControlAssertionDefinition{validAssertion()},
	}
}

func validFramework() *compliancev1.FrameworkDefinition {
	return &compliancev1.FrameworkDefinition{
		FrameworkId:      "fedramp-20x",
		FrameworkVersion: "CR26-2026-06-24",
		Title:            "FedRAMP 20x",
		Publisher:        "FedRAMP",
		Source:           "https://www.fedramp.gov/20x/",
		CatalogSha256:    validSHA256,
		EffectiveDate:    "2026-06-24",
		Controls: []*compliancev1.FrameworkControlDefinition{
			{ControlId: "KSI-MLA-07", Title: "Protecting Logs", Description: "Protect audit information.", Responsibility: "shared", SourceReference: "CR26", SupportStatus: "mapped", SupportRationale: "A reviewed crosswalk maps this control to the initial assertion catalog."},
		},
	}
}

func validCrosswalk() *compliancev1.ControlCrosswalkCatalog {
	return &compliancev1.ControlCrosswalkCatalog{
		CatalogId:      "fedramp-20x-ztd-crosswalk",
		CatalogVersion: "1.0.0",
		Sha256:         validSHA256,
		Mappings: []*compliancev1.ControlCrosswalk{
			{
				CrosswalkId:           "fedramp-20x:KSI-MLA-07:ZTD-GOV-BLOCK-001",
				CrosswalkVersion:      "1.0.0",
				FrameworkRef:          &compliancev1.VersionedReference{Id: "fedramp-20x", Version: "CR26-2026-06-24"},
				ControlId:             "KSI-MLA-07",
				AssertionRefs:         []*compliancev1.VersionedReference{{Id: "ZTD-GOV-BLOCK-001", Version: "1.0.0"}},
				MappingType:           "supporting",
				Rationale:             "A verified denied destruction attempt supports protection of audit information.",
				Responsibility:        "shared",
				RequiredEvidenceLevel: "L3",
				ReviewedAt:            timestamppb.New(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)),
				ReviewerIdentity:      "ztd-project",
			},
		},
	}
}

func TestValidateAssertionCatalogRejectsInvalidRecords(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*compliancev1.ControlAssertionCatalog)
	}{
		{name: "duplicate assertion reference", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions = append(c.Assertions, validAssertion()) }},
		{name: "malformed catalog digest", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Sha256 = "sha256:not-a-digest" }},
		{name: "invalid responsibility", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].Responsibility = "vendor" }},
		{name: "unknown evidence level", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].MinimumEvidenceLevel = "L9" }},
		{name: "unknown missing evidence policy", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].MissingEvidencePolicy = "pass" }},
		{name: "absence-driven not applicable policy", mutate: func(c *compliancev1.ControlAssertionCatalog) {
			c.Assertions[0].MissingEvidencePolicy = "not_applicable"
		}},
		{name: "missing action classes", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].ApplicableActionClasses = nil }},
		{name: "missing applicable arms", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].ApplicableArms = nil }},
		{name: "missing grader references", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].RequiredGraderRefs = nil }},
		{name: "missing verifier references", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].RequiredVerifierRefs = nil }},
		{name: "unknown evidence type", mutate: func(c *compliancev1.ControlAssertionCatalog) {
			c.Assertions[0].RequiredEvidenceTypes = []string{"artifact_presence"}
		}},
		{name: "unknown passing rule", mutate: func(c *compliancev1.ControlAssertionCatalog) { c.Assertions[0].PassingRule = "any_present" }},
		{name: "duplicate component scope", mutate: func(c *compliancev1.ControlAssertionCatalog) {
			c.Assertions[0].ComponentScope = append(c.Assertions[0].ComponentScope, c.Assertions[0].ComponentScope[0])
		}},
		{name: "duplicate evidence type", mutate: func(c *compliancev1.ControlAssertionCatalog) {
			c.Assertions[0].RequiredEvidenceTypes = append(c.Assertions[0].RequiredEvidenceTypes, c.Assertions[0].RequiredEvidenceTypes[0])
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validAssertionCatalog()
			tt.mutate(candidate)
			err := catalog.ValidateAssertionCatalog(candidate)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
		})
	}
}

func TestValidateAssertionCatalogRejectsUnknownVerifierAndGraderVersions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*compliancev1.ControlAssertionDefinition)
		want   error
	}{
		{name: "unknown verifier", mutate: func(a *compliancev1.ControlAssertionDefinition) { a.RequiredVerifierRefs[0].Version = "2.0.0" }, want: constants.ErrUnsupportedVerifier},
		{name: "unknown grader", mutate: func(a *compliancev1.ControlAssertionDefinition) { a.RequiredGraderRefs[0].Version = "2.0.0" }, want: constants.ErrUnsupportedGrader},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validAssertionCatalog()
			tt.mutate(candidate.Assertions[0])
			err := catalog.ValidateAssertionCatalog(candidate)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestValidateAssertionCatalogRejectsUnknownComponentScope(t *testing.T) {
	tests := []struct {
		name      string
		component string
	}{
		{name: "actuator is not an assessed component", component: "actuator"},
		{name: "scope values are case sensitive", component: "Gateway"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validAssertionCatalog()
			candidate.Assertions[0].ComponentScope = []string{"gateway", tt.component}
			err := catalog.ValidateAssertionCatalog(candidate)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
			assert.ErrorContains(t, err, "ZTD-GOV-BLOCK-001")
			assert.ErrorContains(t, err, tt.component)
		})
	}
}

func TestShippedAssertionCatalogComponentScopesAreClosedVocabulary(t *testing.T) {
	assertions := &compliancev1.ControlAssertionCatalog{}
	require.NoError(t, compliancev1.UnmarshalCanonical(complianceconstants.AssertionCatalogJSON(), assertions))
	require.NoError(t, catalog.ValidateAssertionCatalog(assertions))
	for _, assertion := range assertions.Assertions {
		for _, component := range assertion.GetComponentScope() {
			assert.Contains(t, constants.ComplianceComponentScopes, component, assertion.GetAssertionId())
		}
	}
}

func TestValidateFrameworkDefinitionAcceptsPlannedControl(t *testing.T) {
	framework := validFramework()
	framework.Controls[0].SupportStatus = "planned"
	framework.Controls[0].SupportRationale = "A future catalog version maps this control."

	assert.NoError(t, catalog.ValidateFrameworkDefinition(framework))
}

func TestValidateFrameworkDefinitionRejectsMalformedDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*compliancev1.FrameworkDefinition)
	}{
		{name: "malformed effective date", mutate: func(f *compliancev1.FrameworkDefinition) { f.EffectiveDate = "2026-13-40" }},
		{name: "duplicate control", mutate: func(f *compliancev1.FrameworkDefinition) { f.Controls = append(f.Controls, f.Controls[0]) }},
		{name: "invalid control responsibility", mutate: func(f *compliancev1.FrameworkDefinition) { f.Controls[0].Responsibility = "vendor" }},
		{name: "missing support status", mutate: func(f *compliancev1.FrameworkDefinition) { f.Controls[0].SupportStatus = "" }},
		{name: "invalid support status", mutate: func(f *compliancev1.FrameworkDefinition) { f.Controls[0].SupportStatus = "deferred" }},
		{name: "missing support rationale", mutate: func(f *compliancev1.FrameworkDefinition) { f.Controls[0].SupportRationale = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			framework := validFramework()
			tt.mutate(framework)
			err := catalog.ValidateFrameworkDefinition(framework)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrInvalidEvidenceGraph)
		})
	}
}

func TestCanonicalCatalogDigestsMatchContent(t *testing.T) {
	assertions := &compliancev1.ControlAssertionCatalog{}
	require.NoError(t, compliancev1.UnmarshalCanonical(complianceconstants.AssertionCatalogJSON(), assertions))
	frameworks := &compliancev1.FrameworkCatalog{}
	require.NoError(t, compliancev1.UnmarshalCanonical(complianceconstants.FrameworkCatalogJSON(), frameworks))
	crosswalks := &compliancev1.ControlCrosswalkCatalog{}
	require.NoError(t, compliancev1.UnmarshalCanonical(complianceconstants.FedRAMPAndNISTCrosswalkJSON(), crosswalks))

	assertDigest := func(t *testing.T, expected string, message proto.Message) {
		t.Helper()
		actual, err := catalog.CatalogDigest(message)
		require.NoError(t, err)
		assert.Equal(t, expected, actual)
	}
	t.Run("assertion catalog", func(t *testing.T) { assertDigest(t, assertions.Sha256, assertions) })
	for _, framework := range frameworks.Frameworks {

		t.Run(framework.FrameworkId, func(t *testing.T) { assertDigest(t, framework.CatalogSha256, framework) })
	}
	t.Run("framework catalog", func(t *testing.T) { assertDigest(t, frameworks.Sha256, frameworks) })
	t.Run("crosswalk catalog", func(t *testing.T) { assertDigest(t, crosswalks.Sha256, crosswalks) })
}

func TestLoadCanonicalCatalogsResolveAllReferences(t *testing.T) {
	assertions, frameworks, crosswalks, err := catalog.LoadCanonicalCatalogs()
	require.NoError(t, err)
	require.NoError(t, catalog.ValidateCatalogSet(assertions, frameworks, crosswalks))

	assert.Equal(t, "2.1.0", assertions.GetCatalogVersion())
	for _, id := range []string{"ZTD-GOV-BLOCK-001", "ZTD-AU-RECEIPT-001"} {
		assert.NotNil(t, catalog.FindAssertion(assertions, id, "2.0.0"), id)
	}
	assert.NotNil(t, catalog.FindAssertion(assertions, "ZTD-CM-STATE-001", "2.1.0"))
	assert.NotNil(t, catalog.FindFramework(frameworks, "fedramp-20x", "CR26-2026-06-24"))
	assert.NotNil(t, catalog.FindFramework(frameworks, "nist-sp-800-53", "rev5"))
}

func TestLoadCanonicalAssertionCatalogDefinesTruthfulNativeClaims(t *testing.T) {
	assertions, _, _, err := catalog.LoadCanonicalCatalogs()
	require.NoError(t, err)

	tests := []struct {
		assertionID       string
		version           string
		level             string
		components        []string
		evidenceTypes     []string
		graderIDs         []string
		verifierIDs       []string
		forbiddenEvidence string
	}{
		{assertionID: "ZTD-GOV-BLOCK-001", version: "2.0.0", level: "L3", components: []string{"gateway", "operator"}, evidenceTypes: []string{"deterministic_stage", "state_observation"}, graderIDs: []string{"policy_outcome", "protocol_chain"}, verifierIDs: []string{"deterministic_stage_chain", "state_observation"}, forbiddenEvidence: "action_receipt"},
		{assertionID: "ZTD-AU-RECEIPT-001", version: "2.0.0", level: "L2", components: []string{"gateway", "operator"}, evidenceTypes: []string{"action_receipt"}, graderIDs: []string{"receipt_integrity"}, verifierIDs: []string{"receipt_integrity"}},
		{assertionID: "ZTD-AU-PERSIST-001", version: "2.0.0", level: "L2", components: []string{"gateway", "operator"}, evidenceTypes: []string{"action_receipt", "final_persistence_attestation"}, graderIDs: []string{"receipt_persistence"}, verifierIDs: []string{"receipt_integrity", "receipt_persistence"}},
		{assertionID: "ZTD-AU-COMMIT-001", version: "2.0.0", level: "L2", components: []string{"gateway", "operator"}, evidenceTypes: []string{"commitment"}, graderIDs: []string{"commitment_attestation"}, verifierIDs: []string{"commitment_attestation"}, forbiddenEvidence: "ledger_commit"},
		{assertionID: "ZTD-CM-STATE-001", version: "2.1.0", level: "L3", components: []string{"gateway", "operator"}, evidenceTypes: []string{"state_observation", "metric"}, graderIDs: []string{"independent_state"}, verifierIDs: []string{"state_observation", "eval_metric"}},
		{assertionID: "ZTD-IA-MTLS-001", version: "2.0.0", level: "L3", components: []string{"gateway", "operator"}, evidenceTypes: []string{"identity_attestation", "state_observation", "metric"}, graderIDs: []string{"authenticated_operation"}, verifierIDs: []string{"identity_attestation", "state_observation", "eval_metric"}},
		{assertionID: "ZTD-IA-NOTARY-001", version: "2.0.0", level: "L2", components: []string{"gateway", "operator"}, evidenceTypes: []string{"deterministic_stage", "attestation"}, graderIDs: []string{"protocol_chain"}, verifierIDs: []string{"deterministic_stage_chain", "notary_proof"}},
		{assertionID: "ZTD-CRYPTO-FIPS-001", version: "2.0.0", level: "L2", components: []string{"gateway", "operator"}, evidenceTypes: []string{"build_attestation", "runtime_attestation"}, graderIDs: []string{"fips_mode"}, verifierIDs: []string{"build_provenance", "runtime_fips"}},
		{assertionID: "ZTD-DLP-BOUNDARY-001", version: "2.1.0", level: "L2", components: []string{"gateway", "operator"}, evidenceTypes: []string{"metric", "state_observation"}, graderIDs: []string{"model_boundary_raw_secret_rate"}, verifierIDs: []string{"eval_metric", "state_observation"}},
		{assertionID: "ZTD-DLP-REHYDRATE-001", version: "2.1.0", level: "L2", components: []string{"gateway", "operator"}, evidenceTypes: []string{"metric", "state_observation"}, graderIDs: []string{"exact_local_rehydration"}, verifierIDs: []string{"eval_metric", "state_observation"}},
	}
	for _, tt := range tests {
		t.Run(tt.assertionID, func(t *testing.T) {
			assertion := catalog.FindAssertion(assertions, tt.assertionID, tt.version)
			require.NotNil(t, assertion)
			assert.Equal(t, tt.level, assertion.GetMinimumEvidenceLevel())
			assert.ElementsMatch(t, tt.components, assertion.GetComponentScope())
			assert.ElementsMatch(t, tt.evidenceTypes, assertion.GetRequiredEvidenceTypes())
			assert.ElementsMatch(t, tt.graderIDs, referenceIDs(assertion.GetRequiredGraderRefs()))
			assert.ElementsMatch(t, tt.verifierIDs, referenceIDs(assertion.GetRequiredVerifierRefs()))
			assert.NotContains(t, assertion.GetRequiredEvidenceTypes(), tt.forbiddenEvidence)
		})
	}
}

func referenceIDs(refs []*compliancev1.VersionedReference) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.GetId())
	}
	return ids
}
