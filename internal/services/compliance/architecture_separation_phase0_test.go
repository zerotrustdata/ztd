// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliance

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

// Phase 0 regression markers for the proof-backed compliance reporting plan
// (v2.1.3). These tests document the current separation between compliance
// and evals. They assert the current (pre-plan) state and are flipped
// to assert the post-plan state when later phases connect the subsystems.
const (
	phase0RegressionBeforeFix = "PHASE0: BEFORE FIX"
	phase0RegressionAfterFix  = "PHASE0: AFTER FIX"
	phase0RegressionIssue     = "PHASE0: ISSUE: v2.1.3 proof-backed compliance reporting"
)

// TestPhase0Architecture_ComplianceDoesNotImportDemoOrEvalPackages documents
// that the compliance package currently has no compile-time dependency on the
// CLI command packages or the eval subsystems. The two systems remain separate:
// compliance evaluates KSIs from audit/ledger/commitment stores, and evals
// produce typed analytical evidence. No shared typed evidence model connects
// them today.
//
// This boundary is expected to change in Phase 3 (evidence graph importers),
// at which point this test is updated to assert the intended shared-model
// imports rather than the current isolation.
func TestPhase0Architecture_ComplianceDoesNotImportDemoOrEvalPackages(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err, "go list -deps must succeed for the compliance package")

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	blockedSubstrings := []string{
		"internal/cli/cmd", // CLI command packages
		"evals",            // eval package
	}

	var found []string
	for _, d := range deps {
		for _, b := range blockedSubstrings {
			if strings.Contains(d, b) {
				found = append(found, d)
			}
		}
	}

	assert.Empty(t, found,
		phase0RegressionBeforeFix+": compliance package must not import CLI command or eval packages; "+
			"current separation is intentional and documented here. Found unexpected deps: %v", found)
}

// TestPhase3Architecture_KSIResultsUseProtocolEvidenceReferences verifies that
// KSI evaluation no longer exposes the legacy local Evidence string record.
func TestPhase3Architecture_KSIResultsUseProtocolEvidenceReferences(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeOf(KSIResult{}).FieldByName("Evidence")
	require.True(t, ok, phase0RegressionAfterFix)
	assert.Equal(t, reflect.TypeOf([]*compliancev1.ComplianceEvidenceReference{}), field.Type, phase0RegressionAfterFix)
}

// TestPhase1Architecture_ModelsAreProtocolOwned verifies that Phase 1 closes
// the untyped-model gap through protocol-owned records rather than adding
// parallel models to the legacy compliance package.
func TestPhase1Architecture_ModelsAreProtocolOwned(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		message  proto.Message
		fullName string
	}{
		{name: "control assertion definition", message: &compliancev1.ControlAssertionDefinition{}, fullName: "ztd.compliance.v1.ControlAssertionDefinition"},
		{name: "framework definition", message: &compliancev1.FrameworkDefinition{}, fullName: "ztd.compliance.v1.FrameworkDefinition"},
		{name: "control crosswalk", message: &compliancev1.ControlCrosswalk{}, fullName: "ztd.compliance.v1.ControlCrosswalk"},
		{name: "assessment scope", message: &compliancev1.AssessmentScope{}, fullName: "ztd.compliance.v1.AssessmentScope"},
		{name: "evidence reference", message: &compliancev1.ComplianceEvidenceReference{}, fullName: "ztd.compliance.v1.ComplianceEvidenceReference"},
		{name: "assertion assessment", message: &compliancev1.ControlAssertionAssessment{}, fullName: "ztd.compliance.v1.ControlAssertionAssessment"},
		{name: "control assessment", message: &compliancev1.FrameworkControlAssessment{}, fullName: "ztd.compliance.v1.FrameworkControlAssessment"},
		{name: "report manifest", message: &compliancev1.ComplianceReportManifest{}, fullName: "ztd.compliance.v1.ComplianceReportManifest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.fullName, string(tt.message.ProtoReflect().Descriptor().FullName()), phase0RegressionAfterFix)
		})
	}
}
