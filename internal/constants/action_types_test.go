// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestActionTypeIsMutation_MutationTypesReturnTrue(t *testing.T) {
	t.Parallel()

	mutations := []ActionType{
		ActionTypeA2aCall,
		ActionTypeCancel,
		ActionTypeDocumentDelete,
		ActionTypeDocumentUpdate,
		ActionTypeEvalWorkspaceSeed,
		ActionTypeExecuteBash,
		ActionTypeFileEdit,
		ActionTypeInference,
		ActionTypeMcpCall,
		ActionTypeOllamaModelRelease,
		ActionTypePlatformEnrollmentBindConsensus,
		ActionTypePlatformEnrollmentDecide,
		ActionTypePlatformEnrollmentIssue,
		ActionTypePlatformEnrollmentPersistPolicy,
		ActionTypePlatformEnrollmentCreateSession,
		ActionTypePlatformEnrollmentRevoke,
		ActionTypeRestoreFile,
		ActionTypeShutdown,
	}

	for _, at := range mutations {

		t.Run(string(at), func(t *testing.T) {
			t.Parallel()
			assert.True(t, at.IsMutation(), "%s should be a mutation", at)
		})
	}
}

func TestActionTypeIsMutation_ReadTypesReturnFalse(t *testing.T) {
	t.Parallel()

	readTypes := []ActionType{
		ActionTypeEvalAnswer,
		ActionTypeFetchFileDiff,
		ActionTypeFetchFileHistory,
		ActionTypeFetchHistory,
		ActionTypeFetchLogs,
		ActionTypeFsGrep,
		ActionTypeFsList,
		ActionTypeFsRead,
		ActionTypeHeartbeat,
		ActionTypeMcpPromptGet,
		ActionTypeMcpPromptList,
		ActionTypeMcpResourceList,
		ActionTypeMcpResourceRead,
		ActionTypePlatformEnrollmentCreate,
		ActionTypePortCheck,
		ActionTypeOllamaModelInventory,
		ActionTypeOllamaModelResidency,
		ActionTypeProviderBoundaryObservation,
		ActionTypeModelProvenanceObservation,
	}

	for _, at := range readTypes {

		t.Run(string(at), func(t *testing.T) {
			t.Parallel()
			assert.False(t, at.IsMutation(), "%s should not be a mutation", at)
		})
	}
}

func TestActionTypeIsMutation_UnknownTypeReturnsFalse(t *testing.T) {
	t.Parallel()

	unknown := ActionType("UNKNOWN_ACTION")
	assert.False(t, unknown.IsMutation())
}

func TestActionTypeIsMutation_EmptyTypeReturnsFalse(t *testing.T) {
	t.Parallel()

	assert.False(t, ActionType("").IsMutation())
}

func TestAllActionTypes_ContainsAllConstants(t *testing.T) {
	t.Parallel()

	allConsts := []ActionType{
		ActionTypeA2aCall,
		ActionTypeCancel,
		ActionTypeDocumentDelete,
		ActionTypeDocumentUpdate,
		ActionTypeEvalAnswer,
		ActionTypeEvalWorkspaceSeed,
		ActionTypeExecuteBash,
		ActionTypeFetchFileDiff,
		ActionTypeFetchFileHistory,
		ActionTypeFetchHistory,
		ActionTypeFetchLogs,
		ActionTypeFileEdit,
		ActionTypeFsGrep,
		ActionTypeFsList,
		ActionTypeFsRead,
		ActionTypeHeartbeat,
		ActionTypeMcpCall,
		ActionTypeMcpPromptGet,
		ActionTypeMcpPromptList,
		ActionTypeMcpResourceList,
		ActionTypeMcpResourceRead,
		ActionTypePlatformEnrollmentCreate,
		ActionTypePlatformEnrollmentDecide,
		ActionTypePlatformEnrollmentIssue,
		ActionTypePlatformEnrollmentPersistPolicy,
		ActionTypePlatformEnrollmentCreateSession,
		ActionTypePlatformEnrollmentRevoke,
		ActionTypePlatformEnrollmentBindConsensus,
		ActionTypePlatformEnrollmentUnbindConsensus,
		ActionTypePortCheck,
		ActionTypeOllamaModelInventory,
		ActionTypeOllamaModelResidency,
		ActionTypeOllamaModelRelease,
		ActionTypeRestoreFile,
		ActionTypeShutdown,
		ActionTypeInference,
		ActionTypeProviderBoundaryObservation,
		ActionTypeModelProvenanceObservation,
	}

	assert.Len(t, AllActionTypes, len(allConsts))

	for _, c := range allConsts {
		assert.Contains(t, AllActionTypes, c)
	}
}

