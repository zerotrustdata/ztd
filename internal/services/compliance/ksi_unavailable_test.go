// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func unavailableTestBinding() EvaluationBinding {
	return EvaluationBinding{
		ScopeID:            "scope-1",
		RunID:              "run-1",
		WindowStartUnixMs:  1_700_000_000_000,
		WindowEndUnixMs:    1_700_000_001_000,
		EvaluatorID:        constants.KSIEvaluatorID,
		EvaluatorVersion:   constants.KSIEvaluatorVersion,
		MethodDefinitionID: constants.KSIMethodDefinitionVersion,
		AssertionAssessments: AssertionAssessmentScope{
			AssessmentIDs: []string{"assessment-1"},
			AttemptIDs:    []string{"attempt-1"},
			ScenarioIDs:   []string{"scenario-1"},
			ActionIDs:     []string{"action-1"},
		},
	}
}

func unavailableTestResultSet() *KSIResultSet {
	binding := unavailableTestBinding()
	return &KSIResultSet{
		Class:         ClassC,
		EvaluatedAtMs: binding.WindowEndUnixMs,
		Binding:       binding,
		Results: []KSIResult{
			{ID: "KSI-CMT-01", Status: KSIStatusSatisfied, Outcome: KSIOutcomeSatisfied, LastValidatedUnixMs: binding.WindowEndUnixMs, Binding: binding},
			{ID: "KSI-MLA-03", Status: KSIStatusUnverifiable, Outcome: KSIOutcomeInvalidEvidence, LastValidatedUnixMs: binding.WindowEndUnixMs, Binding: binding},
			{ID: "KSI-SVC-05", Status: KSIStatusNotApplicable, Outcome: KSIOutcomeNotApplicable, LastValidatedUnixMs: binding.WindowEndUnixMs, Binding: binding},
		},
	}
}

func TestUnavailableIntervalValidateRejectsCrossBindingFields(t *testing.T) {
	binding := unavailableTestBinding()
	valid := UnavailableInterval{
		KSIID:       "KSI-MLA-03",
		ScopeID:     binding.ScopeID,
		RunID:       binding.RunID,
		StartUnixMs: binding.WindowStartUnixMs,
		EndUnixMs:   binding.WindowEndUnixMs,
		Outcome:     KSIOutcomeInvalidEvidence,
		Status:      KSIStatusUnverifiable,
		Binding:     binding,
	}
	tests := []struct {
		name   string
		mutate func(*UnavailableInterval)
	}{
		{name: "scope differs from binding", mutate: func(interval *UnavailableInterval) { interval.ScopeID = "scope-2" }},
		{name: "run differs from binding", mutate: func(interval *UnavailableInterval) { interval.RunID = "run-2" }},
		{name: "start precedes binding window", mutate: func(interval *UnavailableInterval) { interval.StartUnixMs-- }},
		{name: "end exceeds binding window", mutate: func(interval *UnavailableInterval) { interval.EndUnixMs++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			tt.mutate(&candidate)
			err := candidate.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrKSIBindingMismatch)
		})
	}
}

func TestUnavailableIntervalStoreAppendFromResultSetPersistsOnlyFailures(t *testing.T) {
	fileSvc := setupHistoryTestFS(t)
	store := NewUnavailableIntervalStore(fileSvc, filepath.Join(constants.DataDirname, constants.ComplianceDirname))
	rs := unavailableTestResultSet()

	require.NoError(t, store.AppendFromResultSet(context.Background(), rs))
	intervals, err := store.ListIntervals(context.Background())
	require.NoError(t, err)
	require.Len(t, intervals, 1)
	assert.Equal(t, "KSI-MLA-03", intervals[0].KSIID)
	assert.Equal(t, rs.Binding.WindowStartUnixMs, intervals[0].StartUnixMs)
	assert.Equal(t, rs.Binding.WindowEndUnixMs, intervals[0].EndUnixMs)
	assert.Equal(t, rs.Binding, intervals[0].Binding)
}

func TestUnavailableIntervalStoreAppendFromResultSetRejectsMismatchedResultBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvaluationBinding)
	}{
		{name: "another run", mutate: func(binding *EvaluationBinding) { binding.RunID = "other-run" }},
		{name: "another assertion assessment", mutate: func(binding *EvaluationBinding) {
			binding.AssertionAssessments.AssessmentIDs = []string{"assessment-2"}
		}},
		{name: "another attempt", mutate: func(binding *EvaluationBinding) { binding.AssertionAssessments.AttemptIDs = []string{"attempt-2"} }},
		{name: "another scenario", mutate: func(binding *EvaluationBinding) { binding.AssertionAssessments.ScenarioIDs = []string{"scenario-2"} }},
		{name: "another action", mutate: func(binding *EvaluationBinding) { binding.AssertionAssessments.ActionIDs = []string{"action-2"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fileSvc := setupHistoryTestFS(t)
			store := NewUnavailableIntervalStore(fileSvc, filepath.Join(constants.DataDirname, constants.ComplianceDirname))
			resultSet := unavailableTestResultSet()
			test.mutate(&resultSet.Results[1].Binding)
			err := store.AppendFromResultSet(context.Background(), resultSet)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrKSIBindingMismatch)
		})
	}
}

func TestUnavailableIntervalStoreRejectsMalformedPersistedRecord(t *testing.T) {
	fileSvc := setupHistoryTestFS(t)
	dir := filepath.Join(constants.DataDirname, constants.ComplianceDirname)
	store := NewUnavailableIntervalStore(fileSvc, dir)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(dir, constants.KSIUnavailableIntervalsFilename), []byte("{"), constants.PermFilePublic))

	_, err := store.ListIntervals(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKSIUnavailableParseFailed)
}

func TestUnavailableIntervalStoreListMissingFileIsEmpty(t *testing.T) {
	fileSvc := setupHistoryTestFS(t)
	store := NewUnavailableIntervalStore(fileSvc, filepath.Join(constants.DataDirname, constants.ComplianceDirname))

	intervals, err := store.ListIntervals(context.Background())
	require.NoError(t, err)
	assert.Empty(t, intervals)
}

func TestUnavailableIntervalStoreAppendFromResultSetRejectsNil(t *testing.T) {
	fileSvc := setupHistoryTestFS(t)
	store := NewUnavailableIntervalStore(fileSvc, filepath.Join(constants.DataDirname, constants.ComplianceDirname))

	err := store.AppendFromResultSet(context.Background(), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKSIUnavailableWriteFailed)
}
