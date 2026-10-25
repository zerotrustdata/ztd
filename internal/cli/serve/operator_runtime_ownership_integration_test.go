// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package serve

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// A second capability set is a second process, not a second owner of one vault.
func TestStartOperatorRejectsSharedRuntimeBeforeConfigOrEnrollment(t *testing.T) {
	opts := ServeOperatorOptions{WorkingDir: testutil.TempDir(t), OperatorCapabilities: constants.OperatorCapabilities{constants.OperatorCapabilityProvenance}}
	files, _, err := newOperatorRuntimeFileService(opts, testLogger())
	require.NoError(t, err)
	held, err := files.TryLock(t.Context(), constants.OperatorLockFilename, constants.PermFilePrivate)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, held.Close()) })
	rt, err := StartOperator(t.Context(), opts, VersionInfo{}, OperatorRuntimeDeps{Logger: testLogger(), ClientIdentity: certs.NewClientIdentity(tls.Certificate{})})
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	require.Nil(t, rt)
	assert.ErrorContains(t, err, "--working-dir")
}

func TestStartOperatorReleasesRootOnConfigurationFailure(t *testing.T) {
	opts := ServeOperatorOptions{WorkingDir: testutil.TempDir(t), OperatorCapabilities: constants.OperatorCapabilities{constants.OperatorCapabilityProvenance}}
	_, err := StartOperator(t.Context(), opts, VersionInfo{}, OperatorRuntimeDeps{Logger: testLogger(), ClientIdentity: certs.NewClientIdentity(tls.Certificate{})})
	require.ErrorIs(t, err, constants.ErrProvenanceModelStorageRootRequired)
	files, _, err := newOperatorRuntimeFileService(opts, testLogger())
	require.NoError(t, err)
	lock, err := files.TryLock(t.Context(), constants.OperatorLockFilename, constants.PermFilePrivate)
	require.NoError(t, err, "failed startup must release runtime ownership")
	require.NoError(t, lock.Close())
}