func TestActionTypeIsMutation_EveryActionTypeClassified(t *testing.T) {
	t.Parallel()

	mutations := []ActionType{
		ActionTypeA2aCall,
		ActionTypeCancel,
		ActionTypeDocumentDelete,
		ActionTypeDocumentUpdate,
		ActionTypeEvalWorkspaceSeed,
		ActionTypeExecuteBash,
		ActionTypeFileEdit,
		ActionTypeInference,
		ActionTypeMcpCall,
		ActionTypeOllamaModelRelease,
		ActionTypePlatformEnrollmentBindConsensus,
		ActionTypePlatformEnrollmentCreateSession,
		ActionTypePlatformEnrollmentDecide,
		ActionTypePlatformEnrollmentIssue,
		ActionTypePlatformEnrollmentPersistPolicy,
		ActionTypePlatformEnrollmentRevoke,
		ActionTypePlatformEnrollmentUnbindConsensus,
		ActionTypeRestoreFile,
		ActionTypeShutdown,
	}
	reads := []ActionType{
		ActionTypeEvalAnswer,
		ActionTypeFetchFileDiff,
		ActionTypeFetchFileHistory,
		ActionTypeFetchHistory,
		ActionTypeFetchLogs,
		ActionTypeFsGrep,
		ActionTypeFsList,
		ActionTypeFsRead,
		ActionTypeHeartbeat,
		ActionTypeMcpPromptGet,
		ActionTypeMcpPromptList,
		ActionTypeMcpResourceList,
		ActionTypeMcpResourceRead,
		ActionTypeModelProvenanceObservation,
		ActionTypeOllamaModelInventory,
		ActionTypeOllamaModelResidency,
		ActionTypePlatformEnrollmentCreate,
		ActionTypePortCheck,
		ActionTypeProviderBoundaryObservation,
	}

	classified := append(append([]ActionType{}, mutations...), reads...)
	assert.ElementsMatch(t, AllActionTypes, classified)
	for _, at := range mutations {
		assert.True(t, at.IsMutation(), at)
	}
	for _, at := range reads {
		assert.False(t, at.IsMutation(), at)
	}
}

func TestActionTypeIsBootstrapAction_PlatformEnrollmentReturnsTrue(t *testing.T) {
	t.Parallel()

	bootstrapActions := []ActionType{
		ActionTypePlatformEnrollmentBindConsensus,
		ActionTypePlatformEnrollmentCreate,
		ActionTypePlatformEnrollmentDecide,
		ActionTypePlatformEnrollmentIssue,
		ActionTypePlatformEnrollmentPersistPolicy,
		ActionTypePlatformEnrollmentCreateSession,
		ActionTypePlatformEnrollmentRevoke,
		ActionTypePlatformEnrollmentUnbindConsensus,
	}

	for _, at := range bootstrapActions {

		t.Run(string(at), func(t *testing.T) {
			t.Parallel()
			assert.True(t, at.IsBootstrapAction(), "%s should be a bootstrap action", at)
		})
	}
}

func TestActionTypeIsBootstrapAction_NonPlatformEnrollmentReturnsFalse(t *testing.T) {
	t.Parallel()

	for _, at := range AllActionTypes {
		if at.IsBootstrapAction() {
			continue
		}

		t.Run(string(at), func(t *testing.T) {
			t.Parallel()
			assert.False(t, at.IsBootstrapAction(), "%s should not be a bootstrap action", at)
		})
	}
}

func TestActionTypeAuthorizedByDecision(t *testing.T) {
	t.Parallel()

	// Issuance and its follow-on writes carry out a committed owner decision.
	// The decision and revocation are owner acts and are not exempt.
	authorized := map[ActionType]bool{
		ActionTypePlatformEnrollmentIssue:         true,
		ActionTypePlatformEnrollmentPersistPolicy: true,
		ActionTypePlatformEnrollmentCreateSession: true,
	}
	for _, at := range AllActionTypes {
		assert.Equal(t, authorized[at], at.AuthorizedByDecision(), "%s", at)
		if at.AuthorizedByDecision() {
			assert.True(t, at.IsBootstrapAction(), "%s must be a bootstrap action", at)
		}
	}
	assert.False(t, ActionTypePlatformEnrollmentDecide.AuthorizedByDecision())
	assert.False(t, ActionTypePlatformEnrollmentRevoke.AuthorizedByDecision())
}

func TestActionTypeIsBootstrapAction_UnknownTypeReturnsFalse(t *testing.T) {
	t.Parallel()

	assert.False(t, ActionType("UNKNOWN_ACTION").IsBootstrapAction())
}

func TestActionTypeIsBootstrapAction_EmptyTypeReturnsFalse(t *testing.T) {
	t.Parallel()

	assert.False(t, ActionType("").IsBootstrapAction())
}
